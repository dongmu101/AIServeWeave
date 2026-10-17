package cliagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
)

var (
	// ErrAuthentication means the CLI reported that no account is signed in.
	// ErrAuthentication 表示 CLI 报告没有已登录的账户。
	ErrAuthentication = errors.New("cliagent: CLI is not signed in")
	// ErrProtocol means CLI output was invalid, truncated or exceeded a bound.
	// ErrProtocol 表示 CLI 输出无效、被截断或超出上限。
	ErrProtocol = errors.New("cliagent: invalid, truncated, or oversized CLI output")
	// ErrRPC means the CLI rejected a discovery request; its message is withheld.
	// ErrRPC 表示 CLI 拒绝了发现请求，不暴露原始错误正文。
	ErrRPC = errors.New("cliagent: CLI rejected a request")
	// ErrUnexpectedTool means the CLI attempted a tool call, which this package never allows.
	// ErrUnexpectedTool 表示 CLI 试图调用工具，本包从不允许。
	ErrUnexpectedTool = errors.New("cliagent: CLI attempted a tool call")
	// ErrTurn means the headless turn failed; CLI output is withheld.
	// ErrTurn 表示 headless 回合失败，不暴露 CLI 输出。
	ErrTurn = errors.New("cliagent: CLI turn failed; output withheld")
	// ErrProcess means the isolated CLI process could not be prepared or started.
	// ErrProcess 表示无法准备或启动隔离的 CLI 进程。
	ErrProcess = errors.New("cliagent: could not prepare or start isolated CLI process")
	// ErrRequest means the chat request is malformed or names an undiscovered model.
	// ErrRequest 表示聊天请求格式错误，或指定了未被发现的模型。
	ErrRequest = errors.New("cliagent: invalid request")

	errUnsupported = errors.New("cliagent: request feature is unsupported")
	errExit        = errors.New("cliagent: CLI exited unsuccessfully")
)

const (
	maxLineBytes   = 1 << 20
	maxOutputBytes = 64 << 20 // whole stdout of one run; text itself is capped by maxTextBytes / 单次运行的全部 stdout；文本另有 maxTextBytes 上限
	maxTextBytes   = 1 << 20
	maxListBytes   = 1 << 20
	maxModels      = 256
	maxRawModels   = 16 * maxModels // raw listing entries kept before filtering / 过滤前保留的原始列表条目数
	maxDiagBytes   = 64 << 10
)

// cliSpec is everything one CLI contributes. Every function runs inside a fresh
// isolated working directory that is removed afterwards.
//
// cliSpec 是单个 CLI 提供的全部内容。每个函数都在一个新建的隔离工作目录中运行，
// 结束后该目录被删除。
type cliSpec struct {
	// Kind and Label identify the CLI in descriptors and fixed error text.
	// Kind 与 Label 在描述符与固定错误文本中标识该 CLI。
	Kind  runtime.Kind
	Label string
	// Executables are candidate names in order of preference; each is looked
	// up on PATH and then in HomeBin under $HOME (when set) before the next.
	// Executables 是按优先级排列的候选名称；每个名称先在 PATH 中查找，再在
	// $HOME 下的 HomeBin（若设置）中查找，然后才轮到下一个。
	Executables []string
	HomeBin     string
	// PassEnv lists extra variables passed through; PassEnvPrefixes and
	// PassEnvSuffixes pass whole families (for example "CODEBUDDY_" or
	// "_API_KEY").
	// PassEnv 列出额外透传的变量；PassEnvPrefixes 与 PassEnvSuffixes 按前缀或
	// 后缀透传整组变量（例如 "CODEBUDDY_" 或 "_API_KEY"）。
	PassEnv         []string
	PassEnvPrefixes []string
	PassEnvSuffixes []string
	// Setup prepares the working directory and may rewrite the child
	// environment; env already holds the allowlisted variables.
	// Setup 准备工作目录，并可改写子进程环境；env 中已含允许列表内的变量。
	Setup func(dir string, env []string, source []string) ([]string, error)
	// SignIn lists lower-case phrases meaning the CLI is not signed in.
	// SignIn 列出表示 CLI 未登录的小写措辞。
	SignIn []string
	// Aliases are catalogue entries the CLI does not accept as a --model
	// value (such as CodeBuddy's "default-model"); "default" is always dropped.
	// Aliases 是 CLI 不接受作为 --model 取值的目录条目（例如 CodeBuddy 的
	// "default-model"）；"default" 总是被去掉。
	Aliases []string
	// Discover returns raw model IDs in CLI order without running inference.
	// Discover 不执行推理，按 CLI 顺序返回原始模型 ID。
	Discover func(c *call) ([]string, error)
	// PromptFile writes the prompt to a 0600 file in the working directory
	// before Turn runs; only CLIs that read the prompt from a file set it.
	// PromptFile 让 Turn 运行前把提示词写入工作目录中权限为 0600 的文件；只有
	// 从文件读取提示词的 CLI 才设置它。
	PromptFile bool
	// Turn returns the command line and stdin for one turn; promptFile holds
	// the prompt when PromptFile is set and is empty otherwise.
	// Turn 返回一次回合的命令行与 stdin；设置了 PromptFile 时 promptFile 中已
	// 写入提示词，否则为空串。
	Turn func(dir, promptFile, prompt, model string) (args []string, stdin []byte)
	// TurnRPC keeps a turn's stdin open until the parser reports done, for
	// dialects that read commands rather than one prompt ending at EOF.
	// TurnRPC 让回合的 stdin 一直打开到解析器报告 done，用于按命令读取、而非
	// 读到 EOF 即视为一份完整提示词的方言。
	TurnRPC bool
	// Parser returns a fresh parser for one turn's JSON output lines.
	// Parser 为一次回合的 JSON 输出行返回一个新的解析器。
	Parser func() parser
}

// parser consumes one turn's JSON object lines; non-JSON lines never reach it.
// parser 消费一次回合的 JSON 对象行；非 JSON 行不会交给它。
type parser interface {
	// line handles one event. text forwards reply text; done ends the turn
	// successfully without waiting for the process to exit.
	// line 处理一个事件。text 转发回复文本；done 表示回合成功结束，无需等待进程退出。
	line(event []byte, text func(string) error) (done bool, err error)
	// finish runs after a clean end and reports usage, or an error such as
	// "no terminal event".
	// finish 在正常结束后运行，报告用量，或报告诸如「没有终止事件」之类的错误。
	finish() (*runtime.Usage, error)
}

var specs = map[runtime.Kind]*cliSpec{}

func register(s *cliSpec) { specs[s.Kind] = s }

// call bundles one isolated run's context and lets a cliSpec start processes.
// call 汇集一次隔离运行的上下文，并让 cliSpec 启动进程。
type call struct {
	ctx    context.Context
	touch  func()
	spec   *cliSpec
	exe    string
	env    []string
	dir    string
	source []string
}

// run starts the CLI with args and closes stdin after writing it, see runCLI.
// run 以 args 启动 CLI，写完 stdin 后关闭它，见 runCLI。
func (c *call) run(args []string, stdin []byte, onLine func([]byte) (bool, error)) ([]byte, error) {
	return c.start(args, stdin, false, onLine)
}

// rpc is run for request/response dialects: stdin stays open until onLine
// stops the process, see runCLI.
// rpc 是请求/响应方言版的 run：stdin 一直保持打开，直到 onLine 停止进程，
// 见 runCLI。
func (c *call) rpc(args []string, stdin []byte, onLine func([]byte) (bool, error)) ([]byte, error) {
	return c.start(args, stdin, true, onLine)
}

func (c *call) start(args []string, stdin []byte, keepOpen bool, onLine func([]byte) (bool, error)) ([]byte, error) {
	return runCLI(c.ctx, c.exe, c.env, c.dir, args, stdin, keepOpen, func(line []byte) (bool, error) {
		if c.touch != nil {
			c.touch()
		}
		return onLine(line)
	})
}

// signedOut reports whether output carries one of the cliSpec's sign-in phrases.
// signedOut 报告 output 是否包含 cliSpec 的未登录措辞。
func (c *call) signedOut(output []byte) bool { return mentions(output, c.spec.SignIn) }

func mentions(output []byte, phrases []string) bool {
	lower := bytes.ToLower(output)
	for _, phrase := range phrases {
		if bytes.Contains(lower, []byte(phrase)) {
			return true
		}
	}
	return false
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

// resolveExecutable finds the trusted CLI: an explicit path as-is, else each
// candidate in order, first on the Agent's PATH and then in $HOME/<HomeBin>,
// so a preferred name installed only under $HOME beats a generic fallback
// name that happens to be on PATH.
// resolveExecutable 查找可信 CLI：显式路径原样使用；否则按顺序处理各候选名，
// 每个名称先查 Agent 的 PATH，再查 $HOME/<HomeBin>。这样只装在 $HOME 下的首选
// 名称，优先于恰好位于 PATH 上的通用备选名称。
func resolveExecutable(spec *cliSpec, explicit string, env []string) (string, error) {
	if explicit != "" && strings.ContainsRune(explicit, filepath.Separator) {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", ErrProcess
		}
		return abs, nil
	}
	names := spec.Executables
	if explicit != "" {
		names = []string{explicit}
	}
	home := envValue(env, "HOME")
	for _, name := range names {
		if found, err := exec.LookPath(name); err == nil {
			if abs, err := filepath.Abs(found); err == nil {
				return abs, nil
			}
		}
		if home == "" || spec.HomeBin == "" {
			continue
		}
		found := filepath.Join(home, spec.HomeBin, name)
		if info, err := os.Stat(found); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return found, nil
		}
	}
	return "", ErrProcess
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

// setEnv replaces or appends key in env.
// setEnv 替换或追加 env 中的 key。
func setEnv(env []string, key, value string) []string {
	out := env[:0:0]
	for _, entry := range env {
		if k, _, ok := strings.Cut(entry, "="); ok && k == key {
			continue
		}
		out = append(out, entry)
	}
	return append(out, key+"="+value)
}

// baseEnv builds the child environment from an allowlist plus the cliSpec's
// extra names, with the executable's directory first on PATH. HOME is kept so
// the CLI finds its own sign-in; a cliSpec that isolates HOME overrides it.
//
// baseEnv 按允许列表加上 cliSpec 额外声明的名称构造子进程环境，并把可执行文件所在
// 目录放在 PATH 最前。HOME 保持不变，以便 CLI 找到自己的登录；需要隔离 HOME 的
// cliSpec 自行覆盖它。
func baseEnv(spec *cliSpec, source []string, executable string) []string {
	allowed := map[string]bool{"HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "LC_ALL": true, "TMPDIR": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true}
	for _, key := range spec.PassEnv {
		allowed[key] = true
	}
	var out []string
	for _, entry := range source {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		pass := allowed[key]
		for _, prefix := range spec.PassEnvPrefixes {
			pass = pass || strings.HasPrefix(key, prefix)
		}
		for _, suffix := range spec.PassEnvSuffixes {
			pass = pass || strings.HasSuffix(key, suffix)
		}
		if pass {
			out = append(out, entry)
		}
	}
	path := filepath.Dir(executable)
	if inherited := envValue(source, "PATH"); inherited != "" {
		path += string(os.PathListSeparator) + inherited
	}
	return append(out, "PATH="+path)
}

// withHome runs fn inside a fresh isolated working directory that is removed
// afterwards. The caller fills in ctx and touch.
// withHome 在新建的隔离工作目录中运行 fn，结束后删除该目录。调用方负责填入
// ctx 与 touch。
func withHome(spec *cliSpec, executable string, source []string, fn func(c *call) error) error {
	exe, err := resolveExecutable(spec, executable, source)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "aisw-"+string(spec.Kind)+"-")
	if err != nil {
		return ErrProcess
	}
	defer os.RemoveAll(dir)
	env := baseEnv(spec, source, exe)
	if spec.Setup != nil {
		if env, err = spec.Setup(dir, env, source); err != nil {
			return ErrProcess
		}
	}
	return fn(&call{spec: spec, exe: exe, env: env, dir: dir, source: source})
}

// runCLI starts one CLI process and feeds each stdout line to onLine until
// onLine stops it, fails, or output ends. stdin nil means /dev/null. stdin
// bytes are written on a separate goroutine, so a large prompt never
// deadlocks against unread stdout; the pipe is then closed (EOF ends a text
// prompt) unless keepOpen, which RPC dialects need until their reply arrives.
// It returns the head of stderr for sign-in classification only; that text is
// never surfaced. errExit reports a non-zero exit after a complete read.
//
// runCLI 启动一个 CLI 进程，把 stdout 的每一行交给 onLine，直到 onLine 要求
// 停止、返回错误或输出结束。stdin 为 nil 表示 /dev/null。stdin 字节在独立协程中
// 写入，大提示词不会因 stdout 未被读取而死锁；写完后关闭管道（EOF 标志文本
// 提示词结束），除非 keepOpen——RPC 方言需要管道一直开着直到收到回复。返回的
// stderr 开头只用于判断是否未登录，从不对外暴露。完整读完后非零退出报告为
// errExit。
func runCLI(ctx context.Context, exe string, env []string, dir string, args []string, stdin []byte, keepOpen bool, onLine func([]byte) (bool, error)) ([]byte, error) {
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
	written := make(chan struct{})
	if input != nil {
		go func() {
			defer close(written)
			// A write error means the child exited or stopped reading; its
			// exit status or output says why.
			// 写入失败说明子进程已退出或不再读取；原因由退出码或输出体现。
			if _, err := input.Write(stdin); err != nil || !keepOpen {
				_ = input.Close()
			}
		}()
	} else {
		close(written)
	}
	stopped, err := readLines(output, onLine)
	if err != nil || stopped {
		// Stop the whole process group; nothing more is read from it.
		// 终止整个进程组，之后不再读取它的任何输出。
		_ = cmd.Cancel()
	}
	if input != nil && keepOpen {
		_ = input.Close()
	}
	waitErr := cmd.Wait()
	<-written
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

// readLines bounds each line and the whole output by bytes rather than by
// line count: token-level dialects print one line per delta, so a long but
// legitimate reply can span far more lines than its size suggests.
// readLines 按字节而非行数限制单行与全部输出：逐 token 输出的方言每个增量
// 一行，合法的长回复行数可能远超其大小所暗示的数量。
func readLines(output io.Reader, onLine func([]byte) (bool, error)) (bool, error) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	for total := 0; scanner.Scan(); {
		if total += len(scanner.Bytes()) + 1; total > maxOutputBytes {
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

// jsonObject reports whether line is one JSON object.
// jsonObject 报告 line 是否为单个 JSON 对象。
func jsonObject(line []byte) bool {
	line = bytes.TrimSpace(line)
	return len(line) > 0 && line[0] == '{' && json.Valid(line)
}

// lookup returns the first present, non-null key of obj.
// lookup 返回 obj 中第一个存在且非 null 的键。
func lookup(obj map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, key := range keys {
		if value, ok := obj[key]; ok && len(value) > 0 && string(value) != "null" {
			return value
		}
	}
	return nil
}

// str decodes the first present key of obj as a string.
// str 把 obj 中第一个存在的键解码为字符串。
func str(obj map[string]json.RawMessage, keys ...string) string {
	var s string
	_ = json.Unmarshal(lookup(obj, keys...), &s)
	return s
}

// object decodes raw as a JSON object, nil when it is not one.
// object 把 raw 解码为 JSON 对象，不是对象时返回 nil。
func object(raw json.RawMessage) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	return obj
}

// modelID admits catalogue IDs that are safe as a single argv value: no
// leading dash (it would read as a flag), no whitespace or control bytes.
// modelID 只接受可安全作为单个 argv 值的目录 ID：不以短横线开头（否则会被
// 当作参数），不含空白或控制字符。
var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+\[\]-]{0,255}$`)

// modelSet collects real model IDs in CLI order, dropping aliases,
// duplicates and IDs unsafe to pass as an argument, and keeping at most
// maxModels of them: a catalogue longer than that is truncated, not refused,
// so a CLI with a large provider catalogue still serves its first models.
// modelSet 按 CLI 顺序收集真实模型 ID，去掉别名、重复项以及不能安全作为参数
// 传递的 ID，最多保留 maxModels 个：更长的目录被截断而不是拒绝，供应商目录
// 很大的 CLI 仍能提供其中靠前的模型。
type modelSet struct {
	aliases []string
	ids     []string
	seen    map[string]bool
}

func (s *modelSet) add(id string) error {
	id = strings.TrimSpace(id)
	if strings.EqualFold(id, "default") || !modelID.MatchString(id) || s.seen[id] {
		return nil
	}
	for _, alias := range s.aliases {
		if strings.EqualFold(id, alias) {
			return nil
		}
	}
	if len(s.ids) >= maxModels {
		return nil
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	s.seen[id] = true
	s.ids = append(s.ids, id)
	return nil
}

func (s *modelSet) result() ([]string, error) {
	if len(s.ids) == 0 {
		return nil, ErrProtocol
	}
	return s.ids, nil
}

// runTurn runs one headless turn: it writes the prompt file when the CLI
// needs one, starts the CLI, forwards reply text through emit as it arrives
// and sends the terminal stop event only after the parser confirms a clean
// end.
//
// runTurn 运行一次 headless 回合：CLI 需要时写入提示词文件，启动 CLI，随到随经
// emit 转发回复文本，并且只在解析器确认正常结束后才发送终止事件。
func runTurn(c *call, model, prompt string, emit func(runtime.ChatEvent) error) error {
	promptFile := ""
	if c.spec.PromptFile {
		promptFile = filepath.Join(c.dir, "prompt.txt")
		if os.WriteFile(promptFile, []byte(prompt), 0o600) != nil {
			return ErrProcess
		}
	}
	args, stdin := c.spec.Turn(c.dir, promptFile, prompt, model)
	p := c.spec.Parser()
	textBytes := 0
	text := func(s string) error {
		if textBytes += len(s); textBytes > maxTextBytes {
			return ErrProtocol
		}
		if s == "" {
			return nil
		}
		return emit(runtime.ChatEvent{Delta: runtime.ChatMessageDelta{Content: s}})
	}
	noise := &headCapture{limit: maxDiagBytes}
	diag, err := c.start(args, stdin, c.spec.TurnRPC, func(line []byte) (bool, error) {
		if !jsonObject(line) {
			// Not an event; kept only for sign-in classification.
			// 不是事件；只保留用于判断是否未登录。
			_, _ = noise.Write(line)
			return false, nil
		}
		done, err := p.line(line, text)
		if errors.Is(err, ErrTurn) && c.signedOut(line) {
			err = ErrAuthentication
		}
		return done, err
	})
	signedOut := func() bool { return c.signedOut(diag) || c.signedOut(noise.buffer.Bytes()) }
	if errors.Is(err, errExit) {
		if signedOut() {
			return ErrAuthentication
		}
		return ErrTurn
	}
	if err != nil {
		return err
	}
	usage, err := p.finish()
	if err != nil {
		// A clean exit without a terminal event may still be a sign-in prompt.
		// 正常退出却没有终止事件，也可能是在提示登录。
		if signedOut() {
			return ErrAuthentication
		}
		return err
	}
	return emit(runtime.ChatEvent{FinishReason: "stop", Usage: usage})
}

// tokenUsage builds a Usage, or nil when nothing was counted.
// tokenUsage 构造 Usage，没有计数时返回 nil。
func tokenUsage(input, output int) *runtime.Usage {
	if input <= 0 && output <= 0 {
		return nil
	}
	return &runtime.Usage{PromptTokens: max(input, 0), CompletionTokens: max(output, 0), TotalTokens: max(input, 0) + max(output, 0)}
}

// sumCounts adds one count per group, each group naming alternative
// spellings of the same field, for example uncached and cached input tokens.
// sumCounts 对每组取一个计数后相加，每组列出同一字段的不同写法，例如未命中缓存
// 与命中缓存的输入 token。
func sumCounts(obj map[string]json.RawMessage, groups ...[]string) (int, error) {
	total := 0
	for _, keys := range groups {
		n, err := count(obj, keys...)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// count decodes the first present key of obj as a non-negative integer.
// count 把 obj 中第一个存在的键解码为非负整数。
func count(obj map[string]json.RawMessage, keys ...string) (int, error) {
	raw := lookup(obj, keys...)
	if raw == nil {
		return 0, nil
	}
	var n int
	if json.Unmarshal(raw, &n) != nil || n < 0 {
		return 0, ErrProtocol
	}
	return n, nil
}
