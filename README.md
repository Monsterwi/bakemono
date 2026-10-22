# Razor

用 Go 实现的 HTTP 缓存代理，借鉴 Apache Traffic Server 的缓存架构，面向文件下载与分发。重点是**合并回源**和**原生部分对象缓存**。

## 核心能力

- **合并回源**：等价并发请求共享填充任务；客户端独立取消，回源、等待者和缓冲均有额度限制。
- **部分对象缓存**：单 Range 与合格公共静态大 GET 共用块，重叠区间只补缺块，按源站、变体、版本和失效代次隔离。
- **块级 RWW**：块完整校验并提交后即可发送，同时最多预取下一块；默认 1 MiB，可配置 64 KiB–8 MiB。
- **磁盘与 RAM 缓存**：CRC 校验、检查点恢复、PURGE 失效；RAM 默认 Otter，可选 S3-FIFO。完整 fresh Range 可在源站离线时读取。
- **HTTP 与运行治理**：新鲜度、条件重验证、TLS/HTTP/2、配置热更新、有界排队和优雅排空。

## 快速开始

要求 Go **1.26.10**。

```bash
make build
./razor -c /path/to/conf
```

配置目录须包含 [records.config](conf/records.config) 和 [storage.config](conf/storage.config)。先按环境设置监听端口、源站、日志目录与缓存文件路径；仓库中的配置是示例。

仅当源站内容不随客户端身份变化时，开启公共静态模式。以下配置允许缓存 100/200 MiB 对象，并使用默认 1 MiB 块：

```text
CONFIG proxy.config.cache.shared_fill_public_static INT 1
CONFIG proxy.config.cache.max_object_bytes INT 268435456
CONFIG proxy.config.cache.range_block_bytes INT 1048576
```

公共模式、块大小和 RAM 策略修改均需重启；`-c` 接收目录，不是单个配置文件。

## 测试与验收

```bash
make test
make test-race
go vet ./...
```

已验收源码 `5ccf32c`：HDD/SSD 覆盖 256 KiB、100 MiB 主场景及 200 MiB 兼容回归，**26 项实测通过**。Otter 与 S3-FIFO 各完成一次全新 **2 小时 SSD 混合长测**，合计 **245,762 次业务 GET**，正文长度与 SHA 校验通过。

实测为同机 HTTP/1.1，不能直接当作公网生产容量。三轮压力原值、吞吐/P99、资源数据及已知限制见[完整验收报告](docs/benchmarks/CDN_ACCEPTANCE_2026-10-21.md)。

## 文档与边界

- [功能与配置契约](docs/FEATURE_PLAN.md)
- [RAM 缓存策略](docs/RAM_CACHE.md)
- [磁盘格式与恢复](docs/CACHE_STORAGE_FORMAT.md)

块缓存面向强 ETag、identity 编码的单 Range；不读取未校验或未提交的块。普通整对象路径等待完整提交。当前不支持多 Range 块缓存、跨节点一致性或 io_uring；提交也不等于逐块 fsync。
