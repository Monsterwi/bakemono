# RAM 缓存策略

默认 `otter`（v2.2.1 adaptive W-TinyLFU），可选 `s3-fifo`。配置、每 stripe 默认 10 MiB 与 0 禁用见 README；两项重启生效，不参与磁盘内容身份，不修改持久格式或读盘 promotion。

## 共同契约

Get/Put/Delete/Items/Size/CleanUp/Stats 均通过显式方法，不依赖嵌入 Otter。写入的 slice 保持现有只读约定，不在更新时修改旧读者持有的 value；同步更新/删除完成后不由旧队列事件复活。允许淘汰导致 MISS；磁盘层继续校验 directory/CRC/版本/PURGE 代次。覆盖成超大值时先删除旧 RAM 映射，避免错误保留旧值。单项 value 超过预算或 uint32 权重、key 超过 64 KiB，直接跳过 RAM。

`Size` 与 `ChargedBytes` 是 resident 权重：序列化 cache value 长度，空值最少记 1 字节。`Stats.ResidentBytes` 统计实际 `len(value)` 之和；真实磁盘路径的 value 可包含 Doc header、HTTP 元数据、manifest 及正文，不是纯 HTTP 正文。ghost 不存原 value。Otter 有异步维护，短时可超权重，CleanUp 后检查收敛；S3-FIFO 在 Put 返回前按权重淘汰。预算不约束 RSS，读者仍可能持有已淘汰 value，分配器也可能保留堆页。

两策略 key 长度上限 64 KiB；空项最小权重使驻留条数受容量约束（Otter 稳态至多预算字节对应的条数，暂时超量按其维护模型）。S3-FIFO resident 另限 `min(capacity, 65536)` 条；ghost 同样限条数，历史权重之和不超过容量，估算 key/node 总成本不超过 `max(128, capacity/10)` 字节。很小预算可能不保留 ghost。S3 resident 节点的最坏元数据上界可接近 `65536 × (128 + 65536)` 字节，远大于默认 10 MiB value 预算；真实磁盘路径的 key 通常较短。Otter 的条数上界也随字节预算增长。这些界限避免无界增长，不代表小常数额外内存或对恶意 key 的 RSS 保证。节点、map、队列、key 元数据与序列化 value 预算分别说明，不能把 value 权重上限宣传成总内存上限。

`Server.Stats().RAM` 按 stripe 返回 `Algorithm/CapacityBytes/ResidentBytes/ChargedBytes/ResidentEntries/GhostEntries/GhostBytes/EstimatedMetadataBytes` 及通用 Hits/Misses/Evictions/EvictionWeight。`GhostBytes` 是记住的原 value 权重之和，**不是 ghost 保存的 value 字节**。S3 另有 SmallEntries/MainEntries/Promotions/GhostHits。每项元数据估算为 `128 + key bytes`，不包含 map 空桶、allocator slack、Otter sketch/buffer、运行时；实际 heap/RSS 由进程采样确定。Otter 使用官方并发安全 All 迭代器采样 value 与 key，快照弱一致，Stats 只适合采样，不应每请求调用。

## S3-FIFO 变体与归属

参考固定的 Apache Traffic Server 10.2.0 [RamCacheS3FIFO.cc](https://github.com/apache/trafficserver/blob/b1df7334b39a878971c376da849e8da4984918b6/src/iocore/cache/RamCacheS3FIFO.cc)，Apache-2.0；归属保留于 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)。不是未经许可复制论文 artifact。

新项进入 small FIFO；ghost 再入进入 main；small 中至少命中 2 次才晋升，晋升频率清零；main 用 0–3 饱和计数，逐圈减 1，零时淘汰。main 超过容量 90% 或 small 为空时从 main 淘汰，否则从 small 淘汰。命中不移动队列。沿用 ATS 的 main 队列目标判断，而不是强制 cold-start small 永远只占 10%。与 ATS 的差别：序列化 value 预算独立于外部 node/key 元数据，resident/ghost 上限如上；更新替换 value，Delete 同时清除 ghost；更新计一次引用且不改变 FIFO 位置；单 mutex 保护全部状态，无异步发布队列，不宣称无锁或总内存精确上限。

## 回归与可复用基准

```bash
go test ./cache ./config ./server -count=1
go test -race ./cache -run 'TestRAM|TestS3FIFO' -count=1
go test ./cache -run '^$' -bench '^BenchmarkRAMTrace$' -benchmem -benchtime=2s -count=3 > ram-trace.txt
```

基准相同 10 MiB 预算、确定性轨迹，256 B metadata / 256 KiB / 1 MiB / 4 MiB 正文；工作集约 0.5/1/4 倍容量，顺序扫描、热点加扫描、热点切换、混合读写。报告 ns/op、逻辑字节率、allocs、GC、请求/字节命中、resident、ghost 和近似元数据。大对象按完整条目取整，不能精确等于容量；采样 peak 是观察值，不是未观察到的短时超量上界。纯 RAM 轨迹不会产生磁盘 I/O 或 HTTP P99，这两项由真实二进制 HDD/SSD 验收单独记录，不凭微基准宣布赢家。

MB/s 只按请求引用的逻辑正文大小折算，基准不复制整个正文，不能作为 HTTP 或磁盘吞吐。开发机三轮短轨迹校准见 [原始输出](benchmarks/RAM_POLICY_2026-10-21.txt) 与 [说明](benchmarks/RAM_POLICY_2026-10-21.md)；同源码两策略/HDD/SSD三轮矩阵和两次全新SSD各两小时实测已通过，见[专机验收报告](benchmarks/CDN_ACCEPTANCE_2026-10-21.md)。报告保留策略适用范围、累计分配/GC和弱一致快照边界，不把单机结果当通用性能胜负。

本次 Otter 的 HDD矩阵与 SSD长测分别观察到 ResidentBytes 为11,037,524B（10.53MiB）和11,282,849B（10.76MiB）的快照，均超过配置10MiB。Items/value汇总与策略ChargedBytes不是原子同刻取得，异步维护也是已知边界；不能将独立峰值合成精确同步驻留量、唯一归因或同步预算违约证明。完整外部元数据、heap/RSS及峰值/趋势以报告和原始统计为准。
