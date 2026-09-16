# aiserveweave-agent

节点侧 Agent 主动出站连接 Gateway，通过 mTLS 隧道执行运行时语义请求，不监听公网推理入口。协议、连接状态机、配置下发与验收记录见 [隧道 README](tunnel/README.md)，启动和部署见 [部署说明](../../deploy/README.md)。

## 当前实现

- `main.go` 装配运行时管理器、后端工厂、自动发现、隧道和可选指标监听；`-version` 输出构建版本。
- `localdiscovery/` 默认探测本机 Ollama/vLLM，可用 `-auto-discover=false` 关闭；`-ollama-url` 可显式注册 Ollama。
- `tunnel/` 已实现身份注册/续期、多副本连接、槽池、心跳、运行时配置应用和流式请求/产物转发。执行 runtime 必须命中本地白名单。
- 后端适配在共享的 `common/runtime`，支持 Ollama、vLLM、SGLang 和 ComfyUI；ComfyUI 适配位于 `common/runtime/workflow/comfyui`。ComfyUI 适配器新增 `comfyui_queue_running`/`comfyui_queue_pending`（`runtime_id` 标签）两个队列深度量表（STATUS.md 的 A06），复用 `Status`/`Cancel` 本就会发起的 `GET /queue`，不产生额外请求；与隧道共用同一个 `metrics.Registry`，未配置 `-metrics-addr` 时安全丢弃。ComfyUI 的失败历史同时被分类为是否显存/内存耗尽（`runtime.WorkflowStatus.OutOfMemory`），供 Gateway 侧统计使用，见 [Gateway README](../aiServeWeaveGateway/README.md) 的相应小节。
- `workflow/` 仍是占位包。受控模板目录与输入绑定已在 Gateway 的 `workflow/` 实现，不能据此目录重复建设。
- `hostresources/`（STATUS.md P2 待办第五条的第一项后续任务）在 Hello 握手前探测本节点的 CPU 核数、内存总量、GPU 数量与显存总量，写入 `ClientConfig.Resources`。探测一律经 `os/exec` 调用系统自带工具（Linux 读 `/proc/meminfo`、`nvidia-smi`；macOS 用 `sysctl` 取内存，GPU 显存无通用查询手段留空），不引入 `gopsutil`/NVML 一类新依赖，守住 AGENTS.md 的 Agent 依赖红线。这是**尽力而为的静态容量声明**：探测失败的字段留零值、不阻塞启动；Gateway 侧调度器尚不消费这份数据（见 [P2 设计文档](../../docs/superpowers/specs/2026-09-15-p2-resource-aware-scheduling-design.md) 第七、八节的后续任务拆分）。

## 尚未交付的范围

运行时配置文件加载、Managed 后端安装/升级、基于资源容量的调度准入过滤、GPU/内存实时利用率采集、模型分发和 Agent 自动升级仍属规划。本机自动发现不等于进程生命周期管理；已有隧道文件转发不等于对象存储或原节点离线后的文件可用性。跨服务任务与验收统一见 [根 STATUS](../../STATUS.md)。
