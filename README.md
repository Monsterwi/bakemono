# Razor

Go HTTP 缓存代理，重点是**合并回源**和**原生部分对象缓存**。

| 能力 | 行为 |
| --- | --- |
| 合并回源 | 等价并发 MISS 共享生产者，独立取消、限额与超时 |
| 部分对象 / RWW | GET/单 Range 共用固定块；只补缺块，完整块提交后发送 |
| 存储 | CRC、检查点恢复、PURGE；RAM 默认 Otter，可选 S3-FIFO |
| HTTP | 缓存新鲜度/重验证、TLS/H2、配置重载和排空 |

## 运行

要求 Go **1.26.10**。

```bash
make build
./razor -c /path/to/conf
```

目录包含 [records.config](conf/records.config) 和 [storage.config](conf/storage.config)；先设置端口、源站、日志和缓存文件路径。Query 项顺序不影响缓存身份，源站必须保证顺序无关。

公共静态大 GET 与单 Range 共用块时，配置示例：

```text
CONFIG proxy.config.cache.shared_fill_public_static INT 1
CONFIG proxy.config.cache.max_object_bytes INT 268435456
CONFIG proxy.config.cache.range_block_bytes INT 1048576
```

公共模式要求资源不随客户端身份变化；公共模式、块大小和 RAM 策略修改须重启。[配置与约束](docs/configuration.md)。

## 文档与验证

[UML 架构](docs/architecture.md) · [原项目 Tech Design](docs/cache_design.md) · [合并回源时序](docs/cache_fill_design.md) · [RWW](docs/rww_implementation.md) · [实现状态](docs/implementation_status.md)

```bash
make test
make test-race
go vet ./...
```

[最新压测](docs/benchmarks/ats_comparison_2026-10-24.md)：源码 `8581267`，256 KiB 热命中，C32/C100；三轮原值和配置随报告附带。本轮未覆盖大文件、HDD 或长测，同机结果不代表公网生产容量。

块路径要求强 ETag、identity；普通整对象等完整提交。不支持多 Range 块缓存、未提交字节级 RWW、跨节点一致性或 io_uring；提交不等于逐块 fsync。
