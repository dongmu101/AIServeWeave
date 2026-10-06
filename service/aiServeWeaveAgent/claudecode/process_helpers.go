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
)

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
	if err := configureProcess(cmd); err != nil {
		return err
	}
	defer func() { _ = cmd.Cancel() }()
	err = cmd.Run()
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
		if ok && allowed[key] {
			out = append(out, entry)
		}
	}
	return out
}
