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
- `cliusage/`：读取本地 Codex（`account/rateLimits/read`）与 Claude Code（`get_usage`）的订阅窗口，在设置页面展示并经隧道上报；额度耗尽时以可重试的 `rate_limited` 加 `retry_after` 报告，Gateway 据此搁置该运行时。事件格式按 CLI 当前版本实现，未在真实账户耗尽场景下验收。2026-11-26 起：暂时性查询失败保留上一轮读数（未登录、未提供额度仍清除）；有窗口达到 80% 时每分钟刷新；Claude 的 `allowed_warning`/`rejected` 事件与 Codex 的额度失败经 `runtime.Dependencies.Quota`（`runtime.QuotaGuard`）触发立即刷新（受每分钟一批约束）；已知账户用完时（用量读数显示适用窗口到 100%，或 CLI 报告带重置时间的拒绝），Claude Code 与 Codex 的回合在本地答复 `rate_limited`，不启动 CLI、不联系服务商（`cliusage/guard.go`）；没有可用重置时间的额度拒绝（未给出、已过去、Codex 生图额度、Claude 无事件的 429）也在 Agent 侧按退避搁置（1 分钟起翻倍，至多 1 小时，读数不提前解除）；覆盖所有模型的窗口已用完的账户不按每分钟刷新。这些改动用一次性测试验证（未提交），默认测试套件尚未覆盖。见 [README「本地 CLI 账户额度」](README.md#本地-cli-账户额度) 与 [「额度耗尽与限流」](README.md#额度耗尽与限流)。
- `tunnel/` 已实现身份注册/续期、多副本连接、槽池、心跳、运行时配置应用和流式请求/产物转发。执行的 runtime 必须命中本地白名单。
- 后端适配：[registry.go](registry.go) 注册以下种类（常量见 `common/runtime/types.go`）。
  - 本地推理服务（共享的 `common/runtime`，Gateway 可下发同 ID 配置）：`ollama`、`vllm`、`sglang`、`trtllm`、`tokenspeed`、`comfyui`（适配位于 `common/runtime/workflow/comfyui`）。TensorRT-LLM、TokenSpeed 未接真实后端验证；ComfyUI 未在真实服务器上验收。
  - 本机 CLI（只能本地声明）：`claude`（`claudecode/`）、`codex`（`codexcli/`）、`gemini-cli`（`geminicli/`，仅图片生成）、`grok`/`opencode`/`cursor`/`codebuddy`/`qoder`/`pi`（`cliagent/`）。
  - 托管供应商（只能本地声明，端点与 Key 固定取本地值）：`openai`、`gemini`、`deepseek`、`kimi`、`qwen`、`zhipu`、`minimax`、`openai_compatible`、`anthropic`、`anthropic_compatible`。
  - 同一 CLI 可经 `account_dir` 以多个订阅账户对外服务，见 [README](README.md#同一-cli-的多个账户)。
- ComfyUI 适配器提供 `comfyui_queue_running`/`comfyui_queue_pending`（`runtime_id` 标签）队列深度量表，复用 `Status`/`Cancel` 本就会发起的 `GET /queue`，不产生额外请求；与隧道共用同一个 `metrics.Registry`，未配置 `-metrics-addr` 时安全丢弃。失败历史同时按是否显存/内存耗尽分类（`runtime.WorkflowStatus.OutOfMemory`），供 Gateway 统计，见 [Gateway STATUS](../aiServeWeaveGateway/STATUS.md)。
- `workflow/` 仍是占位包。受控模板目录与输入绑定已在 Gateway 的 `workflow/` 实现，不能据此目录重复建设。
- `hostresources/` 在 Hello 握手前探测本节点的 CPU 核数、内存总量、GPU 数量与显存总量，写入 `ClientConfig.Resources`。探测经 `os/exec` 调用系统自带工具（Linux 读 `/proc/meminfo`、`nvidia-smi`；macOS 用 `sysctl` 取内存，GPU 显存无通用查询手段留空），不引入 `gopsutil`/NVML，见 [依赖边界](../../docs/agents/dependencies.md)。这是**尽力而为的静态容量声明**：探测失败的字段留零值、不阻塞启动。Gateway 调度器用其中的 GPU 显存总量做准入过滤（target 的 `min_gpu_memory_bytes`，未上报或上报零显存的节点不被排除），见 [Gateway STATUS](../aiServeWeaveGateway/STATUS.md)；CPU、内存与 GPU 数量目前只用于展示。2026-11-14 起，`hostresources/usage_*.go` 另行采集**实时** CPU、内存与负载（Linux `/proc` 与 cgroup v2、macOS sysctl 估算 CPU、Windows kernel32），由 `admission/` 经心跳上报，并驱动可选的资源闸门与本机分类信号量，见 [README「整机准入与负载上报」](README.md#整机准入与负载上报)。
- `comfyuimanaged/`：Agent 本地、单容器的 ComfyUI Docker 生命周期管理，shell out 到 `docker` CLI（不新增 SDK 依赖）。镜像、GPU 设备、挂载路径与自定义节点来源全部来自本地 `-comfyui-managed-*` flag，从不接受 Gateway/控制面下发（`tunnel.proto`「不得表达 run this command / fetch this URL」）。
  - 启动：`Launcher.Start` 幂等——接管运行中的同名容器，不存在或已退出才创建/重建，镜像缺失先 `docker pull`；镜像必须带显式、非 `latest` 的 tag，端口固定绑定 `127.0.0.1`。`WaitReady` 只确认 TCP 可连通（未做 WebSocket 就绪检查），随后调用与 External 模式相同的 `manager.Add`，身份/能力校验交给 `comfyui.Runtime` 的 Probe/Discover。配置了 `-comfyui-managed-image` 但启动或校验失败会让 Agent 启动失败；普通本地后端未就绪不阻止启动。
  - 远程动作（`supervisor.go`）：Gateway 经隧道 Control 流触发 START/STOP/RESTART，动作不带容器名，只作用于本地声明的唯一实例；容器状态（pending/starting/running/stopped/failed）回报 Gateway。Agent 侧 RESTART 即 STOP 后 START，不检查在途 Job、不比对镜像版本，换镜像要先改本机 flag。活跃 Job 检查在 Gateway（`comfyuimanagedapi`：本副本仍有路由到该节点的非终态 Job 时答 409），跨副本聚合在控制面 `/operator/v1/nodes/:id/comfyui-managed`，Console 页面为 `/operator/comfyui-managed`。
  - 自定义节点（`customnodes.go`）：Gateway 只能按名字触发安装 `-comfyui-managed-custom-nodes`（`name=repourl@ref,...`）中已声明的节点。`InstallCustomNode` 经 `docker exec` 执行 git clone/fetch+checkout 到 `-comfyui-managed-custom-nodes-dir`（默认 `/comfyui/custom_nodes`，因镜像而异，不自动发现），写入版本标记；`ListCustomNodes` 随 `Supervisor.Snapshot` 上报。镜像须自带 `git`，不装 pip 依赖；安装后不自动重启，新节点需另行 RESTART 才生效。
  - 限制：一台机器只管一个 Managed 实例；Agent 侧只写结构化日志，审计由控制面转发时写入 `audit_logs`。默认测试使用假 `docker` 脚本；`AISW_DOCKER_LIVE_TEST=1` 门控的真实 Docker 往返测试只验证容器生命周期，ComfyUI 协议与 GPU 直通未在真实 Linux NVIDIA 环境验收。
- `modelpull/`：Agent 本地、清单驱动的模型下载器（`net/http` + `crypto/sha256`，不新增依赖）。`-model-pull-manifest` 指向本地 JSON 清单（`[]modelpull.Spec`），为空（默认）时整体关闭；下列 flag 都只在本地配置，从不接受 Gateway 或控制面下发。
  - HTTP 条目（`Kind` 零值 `KindHTTP`）：SHA-256 校验；HTTP Range 断点续传到 `<TargetPath>.part`，完成后原子改名。哈希边写边算（续传先对已有前缀求哈希，再接着算），完成后不再整文件回读；校验通过后先 `fsync` 再改名。拷贝缓冲 1MiB。已就位的目标文件旁会写 `<TargetPath>.sha256cache`（`size`/`mtime`/`sha256`），`alreadySatisfied` 在大小与 mtime 都匹配时直接采信、跳过整文件哈希；sidecar 缺失、损坏或不匹配一律退回完整哈希。它只是缓存，信任边界等同于「谁能写模型目录」：能写目录的人本来就能替换模型，但「改内容不改大小与 mtime」（静默磁盘损坏或蓄意篡改后 `Chtimes`）不再被启动检查发现，删掉 sidecar 即可强制重新校验；来源白名单 `-model-pull-allowlist` 默认「空=拒绝全部」，与 `allowed_runtimes` 的「空=放行」刻意相反。
  - 触发：启动时对整份清单执行一次；运行期 `Puller` 让 Gateway 经隧道 Control 流按名字（不是 URL）排队拉取，Agent 对照本地清单解析。逐名字进度（`BytesDownloaded`/`BytesTotal`/状态）经 `ModelPullReport` 回报，失败原因是封闭枚举 `common/modelpullstatus.FailureReason`，不携带原始错误文本，防止预签名 URL 经错误文本泄漏。Agent 关闭时取消在途下载，`.part` 保持可续传。控制面转发与 Console `/operator/model-pulls` 已提供可见性。
  - Ollama 条目（`Kind: "ollama"`）：`Name` 即模型 tag，`SourceURL`/`SHA256`/`TargetPath` 必须留空；调用 `-ollama-url` 的 `POST /api/pull`（NDJSON 流），不 shell out 到 `ollama` CLI。未配置 `-ollama-url` 时报 `ReasonOllamaUnconfigured`。不受白名单、配额与磁盘余量约束。
  - 限额：`-model-pull-max-concurrency`（默认 1，单个文件仍是一条请求、不分片）；`-model-pull-quota-bytes` 是一次 worker session（队列清空前）共享的预算，并发下用 `atomic.Int64` CAS 防透支；`-model-pull-ledger-path`/`-model-pull-ledger-quota-bytes`/`-model-pull-ledger-period` 把预算持久化为跨重启的 JSON 账本，到期自动清零；账本按大块租约记账（每次 min(64MiB, 配额/16, 剩余待下载量，下限 1MiB)，一次下载写账本约每租约一次而非每 32KiB 一次），下载结束或失败时归还未用部分，租约在账本里登记 `(path, base, bytes)`，进程崩溃后下次启动按 `.part` 实际增长量修剪（`.part` 已不存在则保留整笔租约，宁多算不少算）；并发下载仍在同一把锁内「检查配额并授予」，不会合计超额，代价是接近配额上限时，其他下载持有的未用租约（至多一个租约大小）可能让后来者提前报账本配额不足，归还后恢复；与 `-model-pull-quota-bytes` 两道闸门独立生效；`-model-pull-disk-free-margin-bytes`（`hostresources.DiskFreeBytes`）在目标文件系统剩余空间低于阈值时中止，尽力而为，不是强一致预留。
- `agentupgrade/`：本地签名的已知版本清单与 `Checker`。`-agent-upgrade-manifest` 指向本地 JSON `Manifest`（`{entries: []agentupgrade.Entry, signature}`），从不接受下发；清单为空或构建时未带 `-ldflags="-X main.agentUpgradePublicKeyHex=..."` 时整体关闭。`Signature` 是对整组 `(Version, SHA256)` 的一次 Ed25519 签名，删条目或换哈希都会验签失败；`LoadManifest` 先验签，失败即拒绝整份清单。动作经隧道 Control 流（`GatewayControl.agent_upgrade_action`/`AgentControl.agent_upgrade`，见 [隧道 README](tunnel/README.md)）到达：
  - CHECK：比较清单与 `main.version`，报告 `HasUpdate`。
  - UPGRADE：`target_version` 不在清单时报 `ReasonUnknownVersion`，不发网络请求；否则下载到 `-agent-upgrade-work-dir`（默认二进制所在目录）、对照清单 SHA256 校验（`ReasonVerificationFailed`）、`tunnel.Manager.DrainAll` 排空全部隧道连接（`-agent-upgrade-drain-timeout`，默认 30s）、`syscall.Exec` 自替换（`ReasonExecFailed`）。进行中收到的第二次触发被忽略。
  - ROLLBACK 未实现，一律报 `StateFailed`/`ReasonNotImplemented`。
  - **当前没有发送方**：Gateway 与控制面都还没有下发 `agent_upgrade_action` 的入口，Agent 侧链路只由默认测试覆盖。二进制托管在 GitHub Releases，签名私钥由维护者离线保管。

## Claude Code CLI 接入

`claudecode/` 注册本地 `kind: claude`，通过原生 Messages 隧道服务 Gateway `/v1/messages`，并经 Gateway 转换服务 `/v1/chat/completions` 与 `/v1/responses`。工具在调用方电脑执行；带 `safeguards` 的原生请求保留完整结构与工具身份，Agent CLI 不注册本地执行工具，每轮独立调用真实上游。无该字段时默认同样原样直通（见下方「未带 safeguards 的原生直通」），旧节点等情形继续使用隔离 MCP 桥接；Chat 的普通多轮历史转换成带角色的文本上下文，活动工具链保持原会话；请求要求思考时转换预算，客户端回传的思考被丢弃。支持文本、Messages 图片/文档/本平台视频附件与调用方工具循环，不代表完整 Anthropic API 兼容。

- **回合分段计时（2026-11-18）**：
  - 新增 `internal/cliphase`。Claude 原生回合与 Codex 对话回合在结束日志中记录各阶段到达时间、上游连接等待（Claude）、进程 CPU 与峰值内存；Codex 新增 `Codex round finished` 日志。`/metrics` 新增 `agent_cli_phase_seconds` 等四个直方图。
  - 离线基准 `TestCLIStartupBenchmark`（`AISW_CLI_BENCH=1`，两个包各一个）走 Agent 真实的代码路径，不联网、不消耗额度。
  - 本机基线：Claude 2.1.327 到模型请求发出 p50 188 ms；codex 0.169.0 到能接收回合 p50 59 ms。
  - 基准发现：Claude CLI 在模型请求之前，每个回合要先等完约 3–5 个发往 api.anthropic.com 的请求（`policy_limits`、`settings`、功能开关等）。上游不可达时，它会在这一步重试约 18 秒。
  - 线上分布已采集（开发机节点 11-18 13:30 至 11-19 12:13，Claude 原生回合 1248 个，Codex 仅 1 个），按门槛的结论记在 [P2 规格](../../.scratch/agent-cli-performance/spec.md#comments)。见 [README「本机 CLI 回合分段计时」](README.md#本机-cli-回合分段计时)。
- **Claude 同账户共用上游连接（2026-11-19）**：同一运行时各回合的每进程代理把解密后的 `api.anthropic.com` 请求交给运行时级连接池（HTTP/2 多路复用），不同运行时不共用；回合取消只取消自己的请求，运行时关闭时释放全部连接。复用连接在收到响应前出错时，改经进程自己的新连接重发一次（请求体已完整保留时）。新增本地字段 `share_upstream_connections: false` 可按运行时关闭，Gateway 下发不能改动。默认测试、claudecode 竞态检查与 Windows/Linux 交叉编译通过；未部署，`cli_ready_ms` 的线上效果待复测（预计只省下约 250 ms 握手）。见 [README](README.md#共用上游连接)。
- **Claude 启动请求缓存（2026-11-27）**：运行时连接池缓存 `policy_limits` 与功能开关求值的答复（键含全部请求头与去掉 `attributes.sessionId` 的请求体），命中即答并后台刷新，10 分钟未刷新成功即失效；新增本地字段 `cache_startup_requests: false`。用一次性测试验证（未提交）：命中/未命中、键隔离、状态码与大小边界、刷新替换/删除/保留、过期、开关组合、并发与关闭，claudecode 竞态检查与 Windows/Linux 交叉编译通过；本机 CLI 2.1.330 回环加 300 ms 延迟时到模型请求发出从约 890 ms 降到约 200 ms。默认测试套件未覆盖。2026-11-28 已部署到 Mac mini（OAuth 登录）：每回合命中 1 次（本机 API key 登录为 2 次）。随后新增回合日志 `startup_cache_misses`（未命中类别与原因，只含名称），并让 `/api/eval-authed/` 同样缓存；再次部署后 72 个回合只有重启后首个 `eval:first` 未命中，`policy_limits` 从未出现，即该账户的 CLI 不发它，功能开关每回合都命中。带安全检查回合 `request_sent_ms` p50 1402 → 1080 ms、p90 1960 → 1224 ms（58 个回合）；原生直通只有 16 个回合，暂无结论。见 [README](README.md#启动请求缓存)。
- **未知事件、终止与诊断（2026-11-18）**：
  - 原生回合跳过不认识的顶层事件，并计入 `ignored_cli_events`；不认识的流事件、增量与停止原因原样转发，未知停止原因按终止处理。`conversation_reset`、`control_request` 与流内 `error` 仍让回合失败；兼容桥接保持严格。`keep_alive` 视为通知。
  - 推理请求被取消时，先 SIGTERM，2 秒后 SIGKILL（新增 `cliprocess.ConfigureGraceful`）。正常结束与隔离失败仍立即终止。实测 2.1.326 收到 SIGTERM 后约 0.55 秒退出。
  - 失败日志新增 `cli_stderr`（标准错误最后 8 KiB 归入的固定分类）与 `cli_startup_failure`（CLI 给出的 `startup_failure_reason`）。真实 2.1.327 以无效代理复现得到 `proxy_invalid`。
  - 每个 CLI 进程新增 `CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=1` 与 `CLAUDE_CODE_STARTUP_FAILURE_RESULTS=1`。遥测开关会连带停止拉取功能开关，未设置。
  - 默认测试、竞态检查与 Windows/Linux 交叉编译通过；真实订阅与生产链路未验收。见 [故障排查](claudecode/README.md#终止原因与故障排查)。
- **CLI 自动更新与并发登录刷新（2026-11-18）**：Agent 启动的每个 CLI 进程都带 `DISABLE_AUTOUPDATER=1` 和 `CLAUDE_CODE_OAUTH_401_WAIT_MS=30000`。后者不在官方文档中，是从 2.1.326 的程序里确认的。CLI 报告「另一个进程正在刷新登录」时，改按可重试的 `backpressure` 报告，日志 `reason=login_refresh_busy`，Gateway 会换下一个候选；以前按 `cli_result` 以 502 应答并计入熔断。开发机上观察到，交互使用的 Claude Code 会把 PATH 上的同一份 CLI 自动更新（2.1.326 → 2.1.327）；生产主机固定版本的做法见 [README](README.md#claude-code)。默认测试与竞态检查通过，新测试在旧实现上失败；真实并发刷新与 401 等待尚未验收。见 [故障排查](claudecode/README.md#终止原因与故障排查)。
- **Claude Code 2.1.323 复验（2026-11-13）**：Agent 代码无需修改。逐条核对 changelog：`--permission-mode auto` 不可用时改为 Manual 启动、含 `sandbox` 键的无效 `--settings` 拒绝启动、hook 心跳、`ALL_PROXY` 不再用于策略请求等变更均不影响 Agent 的无头进程。本机 `2.1.323` 与 `2.1.322` 的全部 7 组 `TestNativeCLI*` 离线契约测试均通过，结果一致；真实订阅、Agent 主机实机与生产链路未验收。门控测试须在装有待验 CLI 的主机上执行，见 [验收记录](../../docs/acceptance/claude-code-2.1.323-2026-11-13/README.md)。
- **原生回合放行未声明工具（2026-11-11）**：16:28–16:29 连续 400 的根因已确认：五次失败都是原生或 safeguards 回合的 `unsafe_tools`，模型调用了本轮 `tools` 未声明的工具（与等待数据库就绪时调用尚未载入 schema 的延迟工具 `Monitor` 的时间吻合；Agent 不记录工具名，属推断），Agent 中止整轮，Claude Code 重试同一轮时模型做同样选择，五次全部失败。原生回合的工具由调用方执行、本机 CLI 已禁用全部本地工具，因此改为原样转发未声明的工具调用，由调用方答复「没有这个工具」；名称为本机 CLI 自有工具（`EndConversation`、`mcp__` 前缀）时仍拒绝，兼容桥接保持严格。新增 `TestNativeForwardsUndeclaredTool`（原生与 safeguards 各一，已确认旧实现下失败）并扩充 `TestUnsafeToolFailureLocation`；Agent 默认测试与 claudecode 竞态检查通过。已部署到 Agent 主机（19:24 重启）并在该主机用 Go 1.27.4 复跑新测试；真实验证：本机 Claude Code `2.1.322` 经生产 Gateway 让模型直接调用未声明的 `WaitForPort`，客户端收到并回复「No such tool available」，两轮均 `completed`，重启后 `unsafe_tools` 为 0。

- **工具隔离拒绝诊断（2026-11-11）**：生产 16:28–16:29（台北）连续 400 已关联到 Agent `unsafe_tools`；旧日志不能区分初始化本机工具、上游未声明工具与缺失 ID。现新增这三类固定 `cli_event_error` 及行号，继续拒绝原有不安全事件，不放宽本机执行或上游声明校验。本机 CLI `2.1.322` 的 8 轮契约及三条链路各 12 段工具测试通过。诊断已在真实 Agent 主机上原生测试、构建并部署；原报错客户端重试后于 17:07:33 收到 `end_turn`，该进程观测窗口内共完成 28 轮、无新失败（包含其他会话）。此次拒绝未复现，具体分支及根因仍未确认，不视为根因修复，见[验证记录](../../docs/acceptance/claude-unsafe-tools-2026-11-11/README.md)。

- **CLI 契约加固（2026-11-11）**：依据官方 CLI/SDK 文档，原生进程增加 `--disallowedTools "*"`，与空内建工具、严格空 MCP 和受限模式共同隔离；调用方工具定义保持原样，兼容桥接仍只开放自己的 MCP。合法认证/工具进度通知及成功结果后的额度通知不再误判；语义重置、未知事件及工具声明/ID 违规仍拒绝。本机 CLI 协议、工具隔离与启动失败改为后端错误（正文前 HTTP 502），实际上游 400/429/529 保留。默认测试、vet/build 与 CLI `2.1.322` 的 6 类工具隔离、31 MiB 历史、4 MiB safeguards、PDF、压缩和三条链路各 12 段工具流程通过。当前代码与真实部署验证进展见[契约记录](../../docs/acceptance/claude-cli-contract-2026-11-11/README.md)；原生产 `unsafe_tools` 具体分支尚未复现，不能视为该故障的根因修复。

- **未带 safeguards 的原生直通（2026-11-05）**：Claude 运行时声明 `anthropic_messages_native`，Gateway 据此把未带 `safeguards` 的 `/v1/messages` 原样、无状态地交给 Agent（隧道 `MessagesRequest.native`），adaptive 思考、缓存标记、`context_management`、延迟工具、对话中途 system 与上游工具 ID 不再被改写；Office/视频/文本文件附件在发送前转换为原生内容块。旧版 Agent、桥接工具调用的续接及 Chat/Responses 仍走兼容桥接；运行时能力覆盖可关闭该能力，但目前没有下发入口，回退需保留旧版 Agent。默认测试覆盖双 Gateway mTLS JSON/SSE、跨副本无状态续接、混合版本回退与附件转换；真实 CLI 与上游尚未验收。须先升级 Agent，再升级 Gateway，见 [原生直通](claudecode/README.md#未带-safeguards-的原生直通)。
- **原生 Messages 上下文策略（2026-11-24）**：调用方省略 `context_management` 时，Agent 移除 CLI 自行生成的上下文策略，修复 `clear_thinking_20251015` 与调用方思考模式冲突的 400。Agent、Gateway HTTP API 与 Gateway e2e 的默认测试通过。VS Code 扩展内置 Claude Code `2.1.331` 的回环测试确认旧逻辑可复现冲突，修复后通过大请求和带 `safeguards` 的显式策略转发测试。生产部署和真实上游待验收。现有原生直通部署只需升级 Agent。
- **验收**：兼容桥接路径下，原版 Claude Code 客户端工具流程只验收过 `claude-sonnet-4-6`；Chat 兼容层验证过 `claude-sonnet-5-5` 的文本、流式、多轮历史和工具续接。其他模型能被目录发现，不代表客户端兼容性已验证。
- **前提**：Gateway 必须配置控制面 API Key 校验；兼容桥接的 MCP 续接还要求共享 `-redis-addr`，原生直通不需要。静态 key 不具备该原生路径需要的身份归属。
- **auto 服务端安全检查（2026-11-04）**：支持 `dangerous_tool_use` / `classifier_context`，经隧道传递 API 版本与 beta，将上游真实 `safeguard_results` 原样返回；安全上下文、原始工具名/ID、结构化历史不重写，不伪造判定。每轮独立，不使用旧 MCP 的一次性续接记录。默认测试覆盖两个 Gateway 副本的 mTLS、JSON/SSE、原始上下文、并行与截断工具流；CLI `2.1.318` 离线实测工具禁用，原版客户端经新 Agent 与真实 `claude-opus-5-5` 上游以两轮原生请求完成一次工具往返，收到两次 `available` 和一次 `evaluated/not_flagged`；[验收记录](../../docs/acceptance/claude-auto-safeguards-2026-11-04/README.md)区分本机真实调用与双 Gateway 模拟 CLI 测试。生产 open7x.com 的受控长工具链已在 2026-11-05 验证，Agent 主机直连对照与实际编程效果仍待验收。该协议初次启用需要先更新 Agent，再更新 Gateway；客户端需允许配套 beta 并新建会话，详见 [兼容说明](claudecode/README.md#claude-code-auto-服务端安全检查)。
- **Claude Code 2.1.319 复验（2026-11-09）**：Agent 代码无需修改，`2.1.319` 下离线契约（8 轮原生契约、三条链路各 12 段工具链）全部通过。客户端环境变量规则有变化：仍要移除 `AUTO_MODE_SERVER=0`；保留 `DISABLE_EXPERIMENTAL_BETAS=1` 时必须显式设 `AUTO_MODE_SERVER=1`；经网关时判定缺失或 `unavailable` 不再退回内置分类器，而是直接拒绝。因此客户端升级前，Agent 和 Gateway 必须已经升级。真实订阅下直连通过；经 Agent 的长工具链因上游 cyber refusal 以及客户端改用回退模型 `claude-opus-4-8` 重试而失败，`2.1.318` 下同样失败，不属于版本回归。直连与经 Agent 时上游拒绝结果不同的原因，以及生产环境下回退模型能否路由，都待跟进。见 [验收记录](../../docs/acceptance/claude-code-2.1.319-2026-11-09/README.md)。
- **原生路径与诊断完善（2026-11-05）**：原生请求跳过旧附件转换、历史渲染和思考/effort/缓存参数映射；Office 与 `video` 平台扩展不在该路径自动转换。生产入口按回合记录 `message_path`、固定 `safeguard_status` 和结束状态，不记录推理载荷。CLI `2.1.318` 加回环模拟上游通过 8 轮原始字段/大历史/客户端压缩摘要/PDF 及工具 PDF 传输契约；原版客户端的直连与 Agent 长工具链均完成 12 次读取，Agent 场景 14 轮模拟判定可用。客户机现有 Gateway 验收入口经 `api.open7x.com` / `claude-opus-5-5` 真实链路完成 12 次工具读取与最终结果校验，16 个 safeguards 请求中 15 轮确认 available，约 93 秒。Agent 主机直连基线、真实编程效果、模型自动压缩及本次修改部署后验收仍待执行；客户机认证类型不能代表远端订阅状态。需更新并重启 Agent，Gateway 协议无需修改，见[验收记录](../../docs/acceptance/claude-native-preservation-2026-11-05/README.md)。
- **Messages 图片输入（2026-11-04）**：user 与工具结果支持 base64 JPEG/PNG/GIF/WebP。有图首轮用 CLI `stream-json` 结构化输入，历史图片以编号对应独立图像块，工具图片经 MCP image 返回；图片字节参与续接历史校验。URL 与独立音频仍拒绝；Chat/Responses 图片输入仍不支持。默认测试覆盖真实 mTLS 隧道及模拟 CLI；此前图片专项验证时缺少可用订阅登录，真实看图及线上修复仍未验收。仅需更新并重启实际承载 Claude 运行时的 Agent，详见 [图片说明](claudecode/README.md#messages-图片输入)。
- **Messages 文件与视频（2026-11-04）**：`attachments/` 支持 UTF-8 文本、原生 PDF、本地 LibreOffice 转 DOC/DOCX/XLS/XLSX/PPT/PPTX、FFmpeg 抽取 MP4/MOV/WebM 画面并交本地 whisper.cpp 转录音轨。工具文档经 Poppler 转文字和页图，保留完整原始历史校验；按 CLI 会话缓存转换结果，最多 32 MiB。顶层本地 `attachments` YAML 保存工具/模型路径及限额，设置页保存时保留，变更需重启；不接受远程命令或模型下载地址。默认模拟进程、跨副本真实 mTLS JSON/SSE、配置保存和 race 测试通过；真实 CLI 2.1.318 加模拟上游的 PDF 传输契约通过。真实转换器、语音识别、模型理解及线上均未验收/未部署；Chat/Responses 未随之开放附件。见 [完整配置、协议与验收](attachments/README.md)。
- **边界**：兼容桥接的续接固定原节点、不自动重试；原 CLI 已不存在的孤立回合以新会话从文本记录恢复；投递给存活 CLI 的结果 24 小时内拒绝重放。原生直通与带安全上下文的请求依靠调用方完整历史，无状态重试与跨副本调用。原生请求最多 32 MiB。
- **非 Claude Code 客户端身份补齐（2026-11-15）**：原生直通回合中，调用方 `system` 缺少 Claude Code 身份块时，代理把 CLI 自己的计费头与身份块放在前面，`metadata` 缺少 `user_id` 时补上 CLI 的值，修复 DeepSeek Harness 等客户端经订阅登录被上游以 429 拒绝的问题。`system`/`metadata` 改经每进程代理合并，不再进入 `CLAUDE_CODE_EXTRA_BODY`。同类调用方的自定义工具名在上游改为 Claude Code 形式（内建工具拼写，其余加 `mcp__caller__`），上游 `tool_use` 发给调用方前改回原名；带 `safeguards` 的请求不改名。离线单元测试通过；真实订阅与 DeepSeek Harness 实机尚未验收。见 [claudecode README](claudecode/README.md#未带-safeguards-的原生直通)。
- **原生大请求修复（2026-11-09）**：修复完整请求经 `CLAUDE_CODE_EXTRA_BODY` 放入设置文件后超过 CLI 2 MiB 上限而产生 `missing_result` 的路径。Agent 检查最终设置字节数，大文档经原有每进程回环代理补入主流式 API 调用，认证继续由 CLI 管理；旁路请求保持原样。缺少结果和异常退出按后端错误报告，回合日志新增 `request_bytes`。默认边界、gzip、字段与认证头保留、旁路回归及相关 race/vet 通过；真实 CLI 2.1.319 与回环模拟 API 验证至 31 MiB 历史及 4 MiB safeguards，上线任务和其他系统仍待验收，见[验收记录](../../docs/acceptance/claude-settings-limit-2026-11-09/README.md)。
- **原生链路后续加固（2026-11-09）**：修复父代理 CONNECT 等待中无法及时取消、多行 SSE 错误未捕获及 `GZip` 编码名误拒绝；会话寿命与生成空闲到期保留超时原因并按 `timeout` 报告，不再返回协议 400。默认离线回归覆盖 HTTP/HTTPS 父代理取消、已建立隧道继续可用、逐字节 SSE 分帧及 429/529 透传、压缩和注入时钟到期。全仓默认测试、vet/build、全服务 race 通过；CLI `2.1.319` 回环复验通过 31 MiB 历史及原有 8 轮原生契约。无协议或配置变更，现有节点更新并重启 Agent；生产长任务、真实父代理和其他系统仍待验收，见[验证记录](../../docs/acceptance/claude-native-robustness-2026-11-09/README.md)。
- **上游响应头与错误透传（2026-11-05）**：原生回合的 CLI 经每进程一个、带口令的回环代理访问上游。代理只解密 `api.anthropic.com`，用名称受限的临时 CA 截获最终一次 `/v1/messages` 应答。成功响应头随首个事件传给 Gateway；上游导致的失败连同状态码、响应头和原始错误文档经隧道新字段传回，原生 `/v1/messages` 原样返回。能力拒绝保留固定分类，上游 401/403/407 不透传。同时修复不带 `tools` 的原生请求向上游发送 `null` 的问题。开发时的临时测试（未入库）验证了双 Gateway mTLS 下的响应头与 529/429/流内错误；CLI `2.1.318` 加回环模拟上游与一次真实订阅请求验证了代理、CA 和真实限额头。真实上游错误与生产部署未验收，详见[透传说明](claudecode/README.md#上游响应头与错误透传)。需先升级 Agent，再升级 Gateway。
- **原生兼容修复（2026-11-05）**：补齐阈值/按需压缩的请求、增量、摘要/签名聚合与压缩停止；从 CLI API 错误元数据提取客户端可识别的固定能力拒绝分类。Gateway 同步保留原始 `thinking`（模型后缀仅解析路由）并按 `usage.iterations` 计量压缩采样。默认回归覆盖双 Gateway mTLS JSON/SSE；真实 CLI 加回环模拟上游已验证压缩与四类能力拒绝，未代表真实模型自动压缩或生产部署验收。须先升级 Agent，再升级 Gateway，详见[验收记录](../../docs/acceptance/claude-native-preservation-2026-11-05/README.md)。
- **终止与诊断**：合法的 `max_tokens`、`stop_sequence`、`refusal`、`model_context_window_exceeded` 保留为终止响应，完整停止后回收 CLI，避免自动续写；自然结束继续校验最终结果与退出状态，成功结果后的 CLI 通知不进入回复。本机失败日志包含固定错误分类，解析器拒绝另带事件行号与固定原因，不记录原始内容。含模拟子进程的离线回归覆盖，未带 `safeguards` 的 Opus 5.5 真实工具流程仍待验收；详见 [故障排查](claudecode/README.md#终止原因与故障排查)。
- **生成前拒绝及连续工具回合（2026-11-04）**：`Claude Messages request rejected` 区分入口校验、续接历史/配置/归属/会话状态及工具结果拒绝，配置差异仅列字段名；公开错误保留运行时 ID。线上日志确认 `continuation_tool_count_mismatch` 和 beta 变化拒绝，本地已复现：已投递旧邮箱不再计入新一轮工具集合；beta 列表按集合比较，实际增删功能在完整归属/历史/结果集合与其他配置匹配时重建 CLI，从已执行结果恢复。默认测试覆盖连续回合、迟到回调、完整并行结果校验、beta 等价/重建及跨两个 Gateway 的 mTLS；真实 Opus 长任务和线上部署仍待验收，详见 [续接规则](claudecode/README.md#生命周期与限额)。

接入步骤见 [README「Claude Code」](README.md#claude-code)，参数与限制见 [Claude 包说明](claudecode/README.md)。

## Codex CLI 接入

`codexcli/` 注册本地 `kind: codex`，经 Chat/ChatStream 隧道为 Gateway `/v1/responses` 与 `/v1/chat/completions` 提供推理；工具调用返回给调用方执行。每次请求使用独立临时目录和 ephemeral thread，通过 `thread/inject_items` 注入完整历史，在首次工具回调处结束响应并回收进程。

- **支持**：文本、SSE、function/custom/namespace 工具循环、JSON Schema 输出（一次响应最多一个工具调用）；思考摘要作为独立 `ReasoningContent` 透出，思考提示映射为 `turn/start` 的 `effort`/`summary`；账户能生图时额外发布 `codex-image`，经 `/v1/images/generations` 每次生成一张图。
- **不支持**：图片/音频/文件输入、嵌入、重排、采样参数、强制工具选择、远程压缩。
- **自动发现**：见上方 `clidiscovery/`。
- **健康检查、终止与诊断（2026-11-18，P1）**：
  - Health 只做 `initialize` 与 `account/read` 确认登录，不再每 10 秒读取一次模型目录。
  - 协议流损坏、未放行条目与未获准指令从 400 `protocol_error` 改为 502 `upstream_error`，与 Claude 一致。
  - 失败信息末尾附上 stderr 的固定分类（`cli_stderr: …`）；被取消的请求先 SIGTERM、2 秒后 SIGKILL（实测 0.169.0 约 0.08 秒退出）。
  - 另外关闭默认开启的宿主工具 `browser_use`、`browser_use_external`、`computer_use`、`in_app_browser`、`sleep_tool`；条目白名单放行只记录结果的 `functionCallOutput`。
  - 真实 0.169.0 确认 19 个功能开关生效；尚未用真实账户跑 `TestLiveCallerToolRoundTrip`，确认新关闭的功能不会连带隐藏客户端动态工具。
- **放宽误伤上限、启动参数与失败分类（2026-11-18）**：
  - 去掉每请求 10000 个 RPC 帧的上限。流式输出大约每个 token 一帧，长回答原本会在中途被截断，并以 400 报错。
  - 注入历史的上限从 1 MiB 提高到 32 MiB，取消 2048 条消息和 128 个工具的上限；codex-cli 0.169.0 实测能接受 17 MB 的注入。
  - 功能开关从 `--disable`/`--enable` 改为 `-c features.*`。旧写法遇到 CLI 删掉的功能名会在启动时退出。
  - 关闭默认开启的 `unbounded_connection_retries`，`willRetry` 通知不再重置空闲期限。以前断网时请求要挂到总期限才失败。
  - `contextWindowExceeded`、`badRequest` 及两类内容政策失败改为 400 `invalid_config`，不计入熔断；`serverOverloaded` 改为可重试的 `backpressure`。
  - 默认测试与竞态检查通过，新测试在旧实现上失败；真实 0.169.0 用新参数启动，`config/read` 确认各功能开关生效。真实长回答、断网与上下文溢出尚未验收。见 [Codex 包说明](codexcli/README.md#失败分类)。

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
- 2026-11-21：Ubuntu 上 `install.sh` 未指定 `--user` 时改为以执行安装的账户运行（`sudo` 调用者，否则当前账户，可能是 root），不再默认创建 `aiserveweave`；已安装节点与 `--upgrade` 沿用已保存的账户。只做了语法检查，真实安装未验证。
- 2026-11-01：Ubuntu 上允许 `--user root`（`install.sh` 与 `agent.sh` 都支持），安装时打印一行警告；默认服务账户仍是 `aiserveweave`。systemd 单元不变（无额外加固），root 下 Agent 及其启动的 CLI 拥有 root 权限。默认测试只在 Linux 上覆盖暂存树，真实 root 服务启动未验证。
- 2026-11-01 一行命令安装：[install.sh](../../deploy/agent/install.sh) 与 [install.ps1](../../deploy/agent/install.ps1) 下载并校验制品、兑换一次性码，再调用 `agent.sh`/`agent.ps1` 安装；`agent.ps1` 同步支持本地提交号版本。Agent 二进制与隧道协议无变化。使用见 [一行命令安装](../../deploy/agent/README.md#一行命令安装)，真实系统未验收。

## 用量的计量项明细（2026-11-16）

为配合[按 token 计价](../../docs/billing-token-pricing.md)，Agent 在上游报告时把缓存与推理用量分开上报，经隧道 `Usage` 的 `details = 5`（计量项名称 → 数量）与 `attributes = 6` 送到 Gateway；`PromptTokens` 仍计入全部输入，明细只是它的一部分。

- **Claude 风格与 Cursor 的 CLI 输出**（`cliagent`）：`cache_read_input_tokens` / `cacheReadTokens` 记为 `input.cache_read`，`cache_creation_input_tokens` / `cacheWriteTokens` 记为 `input.cache_write`，`cache_creation.ephemeral_1h_input_tokens` 记为其中的 `input.cache_write.1h`。
- **Codex**（`codexcli`）：`thread/tokenUsage/updated` 的 `cachedInputTokens` 记为 `input.cache_read`。
- **托管供应商**：经 `common/runtime` 的 Anthropic 与 OpenAI adapter 填写，与 Gateway 直连上游同一套。
- Claude Code 原生 Messages 桥接只转发原始事件，用量由 Gateway 解析，见 [Gateway STATUS「按租户/模型的持久化用量账本」](../aiServeWeaveGateway/STATUS.md#按租户模型的持久化用量账本statusmd-的-p2)。

旧版 Agent 不上报明细，缓存 token 按普通输入价收费，与升级前相同；升级顺序是控制面、Gateway、Agent。字段名按各 CLI 现有输出与协议文档实现，未用真实 CLI 实机验收（尤其是 1 小时缓存的字段）。

## 尚未交付的范围

- **资源感知调度**：Gateway 已按 Hello 上报的静态 GPU 显存做准入过滤。实时 CPU/内存采集、心跳上报、Agent 本机准入（资源闸门 + 分类信号量）与 Gateway 资源紧张降级已于 2026-11-14 交付，默认测试与 Linux 容器（cgroup 256 MiB）、macOS 本机实测采样通过，真实负载与 Windows 实机未验收。GPU 实时利用率、cgroup CPU 配额换算未做。
- **Agent 自动升级**：Agent 侧 CHECK/UPGRADE 已交付；ROLLBACK、Gateway 下发入口、控制面聚合与 Console 灰度可见性未做，因此目前无法从平台侧触发升级。
- **ComfyUI Managed**：Agent 本地生命周期、Gateway 动作与 409 检查、控制面聚合、自定义节点、Console 页面均已交付；单机多实例未做；排空检查只看得到本副本的 Job，且不会自动等排空完成再执行。
- **模型分发**：HTTP/Ollama 条目、按名字触发、并发、跨重启账本、磁盘余量与 Console 可见性均已交付。
- **托管供应商远程下发**：设置页只读展示远程运行时、本地「忽略某个远程 ID」未做；要停用某个远程运行时，在 Console 中删除，或在本地声明同 ID 覆盖。
- **其他**：`workflow/` 是占位包；Claude Code 不自动发现；本机自动发现不等于进程生命周期管理；已有隧道文件转发不等于对象存储或原节点离线后的文件可用性。

跨服务任务与验收见 [根 STATUS](../../STATUS.md)。
