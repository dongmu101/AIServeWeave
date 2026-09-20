# aiserveweave-agent

节点侧 Agent 主动出站连接 Gateway，通过 mTLS 隧道执行运行时语义请求，不监听公网推理入口。协议、连接状态机、配置下发与验收记录见 [隧道 README](tunnel/README.md)，启动和部署见 [部署说明](../../deploy/README.md)。

## 当前实现

- `main.go` 装配运行时管理器、后端工厂、自动发现、隧道和可选指标监听；`-version` 输出构建版本。
- `agentconfig/`：`-config` 指向一份 YAML 文件，覆盖远程 Gateway 连接（`gateway:` 段，对应 `-gateway`/`-registry`/证书三件套/`-allowed-runtimes`/`-labels`/`-max-gateways`）与本地运行时声明（`runtimes:` 列表，任意 `ollama`/`vllm`/`sglang`/`comfyui` 种类，是 `-ollama-url` 之外的补充而非替代）以及 `auto_discover`/`metrics_addr`/`log_level`。命令行显式传入的 flag 始终优先于文件里的同名设置（`main.go` 的 `applyConfig`），文件只填补 flag 留在默认值上的空白，两者可以同时使用。**不传 `-config` 时，`main.go` 的 `resolveConfigPath` 会在当前工作目录下找 `config.yaml`，存在就加载，不存在就照常只靠 flag 运行**——一次全新检出或全新部署，行为与这个功能从未上线时完全一样；文件存在但解析失败仍然是致命错误，不会被当成"没找到"悄悄跳过。`-config-ui`（见下）不带 `-config` 时也落到同一个默认路径。YAML 经由 `github.com/spf13/viper` 解码——这是 Agent 依赖红线上一次记录在案的例外，理由见 AGENTS.md 对应小节；这份文件是给人手改或由 `configui` 生成的，因此与本代码库其余纯 JSON 本地清单（`modelpull`/`agentupgrade`）不同源。
- `configui/`：`-config-ui`（开关，默认关闭）在 `-config-ui-addr`（默认 `127.0.0.1:8899`，不用另外指定端口）上提供一个本地 HTML 设置页面，把上面这份 YAML 的字段变成表单，保存后写回 `-config` 指向的文件。`-config-ui-addr` 只是地址覆盖项，单独传它不会开启任何东西——必须同时传 `-config-ui` 才生效，这样一次什么都没传的普通启动绝不会意外带起这个页面。这是与正常运行互斥的独立模式——启动后只提供这个页面，不建隧道、不起运行时，保存后需要重启不带 `-config-ui` 的 Agent 才会生效。`-config-ui-addr` 只接受回环地址（`127.0.0.1`/`::1`/`localhost`），传入其他地址直接拒绝启动——这个页面能让 Agent 改去连接不同的 Gateway，绝不能暴露到公网，见 AGENTS.md「Agent 只主动出站建连，从不监听公网端口」。**`main.go` 的 `shouldAutoOpenConfigUI` 另外兜了一条底：命令行完全没提 `-config-ui`、`-config-ui-addr`、`-config`、`-gateway` 中任何一个，且当前目录也没有 `config.yaml` 时，会自动以这个默认端口打开设置页面**，而不是悄悄以隧道关闭的状态跑起来——这正是一次全新检出、什么都没配置过时最有用的行为。这四个 flag 里任何一个被显式传入（哪怕是 `-gateway=""`）都会关掉这条自动路径，视为操作者已经在明确表达意图。
- `localdiscovery/` 默认探测本机 Ollama/vLLM，可用 `-auto-discover=false` 关闭；`-ollama-url` 可显式注册 Ollama。
- `tunnel/` 已实现身份注册/续期、多副本连接、槽池、心跳、运行时配置应用和流式请求/产物转发。执行 runtime 必须命中本地白名单。
- 后端适配在共享的 `common/runtime`，支持 Ollama、vLLM、SGLang 和 ComfyUI；ComfyUI 适配位于 `common/runtime/workflow/comfyui`。ComfyUI 适配器新增 `comfyui_queue_running`/`comfyui_queue_pending`（`runtime_id` 标签）两个队列深度量表（STATUS.md 的 A06），复用 `Status`/`Cancel` 本就会发起的 `GET /queue`，不产生额外请求；与隧道共用同一个 `metrics.Registry`，未配置 `-metrics-addr` 时安全丢弃。ComfyUI 的失败历史同时被分类为是否显存/内存耗尽（`runtime.WorkflowStatus.OutOfMemory`），供 Gateway 侧统计使用，见 [Gateway README](../aiServeWeaveGateway/README.md) 的相应小节。
- `workflow/` 仍是占位包。受控模板目录与输入绑定已在 Gateway 的 `workflow/` 实现，不能据此目录重复建设。
- `hostresources/`（STATUS.md P2 待办第五条的第一项后续任务）在 Hello 握手前探测本节点的 CPU 核数、内存总量、GPU 数量与显存总量，写入 `ClientConfig.Resources`。探测一律经 `os/exec` 调用系统自带工具（Linux 读 `/proc/meminfo`、`nvidia-smi`；macOS 用 `sysctl` 取内存，GPU 显存无通用查询手段留空），不引入 `gopsutil`/NVML 一类新依赖，守住 AGENTS.md 的 Agent 依赖红线。这是**尽力而为的静态容量声明**：探测失败的字段留零值、不阻塞启动；Gateway 侧调度器尚不消费这份数据（见 [P2 设计文档](../../docs/superpowers/specs/2026-09-15-p2-resource-aware-scheduling-design.md) 第七、八节的后续任务拆分）。
- `comfyuimanaged/`（STATUS.md P2「ComfyUI Managed Docker 部署」待办子任务一，设计文档见 [`2026-09-18-p2-comfyui-managed-docker-design.md`](../../docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-design.md)）：Agent 本地、单容器的 Docker 生命周期管理器，shell out 到 `docker` CLI（不新增 SDK 依赖，守住 Agent 依赖红线），由 `-comfyui-managed-image` 等一组本地 flag 驱动，从不接受 Gateway/控制面下发。`Launcher.Start` 幂等——已在运行的同名容器直接接管，不存在或已退出的容器才创建/重建，镜像本地缺失时先 `docker pull`；镜像必须携带显式、非 `latest` 的 tag，容器端口固定绑定 `127.0.0.1`。容器起来、`Launcher.WaitReady` 确认端口能建立 TCP 连接后，直接调用与 External 模式完全相同的 `manager.Add(...)`——ComfyUI 身份/能力校验交给 `comfyui.Runtime` 既有的 Probe/Discover，本包从不重新实现 ComfyUI 协议。配置了 `-comfyui-managed-image` 但启动或校验失败会让 Agent 启动失败，与 `-ollama-url` 同样的失败语义。**子任务二（`supervisor.go`，设计文档见 [`2026-09-18-p2-comfyui-managed-docker-subtask2-design.md`](../../docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-subtask2-design.md)）**：`Supervisor` 让 Gateway 可以经隧道 Control 流对这一个本地已声明的实例远程触发 START/STOP/RESTART，并把容器生命周期状态（pending/starting/running/stopped/failed）回报给 Gateway；动作不携带容器名——Agent 今天最多管理一个 Managed 实例，动作总是针对 Agent 本地已声明的那一个 Spec 执行。镜像、GPU 设备、挂载路径依旧 100% 来自本地 flag，从不接受隧道下发——`tunnel.proto` 头部「不得表达 run this command」的红线迫使"下发部署规格"收窄为"按动作触发本地已声明的实例"，运维想换镜像版本仍要先改本机 flag 再触发 RESTART。RESTART 是 STOP 后接 START，不检查有没有正在跑的 Job、也不对比镜像版本是否真的变了——排空升级编排是子任务三的范围。**子任务四（`customnodes.go`，设计文档见 [`2026-09-19-p2-comfyui-managed-docker-subtask4-design.md`](../../docs/superpowers/specs/2026-09-19-p2-comfyui-managed-docker-subtask4-design.md)）**：Gateway 可以按名字触发 Agent 安装本地 `-comfyui-managed-custom-nodes` 允许列表（`name=repourl@ref,...`）里已声明的一个自定义节点——安装源（仓库 URL + 固定 ref）100% 留在 Agent 本地，触发消息只带一个名字，同一条 `tunnel.proto`「不得表达 fetch this URL」红线。`Launcher.InstallCustomNode` 经 `docker exec` 跑 git clone/fetch+checkout 把代码放进容器的 `-comfyui-managed-custom-nodes-dir`（默认 `/comfyui/custom_nodes`，因镜像而异，未做路径自动发现），成功后写入版本标记文件；`Launcher.ListCustomNodes` 读回已安装节点名+版本，随 `Supervisor.Snapshot` 并入上报。安装成功不自动触发重启——ComfyUI 只在进程启动时扫描该目录，新节点生效仍需运维之后另外触发 RESTART。**范围边界**：仍是单容器（一台机器多个 Managed 实例、以及无控制面跨副本路由聚合，都留给后续子任务）、无审计日志（安装动作只写结构化日志，真正的审计经由控制面转发时落 `audit_logs`）——均是设计文档拆分出的后续子任务；WebSocket 就绪检查未实现，`WaitReady` 只确认 TCP 可连通；不装 pip 依赖，只克隆代码本身。默认测试套件（假 `docker` 脚本）之外另有 `AISW_DOCKER_LIVE_TEST=1` 门控的真实 Docker 往返测试，验证的是容器生命周期管理机制本身，不是 ComfyUI 协议或 GPU 直通——后两者需要 Linux NVIDIA 环境，与 A06/P10 已确认的"本机无真实 ComfyUI/GPU 环境"同一边界。
- `modelpull/`（STATUS.md P2「模型分发」待办，子任务一见 [P2 模型分发设计文档](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md)，子任务二见 [子任务二设计文档](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md)，子任务三见 [子任务三设计文档](../../docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md)）：一个纯 Agent 本地、标准库实现（`net/http` + `crypto/sha256`，不新增依赖）的清单驱动下载器，覆盖校验和、断点续传（HTTP Range 到 `<TargetPath>.part`，完成后原子改名）、来源白名单四项要求。`-model-pull-manifest` 指向一份本地 JSON 清单（`[]modelpull.Spec`），`-model-pull-allowlist`/`-model-pull-quota-bytes` 控制白名单与预算；三者均为纯本地 flag，从不接受 Gateway 或控制面下发。清单为空（默认）时功能整体关闭。来源白名单默认方向是「空=拒绝全部」，刻意不照搬 `AllowedRuntimes` 的「空=放行」，理由见子任务一设计文档第三节。**子任务一**：`RunManifest` 是同步、一次性处理整份清单的下载器，字节配额是单次调用共享的预算。**子任务二（`puller.go`）**：`Puller` 让 Gateway 可以经隧道 Control 流按名字触发一次按需拉取——只能按名字，从不是 URL，Agent 对着自己本地清单解析；启动时仍自动对整份清单触发一次（保留子任务一的既有行为），运行期间在后台以最多一个 worker 顺序处理排队的名字，逐名字的进度（`BytesDownloaded`/`BytesTotal`/状态）经 `ModelPullReport` 回报给 Gateway，失败原因是封闭枚举（`common/modelpullstatus.FailureReason`），从不携带原始错误文本——`SourceURL` 可能是带签名 token 的预签名 URL，这条边界防止它经由 Go 网络错误文本泄漏出 Agent。字节配额语义从"单次 `RunManifest` 调用"扩展为"单次 worker session"：一批一起触发的名字共享一份预算，session 处理完（队列清空）后下一次触发拿到全新预算。整个 Puller 的工作跨越隧道连接的生命周期，Agent 关闭时靠取消 ctx 中止在途下载，留下的 `.part` 文件与进程被杀死时完全同一种可续传状态。**子任务三（`ollamapull.go`）已交付**：清单条目新增 `Kind: "ollama"`（`Spec.Kind`，零值 `KindHTTP` 是子任务一/二的通用下载器），此类条目的 `Name` 直接是 Ollama 模型 tag，`SourceURL`/`SHA256`/`TargetPath` 必须留空；`pullOllama` 调用 Ollama 服务器自己的 `POST /api/pull`（NDJSON 流式响应），不重新实现其 manifest/blob 存储格式，也不 shell out 到 `ollama` CLI——核实并偏离了子任务一设计文档原先设想的 `os/exec` 路径，理由见子任务三设计文档第一节。目标服务器地址复用已有的 `-ollama-url`（不新增 flag），为空时新增的 `ReasonOllamaUnconfigured` 拒绝该条目。`-model-pull-allowlist`/`-model-pull-quota-bytes` 均不适用于 `Kind: "ollama"` 条目（无 `SourceURL` 可比对，字节也不是 Agent 自己写入的），子任务二已有的 Gateway 触发/状态回传/控制面转发/Console 可见性对 Ollama 原生条目天然可用，不需要为此改动 Gateway 或控制面。**子任务四（`ledger.go`，设计文档见 [`2026-09-19-p2-model-distribution-subtask4-design.md`](../../docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md)）已交付**：`-model-pull-max-concurrency`（默认 1，保持子任务一/二/三既有的顺序行为）让 `RunManifest`/`Puller` 同一时间处理多个 Spec，单个 Spec 自己的下载仍是一条 HTTP 请求、不做分片；`Config.QuotaBytes` 的共享预算随之改为 `atomic.Int64`，用 CAS 循环防止并发下载合计透支。`Ledger`（`-model-pull-ledger-path`/`-model-pull-ledger-quota-bytes`/`-model-pull-ledger-period`）把字节预算持久化到一个 JSON 文件，跨 Agent 重启累计，与每次调用都重新计满的 `QuotaBytes` 是两把独立生效的闸门；`Period` 到期前拒绝、到期后账本自动清零重新计算。`-model-pull-disk-free-margin-bytes`（配合新增的 `hostresources.DiskFreeBytes`，标准库 `syscall.Statfs`，不新增依赖）是配额之外的二次防线——目标文件系统剩余空间低于阈值即中止，尽力而为，不是强一致的资源预留。`Kind: "ollama"` 条目不受三者中任何一个约束，理由与子任务三记录的一致。
- `agentupgrade/`（STATUS.md P2「Agent 自动升级」，设计文档见 [`2026-09-19-p2-agent-auto-upgrade-design.md`](../../docs/superpowers/specs/2026-09-19-p2-agent-auto-upgrade-design.md)）：一份本地已签名的已知 Agent 版本清单，以及一个 `Checker`——按需把清单与 Agent 自己运行版本（`main.version`）比较，并执行 UPGRADE 的完整链路。`-agent-upgrade-manifest` 指向一份本地 JSON `Manifest`（`{entries: []agentupgrade.Entry, signature}`，`Entry` 结构类比 `modelpull.Spec`），从不接受 Gateway 或控制面下发；清单为空、或节点构建时没带 `-ldflags="-X main.agentUpgradePublicKeyHex=..."`，功能整体关闭。`Manifest.Signature` 是对整组 `(Version, SHA256)` 的一次 Ed25519 签名——不是每条各签一次——这样一个条目被悄悄删除或哈希被替换都会让签名校验失败，而不只是造成一次下载不匹配；`agentupgrade.LoadManifest` 在返回任何条目之前先校验这个签名，签名不过或没配置公钥都直接拒绝整份清单。经隧道 Control 流触发（`GatewayControl.agent_upgrade_action`/`AgentControl.agent_upgrade`，详见 [隧道 README](tunnel/README.md) 对应小节）：**子任务一**只实现 CHECK：`Checker.Trigger` 比较清单与当前版本，`HasUpdate` 报告是否存在已知更新。**子任务二**补上了 UPGRADE 的完整链路——`target_version` 未命中清单直接报告 `ReasonUnknownVersion`（不发起网络请求）；命中则下载到 `-agent-upgrade-work-dir`（默认是运行中二进制自己所在目录）、对照清单已验证的 `SHA256` 校验下载结果（`ReasonVerificationFailed`）、通过新增的 `tunnel.Manager.DrainAll` 排空本 Agent 持有的**每一条**隧道连接（方向与 Gateway 主动下发的单连接排空相反，`-agent-upgrade-drain-timeout` 限定等待多久，默认 30s）、最后 `syscall.Exec` 自替换进程镜像（`ReasonExecFailed`）。一次 UPGRADE 进行中收到的第二次触发被静默忽略。ROLLBACK 仍一律报告 `StateFailed`/`ReasonNotImplemented`（子任务三范围）。签名私钥保管在维护者本机离线、二进制托管在 GitHub Releases，是设计文档第六节记录的维护者裁决（详见该文档的后续记录）。

## 配置文件示例

一份完整的 `-config` 文件（字段说明见上面的 `agentconfig/` 小节）：

```yaml
# 远程 Gateway 连接。种子列表只需一个可达即可，连接后由名册接管其余副本。
gateway:
  endpoints:
    - gw-1.example.com:8443
    - gw-2.example.com:8443
  registry: registry.example.com:9443
  node_id: node-mac-01               # 留空则由 Registry 分配
  cert_file: /etc/aisw/agent-cert.pem
  key_file: /etc/aisw/agent-key.pem
  ca_file: /etc/aisw/registry-ca.pem
  bootstrap_token_file: /etc/aisw/bootstrap-token   # 仅首次注册用，用后即删
  allowed_runtimes:                  # 留空表示放行本节点全部已配置的运行时
    - ollama-local
    - vllm-local
  labels:
    region: home-mac
    gpu: 4090
  max_gateways: 4

# 本地推理后端声明，是 -ollama-url 之外的补充：可以声明任意数量、任意受支持种类
# （ollama/vllm/sglang/comfyui）。
runtimes:
  - id: ollama-local
    kind: ollama
    base_url: http://127.0.0.1:11434
  - id: vllm-local
    kind: vllm
    base_url: http://127.0.0.1:8000

auto_discover: true                  # 额外探测本机 Ollama/vLLM 常见端口
auto_discover_interval: 30s
metrics_addr: 127.0.0.1:9091         # 留空关闭 /metrics
log_level: info
```

命令行显式传入的同名 flag 始终覆盖这里的值（如 `-node-id` 会覆盖 `node_id`），文件只填补 flag 留在默认值上的空白，两者可以同时使用，因此不必为了改一个值就整份重写。这份文件既可以手写，也可以用 `-config-ui` 起本地设置页面填表单生成——见上面的 `configui/` 小节。把这份文件存成 `config.yaml` 放在 Agent 的工作目录下，甚至不用传 `-config` 就会被自动读取。

## 尚未交付的范围

运行时配置文件加载已交付（`-config`/`agentconfig`，见上），覆盖远程 Gateway 连接与本地运行时声明；本地设置页面（`-config-ui`/`configui`，见上）随之一并交付。基于资源容量的调度准入过滤和 GPU/内存实时利用率采集仍属规划。**Agent 自动升级**已交付子任务一（本地清单 + CHECK）与子任务二（下载/签名校验/排空/自重启，见上）；回滚（子任务三）、Gateway 独立监听器（子任务四）与控制面聚合/Console 可见性（子任务五）仍属规划，见 [Agent 自动升级设计文档](../../docs/superpowers/specs/2026-09-19-p2-agent-auto-upgrade-design.md) 第五节的子任务拆分。**模型分发**五项子任务（本地清单驱动、Gateway 按名字触发/状态回传、Ollama 原生拉取、并发下载/跨重启配额账本/磁盘二次防线、Console 可见性）均已交付（见上），见 [P2 模型分发设计文档](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-design.md) 第二节、[子任务二设计文档](../../docs/superpowers/specs/2026-09-17-p2-model-distribution-subtask2-design.md) 第六节、[子任务三设计文档](../../docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask3-design.md) 第三节与 [子任务四设计文档](../../docs/superpowers/specs/2026-09-19-p2-model-distribution-subtask4-design.md) 第三节的拆分列表。**ComfyUI Managed 后端**已交付单容器本地生命周期管理（子任务一）、Gateway 按动作触发/状态回传（子任务二）、控制面跨副本路由聚合与排空升级编排（子任务三）、自定义节点按名字安装与对账诊断（子任务四，见上），仅 Console 可见性（子任务五）仍属规划，见 [ComfyUI Managed Docker 设计文档](../../docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-design.md) 第四节、[子任务二设计文档](../../docs/superpowers/specs/2026-09-18-p2-comfyui-managed-docker-subtask2-design.md) 与 [子任务四设计文档](../../docs/superpowers/specs/2026-09-19-p2-comfyui-managed-docker-subtask4-design.md) 已知缺口一节。本机自动发现不等于进程生命周期管理；已有隧道文件转发不等于对象存储或原节点离线后的文件可用性。跨服务任务与验收统一见 [根 STATUS](../../STATUS.md)。
