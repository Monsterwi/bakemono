# 真实二进制端到端与容量验证：2026-10-20

关联 [Issue #14](https://github.com/Monsterwi/bakemono/issues/14)，总计划 [#1](https://github.com/Monsterwi/bakemono/issues/1) 保持打开。

本机上线前验证完成：256 KiB 和 200 MiB 对象、真实磁盘缓存、实际主程序与流式 HTTP client 均已执行。正式测量 **64 批、49,480 请求，非预期错误 0**；其中 30 个 503 是刻意设置 `max_requests=2 / queue=0` 后的预期拒绝。追加跨偏移重叠 Range 的 40 个请求单列，不计入此分母。最终功能记录 **19 PASS、1 个已确认的可用性限制**。允许有条件进入可回滚的小流量试用；没有进行生产部署，也不把这次有限时长测试称为长期生产验收。

## 版本、环境与测量边界

- 性能/首次完整功能工具提交：`d2c62566e69ffdb60d24faef12326c626098e94e`，启动时工作树干净。后续工具提交 `0f48add85601158358b0126e37e0e29cc483dbc5` 只收紧健康故障断言并增加并发重叠 Range；重新完整执行功能矩阵，性能代码和被测主程序没有变化。
- 主程序 SHA256：`b35db3c2d8de185e067237de4331ab49ffe07360a48e50804df07bdb3ac5f6a9`。Go **1.26.10 linux/amd64**；Python **3.12.3** 标准库客户端/源站。不是 demo server，也不是 `httptest` 替代主程序。
- ATS **9.2.3**，包 `trafficserver=9.2.3+ds-1+deb12u1ubuntu0.1`，二进制 SHA256：`4f12be0c0ed68e44552e0dbc99e4decf15bd2281bf315c0cfd3da33b023fd853`。这是固定发行版补丁版本，未声称最新 ATS。
- Linux **3.10.0-957.27.2.el7.x86_64**，Intel Skylake 虚拟 CPU **8 个逻辑核**，内存约 **31.3 GiB**。测试盘 `/workspace/data` 位于 `/dev/sdb[/code]` 的 ext4 `/workspace` 挂载，约 **98.3 GiB**，正式运行开始可用约 **10.4 GiB**。没有新建裸盘、格式化或填满共享文件系统。
- 每产品独立 **1 GiB 文件缓存、1 条带、10 MiB 应用 RAM cache**；Bakemono 对象限额 **256 MiB**、写缓冲预算 **128 MiB**、请求 **256+64 等待**，`GOMAXPROCS=8`。ATS 网络线程 **8**、task 线程 **2**、每盘 I/O 线程 **8**。两者均启用请求落盘日志，格式与实现不同。完整配置保留在原始目录。
- HTTP/1.1、loopback 明文，显式 `public,max-age=3600`、identity 编码、相同文件/客户端并发。ATS 与 Bakemono 顺序测量，没有同时跑 Go 构建/race。TLS/H2/私有 CA 由实际主程序的既有集成测试另行验证，不计入本吞吐表。
- **应用 RAM 容量相同不表示 I/O 路径相同。** 同一 ATS binary/config 的短重启中，实际 cache fd flags 为八进制 `0150002`，确认 `O_DIRECT` 和 `O_DSYNC`；Bakemono 使用普通文件及自身提交同步。没有清除 OS page cache，冷指应用缓存 MISS；这些结果不能当作相同物理盘访问模式的纯存储性能比较。
- 测试源站与客户端每次读写仅 **64 KiB** 流式缓冲；源文件约 200 MiB，客户端每份正文都校验长度与 SHA256。大文件 v1 摘要 `caba8fcf96c4839e35b88b6207a53d1731d48f9d571940add6bf8f2e32c87e3a`；小文件 v1 摘要 `aa65f2c70f23be4dc7a42679993d2bd1a7b1bde694ba18575aa32ff6fe593883`。没有按客户端分配 200 MiB 全量正文。
- `/proc` **100 ms** 采样 RSS/FD，进程 user+system CPU 差值除以成功正文 GiB；**500 ms** SIGUSR1 记录真实 `runtime.NumGoroutine` 和预算/任务计数，批次开始另取样。峰值是采样峰，不是瞬时上界；ATS 不使用 Go，goroutine 为不适用，未拿 Linux Threads 冒充。
- 热缓存各档 **3 次重复**，目标窗口 **4 秒**，已开始的请求完整读完，所以 200 MiB/32 并发批次实际可超过 20 秒。分位数用最近秩，表中 p99 为三批中最高值，不是混合分位数。大对象三档热请求数 Bakemono为20/48/96，ATS为18/48/96，不足以估计生产长尾。

**客户端、源站和被测服务共享主机；Python 调度/GIL、SHA256、文件读取与 loopback 都可能先成为瓶颈。** 产品 CPU/GiB 单独统计，但整体吞吐是这一验证负载的交付能力，不是服务端独立极限或无条件产品排名。

## 功能与故障结果

| 范围 | 实际结果与硬断言 |
| --- | --- |
| 完整下载/缓存语义 | 256 KiB、200 MiB MISS→HIT 正文长度与 SHA256 相同，第二次回源 0；请求 no-cache 触发源站 304 重验证，客户端条件请求 304；private/Authorization 两次均旁路。主程序日志出现 MISS/HIT/REVALIDATE/BYPASS/PURGE。 |
| 合并回源 | 100 个小文件请求只回源 1 次/256 KiB；16 个 200 MiB 请求只回源 1 次/200 MiB，正文逐份校验。压力矩阵 32 个冷大文件请求仍只回源 1 次。 |
| 原生 Range | 200 MiB 全区间跨 200 个 1 MiB 块，正文正确；闭/开/suffix/416/If-Range 不匹配均验证。两个重叠请求返回 2.5 MiB，仅获取 2 MiB。 |
| 真正并发重叠 | 4 种 16 MiB 区间，起点 0/8/16/24 MiB，8 个并发返回 128 MiB，仅 **1 HEAD + 40 GET / 40 MiB union**；随后 32 并发返回 512 MiB，新增回源 **0**。40 个响应各自精确校验；冷批完成 p99 **490 ms**，热批 **2363 ms**。 |
| 强版本 | 第二块前源站版本变化，旧请求受控失败，不发混合 206；下一请求完整拿到新版本。 |
| 取消与 PURGE | creator 取消不影响 follower 的完整 200 MiB。PURGE 后新请求在旧源站释放前拿到新版本；旧 creator 可按已接受语义 **仅 BYPASS** 收到原正文，旧 follower **503**；随后 HIT 仍为新版本，源站总 GET=2。PURGE 不承诺撤销原请求私有透传，禁止的是旧共享发布和后续旧缓存复用。 |
| 重启/换源 | 正常重启完整对象及已验证 Range 块回源 0；200 MiB 填充中 SIGKILL 后重启重新获取完整正文，未提交片段不作为 HIT；SIGHUP 时旧 200 MiB 响应按旧源完成，新请求取新源版本。 |
| 截断/磁盘压力 | 源站截断不产生完整假成功，下一请求重新取完整正文。32 MiB 独立缓存写入 160×256 KiB 触发环写，旧对象正确 MISS/refetch；超过容量的 200 MiB Range 在任何块 GET 前 503。 |
| 限额饱和/恢复 | max_requests=2、queue=0，32 个请求得到 **2×200、30×503**，拒绝后正常请求恢复；采样 InFlight≤2、writer=1、保留缓冲 6,324,480 字节。 |
| **已知限制** | 健康检查确认所有源站摘除后，仍 fresh 的完整对象 HIT 成功；已完整缓存的 Range 返回 **502**，因为它先选择健康固定源站再读 manifest。重新健康后 Range 恢复。它不是过期对象的 stale-if-error，也没有被记成通过的 Range HIT。仅完整502允许记此限制，超时/其他错误/错误206正文都会使工具失败。 |

存储短写、写失败、同步失败、CRC 损坏、覆盖代次和关闭竞争还由公开 Cache/HTTP 接口上的 `TestStorageFailuresCannotCommitObjects`、恢复测试和并发测试覆盖。它们是可控 I/O 故障注入，**不是**对共享文件系统真实 ENOSPC、裸设备掉电或物理断电的证明。

## 同环境热缓存基线

所有热缓存批次实际回源请求/字节 **0**，有效正文/状态无错误。吞吐为三次中位数（括号内 min–max）；CPU 为三次中位数。CPU、TTFB、完成延迟的成对数字依次为 **Bakemono / ATS**。

| 对象 / 并发 | Bakemono MiB/s | ATS MiB/s | CPU 秒/GiB | TTFB p99 ms | 完成 p99 ms |
| --- | ---: | ---: | ---: | ---: | ---: |
| 256 KiB / 1 | 86.47 (83.34–89.50) | 154.39 (143.81–167.62) | 4.92 / 1.44 | 1.53 / 1.07 | 4.45 / 3.85 |
| 256 KiB / 8 | 131.44 (128.11–131.52) | 132.93 (131.67–133.16) | 3.94 / 1.63 | 7.05 / 6.38 | 24.90 / 22.33 |
| 256 KiB / 32 | 129.90 (123.31–132.76) | 125.17 (124.83–126.57) | 3.45 / 1.75 | 18.96 / 17.15 | 53.91 / 51.51 |
| 256 KiB / 100 | 122.42 (122.37–122.89) | 124.72 (123.32–127.39) | 4.41 / 1.81 | 98.80 / 75.98 | 153.98 / 160.05 |
| 200 MiB / 1 | 302.87 (293.20–304.41) | 292.02 (289.72–294.30) | 2.26 / 0.96 | 3.57 / 3.92 | 746.86 / 724.39 |
| 200 MiB / 8 | 482.83 (482.31–491.73) | 526.07 (502.90–551.11) | 1.54 / 0.52 | 32.39 / 26.33 | 3512.73 / 3347.72 |
| 200 MiB / 32 | 286.67 (276.80–298.74) | 302.65 (296.90–347.21) | 1.61 / 0.52 | 186.63 / 53.70 | 23105.72 / 21538.56 |

同机客户端下，小对象并发增加到 8 后吞吐已基本平坦；200 MiB 在并发 8 时吞吐较好，并发 32 时完成长尾明显增加。**不能由此断言服务端最大 QPS 已被压到。** CPU 差距可确认存在：200 MiB/并发 8，Bakemono **1.536 CPU秒/GiB**，ATS **0.522**；后续优化应针对真实热点剖析，不只看吞吐接近。

## 冷缓存、慢客户端与资源

冷并发测试在源站头部前增加 200 ms 延迟。小文件两产品各并发档均只 GET 1 次。200 MiB 单冷请求：Bakemono TTFB **1039 ms**、完成 **1686 ms**；ATS **205 ms / 1382 ms**。Bakemono 必须整对象提交后才发首字节，ATS 可 RWW；这是已接受的数据发布策略差异。

| 200 MiB 场景 | Bakemono 源站 GET / MiB | ATS 源站 GET / MiB | Bakemono / ATS 完成 p99 ms |
| --- | ---: | ---: | ---: |
| 冷热门，并发 8 | 1 / 200 | 8 / 1600 | 5010 / 7529 |
| 冷热门，并发 32 | 1 / 200 | 32 / 6400 | 24736 / 26589 |
| 慢源，每64KiB暂停1ms，并发8 | 1 / 200 | 8 / 1600 | 7345 / 9089 |
| 热对象慢client，每64KiB暂停1ms，并发8 | 0 / 0 | 0 / 0 | 5998 / 6176 |

ATS 显式启用了 `enable_read_while_writer=1`、`open_write_fail_action=5`、open-read 与 RWW 重试 50 次/起始10 ms、`ignore_client_no_cache=0`，但上述大文件场景实测仍泄漏多个回源。未启用 `collapsed_forwarding` 插件，不能据此称 ATS 不支持预回源合并或把这些冷并发数字排成性能优劣。[ATS 官方插件说明](https://docs.trafficserver.apache.org/en/9.2.x/admin-guide/plugins/collapsed_forwarding.en.html) 区分了 RWW 与头部到达前的合并；参数含义见 [9.2 records.config](https://docs.trafficserver.apache.org/en/9.2.x/admin-guide/files/records.config.en.html)。原生 Range 功能不同，没有拿它和 ATS 普通完整缓存做速度排名。

Bakemono 正式矩阵的采样峰为 **RSS 152.46 MiB、FD 115、goroutine 210**；200 MiB/32 热请求峰 RSS **119.51 MiB**。跨偏移 Range 补测峰 RSS **129.00 MiB**，最多4个块生产者保留 **25,297,920 字节**。短时序列没有看到资源持续失控；正式负载结束的快照为 InFlight/Queued/CacheWriters/SharedFillTasks/SharedFillWaiters **全0、goroutine9**，进程正常排空退出。不能把有限采样和数分钟结果外推为长期无泄漏。

## 试用边界、复测阈值与回滚

- 初始小流量可选择不超过 **1%** 的可撤回流量，256 KiB 类请求并发不超过 **32**、约200 MiB完整下载不超过 **8**，这是本次已实际验证的保守起点，不是产品硬上限。保持对象限额256MiB和足够缓存空间，源站必须能承受冷启动回源。
- 若业务要求“全部源站被健康检查摘除后，已缓存Range也必须可用”，**本版本不满足此上线条件**；不要把这项限制隐去。试用前明确健康检查/Range的可用性要求，不能用本次全对象HIT代替该保证。
- 正确性为硬门槛：任意错误长度/hash、混版、PURGE后旧缓存复活或非预期5xx立即停止试流。正常基线非预期错误率为0；刻意限额触发的503单列。
- 同机器、相同脚本/配置再跑3次：本次 Bakemono 每组吞吐极差/中位数最高 **7.65%**，把中位吞吐下降 **20%** 作为调查线。CPU按 `max(30%, 2×该组历史极差/中位数)` 上升调查（当前最嘈杂小文件并发32约55%）；延迟若超过本组最高实测p99的2倍也调查。仅3次重复，以上是回归筛查，不是经统计证明的生产SLO；并发、数据大小或硬件变化必须重建基线。
- 回滚顺序：撤下试流→SIGTERM等待排空（本测试宽限5秒）→恢复上一已验证二进制及配套配置→使用独立缓存目录启动→校验代表URL正文、HIT和源站计数后恢复流量。保留原缓存文件；不要让两个进程共用同一缓存。紧急强杀后的冷启动回源增加已经在本轮边界内验证。

## 复现与证据

本地已实际通过 `go test ./... -count=1 -timeout=180s`、`GOFLAGS=-race go test -race ./... -count=1 -timeout=180s` 和 `go vet ./...`；实际binary也成功构建。

可复用工具：[tools/acceptance.py](../tools/acceptance.py)。下列输出目录必须尚不存在；所有 fixture、cache、结果均在用户指定测试盘，工具不会清理既有目录。

```bash
export TMPDIR=/workspace/data/bakemono-10-14/tmp
export GOCACHE=/workspace/bakemono-validation-10-14/go-build
go build -o /workspace/data/bakemono-10-14/tools/bakemono-repeat .
python3 tools/acceptance.py \
  --binary /workspace/data/bakemono-10-14/tools/bakemono-repeat \
  --output /workspace/data/bakemono-10-14/results/repeat-new-directory \
  --ats-environment /workspace/data/bakemono-10-14/tools/ats-environment.json \
  --mode all --seconds 4 --repeats 3
# 仅功能与真实重叠Range压力，不需要ATS：--mode functional
```

ATS环境JSON引用固定包解压目录及隔离runroot模板，未安装系统服务。离开此机器复现时须准备同版本包/模板并修正JSON路径。工具、脚本解释器、二进制和配置版本都需记录，不要只复制性能数字。

原始根目录 `/workspace/data/bakemono-10-14/results/`：

- `issue14-acceptance-d2c6256/`：正式64批测量；`environment.json`、`batches.json`（含每100ms资源样本）、`requests.jsonl`（逐请求SHA/长度/延迟）、`origin-*.json`、配置和全部进程日志，`outcome.json` 为 success=true。
- `issue14-functional-overlap/`：0f48add 的完整功能复验，19 PASS/1 LIMITATION，另含跨偏移8/32并发原始结果；仍使用同一d2二进制SHA。两个目录不能混成同一请求数分母。
- `issue-14-ats-fd-flags.json`：已测binary/config短重启的实际fd flags；通过同UID只读检查，未修改ptrace安全设置。
- `issue-14-normal.log`、`issue-14-race.log`、`issue-14-vet.log`：全套 normal、全套race（`GOFLAGS=-race`使子binary也带race）、vet。
- `issue-14-stats-red.log` / `issue-14-stats-green.log`：主程序本地SIGUSR1资源快照的RED→GREEN证据。SIGUSR1现在输出既有计数及真实goroutine，不新增公开管理HTTP接口。
- `root-metrics-check.json`：协调代理独立聚合交叉核对。最终集成分支的精确CI HEAD/run链接由 [#14验收记录](https://github.com/Monsterwi/bakemono/issues/14) 记录；本报告不冒称尚未发生的最终CI成功。

未执行：长时间生产soak、真实公网拥塞/丢包、共享文件系统真实ENOSPC、物理断电/硬盘掉电，以及HTTP/3。没有自动生产部署；#1未关闭。
