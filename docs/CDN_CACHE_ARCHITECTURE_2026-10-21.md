# CDN 缓存架构调研（2026-10-21 快照）

历史调研，非当前产品排名或持续版本追踪；Razor 现状见 [UML 架构](ARCHITECTURE.md)。原详细论证保留在 Git 历史。

## 分层策略

| 层 / 参考 | 机制 | 设计启示 |
| --- | --- | --- |
| ATS 10.2.0 RAM | CLFUS / LRU（默认）/ S3-FIFO（experimental，不支持压缩） | RAM 淘汰不等于磁盘布局；ATS 不是只有 FIFO |
| ATS 磁盘 | stripe 环形追加、覆盖；可保护在用片段及 hit evacuation | 物理覆盖与对象保留分开；发布源码 hit_evacuate_percent 默认 0，文档旧段落有差异 |
| CacheLib RAM | LRU / 2Q / TinyLFU，延迟 promotion | 控制命中维护成本 |
| Navy SSD | 小对象 BigHash 桶、大对象 BlockCache region；LRU/FIFO/segmented FIFO 与重插入 | 按 I/O/回收单位设计策略，不能用任意 key Victim 接口替换环形分配器 |
| 准入 / 写预算 | seen filter、NVM 准入、写入预算、在途插入上限 | 缓存安全准入、热度、SSD 寿命和过载分开 |

固定依据：[ATS 配置](https://github.com/apache/trafficserver/blob/b1df7334b39a878971c376da849e8da4984918b6/src/records/RecordsConfig.cc)、[ATS S3-FIFO](https://github.com/apache/trafficserver/blob/b1df7334b39a878971c376da849e8da4984918b6/src/iocore/cache/RamCacheS3FIFO.cc)、[ATS 存储](https://docs.trafficserver.apache.org/en/10.2.x/developer-guide/cache-architecture/architecture.en.html#storage-layout)、[Navy](https://cachelib.org/docs/Cache_Library_Architecture_Guide/navy_overview/)、[CacheLib RAM](https://cachelib.org/docs/Cache_Library_User_Guides/eviction_policy/)、[固定 region 接口](https://github.com/facebook/CacheLib/blob/3fbfbbd6f7a5c6c69b096741621fe373e6a147a6/cachelib/navy/block_cache/EvictionPolicy.h)。

## 可借鉴能力

| 方向 | 公开参考 | 边界 |
| --- | --- | --- |
| 分层 / Shield | [Cloudflare](https://developers.cloudflare.com/reference-architecture/architectures/cdn/) | 跨节点聚拢提高复用，增加额外网络跳数 |
| 对齐分段 | [Fastly segmented caching](https://www.fastly.com/documentation/guides/full-site-delivery/caching/segmented-caching/) | 只补缺块；压缩/ESI 等有兼容限制 |
| 合并 / 增量返回 | [Fastly collapsing / Streaming Miss](https://www.fastly.com/documentation/guides/concepts/cache/request-collapsing/) | 网络生产、缓存保留、客户端发送进度分离；不等于 Razor 的完整块提交边界 |
| 代理 / 变体治理 | [Pingora 2026-05-04](https://developers.cloudflare.com/changelog/post/2026-05-04-pingora-powers-cache/)、[Vary 2026-07-02](https://developers.cloudflare.com/changelog/post/2026-07-02-vary-for-cache-rules/) | 请求身份和缓存语义与热路径一起设计 |
| 主动失效 | [Cloudflare CacheDB](https://blog.cloudflare.com/instant-purge/) | 逻辑不可见先于物理回收 |
| 低维护淘汰 | [S3-FIFO 论文](https://www.cs.cmu.edu/~rvinayak/papers/s3-fifo-sosp-2023-fifo-queues-are-all-you-need-for-cache-eviction.pdf)、[SIEVE](https://www.usenix.org/conference/nsdi24/presentation/zhang-yazhuo) | 论文实验不等于本项目吞吐；命中不重排也不等于整个系统无锁 |

## 对 Razor 的结论

Otter 默认、S3-FIFO 可选已落地，见 [RAM 契约](RAM_CACHE.md)。HTTP 准入、对象身份、PURGE 不交给淘汰策略；RAM 与磁盘按各自资源单位决策，不建立尚无需求的通用算法插件框架。

后续候选：有预算的块保留/重插入、磁盘准入与写预算、必要时 region 分配器。均需单独 Issue，不属于已实现能力。

验证使用超过容量的同一轨迹，覆盖热点/一次性扫描/切换/重叠 Range；同时记录命中、回源字节、写放大、CPU/锁、元数据、P99/TTFB 和恢复成本。客户端/源站字节比是回源卸载口径，不是严格缓存命中率。本次调研没有运行厂商公平对照，不能推断未公开算法或性能领先。
