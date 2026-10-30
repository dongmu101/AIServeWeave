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
	// errSessionGone means no retained session holds any submitted tool ID,
	// so the CLI that asked for them has exited, expired or never ran here.
	// errSessionGone 表示没有任何保留中的会话持有所提交的工具 ID：发起这些调用的
	// CLI 已退出、已过期，或从未在本进程中运行过。
	errSessionGone = errors.New("CLI tool session no longer exists")
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
}

func (e *quotaError) Error() string { return errQuotaExhausted.Error() }
func (e *quotaError) Unwrap() error { return errQuotaExhausted }

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

const (
	maxPending    = 8
	maxCalls      = 64
	maxEventBytes = 1 << 20
)

type principal struct{ tenant, key string }
type toolResult struct {
	Text    string
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
	return out
}
