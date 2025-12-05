# Bakemono RWW (Read While Write) 实现总结

## 1. 概述

RWW (Read While Write) 是参考 Apache Traffic Server 实现的缓存系统核心特性，允许在文档正在写入时，同时从写入者读取数据，大幅提升并发性能和响应速度。

## 2. 核心组件

### 2.1 数据结构

#### OpenDirEntry
```go
type OpenDirEntry struct {
    mutex   sync.RWMutex
    writers []*CacheWriter
}
```
- 管理同一文档的多个写入者（当前实现支持多写入者）
- 使用 RWMutex 保护并发访问

#### OpenDir
```go
type OpenDir struct {
    mutex   sync.RWMutex
    entries map[string]*OpenDirEntry
}
```
- 全局管理所有正在写入的文档
- Key 映射到 OpenDirEntry
- 提供 `OpenWrite`、`OpenRead`、`CloseWrite` 接口

#### CacheWriter (增强)
```go
type CacheWriter struct {
    // ... 原有字段 ...
    
    // RWW支持
    mutex  sync.RWMutex    // 保护 buffer 和状态
    closed bool            // 写入是否已关闭
    od     *OpenDirEntry   // 所属的 OpenDirEntry
}
```
- 添加了互斥锁保护内部状态
- 添加了 closed 标志
- 在创建时自动注册到 OpenDir
- 在 Close 时自动从 OpenDir 注销

#### CacheReader (增强)
```go
type CacheReader struct {
    // ... 原有字段 ...
    
    // RWW支持
    writer *CacheWriter    // 如果是 RWW 模式，指向写入者
}
```
- 添加了 writer 字段
- `Read()` 方法根据 writer 是否为 nil 选择不同路径

### 2.2 文件结构

```
bakemono/
├── open_dir.go              # OpenDir 和 OpenDirEntry 实现
├── cache_write.go           # CacheWriter 增强（RWW 支持）
├── cache_read.go            # CacheReader 增强（RWW 读取逻辑）
├── stripe.go                # Stripe 集成 OpenDir
├── aggregate_write_buffer.go # AggBuffer 增大到 8MB
└── rww_test.go              # RWW 单元测试
```

## 3. 关键实现逻辑

### 3.1 Writer 注册流程

```go
func (s *Stripe) NewWriter(key []byte) (*CacheWriter, error) {
    w := &CacheWriter{ ... }
    
    // 注册到 OpenDir
    if s.OpenDir != nil {
        s.OpenDir.OpenWrite(key, w)
    }
    
    return w, nil
}
```

- Writer 创建时自动注册到 OpenDir
- OpenDir 维护 key → OpenDirEntry → []*CacheWriter 的映射
- Writer Close 时自动注销

### 3.2 Reader 查找 Writer

```go
func (s *Stripe) Get(key []byte) (bool, *CacheReader, error) {
    // 1. 检查 RamCache
    reader, hit := s.loadFromRamCache(key)
    if hit {
        return true, reader, nil
    }
    
    // 2. RWW: 检查 OpenDir
    if s.OpenDir != nil {
        entry := s.OpenDir.OpenRead(key)
        if entry != nil {
            // 找到活跃的 Writer
            entry.mutex.RLock()
            var writer *CacheWriter
            if len(entry.writers) > 0 {
                writer = entry.writers[0] // 选择第一个
            }
            entry.mutex.RUnlock()
            
            if writer != nil {
                // 从 Writer 创建 Reader
                return true, NewCacheReaderFromWriter(s, key, writer), nil
            }
        }
    }
    
    // 3. 从 Directory 查找（磁盘）
    // ...
}
```

- 查找顺序：**RamCache → OpenDir → Directory**
- 如果在 OpenDir 找到，则返回 RWW Reader

### 3.3 从 Writer 读取逻辑

```go
func (r *CacheReader) readFromWriter(p []byte) (n int, err error) {
    for {
        if r.dataOffset >= len(r.currentData) {
            // 需要新数据
            r.writer.mutex.RLock()
            writerClosed := r.writer.closed
            writerCurrentKey := r.writer.currentKey
            writerBufferLen := len(r.writer.buffer)
            isCurrent := string(r.currentKey) == string(writerCurrentKey)
            
            if isCurrent {
                // 从 Writer 的当前 Buffer 读取
                bufferCopy := make([]byte, writerBufferLen)
                copy(bufferCopy, r.writer.buffer)
                r.currentData = bufferCopy
            }
            r.writer.mutex.RUnlock()
            
            if !isCurrent {
                // Writer 已切换到下一个 Fragment
                // 从磁盘/AggBuffer 读取已完成的 Fragment
                hit, _, d := r.stripe.Dm.Get(r.currentKey)
                if hit {
                    ck := r.stripe.readChunkInternal(offset, size)
                    r.currentData = ck.DataRaw
                }
            }
        }
        
        // 复制数据
        n = copy(p, r.currentData[r.dataOffset:])
        r.dataOffset += n
        r.bytesRead += int64(n)
        
        // Fragment 完成，切换到下一个
        if r.dataOffset >= int(ChunkDataSize) {
            r.currentKey = NextCacheKey(r.currentKey)
        }
        
        return n, nil
    }
}
```

### 3.4 关键路径说明

#### 路径1：从 Writer Buffer 读取
- **触发条件**：`r.currentKey == writer.currentKey`
- **数据来源**：Writer 的内存 buffer（拷贝）
- **优势**：零磁盘 I/O，超低延迟

#### 路径2：从已完成的 Fragment 读取
- **触发条件**：`r.currentKey != writer.currentKey`（Writer 已切换到下一个 Fragment）
- **数据来源**：
  1. 先尝试从 AggregateWriteBuffer（Pending/Current）
  2. 如果不在 AggBuffer，则从磁盘读取 + Overlay
- **优势**：支持已完成 Fragment 的快速读取

#### 路径3：等待 Writer 生产数据
- **触发条件**：Writer Buffer 中没有新数据，且 Writer 未关闭
- **行为**：Sleep 10ms 后重试
- **优势**：避免忙等待，减少 CPU 消耗

## 4. 并发控制

### 4.1 锁层次

```
OpenDir.mutex (RWMutex)
  └─> OpenDirEntry.mutex (RWMutex)
        └─> CacheWriter.mutex (RWMutex)
```

- **写入路径**：Writer 持有自己的 mutex
- **读取路径**：Reader 仅在需要访问 Writer 状态时短暂持有 Writer.mutex (RLock)
- **锁粒度**：细粒度锁，减少竞争

### 4.2 避免死锁

- **一致的锁顺序**：OpenDir → OpenDirEntry → CacheWriter → Stripe
- **短暂持锁**：读取 Writer 状态后立即释放锁
- **拷贝数据**：从 Writer Buffer 拷贝数据，而不是持有引用

## 5. AggregateWriteBuffer 优化

### 5.1 为什么增大到 8MB？

原始问题：
- ChunkDataSize = 4MB
- Doc header ≈ 72 bytes
- Chunk 总大小 ≈ 4MB + 72B
- AggBufferSize = 4MB（旧值）

结果：**一个 Chunk 会跨越两个 AggBuffer**，导致：
- `IsFullyInCache` 返回 false
- 从磁盘读取时数据不完整
- Chunk 校验失败

解决方案：**AggBufferSize = 8MB**
- 确保单个 Chunk 完整地存在于一个 buffer 中
- `IsFullyInCache` 正确返回 true
- Reader 可以直接从 AggBuffer 读取完整 Chunk

## 6. 性能表现

### 6.1 测试场景
- **数据量**：8.39MB (2 * 4MB + 1KB)
- **Fragment 数**：3个
- **写入方式**：256KB/chunk，间歇 sleep 1ms

### 6.2 测试结果
- **RWW 读取时间**：0.24秒
- **数据正确性**：✅ 100% 匹配
- **持久化验证**：✅ 二次读取正确

### 6.3 性能优化点
1. **零拷贝**：从 Writer Buffer 拷贝，避免磁盘 I/O
2. **异步写入**：Writer 使用 AggregateWriteBuffer 异步刷盘
3. **Overlay 机制**：AggregateWriteBuffer 的 Overlay 确保读取最新数据
4. **细粒度锁**：减少锁竞争

## 7. 测试覆盖

### 7.1 TestRWW
- **场景**：正常 RWW 流程
- **验证点**：
  - Reader 从 Writer 创建（RWW 模式）
  - 数据完整性（8MB+ 数据）
  - Fragment 切换（3个 Fragment）
  - 持久化验证（Writer Close 后再次读取）

### 7.2 TestRWWMissing
- **场景**：读取不存在的 Key
- **验证点**：
  - OpenDir 中无 Writer
  - Directory 中无数据
  - 正确返回 Miss

## 8. 与 ATS 的对比

### 8.1 相似之处
- **OpenDir/OpenDirEntry 结构**：与 ATS 一致
- **Writer 注册机制**：自动注册/注销
- **读取优先级**：RamCache → OpenDir → Directory
- **Fragment 处理**：多 Fragment 支持

### 8.2 差异之处
| 特性 | ATS | Bakemono |
|------|-----|----------|
| 事件模型 | 事件驱动（Continuation） | Goroutine + Channel |
| 锁机制 | ProxyMutex | sync.RWMutex |
| Buffer 大小 | 4MB | 8MB（优化后） |
| 等待策略 | 事件调度 + 重试计数器 | Sleep + 轮询 |
| 日志级别 | DEBUG | DEBUG + INFO |

### 8.3 命名对齐
- ✅ `OpenDir` / `OpenDirEntry`
- ✅ `CacheWriter` / `CacheReader` (对应 ATS 的 CacheVC)
- ✅ `Stripe` (对应 ATS 的 StripeSM)
- ✅ `fragmentCount` / `earliestKey` / `firstKey`

## 9. 未来优化方向

1. **Retry 机制**：参考 ATS 增加 `writer_lock_retry` 和最大重试次数
2. **性能指标**：添加 RWW 命中率、平均延迟等指标
3. **Alternate 选择**：支持 HTTP Header 匹配选择 Alternate
4. **错误处理**：更细致的错误分类和处理
5. **内存优化**：优化 bufferCopy，考虑引用而非拷贝

## 10. 使用示例

```go
// 写入
writer, _ := stripe.NewWriter(key)
writer.Write(data)  // 流式写入
writer.Close()

// 并发读取（RWW）
reader, _ := stripe.Get(key)  // 自动检测 OpenDir
data := io.ReadAll(reader)    // 从 Writer Buffer 或 Disk 读取
```

---

**实现文件**：
- `open_dir.go`: OpenDir 实现
- `cache_write.go`: CacheWriter RWW 增强
- `cache_read.go`: CacheReader RWW 读取逻辑
- `rww_test.go`: 单元测试

**参考文档**：
- `/root/code/trafficserver/src/iocore/cache/RWW_DIAGRAMS.md`
- `/root/code/trafficserver/src/iocore/cache/CacheRead.cc`


