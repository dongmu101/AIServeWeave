# AIServeWeave

本词汇表记录 AIServeWeave 中已确认的领域术语，供需求讨论、任务与实现使用。

## Language

**Agent 独立部署**：
推理节点仅部署 Agent，接入其他机器上已有的 Registry 与 Gateway，并向平台提供该节点上的推理后端能力。
_Avoid_: 脱离平台的 Agent、独立推理 API 服务

**Windows 原生 Agent**：
直接运行在 Windows 环境中、向平台提供该 Windows 节点推理后端能力的 Agent，其运行身份与本机 CLI 账户均属于 Windows 环境。
_Avoid_: WSL Agent、Windows 宿主机上的 Linux Agent
