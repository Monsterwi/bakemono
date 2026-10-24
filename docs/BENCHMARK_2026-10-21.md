# 早期 100 MiB 验证（2026-10-21，历史）

主矩阵源码 `a8d6a86`，工具 `f101787`；当前能力见 [实现状态](IMPLEMENTATION_STATUS.md)，本结果不代表最新实现。

| 项目 | 结果 |
| --- | --- |
| HDD + SSD 主矩阵 | 94 批、342,271 请求，非预期错误 0；含 60 个预期过载 503 |
| 功能 | 各介质 19 PASS、1 LIMITATION |
| 文件 | 256 KiB / 100 MiB，200 MiB 兼容 |
| 场景 | 冷/热、合并、重叠 Range、慢源/慢客户端、取消/PURGE、重启/回卷 |
| 当时边界 | 保守整对象路径；跨 IP/UA 公共合并、离线 Range、块级 RWW 由后续版本完善 |

**方法**：专机 E5-2620 v4，HTTP/1.1 loopback、每产品 1 GiB 磁盘/10 MiB RAM。主压力三轮、逐响应 SHA；hey 交叉测量和文件冷读单列。页缓存命中不等于物理盘吞吐，短时结果不代表生产容量。

工具：[acceptance.py](../tools/acceptance.py)、[hey_benchmark.py](../tools/hey_benchmark.py)。原始 run-id `cdn-20261021T033401Z`，索引及完整表见 [原完整记录](https://github.com/Monsterwi/razor/blob/8581267/docs/BENCHMARK_2026-10-21.md)、[#15](https://github.com/Monsterwi/razor/issues/15)；后续验收见 [#19 报告](benchmarks/CDN_ACCEPTANCE_2026-10-21.md)。
