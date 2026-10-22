# Razor RWW 能力速览

当前 HTTP 服务采用**块级、提交后可读**的 RWW。整对象未填完不妨碍发送已完成块，但当前块必须完整校验并提交。

| 项目 | 当前实现 |
| --- | --- |
| 普通整对象路径 | 完整提交后响应 |
| 单 Range / 合格公共大 GET | 首个所需块提交后发送；共享 manifest 和块 |
| 块大小 | 默认 1 MiB；64 KiB–8 MiB 的 2 次幂；重启生效 |
| 并行窗口 | 发送当前块，同时最多准备下一块 |
| 读者 | 独立偏移、取消与发送缓冲，共享块生产任务 |
| 失败与失效 | 后续块失败或已观察到的版本/PURGE 失效中断响应，避免拼接错误内容 |
| 未提交字节级 HTTP RWW | 未支持；底层活跃 Writer API 不等于 HTTP 行为 |
| 持久化 | 不承诺逐块 fsync、异常退出零丢失 |

- [合并回源与原生部分对象缓存](CACHE_FILL_DESIGN.md)：共享身份、任务生命周期、manifest、缺块和离线边界。
- [RWW 实现](RWW_IMPLEMENTATION.md)：发送时序、提交、背压、失败与行为回归。
- [与 ATS 的机制对比](RWW_ATS_COMPARISON.md)：区分 RWW、请求合并和插件分块，不给出未经对照的性能结论。
- [实际验收](benchmarks/CDN_ACCEPTANCE_2026-10-21.md)：256 KiB、100 MiB、200 MiB，HDD/SSD 与两种 RAM 策略的测量范围。
