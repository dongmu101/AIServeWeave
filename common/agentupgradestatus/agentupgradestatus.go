// Package agentupgradestatus is the wire contract for STATUS.md's P2 Agent
// auto-upgrade feature: the Agent-to-Gateway status of its own upgrade
// process, and the vocabulary a Gateway-to-Agent action names by. It never
// carries a download URL, a signature, or raw error text.
//
// All three exclusions are deliberate, not oversights. tunnel.proto's
// load-bearing rule forbids a message that expresses "fetch this URL" or
// "run this command" — and this feature's payload is, eventually, code that
// will be exec'd, which makes that rule harder than it is for model
// distribution or ComfyUI Managed. An Action can therefore only name a
// version string the Agent already resolved against its own local, signed
// manifest (service/aiServeWeaveAgent/agentupgrade) — never the URL or
// signature behind it. Reason stays a closed vocabulary for the same reason
// modelpullstatus.FailureReason does: the underlying error can embed a
// source URL (ReasonDownloadFailed's cause, in particular).
//
// Subtask 1 implemented CHECK end to end: comparing the Agent's local
// manifest against its own running version and reporting whether a newer
// known version exists. Subtask 2 adds UPGRADE's full chain — download,
// SHA256 verification against the signed manifest, draining every held
// tunnel connection, and replacing the running process image — so States
// Downloading/Verifying/Draining/Restarting and Reasons
// DownloadFailed/VerificationFailed/ExecFailed now have real code paths
// behind them. ROLLBACK remains wire contract only, reported as
// StateFailed/ReasonNotImplemented, until a later subtask.
//
// agentupgradestatus 是 STATUS.md P2 Agent 自动升级功能的契约：Agent 到
// Gateway 的自身升级进度状态，以及 Gateway 到 Agent 的动作指令所用的词表。
// 它从不携带下载 URL、签名或原始错误文本。
//
// 三处排除都是刻意的，不是疏漏。tunnel.proto 的 load-bearing 规则禁止表达
// "fetch this URL" 或 "run this command" 的消息——而这个功能的载荷最终是会
// 被 exec 的代码本身，这条规则在这里比模型分发或 ComfyUI Managed 更硬。因此
// Action 只能指名一个 Agent 已经在自己本地、已签名清单
// （service/aiServeWeaveAgent/agentupgrade）里解析过的版本号——从不是它背后
// 的 URL 或签名。Reason 保持封闭词表的理由与 modelpullstatus.FailureReason
// 相同：底层错误可能带有来源 URL（尤其是 ReasonDownloadFailed 的成因）。
//
// 子任务一完整实现了 CHECK：把 Agent 本地清单与自己正在跑的版本比较，报告
// 是否存在已知的更新版本。子任务二补上了 UPGRADE 的完整链路——下载、对照已
// 签名清单校验 SHA256、排空每一条持有的隧道连接、替换正在运行的进程镜
// 像——因此 Downloading/Verifying/Draining/Restarting 四个状态与
// DownloadFailed/VerificationFailed/ExecFailed 三个原因现在都有真实的代码
// 路径。ROLLBACK 在后续子任务之前仍然只是协议契约，报告
// StateFailed/ReasonNotImplemented。
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
	// StateDownloading means the bytes for TargetVersion are actively being
	// fetched over HTTP into a local temp file.
	//
	// StateDownloading 表示正在通过 HTTP 主动获取 TargetVersion 的字节到本
	// 地临时文件。
	StateDownloading
	// StateVerifying means the fully downloaded bytes' SHA256 is being
	// checked against the value the Agent's signed local manifest declares
	// for this version.
	//
	// StateVerifying 表示正在把已完整下载字节的 SHA256 与 Agent 已签名本地
	// 清单为这个版本声明的值做比对。
	StateVerifying
	// StateDraining means the Agent is waiting for its own in-flight
	// requests, across every Gateway connection it holds, to reach zero
	// before it replaces itself.
	//
	// StateDraining 表示 Agent 正在等待它自己持有的每一条 Gateway 连接上的
	// 在途请求归零，之后才会替换自己。
	StateDraining
	// StateRestarting means the Agent is about to call syscall.Exec with the
	// verified new binary. A successful Exec replaces this process's image
	// and never returns, so no further report is ever sent from the old
	// process — the next report an observer sees comes from the new one's
	// initial Hello/Control handshake reporting a changed CurrentVersion.
	//
	// StateRestarting 表示 Agent 即将用校验通过的新二进制调用
	// syscall.Exec。一次成功的 Exec 会替换本进程的镜像且从不返回，因此旧进
	// 程不会再发送任何后续报告——观察者接下来看到的报告来自新进程初始的
	// Hello/Control 握手，其中 CurrentVersion 已经变化。
	StateRestarting
	// StateFailed means the triggered action stopped short; Reason narrows
	// why: an unknown target version (CHECK/UPGRADE/ROLLBACK), a ROLLBACK
	// trigger (ReasonNotImplemented, no execution path yet), or an UPGRADE
	// that failed during download, verification, or exec.
	//
	// StateFailed 表示被触发的动作中途停止；Reason 说明具体原因：目标版本
	// 未知（CHECK/UPGRADE/ROLLBACK）、一次 ROLLBACK 触发（ReasonNotImplemented，
	// 目前没有执行路径），或者一次在下载、校验或 exec 阶段失败的 UPGRADE。
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
	case ReasonDownloadFailed:
		return "download_failed"
	case ReasonVerificationFailed:
		return "verification_failed"
	case ReasonExecFailed:
		return "exec_failed"
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
	// ReasonNotImplemented means the triggered action is ROLLBACK, which has
	// no execution path yet (STATUS.md P2 Agent 自动升级子任务二实现了
	// UPGRADE；ROLLBACK 见设计文档子任务三).
	//
	// ReasonNotImplemented 表示被触发的动作是 ROLLBACK，目前没有执行路径
	//（STATUS.md P2 Agent 自动升级子任务二实现了 UPGRADE；ROLLBACK 见设计
	// 文档子任务三）。
	ReasonNotImplemented
	// ReasonDownloadFailed means fetching or locally storing the new
	// binary's bytes failed (network error, unexpected HTTP status, or a
	// local filesystem error). The underlying error is logged locally by
	// the Agent and never crosses the tunnel — it can embed the manifest's
	// SourceURL, which AGENTS.md's security rules forbid putting in a
	// message the Gateway observes.
	//
	// ReasonDownloadFailed 表示获取或本地落盘新二进制字节失败（网络错误、
	// 非预期的 HTTP 状态码，或本地文件系统错误）。底层错误只由 Agent 本地
	// 记日志，从不跨隧道传递——它可能带有清单里的 SourceURL，AGENTS.md 的
	// 安全规则禁止把这个放进 Gateway 能观测到的消息里。
	ReasonDownloadFailed
	// ReasonVerificationFailed means the downloaded bytes' SHA256 does not
	// match the value the Agent's signed local manifest declares for this
	// version. Because the manifest signature already authenticates the
	// (version, SHA256) mapping (see agentupgrade.LoadManifest), this
	// failure means the transport delivered the wrong bytes — corruption or
	// a compromised distribution host — not that the manifest itself was
	// forged.
	//
	// ReasonVerificationFailed 表示下载字节的 SHA256 与 Agent 已签名本地清
	// 单里为这个版本声明的值不一致。由于清单签名本身已经对 (版本号,
	// SHA256) 的映射做了身份验证（见 agentupgrade.LoadManifest），这个失
	// 败意味着传输环节送错了字节——传输损坏或分发主机被攻破——而不是清单本
	// 身被伪造。
	ReasonVerificationFailed
	// ReasonExecFailed means syscall.Exec (or its test double) itself
	// returned an error after the new binary was downloaded, verified, and
	// the tunnel connections drained. The Agent's old process image is
	// still the one running — this is not a partial upgrade, it is one
	// that never took effect.
	//
	// ReasonExecFailed 表示在新二进制已下载、校验通过、隧道连接已排空之
	// 后，syscall.Exec（或测试替身）本身返回了错误。Agent 仍在运行旧的进
	// 程镜像——这不是一次部分生效的升级，而是完全没有生效的一次。
	ReasonExecFailed
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
	// ActionUpgrade asks the Agent to replace itself with TargetVersion:
	// download it, verify its SHA256 against the signed local manifest,
	// drain every held tunnel connection, and syscall.Exec into it.
	// TargetVersion must name an entry in the Agent's local manifest, or
	// the Agent answers ReasonUnknownVersion without any network request.
	//
	// ActionUpgrade 要求 Agent 把自己替换为 TargetVersion：下载它、对照已
	// 签名的本地清单校验其 SHA256、排空每一条持有的隧道连接，然后
	// syscall.Exec 进入它。TargetVersion 必须命中 Agent 本地清单里的某个条
	// 目，否则 Agent 不发起任何网络请求，直接回答 ReasonUnknownVersion。
	ActionUpgrade
	// ActionRollback asks the Agent to replace itself with the previously
	// running TargetVersion. There is no execution path for it yet — the
	// Agent reports ReasonNotImplemented for every ActionRollback trigger
	// (see the design doc's subtask three).
	//
	// ActionRollback 要求 Agent 把自己替换回此前运行过的 TargetVersion。目
	// 前没有执行路径——Agent 对每一次 ActionRollback 触发都报告
	// ReasonNotImplemented（见设计文档子任务三）。
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
