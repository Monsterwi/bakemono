# 初期端到端验收（2026-10-20，历史）

性能源码 `d2c6256`；最终功能复验 `6672737`。仅适用于当时版本，当前能力见 [实现状态](IMPLEMENTATION_STATUS.md)。

| 项目 | 结果 |
| --- | --- |
| 正式负载 | 64 批、49,480 请求、非预期错误 0；含 30 个预期过载 503 |
| 功能 | 19 PASS、1 LIMITATION；追加重叠 Range 请求不混入正式分母 |
| 文件 / 缓存 | 256 KiB、200 MiB；真实主程序与磁盘 |
| 合并 / 部分缓存 | 冷对象单生产者；跨偏移并发只取 40 MiB 对齐并集，随后零回源 |
| 当时限制 | 全源站不健康时 fresh Range 不可用；已由后续版本处理 |

**条件**：开发机 8 逻辑核，HTTP/1.1 loopback；对照 ATS 9.2.3，3×4 秒热读。ATS 直接 I/O 与 Razor 页缓存路径不同，不是纯磁盘公平对比，也不是长期生产验收。

工具：[acceptance.py](../tools/acceptance.py)。原始目录 `/workspace/data/bakemono-10-14/results/`；run-id、二进制 SHA、完整表与故障记录见 [原完整记录](https://github.com/Monsterwi/razor/blob/8581267/docs/ACCEPTANCE_2026-10-20.md) 和 [#14](https://github.com/Monsterwi/razor/issues/14)。
