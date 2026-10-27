// Package comfyuimanagedstatus is the wire contract for STATUS.md's P2
// ComfyUI Managed Docker deployment, subtask two: the Agent-to-Gateway
// container-lifecycle status of the one Managed ComfyUI instance an Agent
// may run, and the vocabulary a Gateway-to-Agent action names.
//
// A Status never carries an image, mount path, or any other Spec field.
// tunnel.proto's load-bearing rule forbids a message that expresses "run
// this command"; a Managed ComfyUI container's image and mounts are exactly
// that, so they stay entirely in the Agent's own local flags
// (service/aiServeWeaveAgent/comfyuimanaged) and never cross the tunnel in
// either direction. An Action can only reference the one instance the Agent
// already declared locally — it is never a way to hand the Agent a new
// image to run.
//
// comfyuimanagedstatus 是 STATUS.md P2 ComfyUI Managed Docker 部署子任务二的
// 契约：Agent 到 Gateway 的容器生命周期状态，以及 Gateway 到 Agent 的动作指令
// 所用的词表。
//
// Status 从不携带镜像名、挂载路径或任何其他 Spec 字段。tunnel.proto 的
// load-bearing 规则禁止表达 "run this command" 的消息；一个 Managed ComfyUI
// 容器的镜像与挂载正是这样的指令，因此它们完全留在 Agent 自己的本地 flag
// 里（service/aiServeWeaveAgent/comfyuimanaged），从不双向跨越隧道。一个
// Action 只能引用 Agent 已经在本地声明的那一个实例——它从不是让 Agent 运行
// 一个新镜像的手段。
package comfyuimanagedstatus

import "time"

// State is where the one Managed ComfyUI container currently stands.
//
// State 是那一个 Managed ComfyUI 容器当前所处的阶段。
type State int

// String returns a lowercase, stable name for s, suitable for JSON and log
// output. An unrecognized value renders as "unspecified" rather than
// panicking or printing a bare integer.
//
// String 返回 s 的小写稳定名称，适合 JSON 与日志输出。未识别的取值渲染为
// "unspecified" 而不是 panic 或打印裸整数。
func (s State) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopped:
		return "stopped"
	case StateFailed:
		return "failed"
	default:
		return "unspecified"
	}
}

const (
	// StateUnspecified is the zero value: no Managed instance is locally
	// configured on this Agent.
	//
	// StateUnspecified 是零值：本 Agent 未本地配置 Managed 实例。
	StateUnspecified State = iota
	// StatePending means no container by this name exists yet.
	//
	// StatePending 表示尚不存在同名容器。
	StatePending
	// StateStarting means the container is created or restarting, and its
	// port is not yet answering.
	//
	// StateStarting 表示容器已创建或正在重启，端口尚未应答。
	StateStarting
	// StateRunning means the container process is running.
	//
	// StateRunning 表示容器进程正在运行。
	StateRunning
	// StateStopped means the container exited cleanly (exit code 0).
	//
	// StateStopped 表示容器正常退出（退出码 0）。
	StateStopped
	// StateFailed means the container exited non-zero, or docker itself
	// errored.
	//
	// StateFailed 表示容器非零退出，或 docker 本身报错。
	StateFailed
)

// Action is a closed lifecycle action a Gateway can ask the Agent to apply
// to its one locally-declared Managed instance.
//
// Action 是 Gateway 可以要求 Agent 对其本地已声明的那一个 Managed 实例施加
// 的一个封闭生命周期动作。
type Action int

// String returns a lowercase, stable name for a, suitable for JSON and log
// output.
//
// String 返回 a 的小写稳定名称，适合 JSON 与日志输出。
func (a Action) String() string {
	switch a {
	case ActionStart:
		return "start"
	case ActionStop:
		return "stop"
	case ActionRestart:
		return "restart"
	default:
		return "unspecified"
	}
}

const (
	// ActionUnspecified is the zero value: no action.
	//
	// ActionUnspecified 是零值：没有动作。
	ActionUnspecified Action = iota
	// ActionStart brings the container up if it is not already running and
	// registered.
	//
	// ActionStart 在容器尚未运行且注册时把它带起来。
	ActionStart
	// ActionStop deregisters the runtime and stops and removes the
	// container.
	//
	// ActionStop 先取消运行时注册，再停止并移除容器。
	ActionStop
	// ActionRestart applies ActionStop followed by ActionStart, so the
	// container is recreated from whatever Spec is currently configured
	// locally — an operator who changed the local image tag applies it by
	// triggering Restart, never by pushing a new Spec over the tunnel.
	//
	// ActionRestart 先执行 ActionStop 再执行 ActionStart，让容器按当前本地
	// 配置的 Spec 重建——运维改了本地镜像 tag 后靠触发 Restart 应用，而不是
	// 靠隧道下发新 Spec。
	ActionRestart
)

// Status is the one Managed ComfyUI container's current state, as the Agent
// reports it and the Gateway last observed it.
//
// Status 是那一个 Managed ComfyUI 容器的当前状态，即 Agent 上报、Gateway 最
// 后一次观测到的样子。
type Status struct {
	ContainerName string
	State         State
	UpdatedAt     time.Time
	// CustomNodes is every custom node subdirectory this Agent found under
	// the container's custom-nodes directory the last time it looked (STATUS.
	// md's P2 ComfyUI Managed Docker deployment, subtask 4). Empty when
	// Managed mode is disabled, the container has no custom_nodes directory
	// yet, or nothing has been installed through this package.
	//
	// CustomNodes 是本 Agent 上次查看容器自定义节点目录时找到的每一个子目录
	// （STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务四）。未启用 Managed
	// 模式、容器尚无 custom_nodes 目录，或从未经本包安装过任何节点时为空。
	CustomNodes []CustomNodeStatus
}

// CustomNodeStatus is one installed custom node, as read back from the
// container rather than from the Agent's local allowlist — Version is the
// pinned ref actually present on disk, which can lag or diverge from the
// allowlist's declared ref if an install failed partway or was performed by
// something other than this package.
//
// CustomNodeStatus 是从容器里读回的一个已安装自定义节点，而不是来自 Agent
// 本地允许列表——Version 是磁盘上实际存在的固定版本，如果一次安装中途失败，
// 或是被本包以外的东西安装的，它可能落后于或不同于允许列表声明的版本。
type CustomNodeStatus struct {
	Name    string
	Version string // "unknown" when the directory exists but this package's version marker does not
}

// DeclaredCustomNode is one workflow template's declared custom-node
// dependency, in the shape ReconcileCustomNodes compares against — callers
// convert their own workflowtemplate.NodeDependency into this local type
// rather than this package importing workflowtemplate, keeping the two
// packages' concerns apart.
//
// DeclaredCustomNode 是一个工作流模板声明的自定义节点依赖，是
// ReconcileCustomNodes 用来比较的形状——调用方自己把
// workflowtemplate.NodeDependency 转换成这个本地类型，而不是让本包导入
// workflowtemplate，让两个包的关注点保持分离。
type DeclaredCustomNode struct {
	Name    string
	Version string // empty means "any version is acceptable"
}

// CustomNodeMismatch is one declared dependency ReconcileCustomNodes found
// unsatisfied.
//
// CustomNodeMismatch 是 ReconcileCustomNodes 发现的一个未满足的已声明依赖。
type CustomNodeMismatch struct {
	Name   string
	Reason string // "missing" or "version_mismatch"
}

// ReconcileCustomNodes compares a workflow template's declared custom-node
// dependencies against what one Managed instance actually reports installed
// (STATUS.md's P2 ComfyUI Managed Docker deployment, subtask 4), and
// reports every declared dependency that is not satisfied. It is a pure
// function with no consumer wired into scheduling — see the subtask 4
// design doc's known-gaps section for why: a workflow template's model
// dependencies get no such hard check today either, so custom nodes should
// not be singled out for one.
//
// ReconcileCustomNodes 把一个工作流模板声明的自定义节点依赖，与一个 Managed
// 实例实际上报已安装的东西做比较（STATUS.md 的 P2 ComfyUI Managed Docker
// 部署子任务四），报告每一个未被满足的已声明依赖。它是一个没有接入调度决策
// 的纯函数——原因见子任务四设计文档的已知缺口一节：工作流模板的模型依赖今
// 天也没有这层硬校验，自定义节点不该被特殊对待。
func ReconcileCustomNodes(declared []DeclaredCustomNode, installed []CustomNodeStatus) []CustomNodeMismatch {
	byName := make(map[string]string, len(installed))
	for _, st := range installed {
		byName[st.Name] = st.Version
	}
	var mismatches []CustomNodeMismatch
	for _, d := range declared {
		version, ok := byName[d.Name]
		if !ok {
			mismatches = append(mismatches, CustomNodeMismatch{Name: d.Name, Reason: "missing"})
			continue
		}
		if d.Version != "" && d.Version != version {
			mismatches = append(mismatches, CustomNodeMismatch{Name: d.Name, Reason: "version_mismatch"})
		}
	}
	return mismatches
}
