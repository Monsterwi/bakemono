# Bakemono Cache TODO List

## 🔴 高优先级 - 性能关键优化

### 1. Directory 存储结构重构（最关键）
- [x] **重构 DirManager 使用 Flat Array 替代 Map**
  - 将 `map[segId][]*Dir` 改为 `[]Dir` (纯结构体数组，非指针)
  - 通过 `index * sizeof(Dir)` 直接访问，消除 Map 查找开销
  - 预期收益：减少 50-70% 内存占用，消除 GC 压力，提升 2-3x 查找性能
  - 参考：ATS 的 `CacheDir` 使用连续内存布局

- [x] **Directory 序列化优化**
  - 当前 `MarshalBinary` 使用 `bytes.Buffer`，考虑使用 `unsafe` 直接内存拷贝
  - 或使用 `mmap` 持久化 Directory，实现热重启（Hot Restart）

### 2. Direct I/O 支持
- [ ] **实现 Direct I/O 模式**
  - 使用 `syscall.Open(path, syscall.O_DIRECT, ...)` 打开文件
  - 确保 Buffer 对齐到 512 字节（或文件系统块大小）
  - 修改 `AggregateWriteBuffer` 使用对齐的 Buffer
  - 预期收益：减少内核 Page Cache 干扰，降低内存拷贝，提升写入吞吐 20-30%

- [ ] **Direct I/O 配置开关**
  - 添加配置项 `cache.direct_io` (默认 false，逐步迁移)
  - 在 `Stripe.Open()` 中根据配置选择打开模式

### 3. I/O 线程池（Worker Pool）
- [ ] **实现固定大小的 I/O Worker Pool**
  - 创建固定数量（如 `CPU核数 * 4`）的 Goroutine 作为 I/O Worker
  - 所有磁盘读写请求通过 Channel 派发给 Worker，避免 Go Runtime 线程数爆炸
  - 修改 `Stripe.diskWriter()` 使用 Worker Pool 而非直接 `WriteAt`

- [ ] **I/O 任务抽象**
  - 定义 `IOTask` 接口（ReadTask, WriteTask）
  - 使用 Channel 传递任务，Worker 执行阻塞 I/O
### 4. 支持purge功能
### 5. 多 Fragment 文件部分被覆盖时的数据一致性问题
- [ ] **添加循环覆盖场景的测试**
## 🟡 中优先级 - 功能完善

### ~~5. HTTP Vary / Alternates 支持~~
- [ ] ~~**实现 HTTP Vary 机制**~~
  - 在 `Doc` 结构或 `Dir` 中增加 Alternates 支持
  - 主 Key 下存储 "Vector" 文档，记录所有变体（gzip, br, raw）的 Key
  - 读取时先读 Vector，根据请求头选择对应变体

- [ ] ~~**扩展 CacheHTTPInfo**~~
  - 增加 `Vary` 字段存储
  - 实现 `CacheKey` 生成时考虑 Vary 头（如 `Accept-Encoding`）

### 6. 大文件分片优化 ✅
- **统一 Key 命名与生成** ✅
  - 在 `Doc` 结构中明确添加 `FirstKey` 字段，对齐 ATS。
  - 实现标准的 `FragmentKey(firstKey, idx)` 生成算法 (MD5组合)。

- **Writer 分片逻辑更新** ✅
  - 确保所有 Fragment 的 `Doc.FirstKey` 都指向 Fragment 0 的 Key。

- **Reader 链接校验** ✅
  - `loadFragment` 时校验 `doc.FirstKey == r.Key`，防止哈希碰撞导致的数据错误。
  - 实现基于 `TotalLen` 的预计算，提前知道有多少个 Fragment。

### 7. 热点防护（Hit Evacuation 完善）
- [ ] **实现 Open Read Retry**
  - 当热点对象过期时，只允许一个请求回源，其他请求等待或使用陈旧副本
  - 在 `OpenDir` 中增加 "正在回源" 状态标记

- [ ] **Thundering Herd Protection（惊群保护）**
  - 检测到大量并发请求同一 Key 时，合并请求
  - 使用 `sync.Once` 或 Channel 实现请求合并

### 8. 零拷贝和 Buffer Pool
- [ ] **实现全局 Buffer Pool**
  - 使用 `sync.Pool` 或自定义 Arena 分配器管理 Buffer
  - 避免频繁的 `make([]byte, size)` 分配
  - 在 `AggregateWriteBuffer` 和 `CacheWriter` 中使用 Pool

- [ ] **减少数据拷贝路径**
  - 当前路径：`CacheVC.Write` -> `CacheWriter.buffer` -> `fullData` -> `AggBuffer` -> Disk
  - 优化：直接向 Buffer Pool 申请 Block，传递指针而非拷贝数据
  - 仅在落盘前进行一次拷贝（如果需要对齐）

### 9. RAM Cache 策略优化
- [ ] **实现 Write-Once 流量感知**
  - 检测 "只写一次" 的大文件，避免其冲刷 RAM Cache 中的热点数据
  - 在 `RamCache.Put()` 中增加大小限制或准入策略

- [ ] **RAM Cache 预热**
  - 启动时从磁盘加载热点数据到 RAM Cache
  - 基于访问时间或频率排序

## 🟢 低优先级 - 工程优化

### 10. 锁粒度优化
- [ ] **细化 Stripe 锁粒度**
  - 将 `Stripe.mu` 拆分为 `writePosMu`、`phaseMu` 等细粒度锁
  - 减少 `handleFlushTask()` 中的锁持有时间

- [ ] **OpenDir 锁优化**
  - 考虑使用 `sync.Map` 或分片 Map 减少锁竞争
  - 或使用无锁数据结构（如 `atomic.Value` + Copy-on-Write）

### 11. 监控和诊断
- [ ] **性能指标收集**
  - I/O 延迟分布（P50/P95/P99）
  - GC 暂停时间
  - 锁竞争统计
  - Directory 命中率

- [ ] **诊断工具**
  - 完善 `dir_manager_diag.go` 中的诊断功能
  - 添加 Directory 碎片率检测
  - 添加 Evacuation 效率统计

### 12. 测试和稳定性
- [ ] **压力测试**
  - 百万级对象并发读写测试
  - 长时间运行稳定性测试（7x24）
  - 异常场景测试（磁盘满、断电恢复等）

- [ ] **基准测试（Benchmark）**
  - 对比 ATS 的性能基准
  - 记录优化前后的性能数据

## 📝 代码质量

### 13. 错误处理
- [ ] **统一错误类型**
  - 扩展 `errors.go`，定义更细粒度的错误类型
  - 使用 `errors.Is`/`errors.As` 进行错误检查

### 14. 文档
- [ ] **架构文档**
  - 绘制 Directory 布局图
  - 绘制 I/O 流程图（RWW、Evacuation）
  - 参考 ATS 的 `ARCHITECTURE.md`

- [ ] **API 文档**
  - 完善 `doc.go` 中的包级别文档
  - 为关键函数添加示例代码

## 🔧 实验性功能（可选）

### 15. 热重启（Hot Restart）
- [ ] **使用 mmap 持久化 Directory**
  - 将 Directory 映射到共享内存
  - 进程重启后直接 attach，无需重新加载

### 16. 多盘支持优化
- [ ] **Stripe 负载均衡**
  - 实现更智能的 Key-to-Stripe 映射（考虑磁盘负载）
  - 支持动态添加/移除 Stripe

---

## 优先级说明
- 🔴 **高优先级**：直接影响性能和内存效率，建议优先实现
- 🟡 **中优先级**：完善功能完整性，提升用户体验
- 🟢 **低优先级**：工程化改进，长期维护性

## 参考资源
- ATS Cache Architecture: `trafficserver/src/iocore/cache/ARCHITECTURE.md`
- ATS Directory Implementation: `P_CacheDir.h`, `CacheDir.cc`
- Go Direct I/O: `golang.org/x/sys/unix` 包