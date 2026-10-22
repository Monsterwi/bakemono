# Razor 与 ATS 10.2.1 基准测试（2026-10-22）

本报告对比 Razor 当前 main `193fe7d`、ATS 10.2.1 原生整对象缓存，以及官方 Slice + cache_range_requests 分块路径。完成22次正式调用：72组热读、18组混合负载、18次文件冷读、6个200MiB兼容组和4组预取敏感性；热读共9,220,306次请求，原始包含9,960条SHA校验/故障请求记录。正式计时在同一台专用机器上串行执行，保留历史缓存；本轮结果不替换旧版本验收数据。

## 主要结论

1. **小文件热命中仍明显落后 ATS。** 256 KiB、32 并发时，Razor 约 4.9k QPS，ATS native 约 49k QPS、ATS Slice 约 23k QPS；Razor 的 CPU/GiB 也更高。
2. **原生块级合并和部分缓存有实际价值。** Razor 两种盘的三轮 16 客户端冷读 100 MiB 均只回源 100 个不同的 1 MiB 块；交叠区间联合为 24 MiB，只回源 24 MiB，后续读取及源站返回 503 时不再回源。ATS native 冷 Range 直接回源；ATS Slice 能复用部分缓存，但本次配置存在重复块回源。
3. **不能把大文件热读优势称为磁盘优势。** Razor 热读几乎没有实际读盘；ATS 默认使用直接 I/O。单请求文件冷读另列，两种介质的结论不同；工作集与硬件缓存也会影响结果。
4. 优先优化响应刷新粒度、重复块读取和分配/GC，保留已提交块校验、预算和失效边界；本轮没有足够证据把 io_uring 或替换 RAM 算法列为首要工作。

## 版本、环境与方法

截至本次核实，官方最新稳定版为 **ATS 10.2.1**，固定上游 commit `e395e13beab200008705fffb942e57bbc969c424`。下载页的日期与发行目录不一致，本轮以版本、tag、发行包 SHA512 为准；官方包校验通过，未做本地 PGP 信任验证。[官方下载页](https://trafficserver.apache.org/downloads)、[发行包与校验文件](https://downloads.apache.org/trafficserver/)、[固定 tag](https://github.com/apache/trafficserver/tree/10.2.1)

- 测试机：CentOS 7，Linux 3.10.0-957，2×E5-2620 v4，16 物理核/32 逻辑核，约 62.64 GiB RAM；HDD 为 ST4000NM0295、SSD 为 Intel SSDSC2BB800G4，均为 XFS。地址及登录信息不公开。
- Razor：Go 1.26.10、CGO 关闭、GOMAXPROCS=8、Otter 默认、公共静态模式、1 MiB 块。ATS：Release、GCC 13.4、OpenSSL 3.6.5、8 个网络线程，关闭线程数自动配置，默认 LRU。未设 CPU 亲和性或硬 CPU 配额；后台 I/O 线程模型不同。
- 每个产品/介质使用独立的新 1 GiB 磁盘缓存；均设 10 MiB RAM 缓存预算。预算不含 OS page cache，也不等于 RSS。ATS `max_doc_size=0`，Razor 最大对象 256 MiB；本轮文件均在两者限额内。
- 不可压缩固定文件：256 KiB、100 MiB，200 MiB 兼容；源站、客户端、代理均在本机，HTTP/1.1 keep-alive、identity，无 TLS。两端均开启访问日志，格式和落盘方式不同。
- 热读：校验版 hey，8 个客户端执行核，发起窗口 10 秒，等待已发请求结束；实际耗时可超过 10 秒。每组 3 轮，报告轮次中位数，JSON 保留原值/min/max。每轮前后 SHA256，轮内逐响应检查实际字节数、状态及读取错误；**轮内不是逐响应 SHA256**。
- 功能、冷突发、Range 和混合负载使用流式 SHA256 客户端逐响应校验。其 CPU 与源站 Python 实现也可能限制吞吐，不能据此推导服务器单独极限。
- ATS 从源码实际使用 `O_DIRECT/O_DSYNC`，运行文件描述符标志也已核对。Razor 是普通 ReadAt/WriteAt，提交不等于 fsync；ATS 默认正文 checksum=0，Razor 始终验证片段 CRC。二者不是相同 I/O、内存和完整性成本模型。[ATS 打开标志](https://github.com/apache/trafficserver/blob/10.2.1/src/iocore/cache/CacheProcessor.cc#L234)、[ATS 默认 checksum](https://github.com/apache/trafficserver/blob/10.2.1/src/records/RecordsConfig.cc#L930)、[Razor CRC](../../cache/doc.go)

构建标识：

```text
Razor SHA256 956761ed9750c1631d8f621659fdb904a05e6adaeae4f3af05cbd155c1fc7d21
ATS   SHA256 6a15fe1db1307719771d1465a85179310e72f9bcee838e0a2194b2d68d231e23
run_id       ats-compare-20261022T095324Z
```

## 256 KiB 热命中

QPS 与延迟均取三轮中位数；P99 为每轮 P99 的中位数，不是合并全部样本后的 P99。CPU 为代理进程 CPU 秒/GiB 有效正文。

| 盘 | 服务 | C32 QPS | C32 P99 ms | C100 QPS | C100 P99 ms | C100 CPU s/GiB |
| --- | --- | --- | --- | --- | --- | --- |
| SSD | Razor | 4,950 | 8.19 | 4,959 | 23.11 | 2.17 |
| SSD | ATS native | 49,029 | 1.62 | 47,835 | 7.63 | 0.60 |
| SSD | ATS Slice | 23,193 | 2.07 | 23,349 | 6.78 | 1.41 |
| HDD | Razor | 4,917 | 8.17 | 5,035 | 23.14 | 2.21 |
| HDD | ATS native | 49,525 | 1.58 | 48,788 | 7.46 | 0.58 |
| HDD | ATS Slice | 22,885 | 2.05 | 23,289 | 7.12 | 1.41 |

## 100 MiB 热命中

吞吐单位 MiB/s；大文件每轮请求数较少，hey 缺少的 P99 保持缺失，表中采用 P95。读放大列为进程读盘字节/有效响应字节，说明两条路径确实承担不同的磁盘工作。

| 盘 | 服务 | C8 MiB/s | C8 P95 ms | C32 MiB/s | C32 P95 ms | C32 CPU s/GiB | 读盘/响应字节 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| SSD | Razor | 902.0 | 935 | 965.7 | 3,412 | 3.98 | 0.000 |
| SSD | ATS native | 373.7 | 2,304 | 337.1 | 9,552 | 0.62 | 1.000 |
| SSD | ATS Slice | 334.4 | 2,557 | 322.4 | 10,032 | 1.20 | 1.028 |
| HDD | Razor | 928.5 | 899 | 978.0 | 3,313 | 3.88 | 0.000 |
| HDD | ATS native | 156.9 | — | 692.2 | 4,657 | 0.61 | 1.000 |
| HDD | ATS Slice | 98.2 | — | 288.7 | 10,577 | 1.31 | 0.930 |

热工作集只有约 100 MiB。HDD 的高并发重复热读可受驱动器/控制器缓存及 I/O 合并影响；本轮没有清除硬件缓存，不能把块设备读量等同于每字节都读了盘片。

## 100 MiB 单请求文件冷读

每轮重启进程、仅对本次私有缓存文件 fsync + POSIX_FADV_DONTNEED，再用单请求读取；均核对回源计数不增长、正文正确，以及进程实际读盘量 ≥ 对象大小 99%。这是 OS 文件缓存层面的冷读，**不保证硬件缓存或盘片冷态**，也不是多盘或高并发容量测试。

| 盘 | 服务 | 三轮 MiB/s | 中位数 MiB/s | 实际读盘核对 |
| --- | --- | --- | --- | --- |
| SSD | Razor | 189.2, 201.5, 196.5 | 196.5 | True |
| SSD | ATS native | 308.1, 305.6, 291.2 | 305.6 | True |
| SSD | ATS Slice | 335.1, 321.5, 311.3 | 321.5 | True |
| HDD | Razor | 188.4, 187.1, 197.9 | 188.4 | True |
| HDD | ATS native | 134.1, 134.0, 134.4 | 134.1 | True |
| HDD | ATS Slice | 112.3, 113.2, 112.2 | 112.3 | True |

## 冷突发合并与部分缓存

小文件为 100 客户端同时请求；大文件为 16 客户端同时请求。Razor/ATS Slice 的基本单位是块，ATS native 是整对象；应比较字节及同块重复量，不能只比 GET 总数。

| 盘 | 服务 | 小文件 GET 三轮 | 100 MiB GET 三轮 | 100 MiB 源站 MiB 三轮 | 相同 Range 最大 GET 三轮 |
| --- | --- | --- | --- | --- | --- |
| SSD | Razor | 1 / 1 / 1 | 100 / 100 / 100 | 100 / 100 / 100 | 1 / 1 / 1 |
| SSD | ATS native | 1 / 1 / 1 | 1 / 1 / 1 | 100 / 100 / 100 | 1 / 1 / 1 |
| SSD | ATS Slice | 1 / 1 / 1 | 277 / 131 / 210 | 277 / 131 / 210 | 17 / 8 / 17 |
| HDD | Razor | 1 / 1 / 1 | 100 / 100 / 100 | 100 / 100 / 100 | 1 / 1 / 1 |
| HDD | ATS native | 1 / 1 / 1 | 1 / 1 / 1 | 100 / 100 / 100 | 1 / 1 / 1 |
| HDD | ATS Slice | 1 / 1 / 1 | 442 / 431 / 421 | 442 / 431 / 421 | 17 / 17 / 17 |

Razor 每轮另有 1 个大文件 HEAD 用于固定版本与长度，已计入 JSON；ATS 路径没有该 HEAD。两种盘的跨 8 个源 IP/不同 User-Agent 小文件冷合并也已检查。

交叠 Range：8 客户端交替读取 `[0,16MiB)` 与 `[8MiB,24MiB)`，响应合计 128 MiB、唯一覆盖 24 MiB。下表是冷读/再次读取/随后立即让源站返回503的实际回源。

| 盘 | 服务 | 冷回源 MiB | 再次回源 MiB | 503阶段回源 MiB | 503阶段失败/8 |
| --- | --- | --- | --- | --- | --- |
| SSD | Razor | 24.0 | 0.0 | 0.0 | 0 |
| SSD | ATS native | 128.0 | 128.0 | 0.0 | 8 |
| SSD | ATS Slice | 41.0 | 0.0 | 0.0 | 0 |
| HDD | Razor | 24.0 | 0.0 | 0.0 | 0 |
| HDD | ATS native | 128.0 | 128.0 | 0.0 | 8 |
| HDD | ATS Slice | 93.0 | 28.0 | 0.0 | 8 |

这不是“所有块已经持久化”的前提测试：ATS 可边写边读，客户端完成不保证所有块均已提交/持久化。HDD Slice 在立即503阶段出现截断206，SSD Slice通过；另有静置2秒、预取0/1的新缓存对照如下，未把此现象直接定性为产品缺陷。

### ATS Slice 预取敏感性

保持插件、合并预算、版本检查不变，只切换 `prefetch-count`，每项使用新缓存；16 客户端冷100MiB重复三轮，交叠Range冷/热后静置2秒再使源站返回503。0 是官方默认。

| 盘 | 预取 | 冷 GET 三轮 | 冷源站 MiB 三轮 | 静置后503读取 |
| --- | --- | --- | --- | --- |
| HDD | 0 | 318 / 292 / 320 | 318 / 292 / 320 | False |
| HDD | 1 | 528 / 513 / 344 | 528 / 513 / 344 | True |
| SSD | 0 | 310 / 244 / 330 | 310 / 244 / 330 | True |
| SSD | 1 | 378 / 222 / 233 | 378 / 222 / 233 | True |

官方源码的后台预取集合只去重后台任务；前台依赖核心写锁/RWW。`max_open_read_retries` 只在特定 BUSY 状态重试，不能理解为所有 MISS 都等待10秒；重读失败仍可能绕过缓存回源。数据只能证明本配置下的重复请求，未完成根因诊断。没有为跑分开启会削弱跨块一致性的 `--ref-relative`。[预取源码](https://github.com/apache/trafficserver/blob/10.2.1/plugins/slice/prefetch.cc#L27)、[前台块请求](https://github.com/apache/trafficserver/blob/10.2.1/plugins/slice/util.cc#L175)、[缓存重试](https://github.com/apache/trafficserver/blob/10.2.1/src/proxy/http/HttpCacheSM.cc#L149)

## 热点与长尾混合

36个URL：16个小文件、4个热门大文件、16个长尾大文件，唯一正文约2004MiB，大于1GiB磁盘缓存。请求槽为256小/56大（约82%/18%），热门大文件权重10、长尾1；每客户端固定种子打散。预热热门对象后，8并发、每轮发起15秒、3轮连续运行。实际请求比例随完成速度不同，JSON保留每轮计数；这是同一生成策略，**不是相同数量/时刻的回放 trace**。

| 盘 | 服务 | MiB/s | TTFB P99 ms | 完成 P99 ms | 回源/响应字节 | 正文失败 | 客户端/源站 CPU核 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| HDD | ATS native | 155.0 | 437.68 | 10,818 | 3.2% | 0 | 1.6 |
| HDD | ATS Slice | 167.6 | 406.45 | 7,738 | 24.3% | 0 | 1.7 |
| HDD | Razor | 653.0 | 41.11 | 2,133 | 18.4% | 0 | 6.4 |
| SSD | ATS native | 375.9 | 35.27 | 3,908 | 19.6% | 0 | 3.4 |
| SSD | ATS Slice | 386.3 | 34.89 | 2,496 | 13.1% | 0 | 3.4 |
| SSD | Razor | 670.7 | 36.13 | 2,048 | 18.4% | 0 | 6.5 |

混合客户端逐响应计算SHA256，且源站同进程；其CPU核数高时会限制路径吞吐。响应字节与回源字节不是相同对象集合的唯一覆盖量，比例不是严格的逐字节缓存命中率。阶段 `/proc/io` 含缓冲写时序，**不当作持久化写放大**。

## 资源开销

RSS为代理进程采样峰值，不含OS page cache；FD为采样峰值。

| 场景 | 热读 RSS峰值 MiB | 热读FD峰值 |
| --- | --- | --- |
| hdd-ats-native-100 | 167.3 | 162 |
| hdd-ats-slice-100 | 200.2 | 153 |
| hdd-razor-100 | 88.5 | 166 |
| ssd-ats-native-100 | 167.7 | 157 |
| ssd-ats-slice-100 | 205.2 | 149 |
| ssd-razor-100 | 87.8 | 174 |

Razor混合负载的每GiB响应分配量与GC频率（各三轮中位数）：

| 盘 | 分配 GiB / 响应 GiB | GC 次/秒 |
| --- | --- | --- |
| SSD | 5.07 | 105.6 |
| HDD | 5.12 | 104.5 |

这是计数器观察，尚未用pprof区分重复读取、元数据、复制与其他分配的贡献。

## 功能与失败边界

| 盘 | 服务 | 功能判定通过 | 未满足项 |
| --- | --- | --- | --- |
| SSD | Razor | 18/18 | 无 |
| SSD | ATS native | 16/18 | cached Range while origin fails；clean restart cached SHA256 |
| SSD | ATS Slice | 18/18 | 无 |
| HDD | Razor | 18/18 | 无 |
| HDD | ATS native | 17/18 | cached Range while origin fails |
| HDD | ATS Slice | 16/18 | cached Range while origin fails；clean restart cached SHA256 |

- 两种盘的200MiB MISS/HIT和Range兼容均通过；正式热读72组的错误与回源增量逐组核对，见JSON。截断源站应报错，清除故障后重试须得到完整正确正文；三种路径均执行该检查，预期失败不计为成功响应。
- RWW只比较“整对象完成前开始输出”及首字节时的源站进度，不直接比较时延：原生ATS使用慢源，块路径在后块设屏障，故注入条件不同。Razor首字节前源站已完成1MiB；ATS本次可在首块尚未完成时输出，完整块校验再输出的语义更强，成本也不同。
- 重启差异仅报告观察：ATS native SSD重启后小文件额外回源1次；HDD Slice也有额外回源。当前没有区分缓存聚合写、目录同步、淘汰和时序的贡献，不能据此声称ATS不支持重启恢复。Razor本轮重启检查未新增回源。
- query换序命中通过；但ATS cachekey排序实现使用集合，会去掉完全重复项，Razor保留重复数量。本轮常规URL无此歧义，未宣称两种完整身份规则等价。[ATS cachekey源码](https://github.com/apache/trafficserver/blob/10.2.1/plugins/cachekey/cachekey.cc)

## 建议的改进顺序

1. **响应写入热路径。** 先做CPU/锁/分配profiling，再验证合并写、减少不必要Flush和deadline更新。当前每次streamResponse分配32KiB缓冲，并在每32KiB写入后更新deadline及Flush；应保留首个已提交块的及时输出和慢客户端超时。[streamResponse](../../server/handler.go#L141)
2. **复用一次已经读取并校验的块。** `ensureRangeBlock` 命中检查读取完整块，发送阶段 `readPreparedRangeBlock` 再读；每次还分配块缓冲。考虑在同一预算/失效代次下移交已校验块及释放句柄，不能绕过版本、CRC或PURGE栅栏。[命中检查](../../server/range_cache.go#L421)、[发送读取](../../server/range_cache.go#L345)
3. **减少分配与GC，并验证RSS/页缓存预算。** 混合组的TotalAlloc、GCCount、堆与正文比值可定位收益；先减少重复复制/序列化，再评估有界缓冲复用。10MiB RAM策略配额无法约束OS page cache，需更大工作集或受控cgroup实验，才能判断节点真实内存效率。
4. **随后优化磁盘并行与预读。** 比较有界异步ReadAt队列/读取预取和文件布局，在相同完整性、缓存温度及持久化口径下再测。当前内核不能验收io_uring，本轮也没单测native AIO；没有证据认为先换I/O API就能解决约10倍的小文件QPS差距。

以上是代码和资源计数支持的候选，未用CPU火焰图定量归因，也未在本轮修改服务器来追分。

## 复现与证据

入口：[tools/ats_comparison.py](../../tools/ats_comparison.py)。先独立安装官方ATS10.2.1及校验版hey，准备固定fixtures，在每种介质上为每次调用指定全新目录。示例：

```sh
python3 tools/ats_comparison.py \
  --binary "$RAZOR" --hey "$HEY_CHECKED" --fixtures "$FIXTURES_100" \
  --ats-prefix "$ATS_PREFIX" --ats-env "$ATS_BUILD_ENV" \
  --output "$NEW_RESULT" --cache-root "$NEW_CACHE" \
  --source-commit 193fe7da77f209b8362f270dc5eb1d0b6e4eaf01 \
  --variant ats-slice --seconds 10 --repeats 3
```

分别切换 `razor/ats-native/ats-slice`；`--compat-only`搭配200MiB；`--mixed-only`搭配100MiB；`--cold-only --ats-prefetch 0/1`复现敏感性组。不同压力调用顺序运行；需要以root创建新目录并让ATS以实际nobody UID/GID运行，不能硬编码UID。不会删除旧缓存或全局drop_caches。

ATS构建要求CMake≥3.20/C++20/OpenSSL≥1.1.1/PCRE2/zlib。此机使用独立conda工具链，CMake4.4.4；Release，CRIPTS/QUICHE/DOCS/测试构建关闭，CPATH指向该独立环境include，源码未改动。[固定构建定义](https://github.com/apache/trafficserver/blob/10.2.1/CMakeLists.txt)

ATS实际records/remap/logging快照在原始包中。合并为RWW=1、open_write_fail_action=5、max_open_read_retries=1000、max_open_write_retries=10、open_read_retry_time=10ms；日志使用logging根节点。运行时读取核对8线程、RAM与合并参数，无Invalid config或日志解析错误。Slice1MiB、prefetch1，cachekey对Range入键，CRR使用no-modify-cachekey/consider-ident/verify-cacheability。[合并配置校验](https://github.com/apache/trafficserver/blob/10.2.1/src/proxy/http/HttpConfig.cc#L1502)、[插件说明](https://docs.trafficserver.apache.org/en/10.2.x/admin-guide/plugins/cache_range_requests.en.html)

ATS默认RAM为LRU；10.2.1也已有实验性S3-FIFO，本轮没有测ATS/Razor的其他RAM策略，不再沿用“ATS只有FIFO”的旧描述。[配置定义](https://github.com/apache/trafficserver/blob/10.2.1/src/records/RecordsConfig.cc)

- [汇总JSON与三轮原值](ATS_COMPARISON_2026-10-22.json)
- [原始指标、逐请求SHA256/源站记录、100ms资源样本及配置](ATS_COMPARISON_2026-10-22.raw.json.gz)
- 原始包SHA256：`fc1cf81d8d0d3d8e12aa528faacf3cd4d2e1299f01a34065ef38587fe664a7f2`；测试机完整日志保留在固定lab的 `results/ats-compare-20261022T095324Z`，不将地址、登录信息、进程日志内主机名纳入公开包。
- 测试机使用当前真实Razor二进制与校验版hey，工具回归20项通过、0跳过；涵盖截断正文检测及新比较配置。
- CI及任务验收记录见 [Issue #28](https://github.com/Monsterwi/razor/issues/28)。未合并main、未发布，也未关闭总计划#1或性能任务#18。

本轮未覆盖TLS/H2/H3、WAN、多机、大于主机RAM的工作集、断电持久性和新增长时soak；短时同机结果不能作为公网生产容量承诺。
