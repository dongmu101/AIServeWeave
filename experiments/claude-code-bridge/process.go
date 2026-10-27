package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"
)

var (
	errCLIExit  = errors.New("CLI process exited unsuccessfully; output withheld")
	errCLIStart = errors.New("CLI process could not start")
)

func runCLI(ctx context.Context, executable string, args []string, dir string, env []string, input io.Reader, expected string) (eventSummary, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stderr = dir, env, input, io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := configureProcess(cmd); err != nil {
		return eventSummary{}, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return eventSummary{}, errCLIStart
	}
	if err := cmd.Start(); err != nil {
		_ = out.Close()
		if ctx.Err() != nil {
			return eventSummary{}, ctx.Err()
		}
		return eventSummary{}, errCLIStart
	}
	// Reap descendants even when the CLI parent exits before cancellation.
	// 即使 CLI 父进程先自行退出，也回收仍在同组中的后代。
	defer func() { _ = cmd.Cancel() }()
	summary, parseErr := inspectEvents(out, expected)
	if parseErr != nil {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return summary, ctx.Err()
	}
	if parseErr != nil {
		return summary, parseErr
	}
	if waitErr != nil {
		return summary, errCLIExit
	}
	return summary, nil
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
