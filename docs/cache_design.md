# Tech Design

来源：[bocchi-the-cache/bakemono README @5279a99](https://github.com/bocchi-the-cache/bakemono/blob/5279a99634d0c8ca7298f8b63b645de518fbe190/README.md)，[Apache-2.0](../LICENSE)。本页保留原 Tech Design 内容，仅调整标题层级、空白和图片路径。

> 以下原文、字段表和四张图保留原样，描述旧 Vol/Chunk 原型；旧格式、容量估算和规划不代表当前实现。当前设计见末尾 [Razor 当前实现扩展](#razor-当前实现扩展)。

... until now.

## Data Structure

There are 3 data structures in `bakemono`:

- **`Vol`**
  - **volume**, represents a single file on disk.
  - A `Vol` is what we finally persist on disk.
  - We will support bare block device in the future.
- **`Chunk`**
  - basic unit of your k-v cache data.
  - restored on disk.
- **`Dir`**
  - **directory**, a meta index of `Chunk`.
  - All `Dir`s are loaded in memory. It is compact.

### 🧱 Chunk
`Chunk` is the basic unit of cache data.

| name           | data type      | desc                  |
|----------------|----------------|-----------------------|
| Magic          | uint32         | fixed: 0x00114514     |
| Checksum       | uint32         | checksum of DataRaw   |
| Key            | [3000]byte     | fixed size key bytes  |
| DataLength     | uint32         |                       |
| HeaderSize     | uint32         | fixed: 4096.          |
| HeaderChecksum | uint32         | checksum of the above |
| DataRaw        | variable bytes | raw data              |

We force set chunk header size to 4KB this version, a sector size.

### 🔖 Dir
`Dir` is the meta index of `Chunk`.

Every `Chunk` has a `Dir` to represent it in memory.

`Dir` is always loaded in memory.
```go
type Dir struct {
	/*
	   unsigned int offset : 24;  // (0,1:0-7)
	   unsigned int bigInternal : 2;      // (1:8-9)
	   unsigned int sizeInternal : 6;     // (1:10-15)
	   unsigned int tag : 12;     // (2:0-11)
	   unsigned int phase : 1;    // (2:12)
	   unsigned int head : 1;     // (2:13)
	   unsigned int pinned : 1;   // (2:14)
	   unsigned int token : 1;    // (2:15)
	   unsigned int next : 16;    // (3)
	   unsigned int offset_high : 16;

	   if unused, raw[2] is `prev`, represents previous dir in freelist.

	   approx_size = sectorSize(512) * (2**3big) * sizeInternal
	*/
	raw [5]uint16
}
```

`Dir` is organized in raw 10 bytes. Use bit operations to get/set fields. This is a design of [Apache Traffic Server].
We must save every bit to reduce memory usage.

Note:
- If Dir is unused, `raw[2]` is `prev`, represents previous dir in freelist. To save 1 byte.
- Size is not exact. It is `sectorSize(512) * (2**3big) * sizeInternal`. Max to represent ~16GB using only 8 bits.

Memory usage:

If we have `100TB` data with `1MB` chunk size, we only need `100TB/1MB*10B = 1GB` memory to index `Dir`s.


### 🪣 Segment, Bucket, Freelist
`Segment` and `Bucket` are logical groups of `Dir`.

`Segment` is a collection of `Bucket`s. `Bucket` is a collection of `dir`s.

They are logically organized as below:
![dirs_init.png](dirs_init.png)


- `bucket` is group of fixed size `4` dirs.

- `segment` has max size `2^16=65536` dirs.

- `Dirs` is a linked list.

- All **non-bucket-head** dirs to freelist when initializing, named `freeDirs`.

- `map[segmentId]dirId` to index the first **free dir** of each segment.

Note, dirs is an array in memory.

Why `segments`? We could lock, flush meta per segment.

### 🗂️ Vol
`Vol` is the volume on disk. It is the final data structure we persist on disk.

![vol](vol_struct.png)

**Vol offset calculation**:

init options are
```go
type VolOptions struct {
	Fp        OffsetReaderWriterCloser
	FileSize  Offset
	ChunkAvgSize Offset

	FlushMetaInterval time.Duration
}
```
Max dirs size is nearly `FileSize/ChunkAvgSize` (There are alignment for bucket and segments).

Meaning, we have max `FileSize/ChunkAvgSize` chunks in this volume.

**Vol Multi meta**

Meta A/B are designed to be flush alternately. To avoid data loss when power failure happens.

In this version, only use meta A. Will implement multi meta in the future.

## Write

We use `md5` to hash key:
- key -> segmentId
- key -> bucketId

Once `segmentId` and `bucketId` are known:
- try to use **bucket-head** dir.
- if bucket-head is used, try to use **non-bucket-head** dir in this bucket.
- if all dirs in this bucket are used, try to grab a free dir from `freeDirs`.

When no free dir in `freeDirs`:
- rebuild `freeDirs` in this segment.
- if still no free dir, purge 10% buckets randomly in this segment.

Once get free dir, unlink it from `freeDirs` and link it as tail of bucket. Write `chunk offset`, `key` and `other metadata` to dir.

One example:
![dirs-used](dirs_used.png)


Then, write data to disk.
- rw lock the `Segment`
- write data to data range of `Vol`

Note: no hard limit for data size. But should **avoid too large data**, hold write lock too long.

Chunk data is cyclic, we will overwrite old data when disk is full. The advantage is
- sequential write is faster
- no need to delete old data
- no need to compact data in background
- no write amplification

![data-write](data_write.png)

## Read

We use `md5` to hash key same as write
- key -> segmentId
- key -> bucketId

Once `segmentId` and `bucketId` are known:
- find `key` dir linked list.
- if not found, return `MISS`
- if found, read `Chunk` data from disk.
- Check again the `full key` in `Chunk`.

Due to the only `12 bit tag` in dir, we have to check `key` again in `Chunk`.
Collision is possible, but will be much less when file size is large(>100GB).

There are a little read amplification due to approx size of `Dir`. But it is acceptable.

## Metadata Persistence
Flush/restore `meta A` with `FlushMetaInterval`.

Will implement multi meta in the future.

[Apache Traffic Server]: https://trafficserver.apache.org/

## Razor 当前实现扩展

Razor 使用 **RAM 加速 + Stripe 环形磁盘日志**。整对象、manifest 和每个 Range 逻辑块都作为独立缓存对象，复用同一套目录、分片、校验与提交机制。HTTP 层决定能否缓存和共享，存储层负责定位、写入所有权与数据完整性。

### 字段扩展对照

旧 Chunk 字段表保留为原型格式；当前记录为 **Doc v3**，按大端序序列化，头部合计 **72 B**，不再使用旧 4 KiB 固定头。以下名称与 [Doc 定义](../cache/doc.go) 一致。

| 字段 | 字节 | 当前含义 |
| --- | ---: | --- |
| Magic | 4 | `0x5F129B13` |
| Len | 4 | 本 fragment 的头、元数据和正文实际总长 |
| TotalLen | 8 | 片 0 为对象总长；后续片为该片正文的逻辑结束偏移 |
| FirstKey | 16 | 对象键 SHA-256 的前 16 B |
| Key | 16 | 当前片键 SHA-256 的前 16 B |
| HLen | 4 | 元数据长度；HTTP 元数据只在片 0 |
| DocType / VMajor / VMinor / Unused | 4 | 各 1 B；当前 writer 写 `0 / 3 / 0 / 0` |
| SyncSerial / WriteSerial | 8 | 各 4 B，组合成随机 writer 版本；不代表 fsync 次数 |
| Pinned | 4 | 当前 writer 写 0，不提供 HTTP 块固定驻留保证 |
| Checksum | 4 | IEEE CRC32，覆盖 checksum 置零的头、元数据和正文 |

Dir 原有 10 B 布局保留，但应补充以下当前语义；新增状态存于 Dir 外部，不是给 10 B 结构直接加字段：

| 字段 / 状态 | 当前解释 |
| --- | --- |
| offset | 40 bit 文件字节偏移，不是 sector 编号 |
| bigInternal / sizeInternal | 近似长度为 `512 × 8^big × (size+1)`，最多 16 MiB；旧原文的约 16 GB 估算不适用 |
| tag / identities | 12 bit tag 仅定位候选；额外的 16 B 身份摘要数组和 Doc 键校验防止误命中 |
| phase / allocation generation | phase 是 Dir 内 1 bit；完整覆盖代次保存在 Stripe/分配记录中 |
| token / FillToken | 原 Dir 的 token 位不等于失效凭证；HTTP 填充使用独立 FillToken，派生块沿用父围栏 |

新增对象关系如下；图中是逻辑拆分关系，不表示块与片段都常驻。

![UML 部分对象关系](diagrams/cache_objects.svg)

manifest 的 `Version/BlockSize/Total/ETag/Origin/Variant/Incarnation` 定义版本和布局；session marker 与检查点的 `Session/Clean/geometry/Checksum` 定义恢复边界。它们都不是旧 Chunk 头的附加字段，格式见 [存储与恢复](cache_storage_format.md)。

### 结构与磁盘布局

![UML 缓存布局](diagrams/cache_layout.svg)

| 层 | 当前实现 |
| --- | --- |
| Cache / Stripe | `CRC32(对象键) % Stripe 数` 选 stripe；一个数据文件可切成多个互不重叠区间 |
| DirManager | MD5 定位 segment/bucket/tag；每 bucket 4 个槽，碰撞链与 freelist 管理目录；额外身份摘要防误命中 |
| Dir | 每项 10 B，记录文件字节偏移、近似长度、tag、phase 和链指针；指向一个 fragment |
| Doc / fragment | v3、72 B 头，后接元数据与正文；正文每片最多 1 MiB；fragment 0 携带 HTTP 元数据及对象总长 |
| RAM | 保存不可变序列化 fragment；默认 Otter，可选 S3-FIFO，每 stripe 默认 10 MiB 权重预算；不是独立权威副本 |
| 检查点 | 独立 `<数据路径>.<Skip>.checkpoint` 文件，保存目录、完整键映射、写位置与 session |

10 B 仅指 Dir 本体；额外身份数组、完整键映射、RAM、写缓冲与运行时都占内存，不能用“片段数 × 10 B”估算总开销。

数据区从 `Start = Skip + align512(24 + 目录保留区大小)` 开始，到 `Skip + Len` 结束。前 24 B 是会话 marker；目录恢复依据独立检查点，**不是旧 Vol 的 meta A/B**。数据区追加 Doc 记录，尾部放不下完整记录时回卷。

Dir 的近似长度用于边界校验；实际读取长度取 Doc.Len。当前聚合写按记录实际字节推进，**不保证每个 Doc 的起点都按 512 B 或 4 KiB 对齐**；这里也没有“一个对象一个文件”的布局。

### 读写与提交

| 路径 | 顺序 |
| --- | --- |
| 读 | HTTP 层选表示键 → stripe/目录 → 检查 phase/覆盖范围 → RAM；RAM MISS 才 ReadAt 读 Doc 头和完整 fragment |
| 校验 | RAM 与磁盘都检查 CRC32、键/首键摘要、writer 版本、片号与长度；HTTP 层另判断 freshness、变体与失效凭证 |
| 写 | 取得 OpenDir 写入所有权和 FillToken → 拆 fragment → 聚合缓冲分配偏移/代次 → WriteAt 完成 → 复核围栏并发布目录/RAM |
| 完整提交 | fragment 1…N 可先写，fragment 0 最后写；发布头前复核全部已写片段仍有效，HTTP 仅使用 OpenReadCommitted |

聚合缓冲是 **2 × 4 MiB**，每 stripe 一个磁盘写 worker；当前写者每片都等待对应 flush 完成，不能假定总会攒满 4 MiB 才写。读写用 Go 文件 ReadAt/WriteAt，不共享 Seek 游标；无 io_uring/native AIO。普通读盘当前不会自动将片段晋升到 RAM；写入、元数据更新和 evacuation 会填充 RAM。

![UML 发布状态](diagrams/publication.svg)

普通整对象 MISS 等完整提交后，各客户端独立读取。底层保留活跃 Writer 读取 API，但它不是 HTTP RWW 路径；“提交可读”也不等于逐块 fsync。

### 部分对象缓存与定位

**HTTP 逻辑块与存储 fragment 是两层粒度。** 逻辑块决定回源区间及 RWW 屏障，默认 1 MiB，可配 64 KiB–8 MiB；存储 fragment 正文上限固定 1 MiB。因此 4 MiB 逻辑块通常拆成 4 个 fragment，整个块提交后才可发送。

| 对象 | 身份与内容 |
| --- | --- |
| URL 根键 | 配置命名空间、scheme/Host、原始路径、排序后的原始 query 项；缓存/任务/PURGE 统一，源站须保证 query 顺序无关 |
| manifest | 固定完整源站、变体、总长、强 ETag、随机 Incarnation、布局 v2 和块大小；不存在全文件常驻位图 |
| 逻辑块 i | 键包含 URL 根键、源站/变体/版本/Incarnation/布局及块号；正文为该固定区间 |
| 存储片 j | 片 0 用对象键；后续片用 `MD5(对象键 || uint64BE(j))`；Doc 中再保存完整键的 SHA-256 截断摘要及 writer 版本 |

请求区间用左闭右开 `[a,b)` 表示；块号从 `floor(a/B)` 到 `floor((b-1)/B)`，第 i 块回源 `[iB, min((i+1)B, 总长))`。源站请求转换成 HTTP 闭区间 `Range`，并带 If-Match 与 identity 编码。

例如 B=1 MiB，fresh manifest 有效，客户端读 `[0.5,2.5 MiB)`；同版本的块 0、2 fresh 且未淘汰，块 1 缺失：

| 步骤 | 行为 |
| --- | --- |
| 1 | 读取 manifest 确定源站、版本与块布局；一般路径中缺失/过期则共享 HEAD |
| 2 | 读块 0，发送后半 MiB；同时可准备块 1 |
| 3 | 块 1 加入/创建共享任务，仅回源 `[1,2 MiB)`；精确校验 206、Content-Range、ETag、长度、EOF 和 Trailer 后提交 |
| 4 | 发送完整块 1，再发送块 2 前半 MiB；最多准备下一块，额度不足改为按需获取 |

每个逻辑块**独立选 stripe、查目录**；同一块的 fragment 在该 stripe 内逐片查键，物理位置可不连续。读取片内正文从 `Dir.offset + 72 + HLen` 开始；不能用“文件基址 + HTTP Range 偏移”直接定位。首尾块先完整读取并校验，再裁剪发送；极小 Range 也可能产生整块回源/读取放大。

合格公共大 GET 与单 Range 共用这些块。并发等价 MISS 由 fillGroup 合并为一个生产者，无人为聚合窗口；普通整对象按对象共享，块路径分别按 manifest/块共享。不同请求顺序读取自己的所需区间，不等待整个文件补齐。完整时序见 [合并回源](cache_fill_design.md#缺块合并与-rww-时序) 与 [RWW](rww_implementation.md)。

### 淘汰、失效与恢复

| 事件 | 当前行为 |
| --- | --- |
| RAM 满 | 按所选策略淘汰；目录仍有效可回磁盘；权重预算不等于进程 RSS |
| 磁盘回卷 | 复用旧空间；phase 和 allocation generation 识别覆盖；近写指针的已访问片段可尝试有界 evacuation，迁移产生额外读写 |
| 块/manifest 淘汰 | 独立淘汰；manifest 存在不代表块齐全；同版本刷新可沿用 Incarnation，丢失重建则换标识 |
| PURGE / 版本 | URL 家族失效，派生块沿用原始 FillToken；每块发送前检查围栏，不跨源/版本拼接；已发字节无法撤回 |
| 正常关闭 | 排空写入，sync 数据，再原子发布并 sync clean 检查点；下次仅恢复验证完整的对象 |
| 异常退出 | 活动 session 不接受旧检查点；安全冷 MISS，不扫描旧日志重建，也不删除/截断缓存文件 |
| 离线 | 同一仍配置源站的 manifest 与全部所需块均 fresh、完整可读时可零回源；不提供 stale-if-error |

已知回卷期间 no-store 更新后仍可能命中旧对象的问题由 [#33](https://github.com/Monsterwi/razor/issues/33) 跟踪，尚未修复。多 Range 块缓存、跨节点合并/一致性/PURGE、未提交字节级 HTTP RWW 未支持。

代码：[Cache](../cache/cache.go)、[目录](../cache/dir_manager.go)、[读](../cache/cache_read.go)、[写](../cache/cache_write.go)、[块缓存](../server/range_cache.go)。细节：[磁盘格式/恢复](cache_storage_format.md)、[迁移](evacuation.md)、[RAM](ram_cache.md)、[配置](configuration.md)。
