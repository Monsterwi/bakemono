# Razor 与 ATS：RWW、请求合并和部分缓存

本文比较机制，不比较跑分。Razor 行为以源码基线 `c93f4d7` 为准；ATS 参考 2026-10-22 查阅的官方 10.2.x 文档，具体能力和配置须对应部署版本。

## 先区分三个问题

- **请求合并**：等价并发 MISS 是否只由一个生产者向源站获取相同数据。
- **RWW**：对象尚未填完时，其他客户端能否开始读取已经可用的数据。
- **部分对象缓存**：对象仅有某些区间时，能否独立缓存、复用并补齐缺失区间。

具备 RWW 不自动等于具有可独立复用的缺块缓存；具备多个 Range 缓存条目也不自动解决重叠区间的去重。

## 实现对照

| 维度 | Razor 当前路径 | ATS 官方说明 |
| --- | --- | --- |
| 在途请求协调 | 回源前由有界 `fillGroup` 协调，任务拥有独立取消/期限；块另有写入所有权保护 | 支持 Read While Writer、open-read 重试及 open-write 失败策略；具体行为依赖配置 [RWW/重试说明](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/configuration/cache-basics.en.html#reducing-origin-server-requests-avoiding-the-thundering-herd) |
| 流式可读边界 | 单 Range/合格公共大 GET 的块完整校验并提交后发送；普通对象仍等整对象提交 | 确认响应头与可缓存性后，RWW 允许读取正在填充的对象，受相关阈值和重试配置影响 [Read While Writer](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/configuration/cache-basics.en.html#read-while-writer) |
| 分块获取与重组 | 核心服务直接处理 manifest、固定块和 GET/206 响应，不需要插件回环 | Slice 将完整/单 Range 请求拆为对齐区间，经 ATS 回环请求并配合 cache_range_requests 缓存 [Slice](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/plugins/slice.en.html) |
| 单独缓存区间 | 每块有源站、强 ETag、Incarnation、变体、布局和块号身份 | cache_range_requests 可把 Range 加到缓存键，将源站 206 改成内部 200 缓存，向客户端恢复 206 [Range 插件](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/plugins/cache_range_requests.en.html) |
| 重叠区间 | 复用固定块，只填所需对齐并集 | cache_range_requests 单独不负责高效处理重叠区间；Slice 负责对齐切块 [Slice](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/plugins/slice.en.html) / [Range 插件](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/plugins/cache_range_requests.en.html) |
| 容量、部署和性能 | 有限 HTTP 子集，单进程失效；以仓库实际验收为准 | 本文未对 ATS 10.2 做同版本、同负载硬件验收，不推导性能高低 |

ATS 的官方插件文档区分“未缓存 Range”和“从完整缓存对象服务 Range”，并提供插件实现单独区间缓存（[官方说明](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/plugins/cache_range_requests.en.html)）。因此，不能把差异写成“ATS 不支持 RWW”或“ATS 完全不能缓存 206”。Razor 的差异是将对齐缺块、任务合并、版本/PURGE 隔离与提交后读取放在同一原生路径中；这不是性能胜出的证明。

## 对本项目的取舍

Razor 选择块级发布，避免将尚未完成长度/Trailer 校验的当前块交给客户端。代价是冷请求至少等待首块接收、提交和读取；普通整对象路径也尚未具备字节级流式广播。块大小、窗口及资源预算的实际行为见 [RWW 实现](RWW_IMPLEMENTATION.md)，共享契约见 [缓存填充设计](CACHE_FILL_DESIGN.md)。

性能判断须使用相同硬件、对象与 Range 分布、协议、并发、缓存策略及冷/热条件。不同提交和机器的历史测量保持原记录，不用于推算当前领先幅度。

## 官方资料

1. [ATS 10.2 HTTP Proxy Caching：Read While Writer / 重试 / 写失败策略](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/configuration/cache-basics.en.html#reducing-origin-server-requests-avoiding-the-thundering-herd)
2. [ATS 10.2 Slice Plugin](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/plugins/slice.en.html)
3. [ATS 10.2 Cache Range Requests Plugin](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/plugins/cache_range_requests.en.html)
