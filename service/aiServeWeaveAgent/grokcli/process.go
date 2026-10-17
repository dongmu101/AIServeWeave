package grokcli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	// ErrAuthentication means the CLI reported that no account is signed in.
	// ErrAuthentication 表示 CLI 报告没有已登录的账户。
	ErrAuthentication = errors.New("grokcli: Grok CLI is not signed in")
	// ErrProtocol means CLI output was invalid, truncated or exceeded a bound.
	// ErrProtocol 表示 CLI 输出无效、被截断或超出上限。
	ErrProtocol = errors.New("grokcli: invalid, truncated, or oversized CLI output")
	// ErrRPC means the CLI rejected a discovery request; its message is withheld.
	// ErrRPC 表示 CLI 拒绝了发现请求，不暴露原始错误正文。
	ErrRPC = errors.New("grokcli: CLI rejected a request")
	// ErrUnexpectedTool means the CLI attempted a tool call, which this adapter never allows.
	// ErrUnexpectedTool 表示 CLI 试图调用工具，本适配器从不允许。
	ErrUnexpectedTool = errors.New("grokcli: CLI attempted a tool call")
	// ErrTurn means the headless turn failed; CLI output is withheld.
	// ErrTurn 表示 headless 回合失败，不暴露 CLI 输出。
	ErrTurn = errors.New("grokcli: CLI turn failed; output withheld")
	// ErrProcess means the isolated CLI process could not be prepared or started.
	// ErrProcess 表示无法准备或启动隔离的 CLI 进程。
	ErrProcess = errors.New("grokcli: could not prepare or start isolated CLI process")
	// ErrRequest means the chat request is malformed or names an undiscovered model.
	// ErrRequest 表示聊天请求格式错误，或指定了未被发现的模型。
	ErrRequest = errors.New("grokcli: invalid request")

	errUnsupported = errors.New("grokcli: request feature is unsupported")
	errExit        = errors.New("grokcli: CLI exited unsuccessfully")
)

const (
	maxLineBytes   = 1 << 20
	maxLines       = 10000
	maxTextBytes   = 1 << 20
	maxListBytes   = 1 << 20
	maxModels      = 256
	maxDiagBytes   = 64 << 10
	acpRequestID   = "aisw_model_discovery"
	isolatedConfig = "[cli]\nauto_update = false\nuse_leader = false\n"
)

// disallowedTools lists every built-in Grok tool; the isolated config also
// declares no MCP server, so the model is offered no tool at all.
// disallowedTools 列出全部 Grok 内建工具；隔离配置同样不声明任何 MCP 服务，
// 因此模型拿不到任何工具。
const disallowedTools = "run_terminal_command,read_file,search_replace,list_dir,grep,kill_command_or_subagent,todo_write," +
	"get_command_or_subagent_output,spawn_subagent,scheduler_create,scheduler_delete,scheduler_list,monitor,search_tool,use_tool," +
	"workflow,enter_plan_mode,exit_plan_mode,ask_user_question,image_gen,image_edit,image_to_video,reference_to_video,write,bash,shell,edit,Glob,Grep"

// signInPhrases are the CLI's not-signed-in wordings, matched case-insensitively.
// signInPhrases 是 CLI 未登录时的提示措辞，不区分大小写匹配。
var signInPhrases = []string{"not signed in", "not authenticated", "not logged", "please login", "login required", "authentication required"}

func mentionsSignIn(output []byte) bool {
	lower := bytes.ToLower(output)
	for _, phrase := range signInPhrases {
		if bytes.Contains(lower, []byte(phrase)) {
			return true
		}
	}
	return false
}

// inferenceArgs builds the headless command line. No --always-approve and no
// --allow rules are passed: with every built-in tool disallowed and no MCP
// server configured, nothing can be approved.
//
// inferenceArgs 构造 headless 命令行。不传 --always-approve 与 --allow 规则：
// 全部内建工具被禁用且未配置 MCP 服务，没有任何东西可被批准。
func inferenceArgs(promptFile, model string) []string {
	return []string{"--no-leader", "--prompt-file", promptFile, "--output-format", "streaming-json",
		"--disable-web-search", "--no-subagents", "--no-plan", "--disallowed-tools", disallowedTools, "--verbatim", "--model", model}
}

// headCapture keeps the first limit bytes and silently discards the rest, so
// a chatty child never blocks on a full pipe.
// headCapture 保留前 limit 字节并静默丢弃其余部分，子进程输出再多也不会因
// 管道写满而阻塞。
type headCapture struct {
	buffer bytes.Buffer
	limit  int
}

func (h *headCapture) Write(p []byte) (int, error) {
	if room := h.limit - h.buffer.Len(); room > 0 {
		h.buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// resolveExecutable finds the trusted CLI: an explicit path as-is, else the
// Agent's PATH, else $HOME/.grok/bin where the official installer puts it.
// resolveExecutable 查找可信 CLI：显式路径原样使用，否则查 Agent 的 PATH，
// 再查官方安装器使用的 $HOME/.grok/bin。
func resolveExecutable(name string, env []string) (string, error) {
	path := name
	if !strings.ContainsRune(name, filepath.Separator) {
		found, err := exec.LookPath(name)
		if err != nil {
			home := envValue(env, "HOME")
			if home == "" {
				return "", ErrProcess
			}
			found = filepath.Join(home, ".grok", "bin", name)
			if info, statErr := os.Stat(found); statErr != nil || info.IsDir() || info.Mode()&0o111 == 0 {
				return "", ErrProcess
			}
		}
		path = found
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", ErrProcess
	}
	return abs, nil
}

func envValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == key {
			value = v
		}
	}
	return value
}

// processEnv builds the child environment from an allowlist, then points
// HOME and GROK_HOME at the isolated directory while reusing the operator's
// sign-in file, so the user's own Grok config, MCP servers and project rules
// never load.
//
// processEnv 按允许列表构造子进程环境，再把 HOME 与 GROK_HOME 指向隔离目录，
// 同时复用运维账户的登录文件，使用户自己的 Grok 配置、MCP 服务与项目规则
// 都不会被加载。
func processEnv(source []string, dir, executable string) []string {
	allowed := map[string]bool{"USER": true, "LOGNAME": true, "LANG": true, "LC_ALL": true, "TMPDIR": true, "XAI_API_KEY": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true}
	var out []string
	for _, entry := range source {
		if key, _, ok := strings.Cut(entry, "="); ok && allowed[key] {
			out = append(out, entry)
		}
	}
	auth := envValue(source, "GROK_AUTH_PATH")
	if auth == "" {
		if grokHome := envValue(source, "GROK_HOME"); grokHome != "" {
			auth = filepath.Join(grokHome, "auth.json")
		} else if home := envValue(source, "HOME"); home != "" {
			auth = filepath.Join(home, ".grok", "auth.json")
		}
	}
	if filepath.IsAbs(auth) {
		out = append(out, "GROK_AUTH_PATH="+auth)
	}
	path := filepath.Dir(executable)
	if inherited := envValue(source, "PATH"); inherited != "" {
		path += string(os.PathListSeparator) + inherited
	}
	return append(out, "HOME="+dir, "GROK_HOME="+filepath.Join(dir, ".grok"), "PATH="+path)
}

// withHome runs fn inside a fresh isolated home that is removed afterwards.
// withHome 在新建的隔离目录中运行 fn，结束后删除该目录。
func withHome(executable string, env []string, fn func(dir, exe string, childEnv []string) error) error {
	exe, err := resolveExecutable(executable, env)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "aisw-grok-")
	if err != nil {
		return ErrProcess
	}
	defer os.RemoveAll(dir)
	grokHome := filepath.Join(dir, ".grok")
	if os.Mkdir(grokHome, 0o700) != nil || os.WriteFile(filepath.Join(grokHome, "config.toml"), []byte(isolatedConfig), 0o600) != nil {
		return ErrProcess
	}
	return fn(dir, exe, processEnv(env, dir, exe))
}

// runCLI starts one CLI process and feeds each stdout line to onLine until
// onLine stops it, fails, or output ends. stdin nil means /dev/null. It
// returns the head of stderr for sign-in classification only; that text is
// never surfaced. errExit reports a non-zero exit after a complete read.
//
// runCLI 启动一个 CLI 进程，把 stdout 的每一行交给 onLine，直到 onLine 要求
// 停止、返回错误或输出结束。stdin 为 nil 表示 /dev/null。返回的 stderr 开头
// 只用于判断是否未登录，从不对外暴露。完整读完后非零退出报告为 errExit。
func runCLI(ctx context.Context, exe string, env []string, dir string, args []string, stdin []byte, onLine func([]byte) (bool, error)) ([]byte, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	diag := &headCapture{limit: maxDiagBytes}
	cmd.Dir, cmd.Env, cmd.Stderr = dir, env, diag
	cmd.WaitDelay = 2 * time.Second
	if err := configureProcess(cmd); err != nil {
		return nil, err
	}
	var input io.WriteCloser
	var err error
	if stdin != nil {
		if input, err = cmd.StdinPipe(); err != nil {
			return nil, ErrProcess
		}
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, ErrProcess
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		return nil, ErrProcess
	}
	stopped := false
	if input != nil {
		if _, err = input.Write(stdin); err != nil {
			err = ErrProtocol
		}
	}
	if err == nil {
		stopped, err = readLines(output, onLine)
	}
	if err != nil || stopped {
		// Stop the whole process group; nothing more is read from it.
		// 终止整个进程组，之后不再读取它的任何输出。
		_ = cmd.Cancel()
	}
	if input != nil {
		_ = input.Close()
	}
	waitErr := cmd.Wait()
	switch {
	case err != nil:
		return diag.buffer.Bytes(), err
	case stopped:
		return diag.buffer.Bytes(), nil
	case waitErr != nil:
		return diag.buffer.Bytes(), errExit
	}
	return diag.buffer.Bytes(), nil
}

func readLines(output io.Reader, onLine func([]byte) (bool, error)) (bool, error) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for lines := 0; scanner.Scan(); {
		if lines++; lines > maxLines {
			return false, ErrProtocol
		}
		stop, err := onLine(scanner.Bytes())
		if err != nil || stop {
			return stop, err
		}
	}
	if scanner.Err() != nil {
		return false, ErrProtocol
	}
	return false, nil
}
