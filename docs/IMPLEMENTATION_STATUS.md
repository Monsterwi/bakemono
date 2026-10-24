# 实现状态

本页描述 `perf/http-hotpath`；已发布/合并状态与 [GitHub Issues](https://github.com/Monsterwi/razor/issues) 分开记录，#1 保持打开。设计入口：[UML 架构](ARCHITECTURE.md)。

## 能力

| 能力 | 当前行为 / 边界 |
| --- | --- |
| HTTP | 标准库代理、TLS/H2、可信转发、源站证书校验、健康探测、取消和排空 |
| 共享缓存语义 | 显式 freshness、Age/Date、s-maxage、请求约束、条件 GET/HEAD、304 更新；不提供启发式 freshness |
| Vary / 准入 | 仅 Accept-Encoding/Accept-Language 及组合，每 URL 最多 16 普通表示；私有/未知语义保守旁路 |
| 合并回源 | 回源前原子 join；独立生产者/取消/期限；普通对象完整提交后独立读取 |
| 部分对象 | manifest + 独立固定块，Range/合格公共大 GET 共用；强 ETag、固定源站和布局，不混版 |
| RWW | 首块完整校验并提交后发送，同时最多准备下一块；默认 1 MiB，可配 64 KiB–8 MiB |
| 离线 Range | 同一仍配置源站的 manifest 和整个所需区间均 fresh、完整可读时零回源；不是 stale-if-error |
| RAM | Otter 默认、S3-FIFO 可选；序列化 value + 解码元数据估算计费，非 RSS 硬上限 |
| 磁盘 | CRC/身份/覆盖代次校验；头片段最后发布；只恢复验证通过的干净检查点 |
| Query | 原始参数项排序，缓存/任务/PURGE 同规则；源站保证顺序无关，实际回源不重排 |
| 重载 | 不可变 runtime 租约，原子替换，最多一个未排空旧 runtime；部分设置须重启 |

## 运行边界

| 类别 | 限制 |
| --- | --- |
| PURGE | 默认关闭；Bearer token 启用；精确/路径前缀失效普通表示与 Range 家族；旧 token 不复活，当前代次可保守影响其他在途任务 |
| Range | HEAD/总长/强 ETag/identity；多 Range、弱 ETag 等旁路；合成下游响应 no-cache；后块失败中断流 |
| 容量 | max_object_bytes 默认 64 MiB；大对象测试须调大；块按必要窗口，不要求磁盘容纳整文件 |
| 索引 | 每 stripe 至多 min(目录容量, 65,536) 映射、16 MiB 键/selector 字符串、每键/selector 16 KiB；URL manifest 快照上限 64 |
| 任务/资源 | 默认 16 生产任务、每任务 256 订阅者、300 秒；请求/源站/写者/读者/队列另限额，非总 RSS 预算 |
| 持久性 | 提交不等于逐块 fsync；异常退出可冷 MISS，不截断旧文件；同缓存 inode 不跨进程共享 |
| 未支持 | 未提交字节级 HTTP RWW、多 Range 块缓存、跨节点合并/一致性/PURGE、HTTP/3、io_uring/native AIO |

完整边界：[填充/失效](CACHE_FILL_DESIGN.md)、[RWW](RWW_IMPLEMENTATION.md)、[RAM](RAM_CACHE.md)、[存储](CACHE_STORAGE_FORMAT.md)。

## 验收证据

| 源码 / 报告 | 结果与范围 |
| --- | --- |
| 5ccf32c / [#19](benchmarks/CDN_ACCEPTANCE_2026-10-21.md) | HDD/SSD，256 KiB/100 MiB、200 MiB 回归；26 项通过，Otter/S3-FIFO 各全新两小时 SSD 长测，共 245,762 业务 GET |
| 193fe7d / [ATS 对照](benchmarks/ATS_COMPARISON_2026-10-22.md) | ATS 10.2.1 native/Slice，72 热压测组、18 混合组、18 冷读；小文件仍落后 |
| e1b158a / [#18](benchmarks/PERFORMANCE_2026-10-23.md) | 同轮 C32/C100 吞吐 2.30/2.48 倍；C100 P99 +10.8%，不是全指标改善 |
| 77e5186 / [#29](benchmarks/HTTP_HOTPATH_2026-10-23.md) | 对上一版 C100 QPS +7.5%、P99 −7.1%；100 MiB 吞吐 −1.5%～−4.1%；功能/旧缓存恢复通过 |

1964fa3 的 [构建与测试](https://github.com/Monsterwi/razor/actions/runs/40073588676) / [coverage](https://github.com/Monsterwi/razor/actions/runs/40073588666) 均通过；#29 未关闭，性能分支未合并 main。图文修订以后续提交为准，不能称为重新压测。

所有结果绑定报告内版本和工况；同机 HTTP/1.1 与两小时长测不是公网/TLS/H2 容量或长期生产保证。旧报告的“RWW 待实施、离线失败”等只适用于对应历史版本。
