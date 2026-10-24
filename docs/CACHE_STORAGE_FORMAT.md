# 磁盘格式与恢复

允许崩溃后安全 MISS；不承诺数据库级持久性或 ATS 磁盘格式兼容。启动日志记录 clean/cold、检查点版本和恢复耗时。

## 发布状态

下图是逻辑状态，不是代码中的枚举。完整对象、manifest、每个逻辑块共用提交边界。

![UML 缓存发布状态图](diagrams/publication.svg)

## 格式

| 层 | 版本与约束 |
| --- | --- |
| Stripe marker | v2；原 24 B 保留区保存 magic、版本、随机 16 B session；数据区偏移不变 |
| Doc | v3；72 B 头；IEEE CRC32 覆盖 checksum 置零的序列化头、元数据、正文 |
| 片段身份 | 完整逻辑键/首键摘要、writer、片段位置和长度独立校验；非头 TotalLen 为逻辑结束偏移，头为总长 |
| Checkpoint | v1；`<data path>.<Skip>.checkpoint`；带校验的 geometry、写位置、phase、session、目录、完整键/selector |
| 编码 | 目录字段小端；envelope/字符串长度大端；每条映射编码额外至多 8 B |
| 限额 | 每键/selector 16 KiB；至多 min(65,536, 目录容量) 条；键/selector 字符串合计 16 MiB |
| 不兼容/损坏 | missing/dirty/torn/geometry/session 不符均冷 MISS；不扫描旧数据重建、不在线迁移 |

## 打开与关闭

| 顺序 | 操作 |
| --- | --- |
| Open 1 | 独占底层 inode；同进程可用不重叠 split stripe，不同进程连不重叠区域也不能共享 |
| Open 2 | 仅载入 session/geometry 匹配的 clean 检查点；先校验长度/索引/链表环，再验证对象全部片段与元数据；仅恢复完整对象，坏项 MISS |
| Open 3 | 暴露访问前写入并 sync 新随机 session；失败则 Open 失败，硬链接/别名也无法复用旧检查点 |
| Running Save | sync 数据并发布 dirty 检查点；当前活动会话不能据此恢复 HIT |
| Close | 阻止新工作/PURGE，终止活跃写者，停止 evacuation，排空磁盘写；发布锁下快照目录/键映射 |
| Close 提交 | sync 数据 → 写/sync 临时 clean 检查点 → 原子 rename → sync 父目录 |

只读访问也可能触发 evacuation，因此也更新 session。split stripe 的文件锁在最后一个 stripe 关闭后才释放。Open 不删除/截断原文件；后续正常环写仍会复用空间。

## 读者与覆盖

读盘/迁移按有界 fragment 长度分配，不按整对象 TotalLen 分配。读者可继续使用已验证字节；后续片段覆盖、替换、损坏或缺失须报错。目录发布复核所有权、allocation generation 和 PURGE 代次。

重开时 Stripe 与聚合写使用同一进程内 allocation generation，保留可表示的前一代，允许旧 phase 对象继续正确失效、刷新和迁移；不改变检查点格式。迁移机制见 [evacuation](EVACUATION_OPTIMIZATION.md)。

验证范围：[提交/回卷](../cache/storage_commit_test.go)、[恢复/损坏/子进程强杀](../cache/recovery_test.go)、[PURGE 后强杀](../cache/invalidation_restart_test.go)。覆盖进程崩溃与文件系统错误，未验证物理断电或恶意伪造 CRC。
