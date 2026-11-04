// Package clidiscovery registers a Codex CLI that is already installed and
// logged in on this machine, so a node whose owner has run `codex login` does
// not also have to hand-write a runtime declaration.
//
// It lives in the Agent, not in common/localdiscovery: that scanner is shared
// with the Gateway and only probes loopback HTTP backends, while a CLI runtime
// spends the local subscription account and must stay an Agent-only decision.
// The runtime is still declared locally (by this process, never by a Gateway),
// and it honors the local runtime allowlist, so the local-only rules for CLI
// runtimes hold.
//
// Package clidiscovery 在本机已安装并登录 Codex CLI 时自动注册它，节点所有者执行过
// `codex login` 之后不必再手写运行时声明。
//
// 它放在 Agent 而不是 common/localdiscovery：后者与 Gateway 共用，只探测回环 HTTP 后端，
// 而 CLI 运行时会消耗本机订阅账户，必须只由 Agent 决定。运行时仍是本地声明（由本进程而非
// Gateway 声明），并遵守本地运行时白名单，因此 CLI 运行时只能本地声明的约束依然成立。
package clidiscovery

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"slices"
	"time"

	"AIServeWeave/common/runtime"
)

const (
	// CodexID is the runtime id a discovered Codex CLI is registered under.
	//
	// CodexID 是被发现的 Codex CLI 注册时使用的运行时 id。
	CodexID = "codex-local"

	// DefaultInterval is how often a scan retries. Registering runs a real
	// probe against the CLI, so it is far slower than the loopback scan.
	//
	// DefaultInterval 是扫描重试的间隔。注册会对 CLI 做一次真实探测，因此比回环扫描慢得多。
	DefaultInterval = 2 * time.Minute

	codexExecutable = "codex"
)

// Config configures a Scanner. Manager and Declare are required.
//
// Config 配置 Scanner。Manager 与 Declare 必填。
type Config struct {
	// Manager registers the discovered runtime.
	//
	// Manager 注册被发现的运行时。
	Manager runtime.Manager

	// Declare records cfg as a locally declared runtime before it is added;
	// the registry only builds CLI runtimes that were declared locally.
	// Undeclare withdraws it again when the add fails.
	//
	// Declare 在添加之前把 cfg 记为本地声明的运行时；注册表只构建本地声明过的 CLI 运行时。
	// 添加失败时 Undeclare 再把它撤回。
	Declare   func(cfg runtime.Config)
	Undeclare func(id string)
	// Registered, when set, is called after cfg has been added, so the
	// caller can start what follows a declared CLI, such as its quota query.
	//
	// Registered 非空时在 cfg 添加成功后调用，使调用方能启动已声明 CLI 的后续工作，
	// 例如额度查询。
	Registered func(cfg runtime.Config)

	// AllowedRuntimes is the Agent's runtime allowlist. When it is not empty
	// and does not list CodexID, nothing is registered, because the tunnel
	// would refuse every request for it anyway.
	//
	// AllowedRuntimes 是 Agent 的运行时白名单。非空且不含 CodexID 时不注册任何东西，
	// 因为隧道本来就会拒绝对它的所有请求。
	AllowedRuntimes []string

	// Interval is how often a scan runs. Zero uses DefaultInterval.
	//
	// Interval 是扫描间隔。零值使用 DefaultInterval。
	Interval time.Duration

	// Clock supplies the scan timer. Nil uses the system clock.
	//
	// Clock 提供扫描定时器。nil 使用系统时钟。
	Clock runtime.Clock

	// LookPath finds the executable on the Agent's PATH. Nil uses
	// exec.LookPath; tests inject a stub.
	//
	// LookPath 在 Agent 的 PATH 中查找可执行文件。nil 使用 exec.LookPath；测试注入桩。
	LookPath func(file string) (string, error)

	// Logger receives one line per registration. Nil discards them.
	//
	// Logger 接收每次注册的一行日志。nil 则丢弃。
	Logger *slog.Logger
}

// Scanner periodically looks for a logged-in Codex CLI and registers it. The
// zero value is not usable; construct one with New.
//
// Scanner 定期查找已登录的 Codex CLI 并注册它。零值不可用，用 New 构造。
type Scanner struct {
	cfg Config
}

// New validates cfg and returns a Scanner ready to Run.
//
// New 校验 cfg 并返回可 Run 的 Scanner。
func New(cfg Config) (*Scanner, error) {
	if cfg.Manager == nil {
		return nil, errors.New("clidiscovery: Manager is required")
	}
	if cfg.Declare == nil || cfg.Undeclare == nil {
		return nil, errors.New("clidiscovery: Declare and Undeclare are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Clock == nil {
		cfg.Clock = runtime.NewSystemClock()
	}
	if cfg.LookPath == nil {
		cfg.LookPath = exec.LookPath
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Scanner{cfg: cfg}, nil
}

// Run scans immediately, then again every interval, until ctx is canceled.
//
// Run 立即扫描一次，之后每隔一个间隔再扫描，直到 ctx 被取消。
func (s *Scanner) Run(ctx context.Context) error {
	s.scanOnce(ctx)

	ch, stop := s.cfg.Clock.NewTimer(s.cfg.Interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ch:
			s.scanOnce(ctx)
			ch, stop = s.cfg.Clock.NewTimer(s.cfg.Interval)
		}
	}
}

// scanOnce registers Codex when nothing already provides it, the allowlist
// permits it, the executable is on PATH and the probe (which needs a ChatGPT
// login) succeeds. A missing executable is checked first and costs no process,
// so a node without Codex pays nothing per scan.
//
// scanOnce 在没有现成的 Codex 运行时、白名单允许、可执行文件在 PATH 中、且探测（需要
// ChatGPT 登录）成功时注册 Codex。先检查可执行文件是否存在，这一步不启动进程，因此没有
// Codex 的节点每轮扫描都没有开销。
func (s *Scanner) scanOnce(ctx context.Context) {
	for _, snap := range s.cfg.Manager.Snapshot() {
		if snap.Descriptor.Kind == runtime.KindCodex {
			return // declared or already discovered / 已声明或已发现
		}
	}
	if len(s.cfg.AllowedRuntimes) > 0 && !slices.Contains(s.cfg.AllowedRuntimes, CodexID) {
		return
	}
	if _, err := s.cfg.LookPath(codexExecutable); err != nil {
		return
	}

	cfg := runtime.Config{ID: CodexID, Kind: runtime.KindCodex}
	s.cfg.Declare(cfg)
	if err := s.cfg.Manager.Add(ctx, cfg); err != nil {
		// Installed but not logged in is the usual reason; the next scan
		// retries once the owner has run `codex login`.
		// 通常是已安装但未登录；所有者执行 `codex login` 后，下一轮扫描会重试。
		s.cfg.Undeclare(CodexID)
		s.cfg.Logger.Debug("cli discovery: codex did not register", slog.String("error", err.Error()))
		return
	}
	s.cfg.Logger.Info("auto-discovered a local Codex CLI",
		slog.String("id", CodexID), slog.String("kind", string(runtime.KindCodex)))
	if s.cfg.Registered != nil {
		s.cfg.Registered(cfg)
	}
}
