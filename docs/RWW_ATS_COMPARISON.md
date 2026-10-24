# Razor 与 ATS：机制对照

ATS 为 2026-10-22 查阅的 10.2.x 快照，非持续版本追踪；跑分见 [实机报告](benchmarks/ATS_COMPARISON_2026-10-22.md)。RWW、合并回源、部分对象缓存是不同能力。

| 维度 | Razor | ATS 10.2.x 官方机制 |
| --- | --- | --- |
| 回源协调 | 有界 fillGroup，回源前 join，生产者独立取消/期限 | RWW、open-read 重试、open-write 失败策略，依配置生效 |
| RWW 边界 | 单 Range/合格大 GET：完整块提交后；普通对象：整对象提交后 | 可从正在填充对象读取；响应头、可缓存性及阈值/重试约束 |
| 部分缓存 | 原生 manifest + 对齐块，GET/Range 共用 | Slice 对齐切块和回环，配合 cache_range_requests 缓存区间 |
| 206 | 原生块校验与重组 | Range 插件以内部 200 缓存，向下游恢复 206 |
| 重叠区间 | 同代固定缺块共享任务 | Range 插件本身不负责对齐去重，需 Slice 配合 |
| 一致性 | 源站/强版本/布局/PURGE 围栏；单进程范围 | 按部署版本、插件及配置核实，本文不推断内部等价 |

ATS 并非“不支持 RWW/206”。Razor 选择完整块校验后发布，冷请求至少等待首块接收、提交和读取；原生整合不证明性能领先。发送管线见 [UML 时序](RWW_IMPLEMENTATION.md#管线时序)。

固定参考：[RWW/重试](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/configuration/cache-basics.en.html#reducing-origin-server-requests-avoiding-the-thundering-herd)、[Slice](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/plugins/slice.en.html)、[Range 插件](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/plugins/cache_range_requests.en.html)。
