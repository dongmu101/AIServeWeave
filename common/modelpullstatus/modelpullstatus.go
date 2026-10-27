// Package modelpullstatus is the wire contract for STATUS.md's P2 model
// distribution subtask 2: the Agent-to-Gateway status of a model pull, and
// the vocabulary a Gateway-to-Agent trigger names by. It carries no source
// URL and no raw error text.
//
// Both exclusions are deliberate, not oversights. tunnel.proto's
// load-bearing rule forbids a message that expresses "fetch this URL", so a
// Status can only ever report on a name the Agent already resolved locally
// (service/aiServeWeaveAgent/modelpull) — never the URL behind it. And a
// Spec.SourceURL can be a presigned URL carrying a signature token in its
// query string; Go's http.Client embeds the full request URL in a transport
// error's text, so Reason is a closed vocabulary instead of the raw error —
// the same reason STATUS.md's P09 request_logs uses a closed outcome enum
// rather than logging a request's raw error text.
//
// modelpullstatus 是 STATUS.md P2 模型分发子任务二的契约：Agent 到 Gateway
// 的拉取状态，以及 Gateway 到 Agent 的触发指令所用的词表。它不携带来源
// URL，也不携带原始错误文本。
//
// 两处排除都是刻意的，不是疏漏。tunnel.proto 的 load-bearing 规则禁止表达
// "fetch this URL" 的消息，因此 Status 只能报告一个 Agent 已经在本地解析过
// 的名字（service/aiServeWeaveAgent/modelpull）——从不是它背后的 URL。而
// Spec.SourceURL 可能是带签名 token 查询串的预签名 URL；Go 的 http.Client
// 会把完整请求 URL 编进传输错误的文本里，所以 Reason 是一个封闭词表而不是
// 原始错误——与 STATUS.md P09 的 request_logs 用封闭 outcome 枚举而不是记录
// 请求原始错误文本，是同一个理由。
package modelpullstatus

import "time"

// State is where one named pull currently stands.
//
// State 是某一个命名拉取当前所处的阶段。
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
	case StateDownloading:
		return "downloading"
	case StateDone:
		return "done"
	case StateFailed:
		return "failed"
	default:
		return "unspecified"
	}
}

const (
	// StateUnspecified is the zero value: known to the Agent's local
	// manifest but never triggered.
	//
	// StateUnspecified 是零值：Agent 本地清单已知，但从未被触发过。
	StateUnspecified State = iota
	// StatePending means the name is queued behind another in-flight pull.
	//
	// StatePending 表示该名字排在另一个在途拉取之后等待。
	StatePending
	// StateDownloading means bytes are actively being fetched.
	//
	// StateDownloading 表示正在主动获取字节。
	StateDownloading
	// StateDone means the target file exists and its checksum matched.
	//
	// StateDone 表示目标文件已存在且校验和匹配。
	StateDone
	// StateFailed means the pull stopped short; Reason narrows why.
	//
	// StateFailed 表示拉取中途停止；Reason 说明具体原因。
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
	case ReasonUnknownName:
		return "unknown_name"
	case ReasonInvalidSpec:
		return "invalid_spec"
	case ReasonNotAllowlisted:
		return "not_allowlisted"
	case ReasonQuotaExceeded:
		return "quota_exceeded"
	case ReasonFetchFailed:
		return "fetch_failed"
	case ReasonUnexpectedStatus:
		return "unexpected_http_status"
	case ReasonChecksumMismatch:
		return "checksum_mismatch"
	case ReasonStorageError:
		return "storage_error"
	case ReasonOllamaUnconfigured:
		return "ollama_unconfigured"
	case ReasonOllamaPullFailed:
		return "ollama_pull_failed"
	case ReasonLedgerQuotaExceeded:
		return "ledger_quota_exceeded"
	case ReasonDiskSpaceLow:
		return "disk_space_low"
	default:
		return "unspecified"
	}
}

const (
	// ReasonUnspecified is the zero value: no failure, or a failure whose
	// reason predates this vocabulary.
	//
	// ReasonUnspecified 是零值：没有失败，或者失败发生在这套词表之前。
	ReasonUnspecified FailureReason = iota
	// ReasonUnknownName means the triggered name is not in the Agent's
	// local manifest.
	//
	// ReasonUnknownName 表示被触发的名字不在 Agent 本地清单里。
	ReasonUnknownName
	// ReasonInvalidSpec means the manifest entry itself is malformed.
	//
	// ReasonInvalidSpec 表示清单条目本身格式不对。
	ReasonInvalidSpec
	// ReasonNotAllowlisted means the entry's source URL falls outside the
	// Agent's local allowlist.
	//
	// ReasonNotAllowlisted 表示该条目的来源 URL 不在 Agent 本地白名单内。
	ReasonNotAllowlisted
	// ReasonQuotaExceeded means the worker session's byte budget ran out.
	//
	// ReasonQuotaExceeded 表示这次 worker session 的字节预算已耗尽。
	ReasonQuotaExceeded
	// ReasonFetchFailed means the HTTP request itself failed (DNS, dial,
	// TLS, a canceled context, ...). The underlying error, which may embed
	// the source URL, stays in the Agent's own log and never crosses here.
	//
	// ReasonFetchFailed 表示 HTTP 请求本身失败（DNS、拨号、TLS、被取消的
	// context 等）。可能带有来源 URL 的底层错误只留在 Agent 自己的日志
	// 里，从不会传到这里。
	ReasonFetchFailed
	// ReasonUnexpectedStatus means the server answered with neither 200 nor
	// 206.
	//
	// ReasonUnexpectedStatus 表示服务端既没有回 200 也没有回 206。
	ReasonUnexpectedStatus
	// ReasonChecksumMismatch means the fetched bytes did not hash to the
	// declared SHA256.
	//
	// ReasonChecksumMismatch 表示获取到的字节校验和与声明的 SHA256 不符。
	ReasonChecksumMismatch
	// ReasonStorageError means a local filesystem operation (create,
	// write, rename) failed.
	//
	// ReasonStorageError 表示本地文件系统操作（创建、写入、改名）失败。
	ReasonStorageError
	// ReasonOllamaUnconfigured means a kind=ollama spec was triggered with
	// no Ollama base URL configured (the Agent's -ollama-url flag is
	// empty), so there is no server to ask for the pull.
	//
	// ReasonOllamaUnconfigured 表示一个 kind=ollama 的 spec 被触发时没有配置
	// Ollama base URL（Agent 的 -ollama-url flag 为空），因而没有可询问的
	// 服务器。
	ReasonOllamaUnconfigured
	// ReasonOllamaPullFailed means the Ollama server itself reported an
	// error partway through the pull (e.g. an unknown model tag). The raw
	// error text stays in the Agent's own log and never crosses here, the
	// same restraint ReasonFetchFailed uses.
	//
	// ReasonOllamaPullFailed 表示 Ollama 服务器自己在拉取过程中报告了一个
	// 错误（例如未知的模型 tag）。原始错误文本只留在 Agent 自己的日志里，
	// 从不会传到这里，与 ReasonFetchFailed 同一种克制。
	ReasonOllamaPullFailed
	// ReasonLedgerQuotaExceeded means the cross-restart cumulative ledger
	// (subtask 4, not the single-call Config.QuotaBytes budget) has no room
	// left for this pull's bytes.
	//
	// ReasonLedgerQuotaExceeded 表示跨重启的累计账本（子任务四，不是单次调
	// 用的 Config.QuotaBytes 预算）已经没有余量容纳这次拉取的字节。
	ReasonLedgerQuotaExceeded
	// ReasonDiskSpaceLow means the target filesystem's free space fell below
	// Config.DiskFreeMarginBytes (subtask 4's secondary defense beyond any
	// quota).
	//
	// ReasonDiskSpaceLow 表示目标文件系统的剩余空间低于
	// Config.DiskFreeMarginBytes（子任务四在配额之外的二次防线）。
	ReasonDiskSpaceLow
)

// Status is one named pull's current state, as the Agent reports it and the
// Gateway last observed it.
//
// Status 是某一个命名拉取的当前状态，即 Agent 上报、Gateway 最后一次观测到
// 的样子。
type Status struct {
	Name            string
	State           State
	BytesDownloaded int64
	// BytesTotal is 0 when the manifest entry's size is unknown.
	//
	// BytesTotal 在清单条目大小未知时为 0。
	BytesTotal int64
	// Reason is set only when State is StateFailed.
	//
	// Reason 只在 State 为 StateFailed 时才会被设置。
	Reason    FailureReason
	UpdatedAt time.Time
}
