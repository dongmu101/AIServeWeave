// Package agentupgradestatus is the wire contract for STATUS.md's P2 Agent
// auto-upgrade subtask one: the Agent-to-Gateway status of its own upgrade
// process, and the vocabulary a Gateway-to-Agent action names by. It never
// carries a download URL, a signature, or raw error text.
//
// All three exclusions are deliberate, not oversights. tunnel.proto's
// load-bearing rule forbids a message that expresses "fetch this URL" or
// "run this command" — and this feature's payload is, eventually, code that
// will be exec'd, which makes that rule harder than it is for model
// distribution or ComfyUI Managed. An Action can therefore only name a
// version string the Agent already resolved against its own local manifest
// (service/aiServeWeaveAgent/agentupgrade) — never the URL or signature
// behind it. Reason stays a closed vocabulary for the same reason
// modelpullstatus.FailureReason does: the underlying error can embed a
// source URL, and this subtask does not implement any path that could fail
// with one anyway (see below).
//
// This subtask implements only the CHECK action end to end: comparing the
// Agent's local manifest against its own running version and reporting
// whether a newer known version exists. UPGRADE and ROLLBACK are wire
// contract only — the Agent reports StateFailed/ReasonNotImplemented for
// both, and States DownLoading/Verifying/Draining/Restarting exist in the
// vocabulary but no code path produces them until a later subtask.
//
// agentupgradestatus 是 STATUS.md P2 Agent 自动升级子任务一的契约：Agent 到
// Gateway 的自身升级进度状态，以及 Gateway 到 Agent 的动作指令所用的词表。
// 它从不携带下载 URL、签名或原始错误文本。
//
// 三处排除都是刻意的，不是疏漏。tunnel.proto 的 load-bearing 规则禁止表达
// "fetch this URL" 或 "run this command" 的消息——而这个功能的载荷最终是会
// 被 exec 的代码本身，这条规则在这里比模型分发或 ComfyUI Managed 更硬。因此
// Action 只能指名一个 Agent 已经在自己本地清单
// （service/aiServeWeaveAgent/agentupgrade）里解析过的版本号——从不是它背后
// 的 URL 或签名。Reason 保持封闭词表的理由与 modelpullstatus.FailureReason
// 相同：底层错误可能带有来源 URL，而且本子任务尚未实现任何真的会带着这种
// 错误失败的路径（见下）。
//
// 本子任务只完整实现 CHECK 动作：把 Agent 本地清单与自己正在跑的版本比较，
// 报告是否存在已知的更新版本。UPGRADE 与 ROLLBACK 目前只是协议契约——Agent
// 对两者都报告 StateFailed/ReasonNotImplemented，DOWNLOADING/VERIFYING/
// DRAINING/RESTARTING 四个状态已经存在于词表中，但要到后续子任务才会有代码
// 路径真正产生它们。
package agentupgradestatus

import "time"

// State is where the Agent's own upgrade process currently stands.
//
// State 是 Agent 自身升级流程当前所处的阶段。
type State int

// String returns a lowercase, stable name for s, suitable for JSON and log
// output. An unrecognized value renders as "unspecified" rather than
// panicking or printing a bare integer.
//
// String 返回 s 的小写稳定名称，适合 JSON 与日志输出。未识别的取值渲染为
// "unspecified" 而不是 panic 或打印裸整数。
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateChecking:
		return "checking"
	case StateDownloading:
		return "downloading"
	case StateVerifying:
		return "verifying"
	case StateDraining:
		return "draining"
	case StateRestarting:
		return "restarting"
	case StateFailed:
		return "failed"
	default:
		return "unspecified"
	}
}

const (
	// StateIdle is the zero value: no upgrade action has been triggered
	// since the Agent started, or the last one finished without leaving the
	// Agent's own version changed (a CHECK that found no update returns
	// here rather than to a dedicated "checked, nothing found" state).
	//
	// StateIdle 是零值：Agent 启动以来从未被触发过升级动作，或者上一次触发
	// 结束后没有改变 Agent 自己的版本（一次没发现更新的 CHECK 回到这里，而
	// 不是回到一个专门的"已检查、未发现"状态）。
	StateIdle State = iota
	// StateChecking means the Agent is comparing its local manifest against
	// its running version. This subtask's CHECK path is synchronous, so an
	// external observer is unlikely to ever catch a report in this state,
	// but it exists so a slower future check (e.g. one that reaches a
	// remote version index) has somewhere to report from.
	//
	// StateChecking 表示 Agent 正在把本地清单与自己的运行版本比较。本子任务
	// 的 CHECK 路径是同步的，外部观察者几乎不可能真的捕捉到处于这个状态的
	// 报告，但保留它是为了将来一次更慢的检查（例如需要访问远程版本索引）有
	// 地方可以上报。
	StateChecking
	// StateDownloading means bytes for a new version are actively being
	// fetched. No code path in this subtask produces this state.
	//
	// StateDownloading 表示正在主动获取新版本的字节。本子任务没有任何代码
	// 路径会产生这个状态。
	StateDownloading
	// StateVerifying means the fetched bytes are being checked against the
	// manifest's declared signature. No code path in this subtask produces
	// this state.
	//
	// StateVerifying 表示正在用清单声明的签名校验获取到的字节。本子任务没
	// 有任何代码路径会产生这个状态。
	StateVerifying
	// StateDraining means the Agent is waiting for its own in-flight
	// requests, across every Gateway connection it holds, to reach zero
	// before it replaces itself. No code path in this subtask produces this
	// state.
	//
	// StateDraining 表示 Agent 正在等待它自己持有的每一条 Gateway 连接上的
	// 在途请求归零，之后才会替换自己。本子任务没有任何代码路径会产生这个
	// 状态。
	StateDraining
	// StateRestarting means the Agent has replaced its own process image
	// and is about to re-establish its tunnel connections. No code path in
	// this subtask produces this state.
	//
	// StateRestarting 表示 Agent 已经替换了自己的进程镜像，即将重新建立隧
	// 道连接。本子任务没有任何代码路径会产生这个状态。
	StateRestarting
	// StateFailed means the triggered action stopped short; Reason narrows
	// why. This subtask reaches it for an unknown target version (CHECK)
	// and for any UPGRADE or ROLLBACK trigger (ReasonNotImplemented).
	//
	// StateFailed 表示被触发的动作中途停止；Reason 说明具体原因。本子任务
	// 会在目标版本未知（CHECK）以及任何 UPGRADE 或 ROLLBACK 触发
	//（ReasonNotImplemented）时到达这个状态。
	StateFailed
)

// FailureReason is the closed vocabulary a failed Status reports instead of
// a raw error. It is set only when State is StateFailed.
//
// FailureReason 是失败的 Status 用来代替原始错误上报的封闭词表，只在 State
// 为 StateFailed 时才会被设置。
type FailureReason int

// String returns a lowercase, stable name for r, suitable for JSON and log
// output.
//
// String 返回 r 的小写稳定名称，适合 JSON 与日志输出。
func (r FailureReason) String() string {
	switch r {
	case ReasonUnknownVersion:
		return "unknown_version"
	case ReasonNotImplemented:
		return "not_implemented"
	default:
		return "unspecified"
	}
}

const (
	// ReasonUnspecified is the zero value: no failure.
	//
	// ReasonUnspecified 是零值：没有失败。
	ReasonUnspecified FailureReason = iota
	// ReasonUnknownVersion means an UPGRADE or ROLLBACK's target_version
	// does not name an entry in the Agent's local manifest. The Agent
	// answers this from the manifest alone, without making any network
	// request — the same restraint modelpullstatus.ReasonUnknownName
	// applies to an unrecognized model-pull name.
	//
	// ReasonUnknownVersion 表示一次 UPGRADE 或 ROLLBACK 的 target_version
	// 没有命中 Agent 本地清单里的任何条目。Agent 仅凭本地清单就能回答这一
	// 点，不发起任何网络请求——与 modelpullstatus.ReasonUnknownName 对未识
	// 别名字的处理同一种克制。
	ReasonUnknownVersion
	// ReasonNotImplemented means the triggered action is UPGRADE or
	// ROLLBACK, which this subtask reports but does not execute (STATUS.md
	// P2 Agent 自动升级子任务一 only implements CHECK; see the design doc's
	// subtask breakdown for subtask two/three).
	//
	// ReasonNotImplemented 表示被触发的动作是 UPGRADE 或 ROLLBACK，本子任
	// 务只上报、不执行（STATUS.md P2 Agent 自动升级子任务一只实现了
	// CHECK；子任务二/三见设计文档的子任务拆分）。
	ReasonNotImplemented
)

// Action is a closed action a Gateway can ask the Agent to apply to its own
// upgrade process.
//
// Action 是 Gateway 可以要求 Agent 对自己的升级流程施加的一个封闭动作。
type Action int

// String returns a lowercase, stable name for a, suitable for JSON and log
// output.
//
// String 返回 a 的小写稳定名称，适合 JSON 与日志输出。
func (a Action) String() string {
	switch a {
	case ActionCheck:
		return "check"
	case ActionUpgrade:
		return "upgrade"
	case ActionRollback:
		return "rollback"
	default:
		return "unspecified"
	}
}

const (
	// ActionUnspecified is the zero value: no action.
	//
	// ActionUnspecified 是零值：没有动作。
	ActionUnspecified Action = iota
	// ActionCheck compares the Agent's local manifest against its own
	// running version and reports whether a newer known version exists.
	// TargetVersion may be empty for this action.
	//
	// ActionCheck 把 Agent 本地清单与自己的运行版本比较，报告是否存在已知
	// 的更新版本。这个动作的 TargetVersion 可以为空。
	ActionCheck
	// ActionUpgrade asks the Agent to replace itself with TargetVersion.
	// This subtask reports ReasonNotImplemented for every ActionUpgrade
	// trigger, whether or not TargetVersion names a known manifest entry.
	//
	// ActionUpgrade 要求 Agent 把自己替换为 TargetVersion。本子任务对每一
	// 次 ActionUpgrade 触发都报告 ReasonNotImplemented，无论 TargetVersion
	// 是否命中已知的清单条目。
	ActionUpgrade
	// ActionRollback asks the Agent to replace itself with the previously
	// running TargetVersion. This subtask reports ReasonNotImplemented for
	// every ActionRollback trigger, the same as ActionUpgrade.
	//
	// ActionRollback 要求 Agent 把自己替换回此前运行过的 TargetVersion。本
	// 子任务对每一次 ActionRollback 触发都报告 ReasonNotImplemented，与
	// ActionUpgrade 相同。
	ActionRollback
)

// Status is the Agent's own upgrade process's current state, as the Agent
// reports it and the Gateway last observed it. Unlike modelpullstatus.Status
// or comfyuimanagedstatus.Status, there is exactly one Status per Agent, not
// one per named thing — an Agent has only one running version.
//
// Status 是 Agent 自身升级流程的当前状态，即 Agent 上报、Gateway 最后一次观
// 测到的样子。与 modelpullstatus.Status 或 comfyuimanagedstatus.Status 不
// 同，每个 Agent 只有一份 Status，不是按名字各一份——一个 Agent 只有一个正
// 在运行的版本。
type Status struct {
	CurrentVersion string
	State          State
	// Reason is set only when State is StateFailed.
	//
	// Reason 只在 State 为 StateFailed 时才会被设置。
	Reason    FailureReason
	UpdatedAt time.Time
}
