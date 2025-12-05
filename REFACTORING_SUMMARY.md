# Bakemono 多片段对象存储重构总结

## 概述

本次重构将 Bakemono 的缓存系统改造为类似 Apache Traffic Server (ATS) 的多片段对象存储架构，实现了流式写入、并发读写、以及覆盖检测等核心功能。

## 主要变更

### 1. 多片段对象存储架构 (Multi-Fragment Object Storage)

#### 1.1 First Doc 和 Earliest Doc 概念

参考 ATS 的设计，实现了以下概念：

- **First Doc (Metadata Doc)**: 
  - 使用 `first_key` (即原始 key) 存储
  - 包含对象的元数据（`Vector` / `HTTPInfo`）
  - **最后写入**，用于覆盖检测安全保证
  - 存储位置：`FirstKey` 字段指向该 Doc

- **Earliest Doc (First Data Fragment)**:
  - 使用 `earliest_key` (MD5 hash of original key) 存储
  - 第一个数据片段
  - **最先写入**，确保数据先于元数据落盘
  - 存储位置：`HTTPInfo.EarliestKey` 字段指向该 Doc

- **数据片段链 (Data Fragment Chain)**:
  - 使用 `NextCacheKey()` 函数生成链式 key
  - 每个片段通过 `earliest_key` → `NextCacheKey(earliest_key)` → ... 链接
  - 支持 O(N) 的随机访问（N 为片段索引）

#### 1.2 写入顺序（覆盖检测安全）

```
1. 写入数据片段 1 (earliest_key)
2. 写入数据片段 2 (NextCacheKey(earliest_key))
3. ...
4. 写入数据片段 N
5. 最后写入 First Doc (first_key, 包含 Vector 元数据)
```

**设计目的**: 如果写入过程中崩溃，旧的 First Doc 仍然指向旧数据，新数据片段虽然可能部分写入，但不会被 First Doc 引用，从而避免数据不一致。

### 2. CacheWriter 流式写入实现

#### 2.1 新增文件: `cache_write.go`

实现了 `CacheWriter` 结构，支持流式写入大对象：

```go
type CacheWriter struct {
    stripe      *Stripe
    key         []byte      // 原始 key
    firstKey    []byte      // First Doc 的 key
    earliestKey []byte      // Earliest Doc 的 key
    currentKey  []byte      // 当前数据片段的 key
    
    buffer []byte           // 当前片段的缓冲区
    
    totalSize     uint64    // 总数据大小
    fragmentCount uint32    // 片段数量
    fragmentSize  uint32    // 标准片段大小
}
```

#### 2.2 核心方法

- **`NewWriter(key []byte)`**: 创建新的 `CacheWriter`，初始化 `first_key` 和 `earliest_key`
- **`Write(p []byte)`**: 实现 `io.Writer` 接口，缓冲数据并自动刷新片段
- **`flushFragment(isLast bool)`**: 将缓冲区数据写入磁盘，更新目录索引
- **`Close()`**: 刷新剩余数据，写入 First Doc (Metadata)

#### 2.3 Stripe.Set 重构

`Stripe.Set` 现在是一个简单的包装器：

```go
func (s *Stripe) Set(key, value []byte) error {
    w, err := s.NewWriter(key)
    if err != nil {
        return err
    }
    if _, err := w.Write(value); err != nil {
        return err
    }
    return w.Close()
}
```

### 3. CacheReader 流式读取实现

#### 3.1 新增文件: `cache_read.go`

实现了 `CacheReader` 结构，支持流式读取多片段对象：

```go
type CacheReader struct {
    stripe      *Stripe
    headKey     []byte      // First Doc 的 key
    earliestKey []byte      // Earliest Doc 的 key
    currentKey  []byte      // 当前读取片段的 key
    nextKey     []byte      // 下一个片段的 key
    
    totalSize int64         // 总数据大小
    bytesRead int64         // 已读取字节数
    
    currentData []byte      // 当前片段的数据
    dataOffset  int         // 在当前片段中的偏移
}
```

#### 3.2 核心方法

- **`Get(key []byte)`**: 读取 First Doc，解析 `Vector`，初始化 `CacheReader`
- **`Read(p []byte)`**: 实现 `io.Reader` 接口，自动加载下一个片段
- **`Seek(offset int64, whence int)`**: 实现 `io.Seeker` 接口，支持随机访问
- **`Size()`**: 返回对象总大小
- **`Close()`**: 清理资源

#### 3.3 读取流程

```
1. Get(key) → 读取 First Doc (first_key)
2. 解析 Vector → 获取 HTTPInfo
3. 从 HTTPInfo 获取 earliest_key 和 totalSize
4. 读取第一个数据片段 (earliest_key)
5. Read() → 按需加载后续片段 (NextCacheKey chain)
```

### 4. HTTPInfo 和 Vector 元数据结构

#### 4.1 新增文件: `http_info.go`

替换了原有的 `ObjectMetadata`，实现了 ATS 风格的元数据结构：

```go
type HTTPInfo struct {
    PayloadSize     uint64    // 对象总大小
    FragmentSize    uint32    // 标准片段大小
    EarliestKey     [16]byte  // Earliest Doc 的 key
    RequestHeaders  []byte    // HTTP 请求头（用于 alternate 选择）
    ResponseHeaders []byte    // HTTP 响应头
}

type Vector struct {
    Alternates []HTTPInfo     // 支持多个 alternate（当前实现使用第一个）
}
```

#### 4.2 序列化

- `HTTPInfo.MarshalBinary()` / `UnmarshalBinary()`
- `Vector.MarshalBinary()` / `UnmarshalBinary()`

使用 `encoding/binary` 进行二进制序列化，存储在 First Doc 的 payload 中。

### 5. Doc 结构对齐 ATS

#### 5.1 重命名和字段调整

- **重命名**: `ChunkHeader` → `Doc`
- **移除字段**: `Key`, `DataLength`, `HeaderSize`, `HeaderChecksum`
- **新增字段**: `KeyHash`, `FirstKey`, `Hlen`, `DocType`, `VMajor`, `VMinor`, `SyncSerial`, `WriteSerial`, `Pinned`

#### 5.2 Doc 结构定义

```go
type Doc struct {
    Magic       uint32      // DOC_MAGIC - 魔数验证
    Len         uint32      // 当前片段总长度 (Doc + hlen + data)
    TotalLen    uint64      // 整个对象的总长度（所有片段）
    FirstKey    [16]byte    // First key (所有片段共享)
    KeyHash     [16]byte    // 当前片段的 key hash (用于目录查找)
    Hlen        uint32      // 扩展头长度 (HTTP headers, vector 等)
    DocType     uint8       // 文档类型
    VMajor      uint8       // 主版本号
    VMinor      uint8       // 次版本号
    Unused      uint8       // 未使用，强制为 0
    SyncSerial  uint32      // 同步序列号
    WriteSerial uint32      // 写入序列号
    Pinned      uint32      // Pin 到期时间戳
    Checksum    uint32      // 数据校验和
}
```

#### 5.3 Chunk 结构简化

- **移除**: `Chunk.Key` 字段
- **更新**: `GetKeyData()` 现在返回 `Doc.KeyHash[:]`
- **更新**: `DataLen()` 使用 `binary.Size(Doc{})` 计算

### 6. AggregateWriteBuffer 并发修复

#### 6.1 问题描述

在并发写入场景下，`AggregateWriteBuffer` 依赖外部传入的 `volWritePos` 来计算当前缓冲区的起始位置。当 Reader 使用过期的 `volWritePos` 快照时，会导致缓冲区范围计算错误。

#### 6.2 解决方案

在 `AggregateWriteBuffer` 内部维护 `currentStartOffset`：

```go
type AggregateWriteBuffer struct {
    // ...
    currentStartOffset int64  // 当前缓冲区的绝对磁盘起始偏移
    // ...
}
```

- **`WriteAt()`**: 当 `currentPos == 0` 时，设置 `currentStartOffset = diskOffset`
- **`switchBufferLocked()`**: 切换缓冲区后，下次 `WriteAt` 会设置新的 `currentStartOffset`
- **`ReadOverlay()` / `IsFullyInCache()`**: 移除 `volWritePos` 参数，使用内部的 `currentStartOffset`

#### 6.3 影响范围

- `stripe.go`: `loadFromAggregationBuffer()`, `loadFromDisk()`, `readChunkInternal()` 不再需要 `writePos` 参数
- `cache_read.go`: `Stripe.Get()`, `CacheReader.Read()`, `CacheReader.Seek()` 不再传递 `writePos`
- `aggregate_write_buffer_test.go`: 更新 `CopyFrom()` 调用签名

### 7. 片段 Key 生成

#### 7.1 NextCacheKey 实现

使用 ATS 的 `CacheKey_next_table` 和 `CacheKey_prev_table` 实现链式 key 生成：

```go
func NextCacheKey(key []byte) []byte {
    // 使用查找表生成下一个 key
    // 支持双向遍历（NextCacheKey / PrevCacheKey）
}
```

#### 7.2 Key 生成策略

- **First Key**: 原始 key（用户提供的 key）
- **Earliest Key**: `MD5(original_key)` 的前 16 字节
- **后续片段**: `NextCacheKey(previous_key)`

### 8. 代码清理和优化

#### 8.1 移除冗余代码

- 移除了 `ObjectMetadata` 结构定义
- 移除了 `Chunk.Key` 字段
- 移除了 `Doc` 中的冗余字段
- 移除了 `CacheWriter.isFirstDocWritten` 未使用字段

#### 8.2 更新测试

- `chunk_test.go`: 更新 `Doc` 结构引用，移除 `Key` 字段验证
- `aggregate_write_buffer_test.go`: 更新 `CopyFrom()` 调用签名
- 所有测试通过，包括并发读写大对象测试

## 文件变更清单

### 新增文件

- `cache_write.go`: `CacheWriter` 流式写入实现
- `cache_read.go`: `CacheReader` 流式读取实现
- `http_info.go`: `HTTPInfo` 和 `Vector` 元数据结构

### 修改文件

- `chunk.go`: 
  - `ChunkHeader` → `Doc`
  - 移除 `Key` 字段，使用 `KeyHash`
  - 更新 `DataLen()`, `GetKeyData()` 等方法
  
- `stripe.go`:
  - `Set()` / `Get()` 重构为 `CacheWriter` / `CacheReader` 包装器
  - `readChunkInternal()` 移除 `writePos` 参数
  - `loadFromAggregationBuffer()` / `loadFromDisk()` 移除 `writePos` 参数
  
- `aggregate_write_buffer.go`:
  - 添加 `currentStartOffset` 字段
  - `WriteAt()` 设置 `currentStartOffset`
  - `ReadOverlay()` / `IsFullyInCache()` 移除 `volWritePos` 参数
  
- `cache_read.go`:
  - 移除所有 `writePos` 相关代码
  
- `chunk_test.go`:
  - 更新 `Doc` 结构引用
  - 更新 key 验证逻辑

- `aggregate_write_buffer_test.go`:
  - 更新 `CopyFrom()` 调用签名

## 测试验证

### 通过的测试

- ✅ `TestChunk_SetVerityGet`
- ✅ `TestChunk_Marshal_Unmarshal_Binary`
- ✅ `TestDoc_Marshal_UnmarshalBinary`
- ✅ `TestMultiFragmentLargeObject` - **关键测试：多片段大对象读写**
- ✅ `TestCacheReader_Seek` - **关键测试：流式读取和随机访问**
- ✅ `TestConcurrentReadWriteLargeObject` - **关键测试：并发读写大对象**
- ✅ `TestConcurrent1MBFiles` - **关键测试：并发写入多个文件**
- ✅ `TestVolPersistence` - 持久化测试
- ✅ 所有其他现有测试

### 测试覆盖的功能

1. **多片段写入**: 17MB 对象被分割为 5 个片段
2. **流式读取**: 支持 `Read()` 和 `Seek()` 操作
3. **并发写入**: 多个对象可以并发写入不同片段
4. **并发读写**: Writer 写入大对象时，Reader 可以同时读取
5. **元数据解析**: `Vector` / `HTTPInfo` 序列化/反序列化正确

## 性能影响

### 改进

- **并发写入**: 修复了 `AggregateWriteBuffer` 的竞态条件，支持真正的并发读写
- **流式处理**: 大对象不再需要全部加载到内存，支持流式写入和读取
- **内存效率**: 使用缓冲区管理，减少内存分配

### 注意事项

- **Seek 性能**: 随机访问片段需要 O(N) 次 `NextCacheKey()` 调用（N 为片段索引）
  - 对于大片段（如 1MB），即使 1TB 文件也只需要约 100 万次迭代，性能可接受
  - 如需优化，可在 `Vector` 中存储片段索引表

## 后续优化建议

1. **片段索引表**: 在 `Vector` 中存储 `fragment_index -> key` 映射，实现 O(1) 随机访问
2. **多 Alternate 支持**: 当前只使用第一个 `HTTPInfo`，可以扩展支持多个 alternate
3. **HTTP Headers**: 完善 `RequestHeaders` / `ResponseHeaders` 的序列化
4. **压缩支持**: 在 `HTTPInfo` 中添加压缩标志和算法
5. **TTL/过期**: 使用 `Pinned` 字段实现对象过期机制

## 参考文档

- Apache Traffic Server Cache Architecture
- `trafficserver/doc/developer-guide/cache-architecture/first-doc-earliest-doc-analysis.md`
- `trafficserver/src/iocore/cache/P_CacheDoc.h`
- `trafficserver/src/iocore/cache/CacheVC.cc`

## 总结

本次重构成功实现了 ATS 风格的多片段对象存储架构，支持：

- ✅ 流式写入和读取
- ✅ 并发读写
- ✅ 覆盖检测安全保证
- ✅ 大对象支持（TB 级别）
- ✅ 随机访问（Seek）
- ✅ ATS 兼容的元数据结构

所有测试通过，代码质量良好，为后续功能扩展打下了坚实基础。



