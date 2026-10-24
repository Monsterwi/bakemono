# 功能与配置契约

原 #16/#17/#20/#21 交付契约，现作为配置速查；排期与完成状态以 [Issues](https://github.com/Monsterwi/razor/issues) 为准，#1 不自动关闭。原轮次基线/工作树/调度记录保留在 Git 历史。

## 共享不变量

| 层 | 不变量 | 设计 |
| --- | --- | --- |
| 身份 | 根键统一缓存/任务/PURGE；query 原始项排序；源站、变体、runtime 和失效代次隔离 | [合并与身份](CACHE_FILL_DESIGN.md) |
| 公共模式 | 显式源站契约；规范化 IP/UA/追踪；未知头仍隔离，私有内容不共享 | [规范化](../proxy/public_request.go) |
| 部分对象 | manifest 不代表块存在；固定源站/强 ETag/布局；缺块独立生产和提交 | [块缓存](CACHE_FILL_DESIGN.md#缺块合并与-rww-时序) |
| RWW | 首块提交后发送；最多准备下一块；预算不足退回按需；失败不混版 | [RWW](RWW_IMPLEMENTATION.md) |
| RAM | Otter 默认、S3-FIFO 可选；同步删除不复活；读者只读；MISS 可回磁盘 | [RAM](RAM_CACHE.md) |
| 失效/恢复 | 原 token 派生到底；异常退出安全 MISS，保留旧文件，不承诺逐块 fsync | [存储](CACHE_STORAGE_FORMAT.md) |

## 配置

前缀 `proxy.config.cache.`。下表变更均需重启；无效重载保留当前服务。

| 键 | 默认 | 范围/含义 |
| --- | --- | --- |
| shared_fill_public_static | 0 | INT 0/1，公共资源契约 |
| range_block_bytes | 1048576 | 64 KiB–8 MiB，2 的幂 |
| ram_cache_algorithm | otter | otter / s3-fifo |
| ram_cache_bytes | 10485760 | 每 stripe 权重预算；0 禁用，非零至多 1 TiB |

另有 `shared_fill_max_tasks=16`、`shared_fill_max_waiters=256`、`shared_fill_timeout=300` 秒。默认 `max_object_bytes=67108864`；测 100/200 MiB 时须调大。RAM 权重包含序列化 value 和已解码 HTTP 元数据的保守估算，不等于 RSS。

## 验收标准

| 类别 | 硬断言 |
| --- | --- |
| 合并 | 100 个等价公共冷请求一个正文生产者；私有/变体隔离；取消/超时/PURGE/重载后排空 |
| 部分缓存 | 先读 [0,16 MiB)、再读 [8,24 MiB)，同版本未淘汰时累计回源 24 MiB；HEAD 单列 |
| RWW | 阻塞第二块时真实客户端已收到首块；后续错误必须产生可见截断/读取失败 |
| 故障 | 离线完整 fresh 区间零回源；版本、If-Range/416、损坏、重启、布局变化和回卷不混读 |
| 策略 | 两 RAM 策略同预算、超容量工作集、扫描/热点切换；统计 resident/ghost/元数据 |
| 交付 | normal/race/vet/build、工具/真实二进制、双轴审查及 CI；专机 HDD/SSD 三轮、256 KiB/100 MiB、200 MiB 回归 |
| 长测 | SSD 每策略全新至少 2 小时，验证正文、排空和资源趋势；不等于长期生产稳定 |

历史验收：源码 `5ccf32c`，24 项矩阵 + 两次长测共 26 项通过，详见 [#19 报告](benchmarks/CDN_ACCEPTANCE_2026-10-21.md)。当前范围与限制见 [实现状态](IMPLEMENTATION_STATUS.md)。
