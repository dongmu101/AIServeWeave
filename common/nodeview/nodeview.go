// Package nodeview is the contract for the fleet inventory: what a Gateway
// replica reports about the nodes connected to it, and what the control plane
// aggregates and serves to an operations console.
//
// It is in common/ for the reason common/apikey and common/quota are: two
// services must interpret one piece of data by the same rules, and a second
// declaration of these structs — one per side, kept in sync by habit — is the
// kind of duplication this repository treats as a contract defect rather than
// a style question.
//
// The other reason it exists is that rendering is an allowlist. A runtime's
// credentials are not in a Snapshot today — they live in runtime.Config, the
// Agent resolves them locally, and common/tunnelwire drops the API key before
// anything crosses the tunnel — so the Gateway has none to leak. This package
// keeps it that way by naming the fields that may cross rather than
// marshalling whatever a struct happens to hold: a field added to
// runtime.Descriptor or runtime.Discovery stays out of this view until
// somebody puts it here on purpose, which is the point at which they have to
// think about who reads it.
//
// nodeview 包是机群清单的契约：一个 Gateway 副本就连到它身上的节点报告什么，以及控制面
// 聚合之后交给运维控制台什么。
//
// 它放在 common/ 的理由与 common/apikey、common/quota 相同：两个服务必须按同一套规则
// 解释同一份数据，而把这些结构体各声明一遍——每边一份、靠习惯保持同步——正是本仓库
// 视为契约缺陷而非风格问题的那类重复。
//
// 它存在的另一个理由是：渲染采用允许列表。今天的 Snapshot 里并没有运行时的凭据——它们
// 在 runtime.Config 中，由 Agent 本地解析，而 common/tunnelwire 在任何东西过隧道之前
// 就丢弃了 API key——因此 Gateway 手上就没有可泄漏的凭据。本包用「点名哪些字段可以外传」
// 而不是「序列化某个结构体碰巧持有的一切」来维持这个状态：往 runtime.Descriptor 或
// runtime.Discovery 上新增的字段不会进入这个视图，除非有人刻意把它放进来——而那一刻，
// 他就不得不去想清楚谁会读到它。
package nodeview

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"AIServeWeave/common/cliusagestatus"
	"AIServeWeave/common/runtime"
)

// Replica is one Gateway replica's answer: the nodes it currently terminates
// tunnels for, and the instant it looked.
//
// GeneratedAt is not decoration. A replica only knows the nodes connected to
// itself — that is the tunnel design's second constraint, not an omission —
// so this document is a partial, point-in-time view by construction, and a
// console that showed it without saying when it was taken would present a
// stale fragment as the state of the fleet.
//
// Replica 是一个 Gateway 副本的回答：它当前为哪些节点终结隧道，以及它是在哪个时刻看的。
//
// GeneratedAt 不是装饰。一个副本只知道连到它自己身上的节点——那是隧道设计的第二条约束，
// 不是疏漏——因此这份文档在构造上就是局部的、某一时刻的视图，而一个不说明它取自何时就
// 展示它的控制台，等于把一个过期的片段当作整个机群的状态呈现出来。
type Replica struct {
	ReplicaID   string    `json:"replica_id"`
	GeneratedAt time.Time `json:"generated_at"`
	Nodes       []Node    `json:"nodes"`
	// LocalRuntimes lists backends discovered on this Gateway, separately from Agents.
	// LocalRuntimes 列出本 Gateway 发现的本机后端，与 Agent 清单分开。
	LocalRuntimes []Runtime `json:"local_runtimes,omitempty"`
}

// Node is one Agent as a Gateway replica sees it.
//
// Node 是一个 Gateway 副本眼中的一个 Agent。
type Node struct {
	NodeID       string `json:"node_id"`
	AgentVersion string `json:"agent_version,omitempty"`
	// Live means a Control stream is established and the last heartbeat is
	// recent enough. It is the replica's judgement, not the Agent's claim.
	//
	// Live 表示 Control 流已建立且最近一次心跳足够新。这是副本的判断，不是 Agent
	// 的声称。
	Live bool `json:"live"`
	// Draining means the Agent announced it is shutting down: no new work,
	// but in-flight requests are still running.
	//
	// Draining 表示 Agent 已宣告自己正在关闭：不再接新活，但在途请求仍在运行。
	Draining bool `json:"draining"`
	// Maintenance means an operator forced this node_id into maintenance via
	// the Registry (STATUS.md's P01): no new work, but unlike Draining the
	// node is not on its way out — its Control stream and in-flight requests
	// are untouched, and it stays Live.
	//
	// Maintenance 表示运维经由 Registry 把该 node_id 强制置入维护状态
	// （STATUS.md 的 P01）：不再接新活，但与 Draining 不同，该节点并非正在离开——
	// 其 Control 流与在途请求都不受影响，且它依然是 Live。
	Maintenance bool `json:"maintenance"`
	// LastHeartbeat is absent when the node has not sent one yet. Absent is
	// not "a long time ago", and the two must not render the same way.
	//
	// LastHeartbeat 在节点尚未发送过心跳时缺席。缺席不等于「很久以前」，两者不能
	// 渲染成同一个样子。
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
	// Labels are what the Agent declared. They are operator-assigned facts
	// that routing selects on, and they are taken at face value: a
	// compromised Agent can claim any label, so nothing here may decide
	// authorization.
	//
	// Labels 是 Agent 声明的内容。它们是路由据以选择的、由运维赋予的事实，且被原样
	// 采信：被攻破的 Agent 可以声称任何标签，因此这里的任何东西都不能参与授权判断。
	Labels    map[string]string `json:"labels,omitempty"`
	Resources *Resources        `json:"resources,omitempty"`
	// InflightRequests is what the Agent last reported across every replica,
	// not just the one that produced this document.
	//
	// InflightRequests 是 Agent 最近一次上报的、跨全部副本的在途请求数，而不只是
	// 产生本文档的那个副本上的。
	InflightRequests int `json:"inflight_requests"`
	// IdleSlots counts slots parked and ready right now, keyed by slot class.
	// It is the only honest measure of spare capacity on this link.
	//
	// IdleSlots 统计此刻已停放、随时可用的槽位，按槽位 class 分组。它是这条链路上
	// 空余容量唯一诚实的度量。
	IdleSlots map[string]int `json:"idle_slots,omitempty"`
	// DeclaredRuntimeIDs is the allowlist the Agent announced at Hello. A
	// runtime named here but absent from Runtimes is one the Agent declared
	// and has not reported an inventory for.
	//
	// DeclaredRuntimeIDs 是 Agent 在 Hello 时宣告的允许列表。出现在这里却不在
	// Runtimes 中的运行时，是 Agent 声明过但尚未上报清单的那些。
	DeclaredRuntimeIDs []string  `json:"declared_runtime_ids,omitempty"`
	Runtimes           []Runtime `json:"runtimes,omitempty"`
	// CLIUsage is the subscription quota of the Codex / Claude Code CLIs this
	// Agent runs, as it last reported it. It is the CLI's own account quota,
	// shared by every request that CLI serves, and not a per-tenant figure.
	//
	// CLIUsage 是该 Agent 所运行的 Codex / Claude Code CLI 的订阅额度，取自它最近一次
	// 的上报。它是 CLI 自身账户的额度，由该 CLI 服务的所有请求共用，不是按租户划分的数字。
	CLIUsage []CLIUsage `json:"cli_usage,omitempty"`
}

// Resources is the hardware the Agent reported at Hello.
//
// Resources 是 Agent 在 Hello 时上报的硬件情况。
type Resources struct {
	CPUCores       int32  `json:"cpu_cores,omitempty"`
	MemoryBytes    int64  `json:"memory_bytes,omitempty"`
	GPUCount       int32  `json:"gpu_count,omitempty"`
	GPUMemoryBytes int64  `json:"gpu_memory_bytes,omitempty"`
	OS             string `json:"os,omitempty"`
	Arch           string `json:"arch,omitempty"`
}

// Runtime is one inference backend on a node, as the Agent last reported it.
//
// Runtime 是一个节点上的一个推理后端，取自 Agent 最近一次的上报。
type Runtime struct {
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty"`
	// BaseURL is the address the Agent reaches this backend at, on its own
	// network. It is shown because an operator diagnosing a node needs it,
	// and it is not a credential — the API key and custom headers that
	// authenticate to that address live in runtime.Config on the Agent and
	// never reach a Snapshot at all.
	//
	// BaseURL 是 Agent 在自己网络上访问该后端所用的地址。展示它是因为排查节点的运维
	// 需要它，而它不是凭据——用于向那个地址认证的 API key 与自定义头位于 Agent 的
	// runtime.Config 中，根本不会进入 Snapshot。
	BaseURL string `json:"base_url,omitempty"`
	State   string `json:"state"`
	Version string `json:"version,omitempty"`
	// IdentityVerified reports whether the probe confirmed the backend is
	// the kind it was declared to be, rather than merely answering.
	//
	// IdentityVerified 报告探测是否确认了该后端确实是它所声明的那一类，而不只是
	// 「它有响应」。
	IdentityVerified bool `json:"identity_verified"`
	// LatencyMillis is the last health check's round trip. It is absent
	// rather than zero when no check has completed: zero milliseconds is a
	// measurement, and it would be a lie here.
	//
	// LatencyMillis 是最近一次健康检查的往返耗时。没有完成过检查时它缺席而不是为零：
	// 零毫秒是一个测量结果，在这里它会是谎言。
	LatencyMillis *int64     `json:"latency_millis,omitempty"`
	CheckedAt     *time.Time `json:"checked_at,omitempty"`
	// ErrorSummary is the sanitized diagnostic the health check produced.
	// runtime.HealthReport documents that it carries no credentials, headers
	// or payloads.
	//
	// ErrorSummary 是健康检查产生的、已脱敏的诊断信息。runtime.HealthReport 记载了
	// 它不携带凭据、请求头或负载。
	ErrorSummary string `json:"error_summary,omitempty"`
	// Capabilities maps a capability to its support level, including the
	// levels that mean "unknown": a capability nobody has determined must not
	// read as unsupported.
	//
	// Capabilities 把能力映射到它的支持级别，包括那些表示「未知」的级别：一个尚无人
	// 判定过的能力，不能被读成不支持。
	Capabilities map[string]string `json:"capabilities,omitempty"`
	Models       []Model           `json:"models,omitempty"`
	Warnings     []string          `json:"warnings,omitempty"`
	// Degraded holds endpoint degradation notes, e.g. a backend missing its
	// health endpoint. A degraded runtime still serves.
	//
	// Degraded 保存端点降级说明，例如某后端缺少健康检查端点。降级的运行时仍在服务。
	Degraded     []string   `json:"degraded,omitempty"`
	DiscoveredAt *time.Time `json:"discovered_at,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
}

// Model is one model a runtime serves, with the capabilities that model
// supports — which are not always the runtime's own.
//
// Model 是一个运行时所提供的一个模型，以及该模型支持的能力——它们未必等同于运行时
// 自身的能力。
type Model struct {
	ID           string            `json:"id"`
	Capabilities map[string]string `json:"capabilities,omitempty"`
}

// FromSnapshot renders one runtime inventory entry, naming every field that
// may cross. This function is the allowlist: nothing reaches a console that is
// not listed here.
//
// FromSnapshot 渲染一条运行时清单记录，逐一点名可以外传的每个字段。本函数就是那份
// 允许列表：不在这里列出的东西，抵达不了任何控制台。
func FromSnapshot(snapshot runtime.Snapshot) Runtime {
	view := Runtime{
		ID:               snapshot.Descriptor.ID,
		Kind:             string(snapshot.Descriptor.Kind),
		BaseURL:          snapshot.Descriptor.BaseURL,
		State:            string(snapshot.State),
		Version:          firstNonEmpty(snapshot.Discovery.Version, snapshot.Probe.Version),
		IdentityVerified: snapshot.Probe.IdentityVerified,
		ErrorSummary:     snapshot.Health.ErrorSummary,
		Capabilities:     capabilities(snapshot.Discovery.Capabilities),
		Warnings:         append([]string(nil), snapshot.Discovery.Warnings...),
		Degraded:         append([]string(nil), snapshot.Degraded...),
	}
	if !snapshot.Health.CheckedAt.IsZero() {
		checked := snapshot.Health.CheckedAt
		millis := snapshot.Health.Latency.Milliseconds()
		view.CheckedAt = &checked
		view.LatencyMillis = &millis
	}
	if !snapshot.Discovery.DiscoveredAt.IsZero() {
		discovered := snapshot.Discovery.DiscoveredAt
		view.DiscoveredAt = &discovered
	}
	if !snapshot.UpdatedAt.IsZero() {
		updated := snapshot.UpdatedAt
		view.UpdatedAt = &updated
	}

	view.Models = make([]Model, 0, len(snapshot.Discovery.Models))
	for _, model := range snapshot.Discovery.Models {
		view.Models = append(view.Models, Model{
			ID:           model.ID,
			Capabilities: capabilities(model.Capabilities),
		})
	}
	// Sorted so two reads of an unchanged node compare equal, which is what
	// lets a console tell a changed fleet from a reordered map.
	//
	// 排序后，对一个未发生变化的节点做两次读取会比较相等，正是这一点让控制台能分辨
	// 「机群变了」与「map 的顺序变了」。
	sort.Slice(view.Models, func(i, j int) bool { return view.Models[i].ID < view.Models[j].ID })
	return view
}

// capabilities renders a capability set, keeping the levels that mean unknown.
//
// capabilities 渲染一个能力集合，保留那些表示未知的级别。
func capabilities(set runtime.CapabilitySet) map[string]string {
	if len(set) == 0 {
		return nil
	}
	out := make(map[string]string, len(set))
	for capability, evidence := range set {
		// Only the level crosses. The evidence's Detail is a free-text note
		// written by a probe against a backend's response, and this view is
		// not the place to find out what a backend put in it.
		//
		// 只有级别外传。证据中的 Detail 是探测器对着后端响应写下的自由文本，而这个
		// 视图不是用来查明某个后端在里面放了什么的地方。
		out[string(capability)] = string(evidence.Level)
	}
	return out
}

// firstNonEmpty picks the first value that says something.
//
// firstNonEmpty 取第一个有内容的值。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// Limits on what one node may report as CLI quota. The data is untrusted Agent
// input on its way to a browser, so it is bounded and checked against a closed
// vocabulary here rather than rendered as received.
//
// 一个节点可上报的 CLI 额度的上限。这些数据是前往浏览器途中的、不受信任的 Agent 输入，
// 因此在这里做数量限制并对照封闭词表检查，而不是原样渲染。
const (
	maxCLIUsageAccounts = 8
	maxCLIUsageWindows  = 8
	maxCLIUsageLabel    = 64
	// maxCLIUsageRuntimeID bounds a runtime ID. Unlike a label it is never
	// truncated, since a console pairs it with a service by exact match; an
	// account whose ID is longer is dropped instead.
	// maxCLIUsageRuntimeID 限定运行时 ID 的长度。与标签不同，它从不截断，因为控制台按
	// 精确匹配把它与服务配对；ID 更长的账户直接丢弃。
	maxCLIUsageRuntimeID = 256
)

// cliUsageStatuses is the closed set of account states a console may show.
//
// cliUsageStatuses 是控制台可以显示的账户状态的封闭集合。
var cliUsageStatuses = map[string]bool{
	"pending": true, "ready": true, "unauthorized": true, "unavailable": true, "error": true,
}

// CLIUsage is one CLI's subscription quota. RuntimeID names the service whose
// login it is, which is how a console pairs it with that service when one node
// runs several accounts of the same kind; it is empty from an Agent that
// predates multi-account support, and a console then pairs by Kind ("codex" or
// "claude") alone.
//
// CLIUsage 是一个 CLI 的订阅额度。RuntimeID 指明它是哪个服务的登录，一个节点运行
// 同一类型的多个账户时，控制台据此把它与对应服务配对；来自尚不支持多账户的旧版 Agent
// 时为空，控制台此时只按 Kind（"codex" 或 "claude"）配对。
type CLIUsage struct {
	RuntimeID string           `json:"runtime_id,omitempty"`
	Kind      string           `json:"kind"`
	Status    string           `json:"status"`
	Windows   []CLIUsageWindow `json:"windows,omitempty"`
	// CheckedAt is when the Agent last queried the CLI. Absent before the first
	// query, and absent is not "a long time ago".
	//
	// CheckedAt 是 Agent 最近一次查询该 CLI 的时刻。首次查询之前缺席，而缺席不等于
	// 「很久以前」。
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

// CLIUsageWindow is one upstream-reported quota window. UsedPercent is what the
// upstream reported, a real zero included; it is not derived from token counts.
//
// CLIUsageWindow 是上游报告的一个额度窗口。UsedPercent 是上游报告的值，真实的零也包括
// 在内；它不是由 token 数量推算出来的。
type CLIUsageWindow struct {
	Name            string     `json:"name"`
	Bucket          string     `json:"bucket,omitempty"`
	UsedPercent     float64    `json:"used_percent"`
	DurationMinutes int64      `json:"duration_minutes,omitempty"`
	ResetsAt        *time.Time `json:"resets_at,omitempty"`
}

// CLIUsageFromAccounts renders the Agent-reported quota through the allowlist:
// only the two supported kinds and the known statuses survive, counts and label
// lengths are capped, an account with an oversized or invalid runtime ID is
// dropped, and a percentage outside 0–100 (or not a number) is
// clamped. The result is sorted by kind, then runtime ID.
//
// CLIUsageFromAccounts 经由允许列表渲染 Agent 上报的额度：只有两种受支持的 kind 与已知
// 状态能保留，数量与标签长度有上限，运行时 ID 过长或非法的账户被丢弃，超出 0–100（或不是数字）的百分比会被夹住。结果按
// kind、再按运行时 ID 排序。
func CLIUsageFromAccounts(accounts []cliusagestatus.Account) []CLIUsage {
	var out []CLIUsage
	for _, account := range accounts {
		if len(out) == maxCLIUsageAccounts {
			break
		}
		if (account.Kind != runtime.KindCodex && account.Kind != runtime.KindClaude) ||
			len(account.RuntimeID) > maxCLIUsageRuntimeID || !utf8.ValidString(account.RuntimeID) {
			continue
		}
		view := CLIUsage{RuntimeID: account.RuntimeID, Kind: string(account.Kind), Status: "error"}
		if cliUsageStatuses[account.Status] {
			view.Status = account.Status
		}
		if !account.CheckedAt.IsZero() {
			checked := account.CheckedAt.UTC()
			view.CheckedAt = &checked
		}
		for _, window := range account.Windows {
			if len(view.Windows) == maxCLIUsageWindows {
				break
			}
			item := CLIUsageWindow{
				Name:            truncateLabel(window.Name),
				Bucket:          truncateLabel(window.Bucket),
				UsedPercent:     clampPercent(window.UsedPercent),
				DurationMinutes: max(window.DurationMinutes, 0),
			}
			if !window.ResetsAt.IsZero() {
				resets := window.ResetsAt.UTC()
				item.ResetsAt = &resets
			}
			view.Windows = append(view.Windows, item)
		}
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].RuntimeID < out[j].RuntimeID
	})
	return out
}

// truncateLabel caps a label at maxCLIUsageLabel bytes on a rune boundary.
//
// truncateLabel 把标签截断到 maxCLIUsageLabel 字节以内，并落在 rune 边界上。
func truncateLabel(label string) string {
	if len(label) <= maxCLIUsageLabel {
		return label
	}
	return strings.ToValidUTF8(label[:maxCLIUsageLabel], "")
}

// clampPercent maps NaN to 0 and bounds the value to [0, 100].
//
// clampPercent 把 NaN 映射为 0，并把数值限制在 [0, 100]。
func clampPercent(value float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	return min(max(value, 0), 100)
}
