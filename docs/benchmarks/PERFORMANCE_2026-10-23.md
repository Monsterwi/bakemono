# 第一轮热路径优化（2026-10-23）

基线 `193fe7d`，优化 `e1b158a`；以下是同轮对照。最新实现与 ATS 的比较见 [新报告](ATS_COMPARISON_2026-10-24.md)。

| 256 KiB / C100 | 原 Razor | 优化 Razor | ATS 10.2.1 native |
| --- | ---: | ---: | ---: |
| QPS | 11,436 | 28,307 | 39,590 |
| 完整响应 P99 ms | 13.63 | 15.10 | 8.28 |
| CPU μs/请求 | 398 | 288 | 193 |
| RSS 峰值中位数 MiB | 34.8 | 44.8 | 59.7 |

**结论**：吞吐提高 2.48 倍，但 P99 增加 10.8%。已移除 HIT 创建 FillToken、打开对象排他锁和 Release(nil) 目录锁；复用 256 KiB 发送缓冲。主要问题是可修复的串行化，不是已证明的架构上限。

**剖析**：多 goroutine 累计 mutex 等待由约 553.5 秒降至 1.185 秒；优化后 CPU flat：系统调用 46.08%、复制 9.70%、CRC 8.74%。这些是诊断运行采样，不是请求耗时占比。

**条件**：同机 HTTP/1.1、GOMAXPROCS=8、无 TLS/绑核，3×5 秒取中位数；热 HIT 零回源、零实际磁盘读取。原版当日复测约 12k，不把旧报告约 5k 的跨日期差异计入优化收益。

**验证/限制**：HDD/SSD 功能各 18/18，200 MiB 回归及 CI 通过；RSS 增加。ATS 默认关闭 checksum，Razor 保留 CRC。最小 net/http 原型不含缓存语义，不能当作公平产品成绩。

证据：[三轮结果](PERFORMANCE_2026-10-23.json) · [脚本/配置/pprof](PERFORMANCE_2026-10-23.raw.tar.gz) · [原完整记录](https://github.com/Monsterwi/razor/blob/8581267/docs/benchmarks/PERFORMANCE_2026-10-23.md)。
