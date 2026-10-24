# ATS 对比：历史版本（2026-10-22）

历史测量：Razor `193fe7d` 对比 ATS 10.2.1。当前实现见 [最新热命中对照](ATS_COMPARISON_2026-10-24.md)，不要用本表评价最新 main。

| SSD / 256 KiB | C32 QPS | C100 QPS | C100 完整响应 P99 ms |
| --- | ---: | ---: | ---: |
| Razor | 4,950 | 4,959 | 23.11 |
| ATS native | 49,029 | 47,835 | 7.63 |
| ATS Slice | 23,193 | 23,349 | 6.78 |

**范围**：HDD/SSD、256 KiB/100 MiB、200 MiB 回归；72 热压测组、18 混合组、18 文件冷读及预取敏感性。

**条件**：同机 HTTP/1.1、无 TLS、GOMAXPROCS/网络线程=8、每产品 1 GiB 磁盘/10 MiB RAM，三轮中位数；每轮前后 SHA256，轮内逐响应检查长度/状态/读取错误。P99 是各轮 P99 中位数。

**其他结论**：Razor 重叠 Range 只回源对齐并集；ATS Slice 本配置有重复块回源。100 MiB 热读受页缓存影响，不能当作磁盘性能优势。ATS 默认 checksum 关闭、使用直接 I/O，与 Razor 成本不同。

**限制**：当日较低 QPS 不能直接与跨日期数字计算优化收益；ATS Slice 异常未完成唯一根因诊断。无 WAN/TLS/H2 或生产容量承诺。

证据：[三轮 JSON](ATS_COMPARISON_2026-10-22.json) · [原始数据与配置](ATS_COMPARISON_2026-10-22.raw.json.gz) · [原完整记录](https://github.com/Monsterwi/razor/blob/8581267/docs/benchmarks/ATS_COMPARISON_2026-10-22.md)。
