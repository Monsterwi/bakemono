# Razor RAM 缓存选型（2026-10-21）

结论：**Otter v2.2.1 是当前合理的默认实现，不能称为所有负载下最优。按已授权需求增加 S3-FIFO 候选，先比较真实淘汰负载，再决定是否更换默认值。** 目前没有运行各库之间的公平基准；作者跑分、旧版本对比不能作为本项目赢家证据。

## 本项目首先需要什么

核对 `/tmp/razor-delivery`：RAM 保存序列化 fragment（包含 Doc/HTTP 元数据），是磁盘与索引之上的加速层，不是 HTTP 缓存内容的唯一存储。读之前仍检查当前 directory 的 offset/phase 等；PURGE、对象版本与地址复用的保护必须留在共享存储逻辑中，不能交给 RAM 淘汰算法。[读路径](../cache/cache_read.go)、[写入发布](../cache/cache_write.go)、[失效](../cache/invalidation.go)

当前 `RamCache` 使用 Otter v2.2.1，value 字节数作为 `MaximumWeight` 的权重；常规磁盘读命中没有直接调用 RAM.Put，RAM 写入来自写入发布、HTTP 头更新和 evacuation。该事实意味着策略测试也必须覆盖真实 promotion 行为；它不是已证明的缺陷。[封装](../cache/ram_cache.go)、[头更新](../cache/http_update.go)、[evacuation](../cache/stripe.go)

合格的替代实现应满足：可变大小 value 的字节预算；缺失可安全退回磁盘；同步删除旧映射；无未完成入队写入在 PURGE 后重新发布；读取字节不会在调用者使用期间被修改；Close 不泄漏资源。同步 Set 只表示映射操作完成，不保证条目不被并发淘汰。

## 候选比较

| 实现及本次核对版本 | 策略与可见性 | 预算及内存取舍 | 对 Razor 的判断 |
|---|---|---|---|
| **Otter v2.2.1** | adaptive W-TinyLFU；Set/Invalidate 同步修改 hashmap，维护热度/淘汰可延后 | 可自定义字节权重；文档允许暂时超预算；节点、key、buffer、runtime 不在当前 value 权重中 | 保持默认，复用现有实现与验证 |
| **Ristretto v2.4.2** | TinyLFU 准入 + SampledLFU；新 Set 经 buffer，可能被丢弃或准入拒绝；现有值更新同步 | cost 可定义字节；内部固定成本另加，但并不涵盖进程全部内存；sketch/buffer 另占空间 | 可作为后续实验，不优先替换；必须适配异步写与删除顺序 |
| **Theine-Go v0.6.2** | adaptive W-TinyLFU；成功 Set 先改 shard map，Delete 先删 map，再发送策略事件；可选 doorkeeper 会拒绝新项 | 自定义 cost；map、entry、timer/sketch、buffer 与临时超量仍须测量 | 同类候选，不是自动的算法升级；不需要它的实验性 secondary cache |
| **BigCache v3.2.0** | shard 内同步读写删除；字节队列满时回收最旧记录，并非频率准入 | byte arena 与整数索引降低 GC 扫描；HardMaxCacheSize 限制字节队列，不是 RSS；单项受每 shard 容量限制 | 只有 GC/小对象数量成为瓶颈时值得试；需重设 shard 大小适配大块 |
| **FreeCache v1.2.7** | 同步 segment 操作；近似 LRU；预分配字节环形区 | 低指针开销；**key+value 上限约为整个 cache 的 1/1024**，另减 entry header；固定 256 segments | 不适合当前 10 MiB RAM 配置下的约 1 MiB fragment，直接替换会拒绝这些项 |
| **S3-FIFO 实现候选** | 小 FIFO + 主 FIFO + ghost；命中只需更新有限计数，不必每次移动队列 | 必须按 resident 字节计算容量，ghost 的 key/节点单独设界；算法本身不保证 Go RSS、无锁或低 GC | 新增第二策略做对照；不因此替换磁盘格式、HTTP 版本与失效模型 |

### 核对依据及关键细节

**Otter。** 固定 tag 提交 `f5b83ffe4e3c4a6d4fe3fa6ddd31ef6685eadfbe` 的 README 明确 adaptive W-TinyLFU。`set` 和 `Invalidate` 先执行 `hashmap.Compute`，再调用 afterWrite/afterDelete。`MaximumWeight` 注释明确允许淘汰过程暂时超过阈值，且权重用于判断超容，并不直接决定下一 victim；所以“按字节限额”不等于“大小感知的最优淘汰”。[README](https://github.com/maypok86/otter/blob/f5b83ffe4e3c4a6d4fe3fa6ddd31ef6685eadfbe/README.md#L37)、[Set](https://github.com/maypok86/otter/blob/f5b83ffe4e3c4a6d4fe3fa6ddd31ef6685eadfbe/cache_impl.go#L428)、[Invalidate](https://github.com/maypok86/otter/blob/f5b83ffe4e3c4a6d4fe3fa6ddd31ef6685eadfbe/cache_impl.go#L1187)、[预算语义](https://github.com/maypok86/otter/blob/f5b83ffe4e3c4a6d4fe3fa6ddd31ef6685eadfbe/options.go#L72)

**Ristretto。** 固定 v2.4.2 提交 `8d05e8aeed4b5aed1039aadb637db61075739a3b`：`Set=true` 仍可能被准入拒绝；`Wait` 只能等待此前 buffer 处理完成，不能保证被拒绝的条目存在。`Del` 先同步删 store，再入队 delete，以处理之前的 buffered Set；队列中的旧 Set 仍可能在 delete 事件处理前短暂写回，因此不能用 Del 返回替代业务代次隔离。RAM miss 本身可接受，旧版本命中不可接受。[Set/Wait/Del 源码](https://github.com/dgraph-io/ristretto/blob/8d05e8aeed4b5aed1039aadb637db61075739a3b/cache.go#L267)、[cost 与内部成本](https://github.com/dgraph-io/ristretto/blob/8d05e8aeed4b5aed1039aadb637db61075739a3b/cache.go#L97)、[官方策略说明](https://github.com/dgraph-io/ristretto/blob/8d05e8aeed4b5aed1039aadb637db61075739a3b/README.md)

**Theine。** 固定 v0.6.2 提交 `6df6a2c926f78c6695a045ce5bae56c89e495e2c`：源码先在 shard 锁下更新/删除映射，再通过 channel 维护 policy；doorkeeper 可拒绝首次访问。不能把异步 policy 直接说成“Set 对 Get 不可见”。entry pooling 默认关闭；是否启用应通过 race 与 churn 场景验证。[同步映射/异步策略](https://github.com/Yiling-J/theine-go/blob/6df6a2c926f78c6695a045ce5bae56c89e495e2c/internal/store.go#L383)、[官方说明](https://github.com/Yiling-J/theine-go/blob/6df6a2c926f78c6695a045ce5bae56c89e495e2c/README.md)

**BigCache。** 固定 v3.2.0 提交 `ae1c781e48dc54fabe3a5b90bb98bc7e79553c8b`：HardMaxCacheSize 按 shard 切分，只限 BytesQueue；默认不设上限。新值同 hash 会替换旧索引；Get 会比对完整 key，碰撞表现为旧项丢失或 miss，不应误说成必然返回另一 key 的正文。[容量定义](https://github.com/allegro/bigcache/blob/ae1c781e48dc54fabe3a5b90bb98bc7e79553c8b/config.go#L25)、[set/get 实现](https://github.com/allegro/bigcache/blob/ae1c781e48dc54fabe3a5b90bb98bc7e79553c8b/shard.go#L62)

**FreeCache。** 固定 v1.2.7 提交 `1442997a512aacd9edd7adadaa831eb6c3004251`：源码限制 `len(key)+len(value) <= segmentBytes/4 - header`，256 segments 使其约等于总容量/1024。要保存 1 MiB value，需大于约 1 GiB 总 arena，还要容纳 key/header；Razor 的 fragment 比纯正文还多一层元数据。低 GC 是缓存结构低指针扫描成本，不是整个服务器“零 GC”。[限制实现](https://github.com/coocood/freecache/blob/1442997a512aacd9edd7adadaa831eb6c3004251/segment.go#L69)、[分段和 API](https://github.com/coocood/freecache/blob/1442997a512aacd9edd7adadaa831eb6c3004251/cache.go#L13)、[GetFn 生命周期](https://github.com/coocood/freecache/blob/1442997a512aacd9edd7adadaa831eb6c3004251/cache.go#L129)

## S3-FIFO 的最小实现方向

保留现有 RAM.Get/Put/Delete 与 value 只读约定，新增策略选择即可；不要将 HTTP/Range、磁盘回收和缓存淘汰揉成一个通用框架。新策略先保证同步映射更新和删除，再考虑统计更新如何减锁；如分 shard，记录预算切分导致热点分布不均的影响。

resident 队列按实际 value 字节累计，超大单项直接跳过 RAM；尾块和元数据同样记账。ghost 只保留 key，不保留 value，按条数或元数据字节严格设上限；Delete 清除 resident 与 ghost。ghost 不是可读取内容，更不具备复活 PURGE 前版本的权限。

参考可以选 ATS 10.2.0 的 Apache-2.0 实现，或当前 libCacheSim 的 Apache-2.0 实现；引用或移植保留其版权和许可。libCacheSim 当前 `S3FIFO.c` 还注明与原始 `S3FIFOv0.c` 的冷启动行为不同，必须记录采用哪种变体。SOSP artifact 仓库未核到 LICENSE，不据“论文公开”推断其代码可直接复制。[ATS 固定实现](https://github.com/apache/trafficserver/blob/b1df7334b39a878971c376da849e8da4984918b6/src/iocore/cache/RamCacheS3FIFO.cc)、[libCacheSim 固定实现](https://github.com/1a1a11a/libCacheSim/blob/93529a1faef2c57bbdb9323213b78cad2adec1cd/libCacheSim/cache/eviction/S3FIFO.c)、[libCacheSim LICENSE](https://github.com/1a1a11a/libCacheSim/blob/93529a1faef2c57bbdb9323213b78cad2adec1cd/LICENSE)、[原始 artifact](https://github.com/Thesys-lab/sosp23-s3fifo)

已有 Go 版 `golang-fifo` 使用 MIT 许可，但其公开 API 以条目数指定容量，不能未经适配就当作字节加权缓存。引入整个库和自行实现一个有界策略之间，应按实际修改量、并发与预算契约选择；本轮不新增第三方库作为研究副作用。[Go 参考](https://github.com/scalalang2/golang-fifo)

## 如何决定谁更合适

先跑两种策略的共同正确性检查：Set 后 Get、覆盖、Delete 后无旧值、并发写/删、超大项拒绝、字节预算、ghost 边界、PURGE/版本切换/磁盘地址复用和 Close。所有检查允许正常淘汰造成 miss；禁止旧内容被误识别成当前内容。

再固定 Go 版本、CPU、并发、分 shard、对象大小和 warmup，重放：热点+长尾扫描、顺序视频块、随机拖动、突发新热点和冷热切换。使用 64 KiB/256 KiB/1 MiB/8 MiB value 加真实元数据，工作集分别小于、约等于和明显大于容量；每组至少三次。记录 request/byte hit ratio、p99、CPU、B/op、allocs/op、GC、resident/ghost 字节、heap 与 RSS。容量内纯热 QPS 无法证明淘汰算法好坏。

最后在现有 HDD/SSD CDN 场景确认端到端吞吐、回源字节、磁盘读量和失效正确性；RAM miss 通常先落到磁盘，不等于一次回源。若瓶颈在 CRC、copy、磁盘或 Range 补块，替换 RAM 库可能几乎不改善用户体验。

本次只有源码与文档调研，未改服务代码、依赖或默认策略，未运行候选对照压测；无依据声称 Otter、S3-FIFO 或任何库普遍更快。
