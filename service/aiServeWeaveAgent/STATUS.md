# aiserveweave-agent

节点侧 Agent 主动出站连接 Gateway，通过 mTLS 隧道执行运行时语义请求，不监听公网推理入口。本文只记录交付范围、验收状态与未做事项；配置、设置页面与各后端接入步骤见 [README](README.md)，协议、连接状态机与配置下发见 [隧道 README](tunnel/README.md)，部署见 [Agent 部署指南](../../deploy/agent/README.md)。

状态用语：「已交付」指代码已合入且默认测试通过；「未验收」指没有在真实环境（真实 CLI 登录、真实 Key、真实硬件或目标系统）验证过。下文没有特别说明的能力都只到「已交付」。

## 当前实现

- `main.go` 装配运行时管理器、后端工厂、自动发现、隧道和可选指标监听；`-version` 输出构建版本。
- `agentconfig/`：`-config` YAML 覆盖 Gateway 连接与本地运行时声明，显式 flag 优先于文件；不传 `-config` 时读取工作目录下的 `config.yaml`，不存在则只靠 flag。YAML 经 viper 解码，属于记录在案的依赖例外，见 [依赖边界](../../docs/agents/dependencies.md#agent-本地-yaml-配置例外)。用法见 [README「配置文件」](README.md#配置文件)。
- `configui/`：本地设置页面默认随 Agent 启动（`http://127.0.0.1:8899`，只监听回环），表单编辑 YAML，`runtimes:` 保存后热重载，其余设置需重启；另展示实时连接状态、本地模型列表与 CLI 账户额度。`-config-ui` 是只运行页面的模式，首次启动无配置时自动进入。见 [README「本地设置页面」](README.md#本地设置页面)。
- 运行时热重载：`SIGHUP` 或设置页面保存时只调和 `-config` 声明的运行时；`-ollama-url`、自动发现与 Gateway 下发的实例不受影响。见 [README「运行时热重载」](README.md#运行时热重载)。
- 后端未就绪不阻止启动：上报 `unhealthy`，每 10～11 秒重试探测，成功后自动发布模型。见 [README](README.md#后端未就绪时的启动行为)。
- `localdiscovery/` 默认探测本机 Ollama（11434）、vLLM/TensorRT-LLM/TokenSpeed（均为 8000，按此顺序互斥探测，一个地址只注册一次）、SGLang（30000）；`-auto-discover=false` 关闭，`-ollama-url` 可显式注册 Ollama。
- `clidiscovery/`（2026-11-01）：随 `-auto-discover` 每 2 分钟检查一次，PATH 中有已登录（ChatGPT）的 `codex` 时以 `codex-local` 走与手写声明相同的 `manager.Add` 与探测，失败则撤回、下一轮重试。已有任何 `kind: codex` 运行时，或 `allowed_runtimes` 非空且不含 `codex-local` 时不注册；Gateway 仍不能新增 Codex ID。注册后与本地声明一样进入 CLI 额度监视，热重载不会把它移出。Claude Code 不自动发现。默认测试用桩，未在真实 Codex 上验收。
- `cliusage/`：读取本地 Codex（`account/rateLimits/read`）与 Claude Code（`get_usage`）的订阅窗口，在设置页面展示并经隧道上报；额度耗尽时以可重试的 `rate_limited` 加 `retry_after` 报告，Gateway 据此搁置该运行时。事件格式按 CLI 当前版本实现，未在真实账户耗尽场景下验收。见 [README「本地 CLI 账户额度」](README.md#本地-cli-账户额度) 与 [「额度耗尽与限流」](README.md#额度耗尽与限流)。
- `tunnel/` 已实现身份注册/续期、多副本连接、槽池、心跳、运行时配置应用和流式请求/产物转发。执行的 runtime 必须命中本地白名单。
- 后端适配：[registry.go](registry.go) 注册以下种类（常量见 `common/runtime/types.go`）。
  - 本地推理服务（共享的 `common/runtime`，Gateway 可下发同 ID 配置）：`ollama`、`vllm`、`sglang`、`trtllm`、`tokenspeed`、`comfyui`（适配位于 `common/runtime/workflow/comfyui`）。TensorRT-LLM、TokenSpeed 未接真实后端验证；ComfyUI 未在真实服务器上验收。
  - 本机 CLI（只能本地声明）：`claude`（`claudecode/`）、`codex`（`codexcli/`）、`gemini-cli`（`geminicli/`，仅图片生成）、`grok`/`opencode`/`cursor`/`codebuddy`/`qoder`/`pi`（`cliagent/`）。
  - 托管供应商（只能本地声明，端点与 Key 固定取本地值）：`openai`、`gemini`、`deepseek`、`kimi`、`qwen`、`zhipu`、`minimax`、`openai_compatible`、`anthropic`、`anthropic_compatible`。
  - 同一 CLI 可经 `account_dir` 以多个订阅账户对外服务，见 [README](README.md#同一-cli-的多个账户)。
- ComfyUI 适配器提供 `comfyui_queue_running`/`comfyui_queue_pending`（`runtime_id` 标签）队列深度量表，复用 `Status`/`Cancel` 本就会发起的 `GET /queue`，不产生额外请求；与隧道共用同一个 `metrics.Registry`，未配置 `-metrics-addr` 时安全丢弃。失败历史同时按是否显存/内存耗尽分类（`runtime.WorkflowStatus.OutOfMemory`），供 Gateway 统计，见 [Gateway STATUS](../aiServeWeaveGateway/STATUS.md)。
- `workflow/` 仍是占位包。受控模板目录与输入绑定已在 Gateway 的 `workflow/` 实现，不能据此目录重复建设。
- `hostresources/` 在 Hello 握手前探测本节点的 CPU 核数、内存总量、GPU 数量与显存总量，写入 `ClientConfig.Resources`。探测经 `os/exec` 调用系统自带工具（Linux 读 `/proc/meminfo`、`nvidia-smi`；macOS 用 `sysctl` 取内存，GPU 显存无通用查询手段留空），不引入 `gopsutil`/NVML，见 [依赖边界](../../docs/agents/dependencies.md)。这是**尽力而为的静态容量声明**：探测失败的字段留零值、不阻塞启动。Gateway 调度器用其中的 GPU 显存总量做准入过滤（target 的 `min_gpu_memory_bytes`，未上报或上报零显存的节点不被排除），见 [Gateway STATUS](../aiServeWeaveGateway/STATUS.md)；CPU、内存与 GPU 数量目前只用于展示。
- `comfyuimanaged/`：Agent 本地、单容器的 ComfyUI Docker 生命周期管理，shell out 到 `docker` CLI（不新增 SDK 依赖）。镜像、GPU 设备、挂载路径与自定义节点来源全部来自本地 `-comfyui-managed-*` flag，从不接受 Gateway/控制面下发（`tunnel.proto`「不得表达 run this command / fetch this URL」）。
  - 启动：`Launcher.Start` 幂等——接管运行中的同名容器，不存在或已退出才创建/重建，镜像缺失先 `docker pull`；镜像必须带显式、非 `latest` 的 tag，端口固定绑定 `127.0.0.1`。`WaitReady` 只确认 TCP 可连通（未做 WebSocket 就绪检查），随后调用与 External 模式相同的 `manager.Add`，身份/能力校验交给 `comfyui.Runtime` 的 Probe/Discover。配置了 `-comfyui-managed-image` 但启动或校验失败会让 Agent 启动失败；普通本地后端未就绪不阻止启动。
  - 远程动作（`supervisor.go`）：Gateway 经隧道 Control 流触发 START/STOP/RESTART，动作不带容器名，只作用于本地声明的唯一实例；容器状态（pending/starting/running/stopped/failed）回报 Gateway。Agent 侧 RESTART 即 STOP 后 START，不检查在途 Job、不比对镜像版本，换镜像要先改本机 flag。活跃 Job 检查在 Gateway（`comfyuimanagedapi`：本副本仍有路由到该节点的非终态 Job 时答 409），跨副本聚合在控制面 `/operator/v1/nodes/:id/comfyui-managed`，Console 页面为 `/operator/comfyui-managed`。
  - 自定义节点（`customnodes.go`）：Gateway 只能按名字触发安装 `-comfyui-managed-custom-nodes`（`name=repourl@ref,...`）中已声明的节点。`InstallCustomNode` 经 `docker exec` 执行 git clone/fetch+checkout 到 `-comfyui-managed-custom-nodes-dir`（默认 `/comfyui/custom_nodes`，因镜像而异，不自动发现），写入版本标记；`ListCustomNodes` 随 `Supervisor.Snapshot` 上报。镜像须自带 `git`，不装 pip 依赖；安装后不自动重启，新节点需另行 RESTART 才生效。
  - 限制：一台机器只管一个 Managed 实例；Agent 侧只写结构化日志，审计由控制面转发时写入 `audit_logs`。默认测试使用假 `docker` 脚本；`AISW_DOCKER_LIVE_TEST=1` 门控的真实 Docker 往返测试只验证容器生命周期，ComfyUI 协议与 GPU 直通未在真实 Linux NVIDIA 环境验收。
- `modelpull/`：Agent 本地、清单驱动的模型下载器（`net/http` + `crypto/sha256`，不新增依赖）。`-model-pull-manifest` 指向本地 JSON 清单（`[]modelpull.Spec`），为空（默认）时整体关闭；下列 flag 都只在本地配置，从不接受 Gateway 或控制面下发。
  - HTTP 条目（`Kind` 零值 `KindHTTP`）：SHA-256 校验；HTTP Range 断点续传到 `<TargetPath>.part`，完成后原子改名；来源白名单 `-model-pull-allowlist` 默认「空=拒绝全部」，与 `allowed_runtimes` 的「空=放行」刻意相反。
  - 触发：启动时对整份清单执行一次；运行期 `Puller` 让 Gateway 经隧道 Control 流按名字（不是 URL）排队拉取，Agent 对照本地清单解析。逐名字进度（`BytesDownloaded`/`BytesTotal`/状态）经 `ModelPullReport` 回报，失败原因是封闭枚举 `common/modelpullstatus.FailureReason`，不携带原始错误文本，防止预签名 URL 经错误文本泄漏。Agent 关闭时取消在途下载，`.part` 保持可续传。控制面转发与 Console `/operator/model-pulls` 已提供可见性。
  - Ollama 条目（`Kind: "ollama"`）：`Name` 即模型 tag，`SourceURL`/`SHA256`/`TargetPath` 必须留空；调用 `-ollama-url` 的 `POST /api/pull`（NDJSON 流），不 shell out 到 `ollama` CLI。未配置 `-ollama-url` 时报 `ReasonOllamaUnconfigured`。不受白名单、配额与磁盘余量约束。
  - 限额：`-model-pull-max-concurrency`（默认 1，单个文件仍是一条请求、不分片）；`-model-pull-quota-bytes` 是一次 worker session（队列清空前）共享的预算，并发下用 `atomic.Int64` CAS 防透支；`-model-pull-ledger-path`/`-model-pull-ledger-quota-bytes`/`-model-pull-ledger-period` 把预算持久化为跨重启的 JSON 账本，到期自动清零，与 `-model-pull-quota-bytes` 两道闸门独立生效；`-model-pull-disk-free-margin-bytes`（`hostresources.DiskFreeBytes`）在目标文件系统剩余空间低于阈值时中止，尽力而为，不是强一致预留。
- `agentupgrade/`：本地签名的已知版本清单与 `Checker`。`-agent-upgrade-manifest` 指向本地 JSON `Manifest`（`{entries: []agentupgrade.Entry, signature}`），从不接受下发；清单为空或构建时未带 `-ldflags="-X main.agentUpgradePublicKeyHex=..."` 时整体关闭。`Signature` 是对整组 `(Version, SHA256)` 的一次 Ed25519 签名，删条目或换哈希都会验签失败；`LoadManifest` 先验签，失败即拒绝整份清单。动作经隧道 Control 流（`GatewayControl.agent_upgrade_action`/`AgentControl.agent_upgrade`，见 [隧道 README](tunnel/README.md)）到达：
  - CHECK：比较清单与 `main.version`，报告 `HasUpdate`。
  - UPGRADE：`target_version` 不在清单时报 `ReasonUnknownVersion`，不发网络请求；否则下载到 `-agent-upgrade-work-dir`（默认二进制所在目录）、对照清单 SHA256 校验（`ReasonVerificationFailed`）、`tunnel.Manager.DrainAll` 排空全部隧道连接（`-agent-upgrade-drain-timeout`，默认 30s）、`syscall.Exec` 自替换（`ReasonExecFailed`）。进行中收到的第二次触发被忽略。
  - ROLLBACK 未实现，一律报 `StateFailed`/`ReasonNotImplemented`。
  - **当前没有发送方**：Gateway 与控制面都还没有下发 `agent_upgrade_action` 的入口，Agent 侧链路只由默认测试覆盖。二进制托管在 GitHub Releases，签名私钥由维护者离线保管。

## Claude Code CLI 接入

`claudecode/` 注册本地 `kind: claude`，通过原生 Messages 隧道服务 Gateway `/v1/messages`，并经 Gateway 转换服务 `/v1/chat/completions` 与 `/v1/responses`。工具在调用方电脑执行，Agent 的 CLI 只调用隔离的 MCP 桥接工具。支持文本、Messages 图片/文档/本平台视频附件与调用方工具循环，不代表完整 Anthropic API 兼容；Chat 的普通多轮历史转换成带角色的文本上下文，活动工具链仍保持原会话。请求要求思考时（Messages 的 `thinking`、Chat 的 `reasoning_effort`、Responses 的 `reasoning`）以对应预算开启 CLI 思考并透出 thinking 块，客户端回传的思考被丢弃。

- **验收**：原版 Claude Code 客户端的工具流程只验收过 `claude-sonnet-4-6`；Chat 兼容层验证过 `claude-sonnet-5-5` 的文本、流式、多轮历史和工具续接。其他模型能被目录发现，不代表客户端兼容性已验证。
- **前提**：Gateway 必须配置控制面 API Key 校验与共享 `-redis-addr`；静态 key 不能用于原生续接。
- **Messages 图片输入（2026-11-04）**：user 与工具结果支持 base64 JPEG/PNG/GIF/WebP。有图首轮用 CLI `stream-json` 结构化输入，历史图片以编号对应独立图像块，工具图片经 MCP image 返回；图片字节参与续接历史校验。URL 与独立音频仍拒绝；Chat/Responses 图片输入仍不支持。默认测试覆盖真实 mTLS 隧道及模拟 CLI；当前本机无可用订阅登录，真实看图及线上修复尚未验收。仅需更新并重启实际承载 Claude 运行时的 Agent，详见 [图片说明](claudecode/README.md#messages-图片输入)。
- **Messages 文件与视频（2026-11-04）**：`attachments/` 支持 UTF-8 文本、原生 PDF、本地 LibreOffice 转 DOC/DOCX/XLS/XLSX/PPT/PPTX、FFmpeg 抽取 MP4/MOV/WebM 画面并交本地 whisper.cpp 转录音轨。工具文档经 Poppler 转文字和页图，保留完整原始历史校验；按 CLI 会话缓存转换结果，最多 32 MiB。顶层本地 `attachments` YAML 保存工具/模型路径及限额，设置页保存时保留，变更需重启；不接受远程命令或模型下载地址。默认模拟进程、跨副本真实 mTLS JSON/SSE、配置保存和 race 测试通过；真实 CLI 2.1.318 加模拟上游的 PDF 传输契约通过。真实转换器、语音识别、模型理解及线上均未验收/未部署；Chat/Responses 未随之开放附件。见 [完整配置、协议与验收](attachments/README.md)。
- **边界**：续接固定原节点、不自动重试；原 CLI 已不存在的孤立回合以新会话从文本记录恢复；投递给存活 CLI 的结果 24 小时内拒绝重放；原生请求最多 32 MiB。
- **终止与诊断**：合法的 `max_tokens`、`stop_sequence`、`refusal`、`model_context_window_exceeded` 保留为终止响应，完整停止后回收 CLI，避免自动续写；自然结束继续校验最终结果与退出状态，成功结果后的 CLI 通知不进入回复。本机失败日志包含固定错误分类，解析器拒绝另带事件行号与固定原因，不记录原始内容。含模拟子进程的离线回归覆盖，Opus 5.5 真实工具流程仍待验收；详见 [故障排查](claudecode/README.md#终止原因与故障排查)。

接入步骤见 [README「Claude Code」](README.md#claude-code)，参数与限制见 [Claude 包说明](claudecode/README.md)。

## Codex CLI 接入

`codexcli/` 注册本地 `kind: codex`，经 Chat/ChatStream 隧道为 Gateway `/v1/responses` 与 `/v1/chat/completions` 提供推理；工具调用返回给调用方执行。每次请求使用独立临时目录和 ephemeral thread，通过 `thread/inject_items` 注入完整历史，在首次工具回调处结束响应并回收进程。

- **支持**：文本、SSE、function/custom/namespace 工具循环、JSON Schema 输出（一次响应最多一个工具调用）；思考摘要作为独立 `ReasoningContent` 透出，思考提示映射为 `turn/start` 的 `effort`/`summary`；账户能生图时额外发布 `codex-image`，经 `/v1/images/generations` 每次生成一张图。
- **不支持**：图片/音频/文件输入、嵌入、重排、采样参数、强制工具选择、远程压缩。
- **自动发现**：见上方 `clidiscovery/`。

接入步骤见 [README「Codex」](README.md#codex)，上限与验收见 [Codex 包说明](codexcli/README.md) 与 [Gateway 工具循环](../aiServeWeaveGateway/STATUS.md#工具循环与-codex-cli-接入)。[实验命令](../../experiments/codex-cli-bridge/README.md) 保留为独立合成验证器。

## 编程智能体 CLI 接入（Grok / OpenCode / Cursor / CodeBuddy / Qoder / Pi）

`cliagent/` 注册本地 `kind: grok`、`opencode`、`cursor`、`codebuddy`、`qoder`、`pi`，以本机已登录的对应 CLI 为纯文本推理后端，经 Chat/ChatStream 隧道服务 Gateway `/v1/chat/completions`。进程生命周期、期限、背压、上限、提示词渲染与错误类别只实现一次，每种 CLI 只提供命令行、环境变量、输出方言与模型发现。每个请求在新建临时目录中启动一次 headless 进程，内建工具与 MCP 按各 CLI 能力关闭，出现任何工具事件即终止进程组；多轮历史以带角色的 JSON 文本作为上下文。模型目录由 CLI 自身的列表命令或 RPC 获取，只发布真实 ID。思考文本作为独立 `ReasoningContent` 透出；思考提示只有 Pi 映射为 `--thinking`，其余 CLI 丢弃提示、不拒绝请求。不支持工具、图片、采样参数与结构化输出；输出 token 上限只作兼容接收、不执行。

未用真实 CLI 实机验收，参数与边界见 [CLI 包说明](cliagent/README.md)。

## Gemini CLI 图片生成

`geminicli/` 注册本地 `kind: gemini-cli`，通过 Gemini CLI 官方 Nano Banana 扩展发布 `gemini-image` 图片模型，复用 Gateway `/v1/images/generations` 与图像流隧道；只支持单张文生图，凭据只从 Agent 本机环境读取，与托管聊天用的 `kind: gemini` 互不影响。

未用有效 Key 执行付费生图，安装与边界见 [Gemini CLI 包说明](geminicli/README.md)。

## 托管模型供应商接入

本地 YAML 可声明 `kind: openai` / `gemini` / `deepseek` / `kimi` / `qwen` / `zhipu` / `minimax` / `openai_compatible` / `anthropic` / `anthropic_compatible`，设置页面已提供全部选项，Key 可在页面填写并保存为 0600 文件（2026-11-02），见 [README「托管供应商 Key」](README.md#托管供应商-key)。这些 ID 只能本地声明；Gateway 下发同 ID 配置时 `base_url` 必须与本地一致，端点、Key、Headers、TLS 始终取本地值，Gateway 无法把本机 Key 指向其他地址。实现见 [托管供应商说明](../../common/runtime/hosted/README.md) 与 [Anthropic 说明](../../common/runtime/anthropic/README.md)。未用真实 Key 实机验收。

**控制面远程下发（2026-11-02，已交付，未验收）**：`remoteproviders/` 接收 `GatewayControl.remote_providers` 密文，用节点私钥（`-key-file`，每次现读）解封，校验 node_id 与版本后保存为密钥目录下的 `remote-providers.json`（0600），再经 `runtimeReloader.ReloadRemote` 在最近一次本地声明之上调和（不重读 YAML）；与本地同 ID 的远程项跳过并回报 `conflict`。结果经 `AgentControl.remote_providers` 回报（`applied_version`、`attempted_version`、`outcome` 与逐项状态，不含错误原文）。没有本地开关，商户节点与平台节点都直接接受（早期的 `gateway.remote_providers` 已移除，YAML 残留被忽略；`-remote-providers` 作为命令行参数会因未定义而启动失败）。规则见 [托管供应商说明「控制面远程下发」](../../common/runtime/hosted/README.md#控制面远程下发)。

## 独立主机部署（2026-10-30）

[部署入口](../../deploy/agent/README.md) 提供 Ubuntu systemd、macOS Apple Silicon LaunchAgent 与 Windows 11 原生用户计划任务。Windows Go 适配包括身份 ACL、受保护写入与恢复记录、同用户 named pipe、执行前加入 Job 的 CLI 后代回收、大小写不敏感环境与无 shell npm 入口、磁盘余量（`internal/localstate`、`internal/localcontrol`、`internal/cliprocess`）。`-init-config`、`-check-config`、`-control` 和 `-log-file`（`internal/rotatelog`）供部署维护；原有手动启动方式保留。身份与 YAML 写入使用临时文件替换，证书组通过受保护 pending 记录恢复。运行时 Close 的上下文限制等待，进程另有最终停止期限。

- 目标为 Ubuntu 22.04/24.04/26.04 × amd64/arm64、Windows 11 25H2/26H2 × amd64/arm64，以及 macOS 15/26/27 arm64；26H1 Windows 新硬件单独验证。真实系统均未验收，见 [矩阵](../../deploy/agent/acceptance.md)。
- 2026-11-01：Ubuntu 上允许 `--user root`（`install.sh` 与 `agent.sh` 都支持），安装时打印一行警告；默认服务账户仍是 `aiserveweave`。systemd 单元不变（无额外加固），root 下 Agent 及其启动的 CLI 拥有 root 权限。默认测试只在 Linux 上覆盖暂存树，真实 root 服务启动未验证。
- 2026-11-01 一行命令安装：[install.sh](../../deploy/agent/install.sh) 与 [install.ps1](../../deploy/agent/install.ps1) 下载并校验制品、兑换一次性码，再调用 `agent.sh`/`agent.ps1` 安装；`agent.ps1` 同步支持本地提交号版本。Agent 二进制与隧道协议无变化。使用见 [一行命令安装](../../deploy/agent/README.md#一行命令安装)，真实系统未验收。

## 尚未交付的范围

- **资源感知调度**：Gateway 已按 Hello 上报的静态 GPU 显存做准入过滤；GPU/内存实时利用率采集与按实时负载调度未做。
- **Agent 自动升级**：Agent 侧 CHECK/UPGRADE 已交付；ROLLBACK、Gateway 下发入口、控制面聚合与 Console 灰度可见性未做，因此目前无法从平台侧触发升级。
- **ComfyUI Managed**：Agent 本地生命周期、Gateway 动作与 409 检查、控制面聚合、自定义节点、Console 页面均已交付；单机多实例未做；排空检查只看得到本副本的 Job，且不会自动等排空完成再执行。
- **模型分发**：HTTP/Ollama 条目、按名字触发、并发、跨重启账本、磁盘余量与 Console 可见性均已交付。
- **托管供应商远程下发**：设置页只读展示远程运行时、本地「忽略某个远程 ID」未做；要停用某个远程运行时，在 Console 中删除，或在本地声明同 ID 覆盖。
- **其他**：`workflow/` 是占位包；Claude Code 不自动发现；本机自动发现不等于进程生命周期管理；已有隧道文件转发不等于对象存储或原节点离线后的文件可用性。

跨服务任务与验收见 [根 STATUS](../../STATUS.md)。
