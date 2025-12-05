# Bakemono 缓存优化总结文档

本文档总结了近期对 `bakemono` 项目进行的架构重构和性能优化。这些优化深度参考了 Apache Traffic Server (ATS) 的设计理念，成功实现了高并发、低延迟、低内存占用的大文件缓存系统。

## 1. 核心架构重构：Stripe (条带) 级并发

为了解决全局大锁导致的并发瓶颈，我们将存储架构从单一 `Vol` 重构为多 `Stripe` 并行结构。

*   **物理与逻辑分离**：物理存储文件 (`Vol`) 被逻辑划分为多个固定大小的 `Stripe`（默认约 1GB/Stripe）。
*   **资源隔离**：每个 `Stripe` 拥有独立的：
    *   **锁机制**：`sync.RWMutex`，锁粒度从文件级降低到 Stripe 级。
    *   **元数据管理**：独立的 `DirManager`（目录）和 `WritePos`（写入游标）。
    *   **写入缓冲**：独立的 `AggregateWriteBuffer`。
    *   **内存缓存**：独立的 `RamCache`。
*   **动态扩容**：`Init` 阶段根据物理文件大小动态计算最优 Stripe 数量（8-128个），在并发度与元数据开销间取得平衡。
*   **路由算法**：使用 `CRC32(Key) % NumStripes` 将请求均匀分发，大幅减少锁竞争。

## 2. 高效 I/O 子系统

### 2.1 异步双缓冲聚合写入 (Double-Buffered Async Aggregation)
实现了 ATS 风格的聚合写入机制，显著提升写入吞吐量：
*   **双缓冲设计**：使用 `bufA` 和 `bufB` 交替进行写入和刷盘。
*   **异步刷盘**：当当前 Buffer 写满时，立即切换 Buffer，后台 Goroutine 负责将满 Buffer 刷入磁盘 (`flushLoop`)。
*   **Read-While-Write (Open Read)**：实现了 `IsFullyInCache` 和 `ReadOverlay` 机制。读取操作能透明地访问尚未刷盘的 Buffer 数据，保证数据一致性的同时实现了“写穿透”般的低延迟。

### 2.2 分层读取策略 (Tiered Read Path)
严格遵循 ATS 的分层读取流程，按优先级逐层尝试，最小化磁盘 IO：
1.  **L1: RamCache** - 内存 LRU 缓存命中（最快）。
2.  **L2: Aggregation Buffer** - 检查数据是否在写入缓冲区（`IsFullyInCache`）。命中则直接内存拷贝，**零磁盘 IO**。
3.  **L3: Disk + Overlay** - 磁盘读取。读取后执行 `ReadOverlay`，将聚合缓冲区中可能存在的最新数据覆盖到读取结果中，处理跨越磁盘/内存边界的边缘情况。

## 3. 大文件与流式处理 (Streaming & Range)

针对大文件场景进行了彻底重构，支持流式读写和高效 Range 请求。

### 3.1 Key Chaining (链式 Key) 与多分片
*   **去除中心化元数据**：废弃了在 Head Chunk 中存储所有分片 Offset 的设计。
*   **Key 计算算法**：移植了 ATS 的 `NextCacheKey` 算法。Fragment N 的 Key 用于计算 Fragment N+1 的 Key。
*   **独立分片**：每个 Fragment 都是独立可寻址的实体，分别写入并更新 Directory。这使得文件大小不再受限于 Head Chunk 的元数据空间。

### 3.2 流式读取接口 (`CacheReader`)
*   **接口变更**：`Stripe.Get` 返回 `*CacheReader` (实现 `io.ReadSeeker`)，而非一次性返回所有数据。
*   **恒定内存占用**：`CacheReader` 内部维护状态，按需逐个读取分片。无论文件多大，内存中仅保留当前分片的数据。

### 3.3 高效 Range 请求 (IO Seeking)
*   **智能 Seek**：实现了 `CacheReader.Seek`。根据目标 Offset 计算出 Fragment Index，利用 `NextCacheKey` 快速迭代计算出目标分片的 Key，直接定位读取。
*   **IO 优化**：对于 Range 请求（如视频拖拽），**无需读取中间数据**，直接跳跃到目标分片，极大节省磁盘带宽。
*   **标准库集成**：接入 Go 标准库 `http.ServeContent`，自动完美处理 HTTP Range 协议（状态码 206、Content-Range 头）。

## 4. 代码与测试
*   **完整性测试**：新增了 `TestMultiFragmentLargeObject`、`TestConcurrentReadWrite`、`TestCacheReader_Seek` 等测试用例，覆盖了并发、流式、Seek 等核心场景。
*   **Demo 修复**：修复并适配了 `demo-app` 下的压力测试工具，验证了系统在高负载下的稳定性。

---
**状态**：核心优化已完成 (v1.0 Optimized)。
