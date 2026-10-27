# P2「ComfyUI Managed Docker 部署」：现状核实、拆分与子任务一（Agent 本地 Docker 生命周期管理器）

本文档交付根 STATUS.md「P2：后续扩展」第二条（编号 119）：「Linux NVIDIA 上的 ComfyUI Managed Docker 部署、健康检查、排空升级和固定版本的自定义节点管理；先验收 External 链路，再做 Managed。macOS 先支持已启动的 ComfyUI，原生 Python/Desktop 生命周期管理另行评估。」此前从未有过针对这一条的设计文档或代码分析。

范围边界：**本文档核实现状、重新定义"Managed"实际要解决的问题、划安全边界、拆出可独立排期的子任务列表；随后落地风险最低、不依赖真实 GPU 或真实 ComfyUI 镜像的子任务一（Agent 本地单容器 Docker 生命周期管理器）**。其余子任务留给后续 session 排期，STATUS.md 对应条目本次不整体打勾，与「模型分发」「P2 API 兼容边界」条目（部分后续实现任务交付、条目整体仍标 `[ ]`）同一先例。

## 一、现状核实

### 1.1 README 里的描述是规划散文，不是规范

README.md:292-335「托管部署（规划）」一节给出 Managed 模式的目标 YAML 形状（`kind: ComfyUIDeployment`，字段含 `runtime: docker`、`image`、`listenAddress`/`port`、`gpuDevices`、`modelPaths`、`storage`、`customNodes.policy`、`resources.memoryLimit`、`healthCheck`）与七条目标行为：

1. 创建、启动、停止、重启和删除 ComfyUI 实例
2. 固定镜像或版本，不自动追踪 `latest`
3. GPU、端口、模型目录和输入输出目录配置
4. 环境变量只引用平台 Secret，不在部署规格中保存明文
5. 启动后依次检查 `/system_stats`、`/object_info` 和 WebSocket
6. 上报 `pending`/`installing`/`starting`/`ready`/`degraded`/`stopped`/`failed` 状态
7. 升级前检查正在运行的 Job，默认等待排空后再滚动重启
8. 自定义节点采用允许列表并固定版本，安装动作写入审计日志

README.md:322：「Managed 的基础目标环境为 Linux NVIDIA GPU 与 Docker/容器运行时；macOS 的 External 接入与原生环境生命周期管理是不同的支持范围。」这与 STATUS.md 条目原文一致，不是本文档的新推导。这是一份目标形状草图，不是接口或协议规范——没有一处代码引用或实现这份 YAML。

### 1.2 ComfyUI 运行时适配器是纯 External-only，零生命周期管理概念

包路径是 `common/runtime/workflow/comfyui/`（不是 `common/runtime/comfyui`）。包注释（`client.go:1-6`）：

```go
// Package comfyui adapts an already-running ComfyUI server to the
// runtime.WorkflowRuntime contract: submitting API Format workflows,
// following their progress over ComfyUI's instance-wide WebSocket,
// reconciling final state against History, cancelling within safe limits,
// and streaming output artifacts.
```

`Runtime` 结构体（`runtime.go:42-62`）只持有 HTTP 客户端、事件多路复用器、限流器、指标记录器——没有 PID、没有容器句柄、没有任何进程相关字段。它的方法集是 `Probe`/`Health`/`Discover`/`Submit`/`Subscribe`/`Status`/`Cancel`/`OpenArtifact`/`UploadInput`/`Artifacts`/`Close`——全部是"对一个假定已经在跑的服务器发请求"，全仓库这个包里**没有 Start/Stop/Install/Upgrade 方法**。README 第七条要求的"生命周期管理"在代码里完全不存在。

### 1.3 今天注册一个 ComfyUI 实例只有两条路径，都不启动进程

- **CLI flag**：Ollama 有 `-ollama-url`/`-ollama-id`（`service/aiServeWeaveAgent/main.go`），**ComfyUI 没有等价 flag**——`main.go` 里 `comfyui` 只出现在 `runtime.KindComfyUI: comfyui.New` 的工厂注册和 `WSDialer` 装配处。
- **控制面经隧道下发**：`service/aiServeWeaveAgent/tunnel/control.go:328-380` 的 `applyConfig`，Gateway 发 `tunnelv1.RuntimeConfig{Action: ADD/REPLACE/REMOVE}`，Agent 解码 `Kind`+`BaseURL` 后先过本地白名单 `c.runtimeAllowed(cfg.ID)`（"即使 Gateway 被攻破也不能让 Agent 访问未声明地址"），再调用 `Manager.Add`/`Manager.Replace`。这仍然只是"把一个地址告诉 Agent"，不是"启动一个进程"。

### 1.4 `localdiscovery` 不探测 ComfyUI

`service/aiServeWeaveAgent/localdiscovery/localdiscovery.go` 的 `DefaultCandidates()`（第 39-46 行）硬编码只有两条：`KindOllama` 探测 `127.0.0.1:11434`，`KindVLLM` 探测 `127.0.0.1:8000`。ComfyUI 默认端口 8188 不在其中。External 模式今天完全依赖 1.3 节的两条路径，没有自动发现。

### 1.5 NVIDIA GPU 探测已经存在，可直接复用

`service/aiServeWeaveAgent/hostresources/hostresources.go` 的 `gpuInventory()`（第 121-130 行）在 Linux 上 shell out 到：

```
nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits
```

经 `runCommand`（第 155-167 行，`exec.CommandContext` + 固定 3 秒超时）执行，`parseNvidiaSMIMemoryTotal` 解析出 GPU 数量与总显存，填进 `tunnelv1.NodeResources`。这是"这台机器上到底有没有 NVIDIA GPU"的现成信号，Managed 模式判断"能不能 `--gpus`"时可以复用同一条 `nvidia-smi` 探测，而不是重新发明。子任务一不改这个包，只是记录这条可复用先例。

### 1.6 Agent 依赖红线与仅有的 os/exec 先例

AGENTS.md：「Agent 与 Registry 的直接依赖只有 gRPC、protobuf 与 `coder/websocket`，这条线要守住。」全仓库 `grep -rln "os/exec" service/aiServeWeaveAgent/` 只命中 `hostresources/hostresources.go`（用法本身）和它的 README 描述——`modelpull/` 明确走标准库 `net/http`/`crypto/sha256`，**不** shell out。`hostresources.runCommand` 的文档注释（第 155-156 行）说明它的安全假设：「a fixed command line (never built from external input, so there is nothing to inject)」——固定命令行，参数从不来自外部输入。

Managed Docker launcher 必须遵循同一条依赖红线：**只能 shell out 到 `docker` CLI**，不能引入任何 Docker SDK/客户端库。但它和 `hostresources` 的信任假设不同——`docker run` 的参数（镜像名、挂载路径、GPU 设备号、端口）来自运维在本节点写的 flag 配置，不是"零参数、不可能被注入"的固定命令行，而是"参数从本机可信配置构造"，与模型分发设计文档第三节推导的"运维手动批准的本机配置"同一信任级别（见 [`2026-09-17-p2-model-distribution-design.md`](2026-09-17-p2-model-distribution-design.md) 第三节）。**全仓库此前没有 Agent 启动/监督长驻子进程或容器的先例——这是全新领域**，不是照抄 `hostresources` 就能完成的扩展。

### 1.7 「先验收 External 链路」这个前置条件今天没有满足，本环境也无法满足

STATUS.md:15（M0 里程碑退出条件）：「保留真实 Ollama/mTLS/流式验证依据，补可复现的真实 ComfyUI 链路验证」——这项至今未关闭。STATUS.md:104/111（A06）如实记录：「本次会话从未针对真实 ComfyUI 服务器运行过这个测试」「本机开发环境既无 GPU 也无可达的 ComfyUI 服务」。这条待办自己写的先决条件（先验收 External，再做 Managed）字面上还没有被满足。

本文档额外核实：本次会话所在环境（macOS/Darwin）可用 Docker daemon（`docker version`/`docker info` 均成功执行），但没有 NVIDIA GPU、也没有可达的真实 ComfyUI 服务器。这意味着：

- 子任务一涉及的"Docker 容器生命周期管理机制本身"（启动、状态查询、停止一个容器）**可以**在本环境用真实 Docker 验证，不需要伪造。
- 但"这个容器里跑的是真的 ComfyUI"以及"GPU 直通" 两件事，在本环境**结构性不可能**验证——与 A06/P10 已经确认的"本机无真实 ComfyUI/GPU 环境"同一约束，不是本任务新引入的缺口，也不因为有 Docker 而改变。

因此本文档的立场是：**不因为"External 链路验证还没做完"而搁置 Managed 的设计与不依赖真实 GPU/ComfyUI 镜像的那部分实现**——两者是可以并行推进的独立工作（Managed 的容器编排逻辑不依赖 ComfyUI 协议本身是否已验证），但如实记录这个前置条件尚未关闭，子任务一交付说明里会重申这一点，不假装它已经满足。

### 1.8 自定义节点声明已有结构，但与运行实例的对账完全空缺

`common/workflowtemplate/workflowtemplate.go` 第 173-202 行：

```go
// NodeDependency names one ComfyUI custom node package a template's graph
// relies on.
type NodeDependency struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type Dependencies struct {
	CustomNodes []NodeDependency  `json:"custom_nodes,omitempty"`
	Models      []ModelDependency `json:"models,omitempty"`
}
```

校验（第 246-332 行）是纯结构性的（非空、去重、数量上限），**从不**与一个连接中的 ComfyUI 实例实际安装了什么做交叉核对——包注释原话「no node currently reports such a thing」。README.md:357：「调度前需要校验目标 Deployment 是否拥有模板要求的模型和节点类型」——这句同样是规划散文，不是已实现的行为（P03 落地时 STATUS.md 已明确记录这一校验只做结构性检查，不与已连接节点实际上报的已装能力交叉核对）。

结论：工作流模板可以*声明*需要哪个自定义节点+版本，但没有任何代码会把这份声明变成"给一个 Managed ComfyUI 实例安装它"的动作，也没有任何代码上报"这个实例现在实际装了哪些自定义节点"。这是子任务四要补的两块。

## 二、场景与目标定义

### 2.1 关键设计洞察：Managed 不需要改动 `comfyui` 运行时适配器一行代码

1.2 节确认 `comfyui.Runtime` 已经是"假设自己在跟一个已经在跑的服务器说话"；`common/runtime/manager.go` 的 `Add`（第 97-99 行文档注释）已经在实例对调用方可见之前**同步**跑 Probe（GET `/system_stats`）然后 Discover（GET `/object_info`），Probe 或 Discover 失败则整个 `Add` 失败、不留下"registering"占位符。这恰好覆盖了 README 第五条目标行为「启动后依次检查 `/system_stats`、`/object_info` 和 WebSocket」三步里的前两步，**不需要 Managed launcher 自己重新实现一遍 ComfyUI 身份校验**。

因此 Managed 真正要新增的能力，是"怎么让一个能答 `/system_stats` 的 ComfyUI 服务器出现在 `127.0.0.1:<port>` 上"这一步，以及围绕它的创建/启动/停止/重启/删除、镜像版本管理、GPU/挂载配置、排空升级编排、自定义节点管理——这些全部是**容器生命周期管理问题**，不是推理协议问题。子任务一的产出应该是"External 注册路径前面多一步自动化"：容器起来、端口能连通之后，直接调用现有的 `manager.Add(ctx, runtime.Config{Kind: KindComfyUI, BaseURL: "http://127.0.0.1:<port>"})`，把剩下的身份/能力校验完全交给已经存在、已经测试过的代码。

### 2.2 WebSocket 检查是一处如实记录、本次不补的缺口

README 第五条目标行为的第三步"检查 WebSocket"，今天没有独立的探测方法——`comfyui.Runtime`/`eventMux`（`events.go`）只在真正提交任务、调用 `Subscribe` 时才建立 WS 连接，`Probe`/`Discover` 都不主动建立 WS。子任务一的 `WaitReady`（见四.5.1 节）只确认"端口上有 TCP/HTTP 服务在应答"，不建立 WS 连接去验证协议握手。这个缺口留给子任务二/三评估是否值得单独加一次 WS 探测（例如复用 `comfyui.Runtime` 已有的 WS dialer 做一次连接即断开的探针），不在本次范围内实现。

### 2.3 目标场景

运维想在一台装了 Docker 与 NVIDIA Container Toolkit 的 Linux GPU 机器上跑 ComfyUI，希望：

- 不用先手动 `docker run` 好 ComfyUI 再启动 Agent——Agent 自己按本地配置把容器带起来。
- 镜像版本固定，Agent 重启不会意外换成别的版本（README 第二条）。
- Agent 重启、容器已经在跑时不重复创建（幂等）。
- 之后一切调度/推理行为与 External 模式完全一致，因为背后是同一个 `comfyui.Runtime`。

## 三、依赖与安全边界推导

现状核实（1.6/1.7 节）发现 AGENTS.md 与现有 README 对"Agent 本地执行/管理容器"这类新能力完全沉默——不是本文档要遵守某条已有规则，而是要**从现有红线的精神出发，显式推导出新的边界**：

- **实现手段**：只能 shell out 到 `docker` CLI（`os/exec`），不引入 Docker SDK/客户端库，理由与 `hostresources` 选择 `nvidia-smi`/`sysctl` 而非 `gopsutil`/NVML binding 完全相同（AGENTS.md 依赖红线）。
- **参数来源与信任级别**：子任务一阶段，镜像名、容器名、端口、GPU 设备号、挂载路径全部来自本节点 flag 配置，是"运维手动批准的本机配置"，与模型分发设计文档第三节推导的信任级别相同——**不是**网络可达就默认可信的场景，不需要 A04 那种 SSRF 级别防护。子任务二把触发权交给 Gateway/控制面之后，这个信任级别的判断需要重新评估（模型分发设计文档第三节已经记录过同一类升级路径，这里同理，记入五.2 已知缺口）。
- **镜像必须固定版本**：`Spec.Image` 必须携带显式 tag，`latest` 被拒绝——直接对应 README 第二条目标行为「固定镜像或版本，不自动追踪 `latest`」，在参数校验阶段（而不是运行时）就把这条规则坐实。
- **端口只能绑定回环地址**：容器的宿主机端口映射固定为 `127.0.0.1:<port>:8188`，不绑定 `0.0.0.0`。AGENTS.md 安全红线「Agent 只主动出站建连，从不监听公网端口」字面上说的是 Agent 自己的监听器，但 Agent 主动创建一个会监听端口的子进程/容器时，同一个安全预期应当延伸过去——不能让 Managed 模式意外地把 ComfyUI 暴露到公网。本文档把这一点显式定为子任务一的强制约束（`buildRunArgs` 不接受可配置的绑定地址），而不是含糊地留到以后。
- **审计缺口如实记录，不在子任务一补**：README 第八条要求「安装动作写入审计日志」，但 Agent 侧从来没有审计日志机制（`audit_logs` 表只存在于控制面）。子任务一是纯本地操作，只用结构化日志（`slog`）记录状态转换；真正的"写入审计日志"依赖子任务二把触发动作挪到控制面（沿用 P01/模型拉取转发"平台会话触发 → 控制面写 `audit_logs`"的既有先例）之后才有意义，记入五.2 已知缺口。

## 四、子任务拆分

把 STATUS.md 一句话拆成五个可独立排期的子任务：

1. **子任务一（本轮交付）**：Agent 本地、单容器 Docker 生命周期管理器——本地静态配置（flag）驱动创建/启动/查询状态/停止一个 ComfyUI 容器，固定镜像版本，loopback-only 端口，幂等启动，就绪后接入既有 `runtime.Manager.Add` 完成身份校验。不涉及隧道协议或控制面下发，不涉及排空升级，不涉及自定义节点管理，不涉及 Console 可见性。
2. **子任务二（未来）**：控制面下发部署规格 + Agent 状态回传。可参考 `tunnel.proto` 的 `RuntimeConfig` ADD/REPLACE/REMOVE 先例，或模型拉取子任务二"节点级 Control 流按名字触发 + 状态报告"的先例；需要新的部署规格契约（类似 README 里的 `ComfyUIDeployment` YAML，但落地为 proto/Go 类型）。这个子任务落地后，子任务一的"本机可信配置"信任假设需要重新评估（三节已预告）。
3. **子任务三（未来）**：排空升级编排——升级前检查正在运行的 Job（复用 P0 已有的 job 路由绑定/查询能力判断"这个节点上有没有在跑的任务"），默认等待排空后再滚动重启到新镜像版本（README 第七条）。依赖子任务二先有"下发新版本"的入口。
4. **子任务四（未来）**：自定义节点允许列表 + 固定版本安装 + 审计日志，并把 `workflowtemplate.Dependencies` 声明的节点名+版本与一个 Managed 实例实际安装的节点对账（1.8 节确认今天完全缺失）。依赖子任务二（触发权在控制面，安装动作才能落审计日志）。
5. **子任务五（未来）**：Console 部署管理可见性/UI。依赖子任务二先把状态回传到控制面。

依赖关系：子任务一完全自洽，不依赖其余任何一个；子任务三、四都需要子任务二先把触发权交给控制面（否则"滚动升级"和"装自定义节点"只是运维手动重启同一个 Agent 进程改本地 flag，跟直接操作 Docker 没有本质区别，够不上"下发/审计"的产品意义）；子任务五依赖子任务二。这与模型分发设计文档给出的子任务拆分是同一种"先补最基础、风险最低的一层"的排期思路。

## 五、子任务一详细设计

### 5.1 新包：`service/aiServeWeaveAgent/comfyuimanaged/`

```go
// Spec describes one ComfyUI container this Agent manages via the docker
// CLI. All fields come from this node's own flags (STATUS.md's P2 ComfyUI
// Managed Docker deployment, subtask one) — Start never accepts a spec
// pushed by the Gateway or control plane.
type Spec struct {
	ContainerName string            // docker container name; must be unique on this host
	Image         string            // must carry an explicit, non-"latest" tag
	Port          int               // host binds 127.0.0.1:Port -> the container's 8188
	GPUDevices    []string          // e.g. ["0"]; empty omits --gpus entirely
	ModelPaths    map[string]string // container path -> host path, mounted read-only
	StoragePaths  map[string]string // container path -> host path, mounted read-write
	MemoryLimit   string            // docker --memory value (e.g. "32g"); empty is unlimited
	Command       []string          // optional entrypoint override; nil uses the image's own
}

// State is the closed set of container-lifecycle states Status can report.
// It is a subset of the seven states README.md's Managed deployment sketch
// names (pending/installing/starting/ready/degraded/stopped/failed): this
// package never returns "installing" (see 5.1's note on Start) or
// "degraded"/"ready", which require combining this package's container-level
// view with runtime.HealthReport from an already-registered comfyui.Runtime —
// out of scope until subtask two reports state anywhere beyond this node's
// own logs.
type State string

const (
	StatePending  State = "pending"  // no container by this name exists yet
	StateStarting State = "starting" // container created/restarting, port not yet answering
	StateRunning  State = "running"  // container process is running
	StateStopped  State = "stopped"  // container exited cleanly (exit code 0)
	StateFailed   State = "failed"   // container exited non-zero, or docker itself errored
)

// Launcher manages one Spec's container via the docker CLI. It is the only
// thing in this package that touches os/exec.
type Launcher struct { /* unexported: docker binary path, runtime.Clock */ }

func NewLauncher(clock runtime.Clock, logger *slog.Logger) *Launcher

// Start makes the container match spec: it adopts an already-running
// container of the same name without recreating it (idempotent across Agent
// restarts), pulls the image first if not already present locally, removes
// and recreates a stopped/failed container so the spec always wins, then
// waits (bounded, via WaitReady) for the container to reach StateRunning.
// It never waits for the ComfyUI HTTP port to answer — see WaitReady.
func (l *Launcher) Start(ctx context.Context, spec Spec) error

// Stop stops and removes the named container. Stopping an already-stopped
// or nonexistent container is not an error.
func (l *Launcher) Stop(ctx context.Context, containerName string) error

// Status reports the container's current State via `docker inspect`. A
// nonexistent container reports StatePending, not an error — the same
// "declared but not yet realized" reading Spec gets before Start is ever
// called.
func (l *Launcher) Status(ctx context.Context, containerName string) (State, error)

// WaitReady polls 127.0.0.1:spec.Port until a TCP connection succeeds or
// timeout elapses. It deliberately does not speak HTTP or ComfyUI's wire
// protocol — that verification belongs to runtime.Manager.Add's existing
// Probe/Discover call, which the caller (main.go) runs immediately after
// WaitReady succeeds. Duplicating that check here would be re-implementing
// code that already exists and is already tested.
func (l *Launcher) WaitReady(ctx context.Context, spec Spec, timeout time.Duration) error
```

要点：

- **镜像固定版本校验**：`Spec.Image` 没有 `:` 分隔的显式 tag，或 tag 为 `latest`，`Start` 直接拒绝，不发任何 docker 命令。
- **幂等启动**：`Start` 先 `Status`；`StateRunning` 直接返回（adopt，不重建，不重复拉镜像）；`StatePending`（不存在）直接走创建；`StateStopped`/`StateFailed`/`StateStarting` 先 `docker rm -f` 再重新创建——保证容器配置始终跟 `Spec` 当前值一致，不会用一个上次配置残留的旧容器悄悄服务请求。
- **镜像拉取**：创建前用 `docker image inspect <image>` 探测本地是否已有该镜像；没有则 `docker pull <image>`，失败则 `Start` 返回错误，不尝试 `docker run`。这个中间步骤只记结构化日志，不导出成 `State` 的一个取值——子任务一里 `Start` 是同步阻塞调用，没有并发调用方会在它执行期间轮询 `Status`，把"正在拉取"设计成一个可观察状态现在没有消费者，留给子任务二（状态回传出现之后）再评估是否需要。
- **参数构造是纯函数** `buildRunArgs(spec Spec) []string`，不执行任何命令，方便单测覆盖 GPU/挂载/内存参数的拼装而不用真的起容器；`ModelPaths`/`StoragePaths` 按 key 排序后再拼接 `-v`，保证同一个 `Spec` 每次生成完全相同的参数列表（可复现、日志可读、测试可断言）。
- **GPU 参数**：`GPUDevices` 非空时追加 `--gpus device=<逗号连接的设备号>`；空则完全不传 `--gpus`（没有 GPU 直通，不是"请求全部 GPU"的隐式默认值——要 GPU 直通必须显式配置）。
- **端口绑定**：固定 `-p 127.0.0.1:<Port>:8188`，三节已推导的强制约束，`buildRunArgs` 不接受任何其他绑定地址的输入路径。
- **Status 状态映射**：`docker inspect --format '{{.State.Status}}|{{.State.ExitCode}}' <name>`；docker 的 `created`/`restarting` 映射到 `StateStarting`，`running` 映射到 `StateRunning`，`exited`/`dead` 按 exit code 是否为零分到 `StateStopped`/`StateFailed`，容器不存在映射到 `StatePending`。"不存在"的判定用大小写不敏感的 "no such" 子串匹配——本地用真实 Docker（29.8.0）联调子任务一时发现，实际报文是小写的 "no such object"，不是最初假设的 "No such container"，跨版本措辞不保证一致，因此不按大小写敏感或整句匹配。
- **WaitReady 用注入的 `runtime.Clock`**：轮询间隔与超时判断经 `Clock.NewTimer` 驱动，不使用真实 `time.Sleep`，符合 AGENTS.md「测试不用真实 `time.Sleep` 推进时间，一律通过注入的 `runtime.Clock` 控制」的约定，与 `modelpull.NewPuller` 已经接受 `runtime.Clock` 同一先例。

### 5.2 Agent 接入（`service/aiServeWeaveAgent/main.go`）

新增一组 `-comfyui-managed-*` flag，比照现有 `-labels` 的 `key=value,key=value` 解析惯例处理 `ModelPaths`/`StoragePaths`：

```go
comfyuiManagedImage        string // e.g. "ghcr.io/example/comfyui:1.4.2"; empty disables Managed entirely
comfyuiManagedContainer    string // default "aiserveweave-comfyui"
comfyuiManagedPort         int    // default 18188
comfyuiManagedGPUDevices   string // comma-separated, e.g. "0"; empty omits --gpus
comfyuiManagedModelPaths   string // key=value,key=value, e.g. "checkpoints=/models/checkpoints,loras=/models/loras"
comfyuiManagedStoragePaths string // same shape, e.g. "input=/data/input,output=/data/output"
comfyuiManagedMemoryLimit  string // docker --memory value; empty is unlimited
comfyuiManagedStartTimeout time.Duration // default 5m; WaitReady's bound
```

`run()` 里紧跟在现有 `ollamaURL != ""` 那个块之后新增一个同构的 `if comfyuiManagedImage != ""` 块：调用 `Launcher.Start` 与 `Launcher.WaitReady`，成功后用同一个 `manager.Add(ctx, runtime.Config{Kind: runtime.KindComfyUI, BaseURL: fmt.Sprintf("http://127.0.0.1:%d", port), ID: containerName})` 完成注册——不新增分支逻辑，注册路径与 External 模式完全一致。与 `ollamaURL` 块同样的失败语义：配置了 Managed 就必须成功，任一步失败让 `run()` 返回错误、Agent 启动失败——运维显式选择了 Managed 模式，快速可见的失败好过悄悄退化成一个从不服务请求的节点。

### 5.3 测试

`comfyuimanaged_test.go`，表驱动，不依赖真实 Docker：

- `buildRunArgs` 纯函数测试——GPU 参数存在/缺失、挂载路径排序稳定、内存限制存在/缺失、镜像 tag 校验（拒绝空 tag 与 `latest`）。
- `docker` CLI 交互用一个临时目录里的假可执行脚本，测试经 `t.Setenv("PATH", ...)` 把它排到真实 `docker` 前面——精神上与 `hostresources.TestRunCommand` 用真实 `echo` 命令测试 `runCommand` 插件一致，只是这里脚本需要按子命令（`inspect`/`run`/`pull`/`stop`/`rm`）模拟不同输出。覆盖：容器已存在且运行中被幂等 adopt（不重复 `run`/`pull`）；容器不存在时先 `pull` 再 `run`；容器已存在但已退出时先 `rm -f` 再重建；`Stop` 对已经不存在的容器不报错；`Status` 对 running/exited(0)/exited(非零)/not-found 四态的分类。
- `WaitReady` 用 `net.Listen("tcp", "127.0.0.1:0")` 起一个真实但空转的本地监听器（只 accept 不处理，验证"端口能连通"这一件事，不涉及 HTTP/ComfyUI 协议），配合注入的假 `runtime.Clock` 验证超时路径不依赖真实时间流逝。

### 5.4 真实 Docker 的补充验证（本环境可做，GPU/ComfyUI 部分本环境结构性做不到）

1.7 节已核实本次会话环境有可用的 Docker daemon。子任务一额外提供一个环境变量门控的 live smoke test（`docker_live_test.go`，`AISW_DOCKER_LIVE_TEST=1` 才运行，仿照 `ollama/live_test.go`/`comfyui/live_test.go` 的既有先例），用一个本机已缓存的极小镜像（`curlimages/curl:8.11.1`，携带显式 tag，`Command` 覆盖为一个长驻命令）验证 `Start`/`Status`/`Stop`/幂等 adopt 的真实往返——这验证的是"Docker 生命周期管理机制本身"是正确的，**不**验证 ComfyUI 协议或 GPU 直通，那两者在本环境（macOS/Darwin，无 NVIDIA GPU）结构性不可能验证，留待 Linux NVIDIA 环境，与 A06/P10 已确认的边界一致。本次会话已在真实 Docker daemon 上运行这个测试并通过。

另外用 `go run ./service/aiServeWeaveAgent -comfyui-managed-image=curlimages/curl:8.11.1 ...` 手动跑通了一次完整的 main.go 接入链路（非 ComfyUI 镜像、无 Command 覆盖）：`Start` 正确创建并启动容器，`WaitReady` 的纯 TCP 检查按设计通过（docker 的端口转发短暂接受了连接），随后交给既有 `manager.Add` 的 Probe 发起真正的 `GET /system_stats` 时因为对端根本不是 ComfyUI 而收到 EOF，Agent 按预期启动失败并打出清晰的错误链（`comfyui managed: registering http://127.0.0.1:38199: runtime[comfyui/...] probe: connection_failed: ...`）。这次手动验证确认了"Start → WaitReady → 既有 Probe/Discover"这条链路按设计顺序正确执行、失败语义清晰可读，且没有把 ComfyUI 身份校验重新实现一遍。

### 5.5 文档同步

- `service/aiServeWeaveAgent/README.md`：新增「ComfyUI Managed Docker（P2 子任务一）」小节，说明范围边界（单容器、本地静态配置、无控制面下发、无排空升级、无自定义节点管理）。
- STATUS.md 第 119 条：补充子任务一已交付的说明、1.7 节的前置条件现状、与已知缺口，条目整体保持未勾选（其余子任务未排期）。

## 六、已知缺口

如实记录，不阻塞子任务一验收，也不阻塞后续子任务排期：

- **README 第五条目标行为的 WebSocket 检查未实现**（2.2 节）——`WaitReady` 只确认 TCP 可连通，ComfyUI 身份校验完全交给随后的 `manager.Add`（其中不含 WS 探测）。
- **不支持多容器/多 Spec**——子任务一的 flag 只能配置一个 ComfyUI Managed 实例；一台机器上想跑多个需要子任务二的部署规格契约（每个部署一个 ID）。
- **不支持排空升级**（子任务三范围）——`Start` 遇到已存在的运行中容器只会 adopt，不会对比镜像版本是否与当前 `Spec.Image` 一致；换版本必须先手动 `Stop` 再改 flag 重启 Agent,这本身也不检查是否有正在运行的 Job。
- **不支持自定义节点管理**（子任务四范围）——`workflowtemplate.Dependencies` 声明的节点名+版本与容器实际安装了什么完全脱节，1.8 节已确认。
- **审计缺口**——安装/启动/停止动作只写结构化日志，不写审计日志（三节已说明原因，依赖子任务二）。
- **无控制面/Console 可见性**——容器状态只在 Agent 本地日志与 `Status` 调用里可见（子任务二、五范围）。
- **「先验收 External 链路」这个前置条件本身仍未关闭**（1.7 节）——本文档没有替 A06 补上真实 ComfyUI/GPU 验证，子任务一的设计与实现不依赖它关闭，但 STATUS.md 条目描述的完整验收口径（含 GPU 直通、真实 ComfyUI 健康检查全链路）在本环境无法闭环。
- **GPU 设备号是纯字符串透传**——`GPUDevices` 不与 `hostresources` 探测到的实际 GPU 数量交叉校验，配置了本机不存在的设备号会在 `docker run` 阶段才报错，不是提前校验。
- **`Spec.MemoryLimit` 是原始字符串透传给 `docker --memory`**——不做格式校验，格式错误同样在 `docker run` 阶段才报错。

## 七、与 STATUS.md 的关系

STATUS.md 第 119 条本次保持未勾选（`[ ]`），补充说明子任务一已交付、四项后续子任务的拆分与依赖顺序、以及 1.7 节记录的"先验收 External 链路"这一前置条件现状。与「模型分发」条目同一先例：现状核实、场景/依赖/验收拆分与第一项风险最低的子任务在同一个 session 内完成，其余子任务留给后续排期。
