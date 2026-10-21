# RAM 策略开发机短轨迹校准（2026-10-21）

来源代码 `a57bd23`（功能提交 `e96fcb8`，已合已验证集成 `318a0ee`）。Go 1.26.10，linux/amd64，开发机 Intel Core Processor (Skylake, IBRS)，benchmark CPU suffix 8。这里没有专用 CPU 隔离，期间还有编译/测试；不作两种策略性能优劣结论。

```bash
go test ./cache -run '^$' -bench '^BenchmarkRAMTrace$' -benchmem -benchtime=10000x -count=3
```

96 组配置各三轮，共 288 行，PASS。每组 10 MiB，metadata 256 B / 256 KiB / 1 MiB / 4 MiB；工作集约 0.5 / 1 / 4 倍容量；scan / hot-scan / hot-switch / read-write。预热工作集尾部，确保四倍工作集扫描在短轨迹中也发生淘汰；热点集最多128项以允许反复访问。全部四倍工作集 scan 三轮均产生了淘汰。所有原始输出保留于同目录 txt。值是共享的不可变 fixture，逻辑 resident 与分配器 heap 不是相同概念；本测试没有 HTTP、磁盘或实际正文复制。

以下是四倍工作集、hot-scan 的三轮中位数，用于校准轨迹，不能代替长时间或端到端测量。

|策略|正文大小|ns/op|命中率|淘汰数|resident 字节|ghost 条数|估算元数据字节|
|---|---:|---:|---:|---:|---:|---:|---:|
|otter|256|640.1|0.7898|2102|10485760|0|5770744|
|otter|262144|469.8|0.9936|64|10485760|0|5482|
|otter|1048576|372.5|0.9988|12|10485760|0|1367|
|otter|4194304|442.8|0.9998|2|8388608|0|272|
|s3-fifo|256|266.2|0.7898|2102|10485760|2102|6067126|
|s3-fifo|262144|171.8|0.9960|40|10485760|32|9898|
|s3-fifo|1048576|163.2|0.9991|9|10485760|7|2326|
|s3-fifo|4194304|162.9|0.9998|2|8388608|2|544|

Otter 维护允许临时超预算；采样 peak 和 settled resident 已在原始结果分别输出，无法捕捉所有短时峰值。S3-FIFO payload 在 Put 返回前收敛；所有策略的 key/node/map/ghost、持有旧 slice 的读者及 runtime 额外计入内存。`metadata-est-B` 只按节点和 key 估算，不包含 allocator、map slack、Otter sketch/buffer；不是 RSS 上界。短轨迹 GC 可能为 0，不证明长期无 GC。

CPU/GiB、HTTP P99、物理磁盘读写、长时间堆/RSS 和至少 2 小时/策略 SSD soak 仍由 #19 的 HDD/SSD 真二进制验收记录，此文件不表示这些门槛已通过。
