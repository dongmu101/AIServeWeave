# AIServeWeave

本词汇表记录 AIServeWeave 中已确认的领域术语，供需求讨论、任务与实现使用。

## Language

**Agent 独立部署**：
推理节点仅部署 Agent，接入其他机器上已有的 Registry 与 Gateway，并向平台提供该节点上的推理后端能力。
_Avoid_: 脱离平台的 Agent、独立推理 API 服务

**Windows 原生 Agent**：
直接运行在 Windows 环境中、向平台提供该 Windows 节点推理后端能力的 Agent，其运行身份与本机 CLI 账户均属于 Windows 环境。
_Avoid_: WSL Agent、Windows 宿主机上的 Linux Agent

**账号预售**：
只按账号类型（Claude Code 或 Codex）、月价与名额出售一个月的独享使用权，付款时尚未绑定具体账号；订单付款后进入待发货，由运维从同类型的空闲包月账号库存中分配，一个月自分配时起算。
_Avoid_: 预付点数、预订账号库存、自动发货
