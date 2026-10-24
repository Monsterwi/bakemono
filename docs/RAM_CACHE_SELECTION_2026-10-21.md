# RAM 选型（2026-10-21 快照）

保留 **Otter v2.2.1** 默认，S3-FIFO 作为可选策略。不能宣称任一库普遍最优；当前实现与预算见 [RAM](RAM_CACHE.md)。以下是当日源码调研，不是候选库公平跑分。

| 固定版本 | 策略 / 可见性 | 对 Razor 的取舍与源码 |
| --- | --- | --- |
| Otter v2.2.1 | adaptive W-TinyLFU；映射同步、维护可延后，允许临时超权重 | 保留默认；[固定源码](https://github.com/maypok86/otter/tree/f5b83ffe4e3c4a6d4fe3fa6ddd31ef6685eadfbe) |
| Ristretto v2.4.2 | TinyLFU + SampledLFU；新 Set 缓冲并可拒绝，Wait 不保证准入 | 旧 Set/Delete 时序需版本保护，不能把 Del 返回等同无短暂复活；[源码](https://github.com/dgraph-io/ristretto/blob/8d05e8aeed4b5aed1039aadb637db61075739a3b/cache.go) |
| Theine-Go v0.6.2 | adaptive W-TinyLFU；先更新 shard map，再维护策略；doorkeeper 可拒绝 | 同类候选；异步维护不等于 Get 不可见，entry pooling 需另验；[源码](https://github.com/Yiling-J/theine-go/blob/6df6a2c926f78c6695a045ce5bae56c89e495e2c/internal/store.go) |
| BigCache v3.2.0 | 字节队列与整数索引，按 shard 回收最旧记录 | GC/海量小对象瓶颈时再试；HardMaxCacheSize 非 RSS，hash 碰撞可丢项/MISS，不应返回别的 key；[源码](https://github.com/allegro/bigcache/blob/ae1c781e48dc54fabe3a5b90bb98bc7e79553c8b/shard.go) |
| FreeCache v1.2.7 | 256 segments，预分配 arena，近似 LRU | key+value 上限约总容量/1024 再减头；10 MiB 配置不适合约 1 MiB fragment；[限制](https://github.com/coocood/freecache/blob/1442997a512aacd9edd7adadaa831eb6c3004251/segment.go) |
| S3-FIFO | Small/Main/Ghost，有限引用计数 | 已采用 ATS 变体；[状态图与边界](RAM_CACHE.md#s3-fifo-状态) |

所有预算都须区分 value 权重、key/node、sketch/buffer 和 runtime；“低 GC”不等于服务器零 GC。Razor RAM 保存序列化 fragment，MISS 先回磁盘，不等于回源；当前磁盘读不直接 promotion 到 RAM，写入、头更新和 evacuation 可填 RAM。

## 替换门槛

| 先验证 | 再测量 |
| --- | --- |
| value 只读、独立读者、同步删除/覆盖、超大项拒绝、字节/ghost 边界、PURGE/版本/地址复用不混读 | 同 Go/CPU/容量/并发，扫描、热点切换、长尾及不同大小，工作集小于/接近/超过容量 |
| RAM MISS 安全回盘，Close 无泄漏；Set 成功不保证不被并发淘汰 | 请求/字节命中、CPU、alloc/GC、P99、resident/ghost、heap/RSS，最后测 HDD/SSD 端到端 |

源码移植须固定版本并保留许可；不从“论文公开”推断 artifact 可复制。[ATS 来源及归属](../THIRD_PARTY_NOTICES.md)、[libCacheSim Apache-2.0](https://github.com/1a1a11a/libCacheSim/blob/93529a1faef2c57bbdb9323213b78cad2adec1cd/LICENSE)。条目数限额的库不能未经适配就当字节缓存。

本文不提供候选库公平跑分；当前保留的 [热命中结果](benchmarks/ATS_COMPARISON_2026-10-24.md)不能用作全部 RAM 策略的性能排名。
