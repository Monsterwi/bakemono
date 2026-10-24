# RWW 速览

Razor 的 HTTP RWW 是**完整块校验并提交后发送**；普通整对象 MISS 仍等完整提交。

| 查看内容 | 文档 |
| --- | --- |
| 架构与模块 | [UML 组件图](ARCHITECTURE.md) |
| 多客户端合并、身份、超时 | [合并时序](CACHE_FILL_DESIGN.md) |
| 逐块发送、背压、失败 | [RWW 实现](RWW_IMPLEMENTATION.md) |
| ATS 机制区别 | [对照表](RWW_ATS_COMPARISON.md) |
