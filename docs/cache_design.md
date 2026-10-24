# 缓存设计

Razor 使用 **RAM 加速 + Stripe 环形磁盘日志**。整对象、manifest 和每个 Range 逻辑块都作为独立缓存对象，复用同一套目录、分片、校验与提交机制。HTTP 层决定能否缓存和共享，存储层负责定位、写入所有权与数据完整性。

## 结构与磁盘布局

![UML 缓存布局](diagrams/cache_layout.svg)

| 层 | 当前实现 |
| --- | --- |
| Cache / Stripe | `CRC32(对象键) % Stripe 数` 选 stripe；一个数据文件可切成多个互不重叠区间 |
| DirManager | MD5 定位 segment/bucket/tag；每 bucket 4 个槽，碰撞链与 freelist 管理目录；额外身份摘要防误命中 |
| Dir | 每项 10 B，记录文件字节偏移、近似长度、tag、phase 和链指针；指向一个 fragment |
| Doc / fragment | v3、72 B 头，后接元数据与正文；正文每片最多 1 MiB；fragment 0 携带 HTTP 元数据及对象总长 |
| RAM | 保存不可变序列化 fragment；默认 Otter，可选 S3-FIFO，每 stripe 默认 10 MiB 权重预算；不是独立权威副本 |
| 检查点 | 独立 `<数据路径>.<Skip>.checkpoint` 文件，保存目录、完整键映射、写位置与 session |

10 B 仅指 Dir 本体；额外身份数组、完整键映射、RAM、写缓冲与运行时都占内存，不能用“片段数 × 10 B”估算总开销。

数据区从 `Start = Skip + align512(24 + 目录保留区大小)` 开始，到 `Skip + Len` 结束。前 24 B 是会话 marker；目录恢复依据独立检查点，**不是旧 Vol 的 meta A/B**。数据区追加 Doc 记录，尾部放不下完整记录时回卷。

Dir 的近似长度用于边界校验；实际读取长度取 Doc.Len。当前聚合写按记录实际字节推进，**不保证每个 Doc 的起点都按 512 B 或 4 KiB 对齐**；这里也没有“一个对象一个文件”的布局。

## 读写与提交

| 路径 | 顺序 |
| --- | --- |
| 读 | HTTP 层选表示键 → stripe/目录 → 检查 phase/覆盖范围 → RAM；RAM MISS 才 ReadAt 读 Doc 头和完整 fragment |
| 校验 | RAM 与磁盘都检查 CRC32、键/首键摘要、writer 版本、片号与长度；HTTP 层另判断 freshness、变体与失效凭证 |
| 写 | 取得 OpenDir 写入所有权和 FillToken → 拆 fragment → 聚合缓冲分配偏移/代次 → WriteAt 完成 → 复核围栏并发布目录/RAM |
| 完整提交 | fragment 1…N 可先写，fragment 0 最后写；发布头前复核全部已写片段仍有效，HTTP 仅使用 OpenReadCommitted |

聚合缓冲是 **2 × 4 MiB**，每 stripe 一个磁盘写 worker；当前写者每片都等待对应 flush 完成，不能假定总会攒满 4 MiB 才写。读写用 Go 文件 ReadAt/WriteAt，不共享 Seek 游标；无 io_uring/native AIO。普通读盘当前不会自动将片段晋升到 RAM；写入、元数据更新和 evacuation 会填充 RAM。

![UML 发布状态](diagrams/publication.svg)

普通整对象 MISS 等完整提交后，各客户端独立读取。底层保留活跃 Writer 读取 API，但它不是 HTTP RWW 路径；“提交可读”也不等于逐块 fsync。

## 部分对象缓存与定位

**HTTP 逻辑块与存储 fragment 是两层粒度。** 逻辑块决定回源区间及 RWW 屏障，默认 1 MiB，可配 64 KiB–8 MiB；存储 fragment 正文上限固定 1 MiB。因此 4 MiB 逻辑块通常拆成 4 个 fragment，整个块提交后才可发送。

| 对象 | 身份与内容 |
| --- | --- |
| URL 根键 | 配置命名空间、scheme/Host、原始路径、排序后的原始 query 项；缓存/任务/PURGE 统一，源站须保证 query 顺序无关 |
| manifest | 固定完整源站、变体、总长、强 ETag、随机 Incarnation、布局 v2 和块大小；不存在全文件常驻位图 |
| 逻辑块 i | 键包含 URL 根键、源站/变体/版本/Incarnation/布局及块号；正文为该固定区间 |
| 存储片 j | 片 0 用对象键；后续片用 `MD5(对象键 || uint64BE(j))`；Doc 中再保存完整键的 SHA-256 截断摘要及 writer 版本 |

请求区间用左闭右开 `[a,b)` 表示；块号从 `floor(a/B)` 到 `floor((b-1)/B)`，第 i 块回源 `[iB, min((i+1)B, 总长))`。源站请求转换成 HTTP 闭区间 `Range`，并带 If-Match 与 identity 编码。

例如 B=1 MiB，fresh manifest 有效，客户端读 `[0.5,2.5 MiB)`；同版本的块 0、2 fresh 且未淘汰，块 1 缺失：

| 步骤 | 行为 |
| --- | --- |
| 1 | 读取 manifest 确定源站、版本与块布局；一般路径中缺失/过期则共享 HEAD |
| 2 | 读块 0，发送后半 MiB；同时可准备块 1 |
| 3 | 块 1 加入/创建共享任务，仅回源 `[1,2 MiB)`；精确校验 206、Content-Range、ETag、长度、EOF 和 Trailer 后提交 |
| 4 | 发送完整块 1，再发送块 2 前半 MiB；最多准备下一块，额度不足改为按需获取 |

每个逻辑块**独立选 stripe、查目录**；同一块的 fragment 在该 stripe 内逐片查键，物理位置可不连续。读取片内正文从 `Dir.offset + 72 + HLen` 开始；不能用“文件基址 + HTTP Range 偏移”直接定位。首尾块先完整读取并校验，再裁剪发送；极小 Range 也可能产生整块回源/读取放大。

合格公共大 GET 与单 Range 共用这些块。并发等价 MISS 由 fillGroup 合并为一个生产者，无人为聚合窗口；普通整对象按对象共享，块路径分别按 manifest/块共享。不同请求顺序读取自己的所需区间，不等待整个文件补齐。完整时序见 [合并回源](cache_fill_design.md#缺块合并与-rww-时序) 与 [RWW](rww_implementation.md)。

## 淘汰、失效与恢复

| 事件 | 当前行为 |
| --- | --- |
| RAM 满 | 按所选策略淘汰；目录仍有效可回磁盘；权重预算不等于进程 RSS |
| 磁盘回卷 | 复用旧空间；phase 和 allocation generation 识别覆盖；近写指针的已访问片段可尝试有界 evacuation，迁移产生额外读写 |
| 块/manifest 淘汰 | 独立淘汰；manifest 存在不代表块齐全；同版本刷新可沿用 Incarnation，丢失重建则换标识 |
| PURGE / 版本 | URL 家族失效，派生块沿用原始 FillToken；每块发送前检查围栏，不跨源/版本拼接；已发字节无法撤回 |
| 正常关闭 | 排空写入，sync 数据，再原子发布并 sync clean 检查点；下次仅恢复验证完整的对象 |
| 异常退出 | 活动 session 不接受旧检查点；安全冷 MISS，不扫描旧日志重建，也不删除/截断缓存文件 |
| 离线 | 同一仍配置源站的 manifest 与全部所需块均 fresh、完整可读时可零回源；不提供 stale-if-error |

已知回卷期间 no-store 更新后仍可能命中旧对象的问题由 [#33](https://github.com/Monsterwi/razor/issues/33) 跟踪，尚未修复。多 Range 块缓存、跨节点合并/一致性/PURGE、未提交字节级 HTTP RWW 未支持。

代码：[Cache](../cache/cache.go)、[目录](../cache/dir_manager.go)、[读](../cache/cache_read.go)、[写](../cache/cache_write.go)、[块缓存](../server/range_cache.go)。细节：[磁盘格式/恢复](cache_storage_format.md)、[迁移](evacuation.md)、[RAM](ram_cache.md)、[配置](configuration.md)。

## 设计来源

沿用原项目的紧凑目录与环形写思路，本文已按当前 Razor 重写；旧 Chunk 4 KiB 头、meta A/B 和“无写放大”等描述不适用于当前实现。原文与四张旧图保留在 [固定历史版本](https://github.com/Monsterwi/razor/blob/d073c27085f39c87d1041e108835c2029a11b6d2/docs/cache_design.md)，来源为 [bocchi-the-cache/bakemono @5279a99](https://github.com/bocchi-the-cache/bakemono/blob/5279a99634d0c8ca7298f8b63b645de518fbe190/README.md)，[Apache-2.0](../LICENSE)。
