// Package codexcli serves inference and probes a locally authenticated Codex app-server over stdio.
// Package codexcli 通过 stdio 提供本机已登录的 Codex app-server 推理及接入验证。
package codexcli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrAuthentication means the CLI did not report a ChatGPT login.
	// ErrAuthentication 表示 CLI 未报告 ChatGPT 登录。
	ErrAuthentication = errors.New("codexcli: ChatGPT login unavailable")
	// ErrProtocol means a bounded RPC frame or its correlation was invalid.
	// ErrProtocol 表示有界 RPC 帧或其关联信息无效。
	ErrProtocol = errors.New("codexcli: invalid, truncated, or oversized protocol stream")
	// ErrRPC means app-server rejected a request; its raw message is withheld.
	// ErrRPC 表示 app-server 拒绝请求，不暴露原始错误正文。
	ErrRPC = errors.New("codexcli: app-server rejected a request")
	// ErrUnexpectedTool means an unapproved tool or approval request appeared.
	// ErrUnexpectedTool 表示出现未批准的工具或审批请求。
	ErrUnexpectedTool = errors.New("codexcli: unexpected tool or approval request")
	// ErrIsolation means app-server loaded unapproved instruction files.
	// ErrIsolation 表示 app-server 加载了未获准的指令文件。
	ErrIsolation = errors.New("codexcli: app-server loaded unapproved instruction files")
	// ErrTurn means the synthetic round trip did not complete successfully.
	// ErrTurn 表示合成工具往返未成功完成。
	ErrTurn = errors.New("codexcli: turn or synthetic tool verification failed")
	// ErrConfig means local probe configuration exceeds the supported bounds.
	// ErrConfig 表示本地实验配置超出支持范围。
	ErrConfig = errors.New("codexcli: timeout must be positive and at most two minutes")
	// ErrProcess means the isolated CLI process could not be prepared or started.
	// ErrProcess 表示无法准备或启动隔离 CLI 进程。
	ErrProcess = errors.New("codexcli: could not start isolated app-server")
)

// Config selects a trusted local executable and an optional model override.
// Config 指定可信本地可执行文件及可选模型覆盖。
type Config struct {
	// Executable defaults to the codex executable found on PATH.
	// Executable 默认使用 PATH 中的 codex 可执行文件。
	Executable string
	// Model preserves the CLI's configured model when empty.
	// Model 为空时保留 CLI 已配置的模型。
	Model string
	// Timeout defaults to 90 seconds and is capped at two minutes.
	// Timeout 默认为 90 秒，上限为两分钟。
	Timeout time.Duration
}

// Report contains only non-sensitive evidence of a synthetic tool round trip.
// Report 只包含合成工具往返的非敏感证据。
type Report struct {
	// Stage identifies the last protocol phase without including raw RPC data.
	// Stage 标识最后的协议阶段，不包含原始 RPC 数据。
	Stage string `json:"stage"`
	// InstructionFiles counts loaded instruction files without revealing paths.
	// InstructionFiles 统计已加载指令文件，不暴露路径。
	InstructionFiles int `json:"instruction_files"`
	// TurnStatus is a validated terminal turn status.
	// TurnStatus 是经过校验的回合结束状态。
	TurnStatus string `json:"turn_status"`
	// FailureCode is a public error category, never the upstream error message.
	// FailureCode 是公开错误类别，不包含上游错误正文。
	FailureCode string `json:"failure_code,omitempty"`
	// Passed requires successful tools, a consumed random token, and a completed turn.
	// Passed 要求工具成功、随机值被消费且回合完成。
	Passed bool `json:"codex_probe_passed"`
	// Model is the model reported by thread/start, not an entitlement assertion.
	// Model 是 thread/start 返回的模型，不代表账户有权调用的声明。
	Model string `json:"model"`
	// Events counts bounded RPC frames received during the probe.
	// Events 统计实验收到的有界 RPC 帧。
	Events int `json:"events"`
	// ToolCalls counts accepted synthetic dynamic-tool requests.
	// ToolCalls 统计已接受的合成动态工具请求。
	ToolCalls int `json:"tool_calls"`
	// ClientTokenSeen records whether an assistant message consumed the tool result.
	// ClientTokenSeen 记录助手消息是否消费了工具结果中的随机值。
	ClientTokenSeen bool `json:"client_token_seen"`
}

// Probe runs one isolated two-tool experiment using the CLI's existing login.
// It does not expose a Gateway runtime or implement the Responses API.
// Probe 使用 CLI 现有登录执行一次隔离的双工具实验。
// 它不注册 Gateway 运行时，也不实现 Responses API。
func Probe(ctx context.Context, cfg Config) (Report, error) {
	return probeWithEnv(ctx, cfg, probeEnv(os.Environ()))
}

func probeWithEnv(ctx context.Context, cfg Config, env []string) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 90 * time.Second
	}
	if cfg.Timeout < 0 || cfg.Timeout > 2*time.Minute {
		return Report{}, ErrConfig
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if cfg.Executable == "" {
		cfg.Executable = "codex"
	}
	path, err := exec.LookPath(cfg.Executable)
	if err != nil {
		return Report{}, ErrProcess
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return Report{}, ErrProcess
	}
	dir, err := os.MkdirTemp("", "aisw-codex-probe-")
	if err != nil {
		return Report{}, ErrProcess
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, path, appServerArgs(dir)...)
	cmd.Dir, cmd.Env, cmd.Stderr = dir, env, io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := configureProcess(cmd); err != nil {
		return Report{}, err
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return Report{}, ErrProcess
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return Report{}, ErrProcess
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		return Report{}, ErrProcess
	}
	// App-server stays alive after a turn; always stop its entire process group.
	// app-server 在回合后继续驻留，因此退出时始终终止整个进程组。
	defer func() { _ = cmd.Cancel(); _ = input.Close(); _ = output.Close(); _ = cmd.Wait() }()
	report, err := runProtocol(output, input, dir, cfg.Model, accountInstructions(env))
	if ctx.Err() != nil {
		report.Passed = false
		return report, ctx.Err()
	}
	return report, err
}

func accountInstructions(env []string) []string {
	var home, codexDir string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key == "HOME" {
			home = value
		}
		if key == "CODEX_HOME" {
			codexDir = value
		}
	}
	if codexDir == "" && home != "" {
		codexDir = filepath.Join(home, ".codex")
	}
	if !filepath.IsAbs(codexDir) {
		return nil
	}
	return []string{filepath.Join(codexDir, "AGENTS.md"), filepath.Join(codexDir, "AGENTS.override.md")}
}

func appServerArgs(dir string) []string {
	args := []string{"app-server", "--listen", "stdio://"}
	// Keep code_mode_host available: disabling it also hides client dynamic tools.
	// 保留 code_mode_host：关闭它也会让客户端动态工具不可见。
	for _, feature := range []string{"shell_tool", "unified_exec", "shell_snapshot", "multi_agent", "apps", "plugins", "hooks", "skill_search", "skill_mcp_dependency_install", "image_generation", "view_image", "code_mode"} {
		args = append(args, "--disable", feature)
	}
	args = append(args, "--enable", "skip_host_skill_discovery")
	for _, value := range []string{`mcp_servers={}`, `hooks={}`, `plugins={}`, `web_search="disabled"`, `tools.view_image=false`, `project_doc_max_bytes=0`,
		`history.persistence="none"`, `analytics.enabled=false`, `forced_login_method="chatgpt"`, "sqlite_home=" + strconv.Quote(dir), "log_dir=" + strconv.Quote(dir)} {
		args = append(args, "-c", value)
	}
	return args
}

func probeEnv(source []string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "LC_ALL": true, "TMPDIR": true, "CODEX_HOME": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true}
	var out []string
	for _, entry := range source {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			out = append(out, entry)
		}
	}
	return out
}
