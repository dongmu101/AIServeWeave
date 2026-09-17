# aiserveweave-agent

节点侧 Agent 主动出站连接 Gateway，通过 mTLS 隧道执行运行时语义请求，不监听公网推理入口。协议、连接状态机、配置下发与验收记录见 [隧道 README](tunnel/README.md)，启动和部署见 [部署说明](../../deploy/README.md)。

## 当前实现

- `main.go` 装配运行时管理器、后端工厂、自动发现、隧道和可选指标监听；`-version` 输出构建版本。
- `localdiscovery/` 默认探测本机 Ollama/vLLM，可用 `-auto-discover=false` 关闭；`-ollama-url` 可显式注册 Ollama。
- `tunnel/` 已实现身份注册/续期、多副本连接、槽池、心跳、运行时配置应用和流式请求/产物转发。执行 runtime 必须命中本地白名单。
- 后端适配在共享的 `common/runtime`，支持 Ollama、vLLM、SGLang 和 ComfyUI；ComfyUI 适配位于 `common/runtime/workflow/comfyui`。ComfyUI 适配器新增 `comfyui_queue_running`/`comfyui_queue_pending`（`runtime_id` 标签）两个队列深度量表（STATUS.md 的 A06），复用 `Status`/`Cancel` 本就会发起的 `GET /queue`，不产生额外请求；与隧道共用同一个 `metrics.Registry`，未配置 `-metrics-addr` 时安全丢弃。ComfyUI 的失败历史同时被分类为是否显存/内存耗尽（`runtime.WorkflowStatus.OutOfMemory`），供 Gateway 侧统计使用，见 [Gateway README](../aiServeWeaveGateway/README.md) 的相应小节。
- `workflow/` 仍是占位包。受控模板目录与输入绑定已在 Gateway 的 `workflow/` 实现，不能据此目录重复建设。
- `hostresources/`（STATUS.md P2 待办第五条的第一项后续任务）在 Hello 握手前探测本节点的 CPU 核数、内存总量、GPU 数量与显存总量，写入 `ClientConfig.Resources`。探测一律经 `os/exec` 调用系统自带工具（Linux 读 `/proc/meminfo`、`nvidia-smi`；macOS 用 `sysctl` 取内存，GPU 显存无通用查询手段留空），不引入 `gopsutil`/NVML 一类新依赖，守住 AGENTS.md 的 Agent 依赖红线。这是**尽力而为的静态容量声明**：探测失败的字段留零值、不阻塞启动；Gateway 侧调度器尚不消费这份数据（见 [P2 设计文档](../../docs/superpowers/specs/2026-09-15-p2-resource-aware-scheduling-design.md) 第七、八节的后续任务拆分）。
- `modelpull/`（STATUS.md P2「模型分发」待办的子任务一，见 [P2 模型分发设计文档](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md)）：一个纯 Agent 本地、标准库实现（`net/http` + `crypto/sha256`，不新增依赖）的清单驱动下载器，覆盖校验和、断点续传（HTTP Range 到 `<TargetPath>.part`，完成后原子改名）、单次运行的字节配额和来源白名单四项要求。`-model-pull-manifest` 指向一份本地 JSON 清单（`[]modelpull.Spec`），`-model-pull-allowlist`/`-model-pull-quota-bytes` 控制白名单与预算；三者均为纯本地 flag，从不接受 Gateway 或控制面下发。清单为空（默认）时功能整体关闭。启动时在后台 goroutine 里运行，**不阻塞** Hello 握手/隧道连接——下载耗时无上限，不应该让 Agent 看起来"卡死"；完成或失败只记日志，best-effort，与 `hostresources` 同一克制。来源白名单默认方向是「空=拒绝全部」，刻意不照搬 `AllowedRuntimes` 的「空=放行」，理由见设计文档第三节。**范围边界**：这是一个通用的「按 URL+校验和下载单个文件」工具，不理解 Ollama 自己的 manifest/blob 存储格式（不能靠它给 Ollama 拉模型），不接受 Gateway/控制面触发，没有 Gateway/Console 可见性，不做跨重启的累计配额账本，均记入设计文档已知缺口、留给后续子任务。

## 尚未交付的范围

运行时配置文件加载、Managed 后端安装/升级、基于资源容量的调度准入过滤、GPU/内存实时利用率采集和 Agent 自动升级仍属规划。**模型分发**已交付本地清单驱动的子任务一（见上），Gateway/控制面触发的拉取指令、Ollama 原生拉取、Console 可见性等后续子任务仍属规划，见 [P2 模型分发设计文档](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md) 第二节的拆分列表。本机自动发现不等于进程生命周期管理；已有隧道文件转发不等于对象存储或原节点离线后的文件可用性。跨服务任务与验收统一见 [根 STATUS](../../STATUS.md)。
