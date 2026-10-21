# 公共分块缓存专机验收（2026-10-21）

**状态：冻结测量源码 5ccf32c 的 24 项功能/布局/三轮压力/checked-hey/物理文件冷读，加上 Otter 与 S3-FIFO 各一次全新 SSD ≥7200秒混合长测，全部26项专机实测通过。** 旧失败运行和旧 12571fc Otter 通过时间不抵扣本次；最终文档审查、后续交付提交及其 CI 仍按 Git/Issue 外部证据完成，不在报告内伪造自身 SHA。

本轮测量源码固定为 `5ccf32c2a4ea79525743cab7bf08534590357301`，新 run-id 为 `20261021T160425Z-5ccf32c`；控制器于 `2026-10-21 16:11:33.464860 UTC` 开始串行执行。二进制 SHA256 为 `4bdcb12e755acead3bad73134cde8a3713b316980d269084a733355f16599e37`，Go 1.26.10 静态构建于 `2026-10-21T16:04:20Z`，版本 `0.1.0-cache-next+5ccf32c`。新原始根目录为 `/data/bakemono-lab/results/razor-cache-next/20261021T160425Z-5ccf32c/`，源码/构建目录分别为 `/data/bakemono-lab/src/razor-20261021T160425Z-5ccf32c`、`/data/bakemono-lab/build/razor-20261021T160425Z-5ccf32c`。本报告主压力表只使用该修复版新矩阵，长测另使用同源码两次全新实际运行，不将旧运行数据改标为新版本。

矩阵阶段170文件已独立核验；最终26项导出188文件的长度和SHA256也全部一致，其中169个矩阵文件字节未变，仅 sequence 更新。完整必要表与三轮原值见 [JSON 汇总](CDN_ACCEPTANCE_2026-10-21.json)；`repetition_columns` 解释原值数组。四次旧运行的中止/失败记录保留在后文，旧 Otter 通过时长也不计入新源码。文档交付 commit 由 Git 历史和最终 Issue 验收评论记录，报告只固定测量源码，不内嵌自身尚未产生的提交 SHA。

## 通过的实测范围与交付记录

实测 run-id：`20261021T160425Z-5ccf32c`。24 项已完成任务按顺序运行于 `2026-10-21 16:11:33.505742–16:46:41.226683 UTC`，每项独立缓存目录；没有同时运行竞争压测组。主压力和 checked-hey 均各做三轮。

| 项目 | 实际覆盖 | 实测/交付状态 |
| --- | --- | --- |
| 公共模式功能 | HDD/SSD × Otter/S3-FIFO，256 KiB、100 MiB | 4 项通过 |
| 200 MiB 兼容 | HDD/SSD × 两策略 | 4 项通过 |
| 保守模式兼容 | HDD/SSD × 两策略，100 MiB | 4 项通过 |
| 其他块布局 | SSD × 两策略 × 64 KiB/4 MiB | 4 项通过；HDD 本次使用默认 1 MiB |
| 主压力 | HDD/SSD × 两策略，各 19 场景 ×3 轮 | 4 项、228 批，664,149 个逐响应长度/SHA 校验请求，非预期错误 0 |
| 独立 Go 客户端 | HDD/SSD × 两策略，各 4 组 ×3 轮 | 4 项、48 批，2,776,124 个正文读取/实际字节检查请求；每批前后 SHA 校验 |
| 自有文件物理冷读 | 上一项每组合额外 3 次，每次 100 MiB | 12 次通过，每次进程实际读盘不少于对象大小 99% |
| Otter SSD 混合 soak | 新版独立至少 7200 秒 | 通过：实际 7200.279144 秒 |
| S3-FIFO SSD 混合 soak | 新版在 Otter 后独立至少 7200 秒 | 通过：实际 7200.089907 秒 |
| 最终文档交付 | Standards/Spec 准确性复审、交付提交及其 CI | 属最终交付门槛，精确提交/结果由 Git 和 Issue 外部记录 |

主压力没有允许正常有效请求静默截断或错误。保守模式四个功能任务的主动限额步骤另外产生 **120 个预期 503**，不计入主压力错误；公共功能步骤的截断/错误 200、未知头写所有权冲突、离线缺块等是显式故障用例，依据各步骤断言单列，不当作正常响应。紧凑导出未包含全部 `requests.jsonl`，不从有限功能摘要伪造所有故障的汇总请求数；完整原始文件留在下述远端目录。

## 环境、配置与证据

| 项目 | 实测值 |
| --- | --- |
| CPU / 内存 | 2× Xeon E5-2620 v4，16 物理核/32 逻辑核；67,261,059,072 B（约 62.64 GiB）RAM |
| 系统 | Linux `3.10.0-957.el7.x86_64`；XFS；未升级内核/未启用 io_uring |
| HDD | `/data3`，`/dev/sdc`，8:32，ST4000NM0295，ROTA=1 |
| SSD | `/ssd1`，`/dev/sdk`，8:160，INTEL SSDSC2BB80，ROTA=0 |
| 服务/工具 | Go 1.26.10 静态 CGO=0；`GOMAXPROCS=8`；Python 3.12.13；已校验构建清单的正文检查版 hey |
| 公共路由 | `shared_fill_public_static=1`，identity 编码；保守功能任务显式关闭公共模式 |
| 对象/块 | `max_object_bytes=268435456`（256 MiB）；默认块 `1048576` B；另测 65536/4194304 B |
| 资源额度 | 每 stripe RAM 序列化 value 权重预算 10485760 B，两策略相同；write buffer 134217728 B；请求 256、队列 64 |
| 缓存文件 | 主功能/压力/hey 每项独立 1 GiB；保守回卷步骤另用 32 MiB；正式 soak 每策略独立 512 MiB |
| 回源/客户端 | 同机 origin 与真实 TCP loopback HTTP/1.1 客户端；没有 WAN、TLS 或 H2 压力测试 |
| 二进制 SHA256 | `4bdcb12e755acead3bad73134cde8a3713b316980d269084a733355f16599e37` |
| 构建时刻 | `2026-10-21T16:04:20Z`，版本 `0.1.0-cache-next+5ccf32c` |

主 fixture（SHA256）：

| 文件 | SHA256 |
| --- | --- |
| 256 KiB v1 | `aa65f2c70f23be4dc7a42679993d2bd1a7b1bde694ba18575aa32ff6fe593883` |
| 256 KiB v2 | `0ae90a16df3e3d50b70b9c1171a3f3877ac4ef6399cea27695da71e3eefd0e86` |
| 100 MiB v1 | `7c872c016adb3bbe27ba37e591041a249818c664a1aea877db6603e7400e0749` |
| 100 MiB v2 | `13b1599b3f7ff69f1a8e87ed61debb3d2f5635f59332e3d1bed7401b8e2a7f1a` |
| 200 MiB v1 | `caba8fcf96c4839e35b88b6207a53d1731d48f9d571940add6bf8f2e32c87e3a` |
| 200 MiB v2 | `8a2fea8d49e5f889dc302d61914fe4e822d394d26c1b95f1ac44e4436b03b07e` |

完整原始目录：`/data/bakemono-lab/results/razor-cache-next/20261021T160425Z-5ccf32c/`。对应源码和构建目录分别为 `/data/bakemono-lab/src/razor-20261021T160425Z-5ccf32c`、`/data/bakemono-lab/build/razor-20261021T160425Z-5ccf32c`。各任务的 `environment.json`、配置、`batches.json`、请求/源站 JSONL、进程日志及 outcome 都保留。

最终只读导出为 `/workspace/data/razor-delivery/report-inputs-5ccf32c-final/`：188文件、48,312,579 B；归档3,961,025 B、SHA256 `8d373e0d6beafb2ecbfe2891508455492bf34458d7fab9c3a1f370e092127afc`，manifest SHA256 `7957570d367c7e78e3d884cd196fca99a502cf75848f3455b9edc8174d124359`。矩阵原导出另保留，未被长测覆盖；169个既有矩阵文件与最终导出字节相同，只有 sequence 更新。`plan.json` 保留26条精确命令，最终 `sequence.json` 为26/26成功，无活动任务。

22个 acceptance environment 明确记录空 `source_status`，4个 checked-hey 的 schema 不记录此字段，不能补为逐任务clean证明。源码/二进制及4项hey SHA/构建绑定均已核验；独立核验的 bootstrap/矩阵后/最终冻结checkout clean是独立首尾证据。精确最终 precheck `/workspace/data/razor-delivery/5ccf32c-final-soak-precheck-exact.json` SHA `4cd2201815f142d1c4e153072b1fc6c133247ed91858528576ea051c8f59754d` 与归档两场 outcome/environment 全对象精确一致。独立最终导出核验及所有证据路径/哈希另存 JSON。

逐原始请求/百分位独立侧证 `/workspace/data/razor-delivery/5ccf32c-final-root-raw-check.json`（3,434 B、SHA `690bd09dd62678e5eff8889a19ee1440d27942f55337e4457ba13d8ac44ba6a2`）在188文件导出之后产生，**不是该归档成员**。它记录14份远端原始/摘要/outcome/environment文件的长度和哈希。原始大 requests/stages/origin JSONL 留在远端，不复制到仓库；本地独立复核导出哈希和汇总数学，逐原始分位核对由独立核验方完成。审计汇总helper SHA `83f94fbcbb19342f82f03f0e6e6aa9c2db466488d34a9d363520be376c835d23` 固定本次源码与二进制。

以下为代表性实测命令；原目录已存在，重跑必须换新 run-id，不能覆盖：

```bash
cd /data/bakemono-lab/src/razor-20261021T160425Z-5ccf32c
/data/bakemono-lab/opt/python-3.12.13/bin/python3 tools/acceptance.py \
  --binary /data/bakemono-lab/build/razor-20261021T160425Z-5ccf32c/razor-5ccf32c \
  --output /data/bakemono-lab/results/razor-cache-next/20261021T160425Z-5ccf32c/ssd-otter-benchmark100 \
  --cache-root /ssd1/bakemono-lab/cache/razor/20261021T160425Z-5ccf32c/ssd-otter-benchmark100 \
  --products razor --public-static --ram-algorithm otter --ram-bytes 10485760 \
  --block-bytes 1048576 --large-mib 100 --mode benchmark --seconds 10 --repeats 3
```

## 功能观测与正确性

| 场景 | 本次专机结果/统计口径 |
| --- | --- |
| 公共等价冷请求 | 默认 1 MiB 布局、100 MiB 主测：100 个不同实际 loopback TCP 源 IP、UA、trace，在生产者完成前订阅；HEAD 1 次、所需 1 MiB 块 GET 1 次、源站正文 1 MiB。访问日志验证 100 个源 IP；不是仅改 XFF |
| 默认布局重叠功能 | 依次请求 `[0,16 MiB)`、`[8,24 MiB)`；第一次正文回源 16 MiB，第二次只补 8 MiB；合计 HEAD 1、GET 24、正文 24 MiB，逐响应精确 SHA |
| GET/Range 共用 | 默认 1 MiB 布局、100 MiB 主测：再完整 GET 累计正文恰为 100 MiB；后续已覆盖 Range 无新增回源。冷完整 GET 是 HEAD 1+块 GET 100，并非整文件仅一个源站 HTTP 请求 |
| 压力重叠 | 四个 16 MiB 区间起点 0/4/8/12 MiB，8 客户端返回共 128 MiB；每轮 HEAD 1+GET 28、正文 28 MiB。与功能 24 MiB、历史保守压力 40 MiB 的并集不同 |
| 提交后 RWW | 真实第二块屏障未放行前，完整 GET 和 Range 的客户端均已收到并校验首个完整块；不读取未校验或未提交字节 |
| 后续块异常 | 第二块真实截断或返回错误 200 后，200/206 已发送流出现不完整正文/读取错误，客户端按实际长度和 SHA 拒绝成功；不只检查状态码 |
| 取消/身份隔离 | 取消后最多本区间下一块生产，任务/等待者/写者/读者/缓冲排空；未知头不错误加入任务，竞争写者受控失败，不额外并发回源 |
| 离线/恢复 | 完整 fresh Range 在所有源站不健康时仍正确 206，步骤前后对象回源计数不变；缺块受控失败，恢复健康后服务可用 |
| 重启/PURGE/布局 | 已提交块重启复用；PURGE 后重新填所需块；布局切换安全 MISS，恢复原布局可复用其 fresh 数据；不删除或迁移旧文件 |
| 小文件/兼容 | 256 KiB 留在整对象路径；100/200 MiB 公共大对象走分块；保守模式保留整对象共享生产者及协议行为 |

以上是硬件 Python 实际执行的场景。精确磁盘位翻转/CRC、同 ETag 不同源站、被配置移除源站、受控时钟 TTL、If-Range/416/弱验证器、PURGE 代次、限额/最小预算等另由真实 HTTP/磁盘 Go 回归覆盖，不写成每个场景都由硬件 Python 重跑。关键回归包括 `TestRangeCorruptCacheFailsSafelyOfflineAfterRestart`、`TestRangePartialUnhealthyOriginDoesNotMaskHealthyReplica`、`TestRangeOfflineLookupCannotReenableReconfiguredOrigin`、`TestPublicGET100MiBThroughMinimumBudgetAndDiskWindow`、`TestKnownManifestReplacementAbortsOldHTTPStream` 和 `TestUnchangedManifestRefreshKeepsOldHTTPStream`。观察到当前强 ETag/总长变化时旧流停止后续块；缺失 manifest 只表示未知，保持原源站/强 If-Match，不承诺全局原子版本切换。

## 三轮主压力

下表每格为 **MiB/s / 完成 P99 毫秒**。两项分别取三轮中位数；“P99 中位数”是三个独立批次 P99 的中位数，**不是把三轮请求合并后的 pooled P99**。所有主要场景均三轮；完整 19 场景、各轮 TTFB/完成 P99、CPU、命中率和 I/O 原值见 JSON。

| 主压力场景 | HDD/Otter | HDD/S3-FIFO | SSD/Otter | SSD/S3-FIFO |
| --- | ---: | ---: | ---: | ---: |
| 256 KiB 热、32 并发 | 425.62 / 13.78 | 431.20 / 13.06 | 427.73 / 12.35 | 433.27 / 12.92 |
| 100 MiB 热、8 并发 | 779.87 / 1,126.97 | 816.14 / 1,087.72 | 818.95 / 1,064.59 | 804.21 / 1,052.48 |
| 100 MiB 冷突发、32 并发 | 1,024.77 / 3,113.46 | 1,165.70 / 2,737.54 | 1,053.89 / 3,026.66 | 1,170.02 / 2,727.93 |
| 重叠 Range、8 并发 | 527.44 / 239.58 | 582.06 / 217.00 | 530.67 / 238.59 | 568.53 / 222.80 |
| 拖动 Range、8 并发 | 376.52 / 126.81 | 371.82 / 118.45 | 387.38 / 124.59 | 416.00 / 107.74 |
| 慢源站、8 并发 | 270.93 / 2,950.23 | 304.91 / 2,620.48 | 279.86 / 2,855.88 | 308.31 / 2,592.13 |
| 慢客户端、8 并发 | 286.44 / 2,790.49 | 287.69 / 2,778.06 | 283.21 / 2,821.71 | 283.44 / 2,819.08 |
| 混合热点/长尾、8 并发 | 774.42 / 1,071.58 | 773.78 / 1,055.71 | 766.58 / 1,059.99 | 772.34 / 1,036.45 |
| 唯一小 URL、8 并发 | 120.00 / 27.42 | 121.20 / 25.84 | 117.84 / 28.09 | 113.72 / 27.62 |

慢源站保留每块请求/每 chunk 延迟；冷突发仅屏障阻塞第一块，以便真实并发订阅，不在全部 100 块各人为加半秒。慢客户端为已经 warm 的同一对象，不能把源站与客户端延迟合并解释。混合流量使用洗牌后的加权 URL 列表，长尾使用独立小 URL；实际大小比例保存在批次结果中。客户端逐响应计算 SHA，Python harness 的 CPU 消耗也记录在原始结果；这一测量路径与下表 Go 客户端不等价。

HDD/S3-FIFO 拖动 Range 的第二轮有明显波动，三轮均保留，未挑选最好轮次或剔除慢轮：

| 轮次 | MiB/s | TTFB P99 ms | 完成 P99 ms | 进程写入 B | 缓存设备写入 B | 设备 io_ms |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| r1 | 421.72 | 37.48 | 100.53 | 50,376,704 | 0 | 0 |
| r2 | 50.88 | 37.62 | 1,743.03 | 50,397,184 | 298,151,936 | 1,811 |
| r3 | 371.82 | 34.70 | 118.45 | 50,360,320 | 0 | 0 |

三轮均为 24 个逐 SHA 正确的 206 响应、源站 HEAD 1+GET 48、正文 48 MiB，进程实际读盘均为 0。第二轮设备写入/忙时与延迟升高同窗出现，但设备统计含后台活动和延迟 writeback，不能唯一归因于本进程缓存写入，也不能称为物理读瓶颈；当前原因未证实。主表仍按完整三轮中位数报告 371.82 MiB/s /118.45 ms，原始延迟、CPU、I/O 与命中数据在 JSON 中完整保留。

冷突发 100 MiB /32 并发补充指标（三轮中位数）：

| 介质/策略（100 MiB 冷突发、32 并发） | TTFB P99 ms | CPU 秒/GiB | 请求 HIT % | 逻辑块/字节 HIT % | RAM lookup HIT % |
| --- | ---: | ---: | ---: | ---: | ---: |
| HDD/Otter | 134.06 | 2.87 | 0.00 | 5.44 | 51.45 |
| HDD/S3-FIFO | 112.94 | 2.27 | 0.00 | 12.03 | 100.00 |
| SSD/Otter | 113.42 | 2.84 | 0.00 | 9.09 | 55.24 |
| SSD/S3-FIFO | 108.80 | 2.15 | 0.00 | 13.34 | 99.96 |

请求 HIT 只有全部块复用且成功才成立。逻辑块/字节 MISS 可以由同一生产者合并，32 个客户端冷 MISS 不表示源站重复抓 32 份；此处每轮实际正文仅 100 MiB，客户端总交付 3200 MiB，按正文计算相对逐客户端独立抓取节省 96.875%（HEAD 单列，不把该比值称为缓存命中率）。`RangeHitBytes/MissBytes` 是各读者已准备、按请求区间裁剪的尝试字节，网络失败前可以已计数；不能代替实际客户端正文或唯一源站对齐块字节。RAM lookup 包括内部 metadata 查找，不是 RAM 字节命中率。对齐区间的块/字节比例恰相同，不推广到所有裁剪区间。

热请求可以有 HTTP HIT 但 RAM lookup MISS：例如本次 100 MiB /8 并发中，四组合请求/逻辑块/逻辑字节 HIT 都为 100%，但 RAM lookup 命中三轮中位数为 Otter 33.77%–34.44%、S3-FIFO 34.44%。256 KiB /32 并发则两策略请求 HIT 和 RAM lookup 命中均为 100%。这只说明这些具体存储 lookup 的观测，磁盘路径还可能命中 OS page cache；不由时延反推 RAM 命中，不据此作通用策略胜负结论，未实施读盘 promotion 等 #18 优化。

请求分类另有明确分母：`request_hit_fraction = Hits/(Hits+Misses)`，只针对已分类请求；`observed_hits_per_workload_get` 与 `observed_misses_per_workload_get` 以全部 workload GET 为分母；`unclassified_workload_get_requests = workload GET - Hits - Misses`，其比例也以 workload GET 为分母。Bypass 独立保留，不用它解释全部差额，减去 Bypass 后的残余仅为另一原始投影。本次 228 批主压力合计 664,149 个 workload GET，662,044 Hits、2,105 Misses、Bypass 0、未分类 0；这只是矩阵窗口的计数，新长测的分类结果在下文独立列出，不能沿用旧 12571fc Otter 长测的 35 个未分类 GET。

## checked-hey 与物理文件冷读

下表同样为三轮中位数。hey 检查每个响应的正文读取错误和实际字节总和，每批前后由 SHA 客户端核验；**hey 压力中的每条响应没有单独计算 SHA**。hey 未采集客户端首字节时间，不补造其 TTFB；100 MiB /8 并发的 12 个原始记录中 9 个缺少 P99：HDD/Otter、HDD/S3-FIFO、SSD/Otter 各三轮均缺失，表中用 —；SSD/S3-FIFO 三轮完整，按其三轮 P99 取中位数。未采集值不补造，也不改用其他场景或平均值。默认 Otter 不变，S3-FIFO 作为可选策略，本轮结果不说明任意负载下都更快。

| 介质/策略 | 256 KiB /32 并发 QPS | 对应完成 P99 ms | 100 MiB /8 并发 MiB/s | 对应完成 P99 ms | 物理文件冷读 MiB/s |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| HDD/Otter | 11,911.52 | 5.11 | 933.59 | — | 162.71 |
| HDD/S3-FIFO | 11,844.24 | 5.12 | 950.52 | — | 150.41 |
| SSD/Otter | 11,935.95 | 5.08 | 928.69 | — | 203.73 |
| SSD/S3-FIFO | 11,991.64 | 5.11 | 968.55 | 885.90 | 198.88 |

物理文件冷读在干净重启、恢复完成后，仅对本次自有静止缓存文件执行 `fsync` 和 `POSIX_FADV_DONTNEED`，每轮单请求读 100 MiB；12 次进程 `read_bytes` 为对象大小约 **99.887%–100.043%**，均通过不少于 99% 的门槛。它与 warm-cache 结果分开，且不是 pooled 延迟；物理表只给吞吐中位数。fadvise 是建议，未清空全机 page cache、驱动器/controller 缓存或其他服务缓存，物理文件冷读也不是裸盘带宽测试。

主功能/压力缓存文件不超过 1 GiB，远小于该主机约 62.64 GiB RAM。warm 磁盘路径可能完全由 OS page cache 服务；冷 MISS 写入也是 buffered write，不能把这些吞吐写成 HDD/SSD 物理吞吐，更不能从不同历史路径计算“性能提升/退化”比例。本次没有超过主机 RAM 的工作集压力。

## 资源、命中与磁盘口径

下表仅为各组合 **57 批主压力的采样峰值**，不是整个正式长测的峰值，也不是所有字段在同一时刻的联合状态。RSS 约 107.73–118.18 MiB 的量级不等于 10 MiB 序列化 value 权重预算。

| 主压力采样峰值 | RSS/heap MiB | FD/goroutine | 任务/等待者 | 预留缓冲 MiB | RAM 序列化 value MiB | ghost 条数/记忆权重 MiB | RAM 估算元数据 KiB |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| HDD/Otter | 110.96 / 84.59 | 122 / 206 | 7 / 32 | 49.22 | 10.53 | 0 / 0.00 | 28.06 |
| HDD/S3-FIFO | 118.18 / 78.96 | 122 / 126 | 5 / 32 | 43.78 | 9.76 | 83 / 9.78 | 30.28 |
| SSD/Otter | 107.73 / 74.88 | 122 / 126 | 6 / 32 | 47.75 | 9.79 | 0 / 0.00 | 24.32 |
| SSD/S3-FIFO | 108.39 / 75.44 | 122 / 126 | 4 / 32 | 43.78 | 9.76 | 79 / 9.78 | 29.67 |

四条主压力进程累计 GC 次数（HDD/Otter、HDD/S3、SSD/Otter、SSD/S3）分别为 15,899 /14,891 /15,340 /15,147，累计 GC pause 分别约 3186.86 /3019.36 /3104.91 /3020.34 ms；这些是各自进程从启动到最终排空的累计快照，不把它们当成单个请求延迟。更完整 heap/sys/total allocation、进程 CPU、各轮 GC、RAM eviction/ghost 及最终快照见 JSON/原始统计。主客户端进程资源约 100 ms、SIGUSR1 约 500 ms 采样，未捕获峰值不是上界；内核 CPU tick 对短批次有量化误差，MemStats/日志/CRC/SHA 也有测量开销。

RAM `ResidentBytes` 是保留的序列化 cache value 字节（`len(value)` 之和），可包含 Doc header、HTTP metadata、manifest 及正文，不是纯 HTTP 正文。ghost 的 MiB 是“记忆的旧 value 权重”，不保存原 value；估算元数据只按 node/key 等估计，不包括 allocator/map slack、Otter sketch/维护缓冲等。HDD/Otter 观察到的 `ResidentBytes` 快照为 11,037,524 B（10.53 MiB），超过配置 10 MiB。同次 Stats 的 ChargedBytes 为 9,988,567 B，独立采样最大 ChargedBytes 为 10,267,448 B；这些计数不能相加或视为联合峰值。Otter 的 Items 迭代弱一致，value 汇总与策略权重计数并非原子同刻取得，异步维护也是已知边界；该观察不证明同一时刻的精确实际驻留量或严格同步预算违约，也不唯一归因于某一机制。S3-FIFO 也不把 10 MiB 当进程 RSS 上界。开发机 [RAM 短轨迹校准](RAM_POLICY_2026-10-21.md) 单独保留，其 metadata/256 KiB/1 MiB/4 MiB 和 0.5/1/4 倍容量测试不能当成本机 HTTP 长测。

`process_io_bytes` 是服务进程 `/proc/<pid>/io`，其写入含日志、索引/metadata 和 kernel 记账；`write_amplification_per_origin_body_byte` 仅为该批进程写入/该批实际源站正文，源站正文为零则为 null，不能宣称纯缓存块写放大。`cache_device_io_delta` 来自缓存文件 st_dev 对应 `/proc/diskstats` 的 sectors×512 与 io_ms，HDD 为 sdc、SSD 为 sdk，含设备上其他进程及延迟 writeback；部分批次设备写为零并不表示该批没有缓存写。每批的两套 I/O 原值都保存，不把进程量和设备量混算。跨重启 CPU/GC/命中累计计数按 PID 分段，不能直接相减。

12 项公共功能、4 项主压力、4 项 hey 的最终结构化快照均通过任务/等待者/写者/读者/队列/origin/buffer/retired-runtime 排空断言；4 项保守功能在退出码 0 的正常关闭流程结束，紧凑导出没有它们的独立最终 drain JSON。空闲连接、goroutine、FD、RAM resident 可以保留，不能要求这些也归零。

## 两种策略各两小时 SSD 混合长测通过

两次均为本次冻结源码 `5ccf32c` 的全新连续运行，Otter 完成后才启动 S3-FIFO；没有借用旧失败运行或旧 12571fc Otter 的时间。控制器全部 26 项成功、returncode 0，至 `2026-10-21T20:46:47.808186+00:00` 结束，最终没有本轮拥有的负载进程。每次 `success/qualifies_two_hour_soak` 为 true、目标 7200 秒且实际 monotonic elapsed ≥7200；服务 PID 和配置 RuntimeVersion=1 从初始到最终及原始阶段采样均保持一致。RuntimeVersion 是配置版本，不是运行中有意改变的 PURGE 代次。

下表 UTC 为 2026-10-21 的 initial/final 负载快照时刻；秒数来自 monotonic elapsed，不用两条墙钟相减替代。原 outcome 的 started/ended UTC 是秒级格式，精确快照时间和纳秒原值保存在 JSON。所有 GET 均在客户端当次执行长度/SHA 检验；原始记录未存完整正文，事后独立检查核验的是记录下来的 error/长度/SHA 证据，未重算不存在的原始正文。

| 策略/介质/服务 PID | initial → final 快照 UTC | 实际秒 | stages | workload GET /PURGE /全部 raw | GET 正文字节 B | 独立 GET URL /tail |
| --- | --- | --- | --- | --- | --- | --- |
| Otter /SSD /PID 40385 | 16:46:44.168126 → 18:46:44.447781 | 7200.279144 | 9,756 | 122,666 /2,786 /125,452 | 422,426,181,632 | 45 /26 |
| S3-FIFO /SSD /PID 10305 | 18:46:47.670922 → 20:46:47.761278 | 7200.089907 | 9,789 | 123,096 /2,796 /125,892 | 423,698,104,320 | 45 /26 |

每场实际访问 45 个独立 GET URL，其中 tail 26 个；配置候选 tail64 不等于实际访问64。`outcome.requests` 只计工作负载 GET；raw 另外含成功的 PURGE 控制请求，不能把二者当同一个分母。两场合计 245,762 个 GET、5,582 个 PURGE、251,344 条原始请求，GET 正文 846,124,285,952 B。源站 max-age=3600，两次运行均跨 TTL 边界；固定内容/强 ETag 使该边界不代表跨版本或任意长期稳定。

| 策略 | GET QPS | GET MiB/s | 整场 TTFB P99 ms | 整场完成 P99 ms | 进程 CPU 秒 | CPU 秒/GiB |
| --- | --- | --- | --- | --- | --- | --- |
| Otter | 17.04 | 55.95 | 28.69 | 811.85 | 1,542.16 | 3.92 |
| S3-FIFO | 17.10 | 56.12 | 28.55 | 801.99 | 1,582.57 | 4.01 |

整场和各 phase 的 p50/p95/p99 均从该集合的全部原始 GET 值按 nearest-rank `ceil(N×p/100)` 计算，**不是阶段 P99 的中位数**；也不与前述三轮矩阵 P99 中位数混用。独立原始核验对两场“整场+7 phase ×2指标×3分位”共 96 个值逐一精确一致；完整 p50/p95/P99 在 JSON，以下列各 phase 的 P99。阶段按固定七项轮转，结束时允许只完成最后一轮的一部分。

| 策略 | phase | stages | GET | 实际 GET 正文字节 B | TTFB P99 ms | 完成 P99 ms |
| --- | --- | --- | --- | --- | --- | --- |
| Otter | hotspot | 1,394 | 44,608 | 11,693,719,552 | 5.05 | 12.04 |
| Otter | longtail | 1,394 | 44,608 | 11,693,719,552 | 2.87 | 7.33 |
| Otter | range-drag | 1,394 | 11,152 | 46,774,878,208 | 15.43 | 77.31 |
| Otter | range-overlap | 1,394 | 11,152 | 46,774,878,208 | 13.18 | 75.80 |
| Otter | fullGET | 1,394 | 2,788 | 292,342,988,800 | 8.63 | 866.40 |
| Otter | slow-origin-client | 1,393 | 2,786 | 11,685,330,944 | 33.63 | 158.88 |
| Otter | cold-miss | 1,393 | 5,572 | 1,460,666,368 | 7.47 | 13.31 |
| S3-FIFO | hotspot | 1,399 | 44,768 | 11,735,662,592 | 5.08 | 12.32 |
| S3-FIFO | longtail | 1,399 | 44,768 | 11,735,662,592 | 2.94 | 7.33 |
| S3-FIFO | range-drag | 1,399 | 11,192 | 46,942,650,368 | 15.67 | 78.15 |
| S3-FIFO | range-overlap | 1,398 | 11,184 | 46,909,095,936 | 13.58 | 77.75 |
| S3-FIFO | fullGET | 1,398 | 2,796 | 293,181,849,600 | 9.02 | 858.04 |
| S3-FIFO | slow-origin-client | 1,398 | 2,796 | 11,727,273,984 | 34.08 | 159.08 |
| S3-FIFO | cold-miss | 1,398 | 5,592 | 1,465,909,248 | 7.41 | 13.47 |

| 策略 | 源站 HEAD /GET /正文 B | 进程 read /write B | 缓存设备 read /write B /io_ms | 进程 write/源站正文 |
| --- | --- | --- | --- | --- |
| Otter | 2,914 /10,797 /10,122,952,704 | 0 /11,966,595,072 | 0 /11,642,847,232 /28,837 | 1.18 |
| S3-FIFO | 2,897 /10,756 /10,096,476,160 | 0 /12,007,014,400 | 0 /11,676,602,368 /29,581 | 1.19 |

两场服务进程 read_bytes 都为0，表明本次混合工作集的读盘路径没有计入内核实际读盘字节；不能据此说没有磁盘层读取，也不能宣称物理 SSD 吞吐，OS page cache 边界继续适用。进程 write 含日志、索引/元数据和内核记账，设备计数含其他进程及延迟 writeback；两者不混算，不作本进程写放大或时延的唯一因果解释。

### 长测分类与分母

| 策略 | Hits /Misses | Bypass | 已分类 /全部 GET | 未分类数 /全部 GET比例 | 已分类 HIT | 观测 HIT /MISS（全部GET） | 逻辑块 /裁剪字节 HIT | RAM lookup HIT |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Otter | 114,082 /8,558 | 0 | 122,640 /122,666 | 26 /0.02% | 93.02% | 93.00% /6.98% | 97.47% /97.47% | 37.03% |
| S3-FIFO | 114,509 /8,557 | 0 | 123,066 /123,096 | 30 /0.02% | 93.05% | 93.02% /6.95% | 97.50% /97.50% | 39.92% |

已分类命中率为 Hits/(Hits+Misses)；观测 HIT/MISS 和未分类比例以全部 workload GET 为分母。Otter 的 26 个、S3-FIFO 的 30 个 GET 没有计入 HIT/MISS；Bypass 都为0，不能把差额解释为 Bypass、真实 MISS 或任何已证实原因。Requests delta 包含 PURGE，不能拿它作为 GET 命中率分母。逻辑块/裁剪尝试字节与 RAM lookup 仍是不同口径；RAM lookup 包含 metadata，不是 HTTP 请求或网络正文的 RAM 命中率。

### 长测资源、GC 与十分钟趋势

| 策略 | RSS /HeapAlloc 峰值 MiB | FD /goroutine | 任务 /等待者 | writer /reader | 预留缓冲 MiB | RAM value /charged MiB | ghost 条数 /记忆权重 MiB | 估算元数据 KiB |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Otter | 69.82 /55.83 | 22 /27 | 2 /2 | 2 /4 | 14.06 | 10.76 /9.77 | 0 /0.00 | 15.83 |
| S3-FIFO | 70.18 /56.25 | 22 /21 | 1 /2 | 1 /5 | 15.31 | 9.76 /9.76 | 48 /9.76 | 18.44 |

| 策略 | GC 增量次数 /累计暂停 ms | TotalAlloc 增量 B | RAM lookup Hits /Misses | evictions /被淘汰权重 B | promotions /ghost hits |
| --- | --- | --- | --- | --- | --- |
| Otter | 70,638 /10,819.53 | 1,653,336,557,432 | 479,054 /814,532 | 11,695 /9,117,242,043 | 0 /0 |
| S3-FIFO | 71,244 /11,076.18 | 1,656,190,312,200 | 517,910 /779,615 | 12,819 /10,305,698,508 | 5,422 /1 |

TotalAlloc 是累计分配字节，不是同时在堆内的存活量：两场分别约 1.65 TB，GC 增量分别70,638/71,244次，暂停累计约10.82/11.08秒；保留这些成本，不用较低 RSS 掩盖分配/GC。HeapInuse/HeapSys、全部 RAM gauge/counter 增量、进程初始/最终快照及每窗 activity peaks 均保存在 JSON。

上表每项是独立采样峰值，不代表同一瞬间联合状态。Otter 长测观察到 `ResidentBytes=11,282,849 B`（10.76 MiB）的弱一致快照，高于配置10MiB，也高于前述 HDD 矩阵的10.53MiB；独立 `ChargedBytes` 峰值为10,242,349B。Items/value 汇总与策略权重非原子同步、异步维护均是已知边界，不能把这两个峰值配成同刻精确驻留量、唯一归因或同步硬预算违约证明。S3-FIFO 的 resident/charged峰值为10,237,042B；两算法的10MiB value预算均不是进程RSS限制。

下表完整保留13个趋势窗。前12窗为0–120分钟的十分钟分箱；最后≥120分钟窗仅有7/5个资源采样，覆盖运行尾部和排空附近，**不是额外完整十分钟**。表中每格“中位数/峰值”仍为采样观测，不是峰值上界或无限期无泄漏结论。

| 距初始快照 | Otter样本数 | Otter RSS中位/峰MiB | Otter heap中位/峰MiB | S3样本数 | S3 RSS中位/峰MiB | S3 heap中位/峰MiB |
| --- | --- | --- | --- | --- | --- | --- |
| 0–10 min | 6,024 | 53.15 /69.82 | 32.17 /55.83 | 6,024 | 51.71 /69.66 | 31.30 /55.58 |
| 10–20 min | 6,054 | 53.19 /67.82 | 31.58 /53.09 | 6,084 | 52.27 /67.69 | 31.92 /49.06 |
| 20–30 min | 6,089 | 53.33 /68.73 | 31.08 /53.14 | 6,028 | 53.60 /68.65 | 31.30 /55.00 |
| 30–40 min | 6,067 | 54.25 /66.98 | 32.06 /54.98 | 6,036 | 53.71 /65.72 | 32.06 /53.47 |
| 40–50 min | 6,106 | 53.68 /69.56 | 31.82 /54.19 | 6,104 | 53.51 /66.32 | 32.38 /54.32 |
| 50–60 min | 6,026 | 53.87 /68.27 | 32.42 /54.74 | 6,044 | 53.23 /69.16 | 32.21 /54.72 |
| 60–70 min | 6,039 | 54.07 /66.17 | 31.70 /53.80 | 6,087 | 53.61 /67.24 | 32.29 /52.19 |
| 70–80 min | 6,039 | 53.54 /65.79 | 32.05 /53.40 | 6,079 | 53.58 /66.52 | 31.62 /55.27 |
| 80–90 min | 6,047 | 53.71 /66.99 | 32.50 /52.54 | 6,124 | 54.60 /66.97 | 31.42 /56.25 |
| 90–100 min | 6,078 | 53.52 /66.89 | 31.73 /53.51 | 6,115 | 55.03 /66.57 | 31.43 /53.46 |
| 100–110 min | 6,160 | 54.06 /67.65 | 31.90 /55.82 | 6,122 | 54.77 /67.39 | 31.89 /53.50 |
| 110–120 min | 6,147 | 55.06 /68.25 | 32.61 /55.08 | 6,144 | 54.82 /70.18 | 31.72 /56.04 |
| ≥120 min，局部尾窗 | 7 | 52.87 /56.68 | 23.46 /35.57 | 5 | 58.00 /58.00 | 41.82 /41.84 |

最终两场全部12个活动字段（请求/队列、writer/reader、磁盘队列/缓冲、预留、shared-fill任务/等待者、retired runtime）及 Origin.InFlight/Queued 均为0；Rejected/Cancelled、SharedFillFailed/Cancelled、CacheWriteErrors、Origin.Errors 均为0。空闲 goroutine/FD、RAM value 和堆允许保留，不要求这些清零。原始 stages/GET/控制/源站记录在全部负载停止后，由绑定本源码/二进制的已审计 helper 串行汇总，并另行独立读取原始记录核对；没有与正式负载竞争解析。

## 失败记录、修复与 CI

保留四次先前中止/失败 run，均不算新 5ccf32c 最终矩阵或长测通过证据：

1. `20261021T085442Z-b3d26ba`：已有前 12 项通过后，SSD/64 KiB 截断注入器写满一个 CHUNK 后才关闭，实际 3 个块均完整且 SHA 正确，导致故障断言失败。`a9978c9` 修复为发送至多 min(CHUNK,length//2) 后立即关闭，1 字节尾块发送 0；真实 HTTP 64 KiB/1 MiB/4 MiB/1 byte 的 IncompleteRead、相邻正常块与双策略三布局二进制回归通过。源站注入错误被修复，未弱化成功正文断言。
2. `20261021T090708Z-a04c8ab`：前 11 项通过后，HDD/S3-FIFO 保守模式启动端口 40762 被占用；CI run `39574545934` 首轮也在端口 42195 冲突，保留原日志后仅重试一次成功。`e11ec31` 改测试工具及三个二进制测试入口，读取实际 outbound ephemeral 范围 32768–60999，选择范围外非特权端口，通配地址绑定探测占用、最多 128 候选绑定，跨重启保留端口；生产服务未加监听继承/重试框架。显式请求相同端口的其他程序仍可能抢先，不承诺原子交接。

3. `20261021T092149Z-f969c1c`：24 项矩阵通过后，Otter SSD 正式 soak 在约 22 分钟失败，最后完成阶段为 1358.808 秒、1824 stages、22,960 个 workload GET。下一批两个正常 100 MiB 完整 GET 中一个 HTTP200 仅收到 89,128,960 B（85 MiB），长度/SHA 失败，另一个完整成功。失败前缀 SHA 独立验证等于期望正文前 85 MiB；该批源站 50 个成功块 GET、50 MiB、HEAD 0，没有 block85 请求。原目录、缓存、日志和 failure-analysis 保留，控制器已停止，S3-FIFO 未开始；无两小时成功 outcome。

4. `20261021T105807Z-12571fc`：24 项矩阵通过，Otter SSD 实际 7200.456110872328 秒通过，但 S3-FIFO SSD 最后完成 6001.203 秒、8076 stages、101,546 个 workload GET 后失败。下一批慢源站/慢客户端的重叠 Range 为 `[0,4 MiB)` 和 `[2,6 MiB)`；前者完整，后者 HTTP206、声明 4 MiB、实际仅 1 MiB，前缀 SHA 正确。此前授权 PURGE 后只有 HEAD1、块2/0/1/3四个成功 GET，没有块4/5，`SharedFillFailed` 增1，CacheWriteErrors/OriginErrors 为0。S3 没有合格两小时 outcome，**该次整体验收失败**。原缓存和日志保持不变；旧 Otter 的两小时结果及其 35 个未分类 GET 只属于旧源码，不抵扣本次两次新长测。

f969c1c 失败后的本机真实 HTTP 快慢双读 100 MiB、32 MiB 环形盘快速复现同类截断；首块发送屏障使“已准备块在消费前被淘汰”稳定复现。后续确定性缓存写入还证明旧目录项可能保留，但其分配已被正常回卷覆盖。`a24b1dc`（集成为 `12571fc`）把目录 lookup 到 RAM/磁盘 copy 放入同一现有读锁，并仅将目录缺失/正常环覆盖分类为 `ErrNotResident`；存储错误、关闭、非法 offset、CRC/文档身份和失效错误保持硬失败。等价读者同时恢复时的在写状态由独立 `ErrWriteInProgress` 表示，通过既有同身份生产者合并。消费端释放失败 reader 后，在有界 reader 额度内检查原 URL 代次和已观察版本，再沿固定源站、强 ETag/If-Match 和原 parent 恢复一次；不重捕 URL 代次、不切换来源、不无限重试。恢复后计为逻辑 MISS。再次淘汰/在写、源错误、预算失败或版本变化仍受控失败。

新回归覆盖快慢 100 MiB/32 MiB 盘、准备完成的 HIT/MISS 被淘汰、同块双读者恢复只一个活动生产者、等价三路初始并发、PURGE/新 manifest 拒绝恢复旧代、真实 CRC/存储错误对照和最终资源排空。未知测试控制头曾造成请求身份差异的 probe 已明确排除为测试 artifact；最终屏障使用外层 handler 的 atomic 请求编号，不改变生产请求身份或未知头隔离。这里证明了同类故障机制和修复，**未唯一重放原远端 block85 的内部失败路径**，不能宣称原故障唯一归因已证明；因此新源码必须重新完成全部矩阵和两策略全新长测。

实现阶段还修复了普通 private/no-store 响应失效 manifest 的稳定前缀，以及已观察到新强版本后停止旧流的围栏，并增加同强版本刷新不中断合法旧流的真实 TCP 回归。具体代码与红绿证据属于实施交接，不把这些修复当成 #18 性能优化。

12571fc 后的本机自然压力及原冻结二进制 512 MiB 盘 180.1269666 秒诊断短测均未复现；该短测明确 `qualifies_two_hour_soak=false`，不算验收。临时 Go overlay 只在 derive 与 writer admission 之间放大合法抢占窗口，配合真实磁盘回卷稳定产生同类截断；探针显示 parent/selector 仍有效但物理 generation 已变。`3984707`（集成为本次 `5ccf32c`）新增 `OpenWriteDerivedFill`，在一个现有 stripe 锁区内取得当前派生快照并占有 writer，保留原 parent/PURGE/版本、selector CAS、活动 writer 与发布围栏；无无限重试或身份放宽。旧 split API 保留过期 generation 拒绝。永久真实 HTTP 双重叠 Range/滚动盘测试、旧 split 放大抢占 RED 与新锁内等价抢占 GREEN 支持该机制；**不能据此唯一证明旧远端自然失败的内部路径**，也不能将 overlay 叫作原部署二进制自然复现。调试 overlay 未进入生产代码。

测量源码 `5ccf32c` 两个 CI **首轮均绿**：[build/test](https://github.com/Monsterwi/bakemono/actions/runs/39631477278)、[coverage](https://github.com/Monsterwi/bakemono/actions/runs/39631477142)。独立 Standards/Spec 增量审查均通过、无发现。集成全仓 normal/race/vet/build 与 16 项真实工具测试无跳过通过：集成 server normal 30.164 秒、race 142.701 秒（现有 CI race 上限 180 秒，未放宽）；集成工具 43.411 秒，专机同源码工具 39.671232 秒。旧源码通过的 CI 和 Otter 长测均不替代新运行。最终报告准确性双轴复审、仅含文档的交付提交及其 CI 以 Git 历史和最终 Issue 验收评论中的外部记录为准；交付提交与二进制测量源码分别记录。

## 本次能说明与不能说明的边界

修复版已完成矩阵证明了单进程、固定配置、真实 HTTP/磁盘路径在已测时段的功能与三轮负载观测；旧版本通过矩阵后仍真实长测失败，说明这些证据不能替代长测。本次两种策略各全新两小时已通过，构成该源码/配置的专机实测验收证据；它只说明该时段、该工作集和机器的观测，不代表长期生产稳定。没有 ATS 10.2 公平对照，不宣称相对 ATS 的性能优势；没有 WAN/TLS/H2 压测、跨节点、任意多 Range、io_uring、未提交字节 RWW 或 #18 泛化优化。

历史 [2026-10-21 保守整对象主报告](../BENCHMARK_2026-10-21.md) 和 [2026-10-20/200 MiB/ATS 9.2.3 报告](../ACCEPTANCE_2026-10-20.md) 保留其原版本/环境。历史“100 MiB 冷 GET 一次回源”和 40 MiB Range 并集不替代本次公共块路径结果；不同路径、机器与验证成本的数字不直接计算提升比例。总计划 #1 保持打开，#18 后置，不合主分支或发布。
