# aiserveweave-agent

节点侧 Agent 主动出站连接 Gateway，通过 mTLS 隧道执行运行时语义请求，不监听公网推理入口。协议、连接状态机、配置下发与验收记录见 [隧道 README](tunnel/README.md)，启动和部署见 [部署说明](../../deploy/README.md)。

## 当前实现

- `main.go` 装配运行时管理器、后端工厂、自动发现、隧道和可选指标监听；`-version` 输出构建版本。
- `localdiscovery/` 默认探测本机 Ollama/vLLM，可用 `-auto-discover=false` 关闭；`-ollama-url` 可显式注册 Ollama。
- `tunnel/` 已实现身份注册/续期、多副本连接、槽池、心跳、运行时配置应用和流式请求/产物转发。执行 runtime 必须命中本地白名单。
- 后端适配在共享的 `common/runtime`，支持 Ollama、vLLM、SGLang 和 ComfyUI；ComfyUI 适配位于 `common/runtime/workflow/comfyui`。ComfyUI 适配器新增 `comfyui_queue_running`/`comfyui_queue_pending`（`runtime_id` 标签）两个队列深度量表（STATUS.md 的 A06），复用 `Status`/`Cancel` 本就会发起的 `GET /queue`，不产生额外请求；与隧道共用同一个 `metrics.Registry`，未配置 `-metrics-addr` 时安全丢弃。ComfyUI 的失败历史同时被分类为是否显存/内存耗尽（`runtime.WorkflowStatus.OutOfMemory`），供 Gateway 侧统计使用，见 [Gateway README](../aiServeWeaveGateway/README.md) 的相应小节。
- `workflow/` 仍是占位包。受控模板目录与输入绑定已在 Gateway 的 `workflow/` 实现，不能据此目录重复建设。
- `hostresources/`（STATUS.md P2 待办第五条的第一项后续任务）在 Hello 握手前探测本节点的 CPU 核数、内存总量、GPU 数量与显存总量，写入 `ClientConfig.Resources`。探测一律经 `os/exec` 调用系统自带工具（Linux 读 `/proc/meminfo`、`nvidia-smi`；macOS 用 `sysctl` 取内存，GPU 显存无通用查询手段留空），不引入 `gopsutil`/NVML 一类新依赖，守住 AGENTS.md 的 Agent 依赖红线。这是**尽力而为的静态容量声明**：探测失败的字段留零值、不阻塞启动；Gateway 侧调度器尚不消费这份数据（见 [P2 设计文档](../../docs/superpowers/specs/2026-09-15-p2-resource-aware-scheduling-design.md) 第七、八节的后续任务拆分）。
- `modelpull/`（STATUS.md P2「模型分发」待办，子任务一见 [P2 模型分发设计文档](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md)，子任务二见 [子任务二设计文档](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md)）：一个纯 Agent 本地、标准库实现（`net/http` + `crypto/sha256`，不新增依赖）的清单驱动下载器，覆盖校验和、断点续传（HTTP Range 到 `<TargetPath>.part`，完成后原子改名）、来源白名单四项要求。`-model-pull-manifest` 指向一份本地 JSON 清单（`[]modelpull.Spec`），`-model-pull-allowlist`/`-model-pull-quota-bytes` 控制白名单与预算；三者均为纯本地 flag，从不接受 Gateway 或控制面下发。清单为空（默认）时功能整体关闭。来源白名单默认方向是「空=拒绝全部」，刻意不照搬 `AllowedRuntimes` 的「空=放行」，理由见子任务一设计文档第三节。**子任务一**：`RunManifest` 是同步、一次性处理整份清单的下载器，字节配额是单次调用共享的预算。**子任务二（`puller.go`）**：`Puller` 让 Gateway 可以经隧道 Control 流按名字触发一次按需拉取——只能按名字，从不是 URL，Agent 对着自己本地清单解析；启动时仍自动对整份清单触发一次（保留子任务一的既有行为），运行期间在后台以最多一个 worker 顺序处理排队的名字，逐名字的进度（`BytesDownloaded`/`BytesTotal`/状态）经 `ModelPullReport` 回报给 Gateway，失败原因是封闭枚举（`common/modelpullstatus.FailureReason`），从不携带原始错误文本——`SourceURL` 可能是带签名 token 的预签名 URL，这条边界防止它经由 Go 网络错误文本泄漏出 Agent。字节配额语义从"单次 `RunManifest` 调用"扩展为"单次 worker session"：一批一起触发的名字共享一份预算，session 处理完（队列清空）后下一次触发拿到全新预算。整个 Puller 的工作跨越隧道连接的生命周期，Agent 关闭时靠取消 ctx 中止在途下载，留下的 `.part` 文件与进程被杀死时完全同一种可续传状态。**范围边界**：不理解 Ollama 自己的 manifest/blob 存储格式（不能靠它给 Ollama 拉模型），没有 Gateway/Console 可见性（子任务二只到 Gateway 一侧的触发/查询 API，未接入控制面/Console，见 Gateway README「模型拉取触发」一节），不做并发下载、跨重启的累计配额账本，均记入设计文档已知缺口、留给后续子任务。

## 尚未交付的范围

运行时配置文件加载、Managed 后端安装/升级、基于资源容量的调度准入过滤、GPU/内存实时利用率采集和 Agent 自动升级仍属规划。**模型分发**已交付本地清单驱动的子任务一与 Gateway 按名字触发/状态回传的子任务二（见上），Ollama 原生拉取、并发下载/跨重启配额账本/磁盘探测、Console 可见性等后续子任务仍属规划，见 [P2 模型分发设计文档](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md) 第二节与 [子任务二设计文档](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md) 第六节的拆分列表。本机自动发现不等于进程生命周期管理；已有隧道文件转发不等于对象存储或原节点离线后的文件可用性。跨服务任务与验收统一见 [根 STATUS](../../STATUS.md)。
