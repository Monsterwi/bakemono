# CDN 缓存架构与策略调研（2026-10-21）

范围：核对 ATS 已发布版本、CacheLib 官方架构和原始算法论文；区分已实现能力、论文结果和 Razor 建议。不存在可由公开资料证明的统一“最先进 CDN”排名。

## ATS 并非只支持 FIFO

截至调研日，官方列出的稳定版是 **10.2.0，2026-08-17 发布**。这里同时核对发布 tag 的源码，避免把持续更新的 `10.2.x` 文档当成发布时快照。[官方下载页](https://trafficserver.apache.org/downloads)

`10.2.0` tag 对应提交 **b1df7334b39a878971c376da849e8da4984918b6**。该提交的 RAM 策略选择如下：

| 配置值 | 策略 | 边界 |
|---|---|---|
| `0` | CLFUS | 结合频率、近期访问与大小；支持 RAM 压缩 |
| `1` | LRU | 默认；可配 seen filter 抑制一次性扫描污染 |
| `2` | S3-FIFO | 已进入 10.2.0；文档标为 experimental；不支持 RAM 压缩 |

发布源码的配置范围是 `[0-2]`，默认 `1`；初始化工厂实际实例化上述三种实现，并非只在文档中出现。发布变更记录包含 `#13255`，S3-FIFO 实现文件也存在。[配置定义](https://github.com/apache/trafficserver/blob/b1df7334b39a878971c376da849e8da4984918b6/src/records/RecordsConfig.cc#L874)、[策略工厂](https://github.com/apache/trafficserver/blob/b1df7334b39a878971c376da849e8da4984918b6/src/iocore/cache/CacheProcessor.cc#L1455)、[S3-FIFO 实现](https://github.com/apache/trafficserver/blob/b1df7334b39a878971c376da849e8da4984918b6/src/iocore/cache/RamCacheS3FIFO.cc)、[发布变更记录](https://github.com/apache/trafficserver/blob/b1df7334b39a878971c376da849e8da4984918b6/CHANGELOG-10.2.0#L527)、[配置说明](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/files/records.yaml.en.html#proxy-config-cache-ram-cache-algorithm)

磁盘层是另一回事：ATS stripe 的正文区是环形追加缓冲，新记录最终覆盖旧记录，默认行为可概括为 **FIFO 风格的物理覆盖**。它不是 RAM 淘汰算法的配置项，也没有因为选择 RAM LRU 就变成磁盘对象级 LRU。[磁盘布局](https://docs.trafficserver.apache.org/en/10.2.x/developer-guide/cache-architecture/architecture.en.html#storage-layout)

但磁盘层也不能简化成“无条件纯 FIFO”：正在使用的片段需要保护；可选 **hit evacuation** 会把写指针前方、近期被访问的记录搬到新位置，避免被覆盖。`hit_evacuate_percent` 在 10.2.0 发布源码中默认 **0**，即默认关闭；架构文档某些段落仍提到默认 10，不应据此误报实际默认值。搬迁会增加写入，因此命中收益需与写放大一起评估。[发布默认值](https://github.com/apache/trafficserver/blob/b1df7334b39a878971c376da849e8da4984918b6/src/records/RecordsConfig.cc#L849)、[hit evacuation 配置](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/files/records.yaml.en.html#proxy-config-cache-hit-evacuate-percent)、[evacuation 操作](https://docs.trafficserver.apache.org/en/10.2.x/developer-guide/cache-architecture/architecture.en.html#evacuation-operation)

缓存准入又是第三个维度：决定“是否写入这一层”，与“写入后淘汰谁”分开。ATS RAM 的 seen filter 就是准入过滤；不能据磁盘追加布局推断其整个系统只有一种缓存策略。[seen filter](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/files/records.yaml.en.html#proxy-config-cache-ram-cache-use-seen-filter)

## CacheLib 值得借鉴的是按层和回收单位划分策略

Meta CacheLib 的 RAM 层提供 LRU、LRU 2Q、TinyLFU；选择适合的 allocator 类型确定策略。它还通过延迟 promotion 降低每次读命中时的维护开销。[RAM eviction 文档](https://cachelib.org/docs/Cache_Library_User_Guides/eviction_policy/)

SSD 引擎 Navy 单独配置：小对象进入 BigHash 桶，大对象进入追加写 region 的 BlockCache。小对象和大对象的索引、I/O 与回收代价不同，不能强求共用一个淘汰结构。BlockCache 在 region 粒度选择 LRU、FIFO 或 segmented FIFO，并可在回收时按命中次数重新插入值得保留的对象；BigHash 桶满时采用 FIFO。[Navy 架构](https://cachelib.org/docs/Cache_Library_Architecture_Guide/navy_overview/)、[布局和回收粒度](https://cachelib.org/docs/Cache_Library_Architecture_Guide/overview_a_random_walk/)、[配置接口](https://cachelib.org/docs/Cache_Library_User_Guides/Configure_HybridCache/#5-engine-settings---block-cache)

Navy 将上层 NVM 准入与设备写预算控制分开，支持随机拒绝、动态写入预算，并限制在途插入与内存。热度策略、SSD 寿命与过载保护不应揉成单一算法。[准入机制](https://cachelib.org/docs/Cache_Library_Architecture_Guide/navy_overview/#engine-driver)

这并非一个无所不包的 `CacheAlgorithm` 接口：BlockCache 的 `EvictionPolicy` 只处理 region 的 `track/touch/evict`，另提供内存统计、持久化与恢复；HTTP 版本、PURGE、对象拼接属于更上层。引用源码固定为调研时 main 提交 **3fbfbbd6f7a5c6c69b096741621fe373e6a147a6**，不宣称它是某个稳定发布包。[EvictionPolicy 接口](https://github.com/facebook/CacheLib/blob/3fbfbbd6f7a5c6c69b096741621fe373e6a147a6/cachelib/navy/block_cache/EvictionPolicy.h)

## 算法候选：看命中收益，也看维护成本

| 候选 | 主要机制 | 适合验证的问题 |
|---|---|---|
| LRU / 延迟 LRU | 优先近期访问；可延迟链表移动 | 近期热点明显时的基线；检查命中锁竞争 |
| TinyLFU / W-TinyLFU | 近似频率准入，结合 LRU 与窗口 | 长尾扫描是否挤掉复用对象；突发新热点能否及时进入 |
| S3-FIFO | 小队列、主队列、ghost；廉价计数，延迟晋升 | 减少一次性对象占用，降低每次命中的重排成本 |
| SIEVE | 入队顺序、访问位与淘汰游标；命中不移动全局队列 | 追求较小实现和较低命中维护开销 |

S3-FIFO 是 SOSP 2023 论文：作者在 14 个数据集、6594 条 trace 上评估，报告较好的 miss ratio 和扩展性。其“三个 FIFO 队列”包含准入与保留机制，不能等同于朴素先进先出。论文中的 16 线程吞吐相对结果只适用于作者的实现、环境和负载。[作者代码与实验说明](https://github.com/Thesys-lab/sosp23-s3fifo)、[原始论文](https://www.cs.cmu.edu/~rvinayak/papers/s3-fifo-sosp-2023-fifo-queues-are-all-you-need-for-cache-eviction.pdf)

SIEVE 是 NSDI 2024 论文：通过读命中只设置访问状态、回收时再决定去留降低维护成本。其原型中命中不需要淘汰结构上的锁，不等于整个缓存引擎无锁；论文 miss ratio 与吞吐数据不能直接变成 Razor 或商业 CDN 的结论。[USENIX 原始论文与实验入口](https://www.usenix.org/conference/nsdi24/presentation/zhang-yazhuo)

## Razor 当前事实

核对已验收工作树 `/tmp/razor-delivery`：`go.mod` 固定 `github.com/maypok86/otter/v2 v2.2.1`，`cache/ram_cache.go` 设置 `MaximumWeight`，以 value 字节数作为权重。Otter **v2.2.1** 的官方 README 明确使用 **adaptive W-TinyLFU**；因此当前 RAM 缓存已经不是自制 FIFO。该 tag 对应提交 **f5b83ffe4e3c4a6d4fe3fa6ddd31ef6685eadfbe**。[固定版本说明](https://github.com/maypok86/otter/blob/f5b83ffe4e3c4a6d4fe3fa6ddd31ef6685eadfbe/README.md#L37)、[本地 RAM 封装](../cache/ram_cache.go)、[依赖版本](../go.mod)

`MaximumWeight` 的 value 字节预算不等于进程 RSS 上限；key、节点、统计及 Go runtime 开销需要另行测量。不能把“添加算法配置项”作为从 FIFO 升级的理由，更合理的是先明确需要改善 RAM 命中、磁盘保留、回源字节还是写放大，再选择对应层的策略。

## 商业 CDN 的公开设计：体系能力比单一淘汰算法更重要

下表归纳公开能力，不代表各厂商内部实现相同，也不构成产品排名。

| 方向 | 公开实例 | 对 Razor 的启示 |
|---|---|---|
| 分层与源站保护 | Cloudflare Tiered Cache、Regional Tier、Cache Reserve；Fastly clustering/shielding | 跨节点把相同资源聚拢，增加合并机会；额外网络跳数与命中率有取舍 |
| 原生分段与部分命中 | Fastly Segmented Caching 只补缺失对齐区间，完整 GET 也视为全范围请求 | 以对象版本统领可独立缓存的块，避免每个原始 Range 都形成互不复用的对象 |
| 共享填充与增量返回 | Fastly request collapsing 与 Streaming Miss | 网络生产者、缓存保留与各客户端发送进度应分开管理 |
| 持续协议演进 | Cloudflare 2026-05-04 公布 Pingora 缓存代理切换、异步 stale-while-revalidate；2026-07-02 公布 Vary 规范化/透传/旁路规则 | 热路径、变体身份、请求合并和后台刷新一起设计，不能只替换 I/O 或淘汰队列 |
| 主动失效 | Cloudflare Rust/RocksDB CacheDB 按索引处理 PURGE；返回 HIT 前检查待执行失效 | 逻辑不可见与物理回收解耦，但被失效内容必须立刻停止对外命中 |

来源：[Cloudflare CDN 架构](https://developers.cloudflare.com/reference-architecture/architectures/cdn/)、[Fastly 分段缓存](https://www.fastly.com/documentation/guides/full-site-delivery/caching/segmented-caching/)、[Fastly 合并及 Streaming Miss](https://www.fastly.com/documentation/guides/concepts/cache/request-collapsing/)、[Cloudflare 2026-05-04](https://developers.cloudflare.com/changelog/post/2026-05-04-pingora-powers-cache/)、[Cloudflare 2026-07-02](https://developers.cloudflare.com/changelog/post/2026-07-02-vary-for-cache-rules/)、[CacheDB 主动失效](https://blog.cloudflare.com/instant-purge/)。

Fastly 明确说明：分段缓存与压缩/ESI 等有兼容限制；其 Streaming Miss 可在未完成整个响应时发送数据。Razor 首版选择“完整块校验并提交后才发送”，不能据此声称具有相同粒度或延迟。以上公开能力也不能用于推断商业 CDN 实际使用某种未公开的全局淘汰算法。

## Razor 如何支持多策略：按决策和资源单位分开

以下是设计建议，尚未实施，不扩大当前 #16/#17/#20/#19 的交付范围。

1. **协议准入固定在 HTTP 层。** `private`、Authorization/Cookie、Vary、源站版本和 PURGE 代次继续由现有契约判断。性能策略不得把不安全内容变成可共享缓存。
2. **保留准入和淘汰分开。** 是否值得长期写入某层，与空间不足时淘汰哪个条目，是两项决策。准入可结合大小、剩余 TTL、历史复用和写入预算；先以允许全部合规对象为基线，用 trace 验证过滤收益。
3. **RAM 复用现有封装。** 当前 Get/Put/Delete/容量统计是自然接口。保留 Otter；有真实基准需求时添加一个 S3-FIFO 或 SIEVE adapter，再形成有限策略选择。按字节计费，包含元数据开销，避免只限制条目数。
4. **磁盘区分策略与分配器。** 当前 Stripe 在写指针处回收空间，不能靠一个任意 key 的 `Victim()` 把它变成对象级 LRU。第一步保留追加布局，独立评估准入与有预算的热块重插入；若需要真正可选的 region 回收顺序，必须让分配器支持选取 region，并处理索引切换、恢复与读者保护，另立存储任务。
5. **粒度与负载一致。** partial caching 按块记录命中与保留价值，manifest 元数据单独限额；不因访问文件开头就把整个 100 MiB 文件提升为热对象。不同租户或资源类别可以有独立容量池，尚无需求时不增加控制面。
6. **RWW 临时驻留与长期保留分开。** 正在发送的已提交块需要有界读者保护；拒绝长期缓存不能使订阅者等在一个永远不会发布的块上。策略只能决定可安全淘汰的候选，具体读者引用、过载拒绝、持久提交和失效保护仍由存储/任务模块执行。
7. **有限配置，先不做热切算法。** RAM 与磁盘保留策略分别配置，不设误导性的全局 `cache.algorithm`；首版切换按重启生效，保留数据格式，策略状态冷启动。准入/淘汰策略不拥有正文写入和 HTTP 响应权限。

每次读命中只做有界、低开销的热度记录，避免为算法维护新增全局链表锁。需要至少两个实际策略时再抽取共享接口，不为尚未实现的算法建立通用插件框架。

## 验证顺序与指标

本轮优先完成安全合并回源、partial caching、可配置块大小和已提交块 RWW。算法实验后置，不抢占这两项重点。

算法选择用同一请求轨迹回放，覆盖热点 + 一次性长尾、顺序扫描、热点切换、256 KiB 与 100 MiB 混合，以及随机/重叠 Range。工作集必须超过缓存容量并运行到发生多轮淘汰；当前能全部驻留的热 HIT 吞吐不能证明淘汰算法更好。RAM/SSD/HDD 各自比较相同字节预算，加入崩溃恢复和 PURGE 回归。

报告同时给出请求命中率、块命中率、源站实际正文流量、重复填充、磁盘写放大、CPU/锁竞争、元数据内存、P99/TTFB 和恢复成本。对合并请求，客户端总字节与源站字节的比值是“回源卸载”而不完全等于“缓存命中率”；另记录强制不保留缓存基线，避免把合并收益误算成淘汰算法收益。

本次仅新增调研文档，未修改服务代码，也未运行 ATS、CacheLib 或算法回放对照实验。算法收益和商业 CDN 内部未公开实现均不能据此推定。
