package claudecode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

var (
	errAuthCheck     = errors.New("CLI authentication check failed; output withheld")
	errLoginRequired = errors.New("CLI subscription login unavailable in this execution context")
	errProbeSetup    = errors.New("could not create isolated CLI resources")
	errCLIProtocol   = errors.New("invalid or oversized CLI event stream")
	errCLIResult     = errors.New("CLI reported an unsuccessful result")
	errMissingResult = errors.New("CLI stream ended without a successful result")
	errUnsafeTools   = errors.New("CLI advertised an unexpected tool")
	errCLIStart      = errors.New("CLI process could not start")
	errCLIExit       = errors.New("CLI process exited unsuccessfully; output withheld")
	errCapacity      = errors.New("CLI session capacity exceeded")
	errClosed        = errors.New("CLI session closed")
	errCallRejected  = errors.New("CLI tool result rejected")
	// errLoginRefreshBusy means the CLI could not refresh its login because
	// another process sharing the account directory was refreshing it; Claude
	// Code calls this transient.
	// errLoginRefreshBusy 表示 CLI 无法刷新登录，因为共用账户目录的另一个进程正在刷新；
	// Claude Code 认为这是暂时性的。
	errLoginRefreshBusy = errors.New("CLI login refresh held by another process")
	// errQuotaExhausted is what a quotaError matches with errors.Is.
	// errQuotaExhausted 是 quotaError 用 errors.Is 匹配的哨兵。
	errQuotaExhausted = errors.New("CLI account usage limit reached")
)

// quotaError is a round the CLI's account limit rejected, as its
// rate_limit_event reported. resetsAt is when that limit resets, zero when the
// event did not say.
//
// quotaError 表示被 CLI 账户限额拒绝的回合，依据其 rate_limit_event 的报告。resetsAt 是
// 该限额重置的时刻，事件没有说明时为零值。
type quotaError struct {
	resetsAt time.Time
	// scope is the model the refusal covers, "" for the whole account (see
	// quotaScope).
	// scope 是该拒绝覆盖的模型，整个账户时为 ""（见 quotaScope）。
	scope string
}

func (e *quotaError) Error() string { return errQuotaExhausted.Error() }
func (e *quotaError) Unwrap() error { return errQuotaExhausted }

// quotaScope is the model a refusal of limit covers, for
// runtime.QuotaGuard.Refused: "" when the limit is the whole account's — the
// five-hour and seven-day windows, and the extra-usage allowance that only
// applies once those are spent — and model itself for a per-family window or
// a limit this Agent does not know, so an unknown name never holds models it
// may not cover.
//
// quotaScope 是对 limit 的一次拒绝所覆盖的模型，供 runtime.QuotaGuard.Refused 使用：限额
// 属于整个账户时为 ""——五小时与七天窗口，以及只在它们用完后才生效的额外用量——按模型
// 系列划分的窗口或本 Agent 不认识的限额则为 model 本身，因此陌生的名称从不搁置它可能并不
// 覆盖的模型。
func quotaScope(limit, model string) string {
	switch limit {
	case "five_hour", "seven_day", "overage":
		return ""
	}
	return model
}

// cliStopGrace is how long a canceled inference CLI has to handle SIGTERM
// before its process group is killed. Claude Code 2.1.326 exits about 0.55 s
// after SIGTERM, so this rarely runs out; it lets a CLI canceled while
// refreshing its login save the new token instead of losing it.
// cliStopGrace 是被取消的推理 CLI 在进程组被杀之前处理 SIGTERM 的时间。Claude Code 2.1.326
// 收到 SIGTERM 后约 0.55 秒退出，因此很少用完；它让在刷新登录时被取消的 CLI 能保存新令牌，
// 而不是将其丢失。
const cliStopGrace = 2 * time.Second

// cliStderrNote attaches the fixed category of a failed CLI's standard error
// (see cliprocess.StderrTail) to its failure; the text never leaves the Agent.
// cliStderrNote 把失败 CLI 标准错误的固定分类（见 cliprocess.StderrTail）附加到其失败上；
// 文本从不离开 Agent。
type cliStderrNote struct {
	error
	category string
}

func (n *cliStderrNote) Unwrap() error { return n.error }

// withStderr attaches stderr's category to err when the CLI wrote anything.
// withStderr 在 CLI 写过任何内容时，把 stderr 的分类附加到 err 上。
func withStderr(err error, stderr *cliprocess.StderrTail) error {
	if category := stderr.Category(); category != "" {
		return &cliStderrNote{error: err, category: category}
	}
	return err
}

// cliStartupFailure is a result in which the CLI named why it refused to
// start (startup_failure_reason, see CLAUDE_CODE_STARTUP_FAILURE_RESULTS);
// reason is reduced to a bounded identifier.
// cliStartupFailure 是 CLI 在其中说明拒绝启动原因的结果（startup_failure_reason，见
// CLAUDE_CODE_STARTUP_FAILURE_RESULTS）；reason 被限制为有界标识符。
type cliStartupFailure struct{ reason string }

func (e *cliStartupFailure) Error() string { return errCLIResult.Error() }
func (e *cliStartupFailure) Unwrap() error { return errCLIResult }

// startupFailureReason keeps reason only when it is a short lower-case
// identifier, the shape of every documented value, and "other" otherwise.
// startupFailureReason 仅在 reason 是较短的小写标识符（所有已记录取值的形状）时保留它，
// 否则为 "other"。
func startupFailureReason(reason string) string {
	if reason == "" {
		return ""
	}
	if len(reason) > 64 {
		return "other"
	}
	for _, c := range reason {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return "other"
		}
	}
	return reason
}

// withAccountDir points env's CLAUDE_CONFIG_DIR at dir, replacing any
// inherited value; an empty dir returns env unchanged.
// withAccountDir 把 env 中的 CLAUDE_CONFIG_DIR 指向 dir，替换继承来的值；dir 为空时
// 原样返回。
func withAccountDir(env []string, dir string) []string {
	if dir == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !cliprocess.KeyEqual(strings.SplitN(entry, "=", 2)[0], "CLAUDE_CONFIG_DIR") {
			out = append(out, entry)
		}
	}
	return append(out, "CLAUDE_CONFIG_DIR="+dir)
}

// maxEventBytes bounds one line the local CLI writes and one MCP request it
// sends. The CLI is a trusted local process whose output its own token limit
// already bounds, so this only stops a broken process: an assistant line
// carrying a long answer, or a tool call writing a large file, stays far below.
//
// maxEventBytes 限制本机 CLI 写出的一行及它发出的一个 MCP 请求。CLI 是受信的本机进程，
// 输出已受其自身 token 上限约束，因此这里只拦住异常进程：携带长回答的 assistant 行、
// 写入大文件的工具调用都远低于该值。
const maxEventBytes = 256 << 20

// localCLITool reports whether name is a tool the local CLI registers itself
// (EndConversation, or the bridge's MCP tools), which it could run on this
// machine. Such a call is refused unless the request declared it.
//
// localCLITool 报告 name 是否为本机 CLI 自行注册、可能在本机执行的工具（EndConversation
// 或桥接的 MCP 工具）。除非请求声明了它，否则这类调用一律拒绝。
func localCLITool(name string) bool {
	return name == "EndConversation" || strings.HasPrefix(name, "mcp__")
}

type principal struct{ tenant, key string }
type toolResult struct {
	Content []messageBlock
	IsError bool
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system entropy unavailable")
	}
	return hex.EncodeToString(b[:])
}

type boundedCapture struct {
	buffer bytes.Buffer
	limit  int
}

// Write rejects excess data without exposing ReaderFrom's unbounded fast path.
// Write 拒绝超量数据，避免暴露 ReaderFrom 无界复制的快速路径。
func (b *boundedCapture) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, errAuthCheck
	}
	return b.buffer.Write(p)
}

func checkAuth(ctx context.Context, program string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "aisw-claude-auth-")
	if err != nil {
		return errProbeSetup
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, program, "auth", "status", "--json")
	var output = boundedCapture{limit: 64 * 1024}
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, &output, io.Discard
	cmd.WaitDelay = time.Second
	if err := cliprocess.Configure(cmd); err != nil {
		return err
	}
	defer func() { _ = cmd.Cancel() }()
	err = cliprocess.Run(cmd)
	var status struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if json.Unmarshal(output.buffer.Bytes(), &status) != nil {
		return errAuthCheck
	}
	if !status.LoggedIn || status.AuthMethod != "claude.ai" {
		return errLoginRequired
	}
	if err != nil {
		return errAuthCheck
	}
	return nil
}

func probeArgs(model, configPath string) []string {
	return []string{"--print", "--model", model, "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--restricted", "--tools", "", "--allowedTools", "mcp__aisw_probe__probe_echo", "--permission-mode", "dontAsk",
		"--strict-mcp-config", "--mcp-config", configPath, "--setting-sources", "", "--settings", `{"disableAllHooks":true,"autoMemoryEnabled":false}`,
		"--disable-slash-commands", "--no-chrome", "--no-session-persistence", "--system-prompt", "You are a protocol test assistant. Use only the provided probe tool; do not access any files or commands."}
}

// cliDefaults are set on every CLI process the Agent starts, after the
// inherited variables are filtered (which would drop them even when the
// Agent's own environment sets them):
//
//   - DISABLE_AUTOUPDATER keeps a CLI the Agent started from replacing the
//     installed version under requests still running; upgrades go through
//     the Agent's re-verification instead.
//   - CLAUDE_CODE_OAUTH_401_WAIT_MS lets a process whose access token another
//     process has just rotated wait for the new token in the credential store
//     instead of failing the round. Several CLI processes share one account
//     directory here, and its refresh token works only once. The variable is
//     not in the official documentation (it is read by Claude Code 2.1.326),
//     so a CLI that ignores it behaves as before. 30 seconds covers a
//     concurrent refresh while bounding the extra wait when a login has
//     really expired.
//   - CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK stops the CLI from retrying a
//     failed stream as a non-streaming request. A native round's document
//     reaches only the streamed request (see upstreamProxy), so the fallback
//     would send the CLI's placeholder prompt, spend quota and still fail.
//   - CLAUDE_CODE_STARTUP_FAILURE_RESULTS makes a CLI that refuses to start
//     say why in a result (startup_failure_reason) instead of on stderr only.
//
// cliDefaults 设置在 Agent 启动的每个 CLI 进程上，位于继承变量过滤之后（过滤会丢掉它们，
// 即使 Agent 自己的环境设置了）：
//
//   - DISABLE_AUTOUPDATER 防止 Agent 启动的 CLI 在仍有请求运行时替换已安装的版本；升级改走
//     Agent 的复验流程。
//   - CLAUDE_CODE_OAUTH_401_WAIT_MS 让访问令牌刚被另一个进程轮换的进程在凭据存储中等待新
//     令牌，而不是让回合失败。这里多个 CLI 进程共用一个账户目录，而其刷新令牌只能使用一次。
//     该变量不在官方文档中（Claude Code 2.1.326 会读取它），不认识它的 CLI 行为不变。30 秒
//     足以覆盖并发刷新，同时限制登录确实过期时多出的等待。
//   - CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK 阻止 CLI 把失败的流式请求改为非流式重试。
//     原生回合的文档只进入流式请求（见 upstreamProxy），回退会发出 CLI 的占位提示词，消耗
//     额度后仍然失败。
//   - CLAUDE_CODE_STARTUP_FAILURE_RESULTS 让拒绝启动的 CLI 在结果中说明原因
//     （startup_failure_reason），而不是只写到 stderr。
//
// Telemetry switches are not set here, so probes such as the account-usage
// read keep fetching feature flags and refresh the CLI's disk cache. A claude
// runtime's round processes get DISABLE_TELEMETRY separately (see
// withoutCLITelemetry), reading flags from that cache.
// CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC is never set.
// 这里不设置遥测开关，额度查询等探测进程因此照常拉取功能开关并刷新 CLI 的磁盘缓存。claude
// 运行时的回合进程另行设置 DISABLE_TELEMETRY（见 withoutCLITelemetry），从该缓存读取功能开关。
// 从不设置 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC。
var cliDefaults = []string{"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_OAUTH_401_WAIT_MS=30000",
	"CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK=1", "CLAUDE_CODE_STARTUP_FAILURE_RESULTS=1"}

func probeEnv(source []string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "LC_ALL": true, "TMPDIR": true, "CLAUDE_CONFIG_DIR": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true}
	var out []string
	for _, entry := range source {
		key, _, ok := strings.Cut(entry, "=")
		if ok && cliprocess.AllowedKey(key, allowed) {
			out = append(out, entry)
		}
	}
	return append(out, cliDefaults...)
}
