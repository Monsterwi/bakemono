# Razor 专用测试环境

本机 `/workspace/bakemono` 用于开发、代码审查和常规回归；磁盘性能、ATS 对照与端到端压力测试使用下述远端机器。环境核验日期：**2026-10-21 UTC**。本页是测试机连接方式、固定目录及使用边界的统一入口。

## 连接与硬件

```bash
ssh -p 10022 root@10.10.135.73
cd /data/bakemono-lab
```

| 项目 | 已核验信息 |
| --- | --- |
| 主机 | `beijing4-ct-10-10-135-73.idc.qiyi.domain` |
| SSH | `10.10.135.73:10022`，用户 `root`；当前开发机已验证非交互登录 |
| ED25519 主机指纹 | `SHA256:McrxEdWTaU3BYG58ilKwmhQfPqD0/L9eDjeWDat2I90`，首次连接接受后从远端读取，未通过独立通道认证 |
| 系统 | CentOS Linux 7，x86_64 |
| 内核 | `3.10.0-957.el7.x86_64` |
| CPU | 2 × Intel Xeon E5-2620 v4 @ 2.10 GHz，共 16 物理核、32 逻辑核，2 个 NUMA 节点 |
| 内存 | `free -h` 显示约 62 GiB，Swap 4 GiB |
| 文件系统 | 以下工作区和测试缓存目录均为 XFS |

不在文档中存放密码、私钥或访问令牌。主机指纹改变时先核实原因，不关闭 SSH 主机校验。

## 固定目录与介质

| 用途 | 路径 | 设备及首次核验可用空间 |
| --- | --- | --- |
| 源码、构建、工具、配置、结果 | `/data/bakemono-lab` | `/dev/sdl6`，约 362 GiB |
| SATA 机械盘缓存 | `/data3/bakemono-lab/cache` | `/dev/sdc`，`ST4000NM0295`，`ROTA=1`，约 3.64 TiB |
| SSD 缓存 | `/ssd1/bakemono-lab/cache` | `/dev/sdk`，`INTEL SSDSC2BB80`，`ROTA=0`，约 92 GiB |

空间会变化，每次测试开始前重新执行 `df -hT`。`/ssd2` 是 PERC H730 Mini 暴露的逻辑设备，当前 `ROTA=1`；未确认底层介质，不将它用于 SSD 基线。

以下目录已创建：

```text
/data/bakemono-lab/
  ENVIRONMENT.md          本文的远端副本
  src/                    按仓库及提交保存源码
  build/                  按项目、版本分开构建
  opt/                    ATS、工具链等独立安装前缀
  configs/                每次测试的实际配置
  fixtures/               小文件和约 200 MiB 源文件
  results/                每次测试独立子目录
  manifests/              主机、版本、依赖等环境记录
  tmp/                    临时文件和构建缓存
/data3/bakemono-lab/cache/{razor,ats}/
/ssd1/bakemono-lab/cache/{razor,ats}/
```

运行缓存目录继续按测试批次隔离，例如 `cache/ats/<run-id>`。项目更名后固定工作根目录仍保留 `/data/bakemono-lab`，旧 `cache/bakemono` 目录保留原样，新测试使用 `cache/razor`。两个进程不得共用同一个缓存目录。清理只针对明确归属于本项目的测试批次，保留原有磁盘数据。

登录后将临时文件放入工作盘，避免构建写满根分区；首次核验根分区仅余约 1.9 GiB：

```bash
export TMPDIR=/data/bakemono-lab/tmp
export GOCACHE=/data/bakemono-lab/tmp/go-build
```

## ATS 与工具链状态

截至核验时，[ATS 官方下载页](https://trafficserver.apache.org/downloads)列出的最新稳定版为 **10.2.0**；后续对照基线固定该版本，而不是沿用旧报告的 9.2.3。再次升级时记录新版本与日期，不使用无法追溯的 `latest` 作为结果标识。

计划安装前缀为 `/data/bakemono-lab/opt/ats-10.2.0`。下载、构建及安装产物都留在固定工作区，保存下载校验值、构建选项及 `traffic_server -V` 输出。**ATS 尚未安装，不能将本机 Razor 测量写成已完成的 ATS 10.2 对照。**

首次核验已有工具：

| 工具 | 版本/状态 |
| --- | --- |
| GCC / G++ | devtoolset-9，GCC 9.3.1 |
| CMake | 3.17.5 |
| Go | 1.24.7 |
| Python 3 | 3.4.8 |
| OpenSSL | 1.0.2k-fips |
| 其他 | 已有 git、ninja、iostat；未发现 `traffic_server`、fio |

这些版本不代表已满足 ATS 10.2.0 或当前验收工具的要求。安装前核对各版本构建要求，将新增工具链及依赖安装到工作区独立前缀，保存版本清单；不要直接替换系统 Python、OpenSSL 或启动系统 ATS 服务。

Razor 本轮测试另在 `/data/bakemono-lab/opt/python-3.12.13` 独立安装了 Python 3.12.13，系统 Python 保留原样。源码使用 Python 官方发布的 SHA256 校验，源码包及构建日志在 `src/`、`build/python-3.12.13/`。该运行时用于 HTTP 压测，不将缺少新版 OpenSSL 的构建当作 TLS 客户端环境。HDD/SSD 的完整 server 包回归均已通过，记录位于 `results/razor-v0.1/core-20261021T031946Z/`。

## I/O 能力与测量边界

- 已实际探测 `io_uring_setup`：返回 `ENOSYS`（errno 38），没有创建 ring。当前内核无法执行 io_uring 对照测试；安装 liburing 或更换容器用户态不能补齐该能力。
- Linux native AIO 尚未在此机验证；不能把它与 io_uring 混称为已支持的异步 I/O。
- 新测试以远端为被测服务端，本机负责开发和发起测试。源站及施压客户端的实际位置、CPU/网络占用均写入每份报告；避免把客户端先饱和误判为服务端上限。
- HDD 与 SSD 分别测试，保持对象、缓存容量、并发、HTTP/TLS、日志及校验策略一致。记录 buffered/direct I/O、同步策略及应用/OS 缓存状态；不同模式的数字不可当作纯磁盘性能比较。
- 正式测试前记录空闲 CPU、磁盘与网络活动。机器上已有数据和进程，不能假定没有背景负载；不全局清页缓存、不格式化磁盘、不停止既有服务。
- 每次报告保存源码提交、二进制 SHA256、ATS 版本、配置、源站计数、长度/hash 校验、CPU/RSS/FD 和磁盘指标。旧报告属于旧主机与旧版本，不直接拼接成新环境的提升百分比。

首次环境快照位于远端 `/data/bakemono-lab/manifests/host-2026-10-21.txt`。仓库中的本页为文档源；修改后同步到远端 `ENVIRONMENT.md`。
