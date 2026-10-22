# Razor 下一轮实施清单

状态：#16/#17/#20/#21功能实现和集成回归已完成，测量源码5ccf32c的24项HDD/SSD矩阵及两策略各一次全新SSD ≥7200秒长测全部26项实测通过。Otter实际7200.279144秒/122,666 GET，S3-FIFO实际7200.089907秒/123,096 GET，均排空且无非预期错误。normal/race/vet/build、双轴代码审查、16项真实工具和测量源码首轮CI通过；最终文档准确性审查、后续交付提交及其CI按Git/Issue外部证据完成。历史f969c1c/12571fc真实长测失败及四次旧运行记录保留，旧Otter通过时间不抵扣本次。需求与验收统一由[总计划 #1](https://github.com/Monsterwi/bakemono/issues/1)及子Issue管理；#1/#18保持打开；已按用户指令由 [#24](https://github.com/Monsterwi/razor/issues/24) 将验收集成提交 c93f4d7 快进合并 main，未发布。以下基线与调度段保留原轮次实施记录；当前实现说明见 [缓存填充设计](CACHE_FILL_DESIGN.md)。

基线：已验收 `d212f7147b7b74abe995a0bb7711e9ba068cc620`。集成分支 `integration/razor-cache-next`，工作树 `/tmp/razor-cache-next`。保留原 `/workspace/bakemono`、`/tmp/razor-delivery` 的改动、历史工作树和所有已有缓存文件。

## 本轮交付

| 优先级 | 功能 | 已有基础与待完成增量 | Issue |
| --- | --- | --- | --- |
| P0 | 跨客户端安全合并 | 已有有界共享填充；新增公共静态模式，等价的不同 IP/UA/追踪头请求可共享生产者 | [#17](https://github.com/Monsterwi/bakemono/issues/17) |
| P0 | fresh Range 离线读取 | 已有源站/强版本隔离；修复全源站不健康时，完整 fresh 缓存区间仍无法 HIT 的问题 | [#16](https://github.com/Monsterwi/bakemono/issues/16) |
| P0 | 原生 partial caching 与块级 RWW | 公共静态大文件 GET 与单 Range 共用块，只补缺块，首块提交后发送，最多前瞻一块 | [#20](https://github.com/Monsterwi/bakemono/issues/20) |
| P0 | 可配置块大小 | 从硬编码 1 MiB 改为可配置，布局变化不混读旧块 | #20 |
| P1，本轮必做 | S3-FIFO RAM 策略 | 保留 Otter 默认，新增按字节计费的 S3-FIFO 与对照 | [#21](https://github.com/Monsterwi/bakemono/issues/21) |
| 交付门槛 | CDN 集成验收 | 两 RAM 策略、HDD/SSD、真实客户端、三轮测量、2 小时 soak、双轴审查与 CI | [#19](https://github.com/Monsterwi/bakemono/issues/19) |

[性能剖析 #18](https://github.com/Monsterwi/bakemono/issues/18) 后置，不属于本轮必做；读盘回填 RAM、CRC/分配/重复读块/Flush 仅为待验证候选，不能把观察当已确认缺陷。

## 共享接口与不变量

### 请求身份（#17）

- 默认保守模式不变；公共静态模式显式启用，表示本实例缓存路由的源站不按客户端身份生成差异内容。本轮不新增路由 DSL。
- 仅规范化符合现有准入的无正文请求；Authorization/Cookie、private、Set-Cookie、未知缓存语义保持保守处理。不可共享响应不交给等待者，不触发所有等待者立即独立重试。
- 缓存身份、任务身份与 PURGE 统一使用同一根键。后续源站契约已明确 query 参数项顺序无关：HTTP 键 v3 按原始项排序，保留重复数量、参数字节和编码差异；原始 path、有效 scheme/Host、源站命名空间、表示与 PURGE 代次继续隔离。实际回源保留原始 query 字节，客户端相关头规范化规则不变；旧键空间安全 MISS，保留缓存文件。
- 公共缓存生产者不携带真实客户端 IP/Forwarded/X-Forwarded-For/X-Real-IP。可信转发 scheme 先解析，Host/scheme 含义保留；不能清了头却让 proxy.rewrite 再注入客户端身份。普通旁路请求保留原代理语义。
- User-Agent 固定为 Razor；删除 Traceparent、Tracestate、Baggage、X-Request-Id、X-Correlation-Id，原始关联留在本地日志。其他头仍保守参与任务身份和回源，不提供任意忽略头的配置。
- 首版 Vary 仍仅支持 Accept-Encoding/Accept-Language 及其组合，值不做语义折叠。首次 MISS 也按这两个维度分组。Vary:* 或未支持维度不共享。
- 公共模式与规范化规则版本进入内容命名空间，变更安全 MISS；运行时版本隔离在途任务。RAM 算法不改变表示，不应单独使磁盘内容失效。
- 复用现有有界 fillGroup；创建者取消不终止其他订阅者，最后订阅者离开取消生产者；任务/等待者/写者/缓冲/运行时租约最终排空。

### manifest 与离线读取（#16/#20）

- manifest 固定格式版本、总长、强 ETag、源站身份、变体、块大小、incarnation。块身份再加入块号；填充继承最初 URL/PURGE token，禁止旧任务重捕新代次后发布。
- 每块复用现有完整对象提交、CRC、索引与覆盖保护。manifest 存在不代表块存在；块可独立丢失/淘汰。异常退出允许安全 MISS，不承诺每块 fsync 或断电零丢失。
- 先在当前配置命名空间内查 fresh manifest/块，仅缺数据时要求健康源站。多个候选按稳定顺序固定一个源站，整次响应不跨源拼接；已从配置移除的源站不能被离线查找重新启用。复用原 proxy 的租约与额度。
- 布局显式版本化：旧 1 MiB 布局仅兼容时读取，否则安全 MISS；不删除、截断或自动迁移旧文件。块大小变化不能复用错误偏移解释。
- 回源校验 206/Content-Range/强 ETag/实际长度/identity 编码。弱或缺失 ETag、HEAD 不支持、多 Range 等保持安全旁路；忽略 Range 返回 200、412、截断等不能发布错误块。

### GET/Range 与 RWW（#20）

- 公共静态模式合格大文件完整 GET 与单 Range 使用同一组块；完整 GET 返回 200，Range 返回 206。保留现有 fresh 整对象 HIT、小文件原路径和非公共模式行为。
- 冷 GET 可共享 HEAD 探测总长/验证器，决定分块或原路径；小文件探测成本、正文 GET 与 HEAD 分开统计，不为每块重复 HEAD。
- 首个所需块完整校验并提交后开始发送；发送当前块时最多前瞻一个后续块，且只限本请求区间。不后台抓完整文件。尾块使用真实长度。
- 每读者独立位置；读块/预取/生产缓冲全部受有界预算约束。预算不足不预取，避免读者持有当前块等待下一块额度形成死锁。容量检查按必要窗口处理，不要求响应前容纳完整对象。
- 每块开始发送前检查失效；观察到 PURGE/版本变化停止后续块，旧代不能发布。已发送和网络缓冲内字节不能撤回。
- 头发送前失败可返回错误或安全旁路；发送后失败必须中断 HTTP 响应/H2 流，客户端感知不完整，不能在文件尾写错误正文或换版本继续。
- 保持 Content-Length/Content-Range、HEAD、条件请求与 If-Range 语义；首版合成响应继续下游 Cache-Control: no-cache，避免夸大后续块新鲜度。

### RAM（#21）

- Otter v2.2.1 默认；S3-FIFO 第二 adapter。统一 Get/Put/Delete/Items/Size、统计和维护语义，覆盖当前嵌入 Otter 方法的调用；不能出现另一 adapter 的 nil 方法访问。
- 超大项跳过 RAM，空项仍须有界，更新/删除不能被旧排队事件复活。允许淘汰造成 MISS，安全回退磁盘；策略不接管 HTTP 版本/PURGE/磁盘回收。
- 参考固定发布的 Apache-2.0 S3-FIFO 源码，记录算法变体与必要归属。resident 按序列化 cache value 字节（含 Doc/HTTP 元数据、manifest 和正文），ghost 不保存原 value 且元数据有界。外部节点/key/ghost 开销另计，不宣传进程 RSS 硬上限。
- 默认每 stripe 10 MiB 序列化 value 权重预算不变；两策略使用相同容量比较。暂不改变读盘 promotion，不预设 S3-FIFO 一定更快。

## 新配置（已实现，重启生效）

统一前缀 `proxy.config.cache.`；首版均重启生效，SIGHUP 改变这些项须拒绝并保留当前服务。原已支持热配置保持不变。

| 键 | 类型 | 默认 | 约束 |
| --- | --- | --- | --- |
| shared_fill_public_static | INT | 0 | 仅 0/1；公共资源契约显式启用 |
| range_block_bytes | INT | 1048576 | 64 KiB–8 MiB，2 的幂 |
| ram_cache_algorithm | STRING | otter | 仅 otter / s3-fifo |
| ram_cache_bytes | INT | 10485760 | 每 stripe 序列化 value 权重字节；0 禁用，非零至多 1 TiB |

## 文件范围与调度

- #16：range_cache.go 的离线 manifest/源站选择和回归；proxy 仅按需增加稳定的已配置源站解析。
- #17：请求规范化模块、fill_group.go、public 配置、namespace 与 proxy rewrite；Range 只接身份，不改发送循环。
- #20：依赖 #16/#17 集成后，改 Range 逐块管线、GET 路由、manifest 布局、预算/预取及块配置，不重写任务所有权。
- #21：RAM adapter、策略测试/基准、构造/配置接线；不改磁盘格式或 GET/Range 协议。
- #19：工具、真实二进制回归、README/状态文档、报告和 CI；依赖 #16/#17/#20/#21。

配置结构/runtime/bootstrap 及 proxy 等共享文件由集成者依次合并。接口细节可采用最小实现，不放宽以上不变量，不新增通用插件框架。

最多两个实现代理，保留合并/审查槽。首批 #16 与 #17；空槽优先启动已解锁 #20，否则启动独立 #21；所有实现集成后执行 #19。每项受影响回归和必要 race 通过才集成，下游从已验证的集成 HEAD 更新工作树，立即动态调度。

唯一集成树 `/tmp/razor-cache-next`；准备 `/tmp/razor-task-16`、`/tmp/razor-task-17`、`/tmp/razor-task-21`。#20/#19 在依赖通过后各建独立工作树。创建分支不代表已启动实现代理。仅实际验收通过的 Issue 关闭，#1 保持打开。

## 统一验收

1. 100 个不同 IP/UA/追踪头的等价公共冷请求在生产者完成前到达，只产生一个正文生产者；不同变体/私有请求不错误共享，未知头仍隔离。取消/失败/超时/限额/PURGE/重载后资源排空。
2. 默认 1 MiB 布局的 100 MiB 对象依次读 [0,16 MiB)、[8,24 MiB)，累计正文回源 24 MiB，第二次仅 8 MiB；其他布局按对齐并集计算，HEAD 单列。同代缺块只有一个活动生产者。
3. 用同步屏障阻塞第二块，真实客户端必须在放行前收到首块；覆盖完整 GET 和 Range。后续块失败须出现真实正文长度/读取错误，不能只检查 200/206。
4. 完整 fresh Range 在全源站不健康时仍正确读取且零回源；缺块/过期/损坏受控失败。覆盖同 ETag 不同源站、换源、版本/If-Range/416、截断/错误响应、位翻转、重启/布局变化/缓存回卷。
5. 两 RAM 策略共享契约/race/磁盘路径回归；相同预算下比较工作集小于/约等于/大于容量、metadata/256 KiB/1 MiB/4 MiB、热点/长尾/扫描/热点切换。记录 ghost、短时超限和元数据成本。
6. 固定[专用测试机](TEST_ENVIRONMENT.md)，每 run-id 新缓存目录，HDD/SSD 分测。主测 256 KiB（<1 MiB）和 100 MiB，200 MiB 作兼容回归；冷突发、重叠/随机拖动、慢源/慢客户端、离线/重启/PURGE。主要场景至少三轮，压力组顺序执行，避免互相争抢主机。
7. 每种 RAM 策略至少 2 小时混合 soak 并验证排空，默认在 SSD 顺序执行（合计至少 4 小时）；HDD 独立覆盖功能与三轮压力，报告明确两策略各自的介质和运行区间。报告请求/块/字节命中、源站 HEAD/GET/实际正文、磁盘读写/写放大、CPU/GiB、吞吐、TTFB/完成 P99、RSS/heap/GC、FD/goroutine、任务/等待者/缓冲，并保存 commit/hash/配置/原始结果与失败。
8. normal/race/vet、真实二进制/工具回归、Standards/Spec 双轴审查通过，最终集成 HEAD CI 全绿；README 区分已验收/条件支持/未支持。2 小时 soak 不代表长期生产稳定。

准备校验：在本集成树执行 `go test ./... -count=1 -timeout=180s` 已通过，日志 `/tmp/razor-cache-next-baseline.log`；这验证已有基线，不代表上述新功能已经实现。

## 暂缓

#18 泛化性能优化、未提交字节级 RWW、多 Range、任意区间树、io_uring/native AIO 扩展、HTTP/3、跨节点/Tiered Cache/Shield、磁盘算法框架、热切 RAM 算法。ATS 10.2 安装/适配与公平对照另行做，不宣称已有性能优势。

参考：[CDN 架构](CDN_CACHE_ARCHITECTURE_2026-10-21.md)、[RAM 选型](RAM_CACHE_SELECTION_2026-10-21.md)。ATS 插件也可分块缓存；本项目以原生整合及实测证据交付，不宣传 ATS 完全不能缓存 206。
