// Package localdiscovery finds Ollama, vLLM and SGLang instances already
// running on this machine and registers them with runtime.Manager, so an
// operator does not have to hand-configure a backend that is already sitting
// on its well-known port.
//
// It defaults to probing only 127.0.0.1: this machine's own loopback
// interface, on the three default ports. Nothing here scans a subnet or
// speaks mDNS — an Agent that started reaching out to arbitrary hosts on its
// own would be exactly the kind of "general-purpose proxy" behavior
// AGENTS.md's security rules rule out for the tunnel, and the same
// restraint applies to this feature. Discovering an instance on another
// host is a job for an operator explicitly configuring its address, not for
// this package.
//
// Package localdiscovery 为 Agent 与 Gateway 发现本机 Ollama/vLLM/SGLang，并向
// runtime.Manager 注册。默认只探测三个已知回环端点，不扫描其他主机。
package localdiscovery

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"AIServeWeave/common/runtime"
)

// DefaultInterval is how often a fresh scan looks for newly appeared
// candidates. It only has to be responsive enough that "start Ollama, then
// start the Agent a bit later" feels instant on a human timescale — once a
// candidate is registered, runtime.Manager's own health loop takes over
// tracking it, not this package.
const DefaultInterval = 30 * time.Second

// Candidate is one (kind, address) pair worth probing.
type Candidate struct {
	Kind    runtime.Kind
	BaseURL string
}

// DefaultCandidates returns the well-known local endpoints this feature
// targets, loopback only: Ollama on 11434, SGLang on 30000, and vLLM,
// TensorRT-LLM and TokenSpeed, which all default to 8000. SGLang's probe
// cannot verify identity (its OpenAI-compatible responses look exactly like
// vLLM's), so whatever answers on 30000 is registered as SGLang with
// IdentityVerified=false; that port is SGLang's launch_server default and
// not one vLLM uses unless told to.
//
// The three 8000 candidates are tried in order and are mutually exclusive:
// vLLM's probe needs /version and rejects TensorRT-LLM's owned_by,
// TensorRT-LLM's needs /version plus its own owned_by, and TokenSpeed's
// needs SMG's /readiness, which neither of the others serves. Once one of
// them registers the address, scanOnce skips the rest.
//
// DefaultCandidates 返回本功能探测的已知本机端点，仅限回环地址：Ollama 在 11434，
// SGLang 在 30000，vLLM、TensorRT-LLM 与 TokenSpeed 的默认端口都是 8000。SGLang
// 的探测无法核实身份（它的 OpenAI 兼容响应与 vLLM 完全一致），因此 30000 端口上应答
// 的服务一律按 SGLang 注册，快照里 IdentityVerified=false；30000 是 SGLang
// launch_server 的默认端口，vLLM 除非被显式指定否则不会用它。
//
// 8000 上的三个候选按顺序尝试且互斥：vLLM 的探测需要 /version 并拒绝 TensorRT-LLM
// 的 owned_by，TensorRT-LLM 的探测需要 /version 加它自己的 owned_by，TokenSpeed 的
// 探测需要 SMG 的 /readiness，另外两者都不提供。其中一个注册了该地址后，scanOnce
// 跳过其余候选。
func DefaultCandidates() []Candidate {
	return []Candidate{
		{Kind: runtime.KindOllama, BaseURL: "http://127.0.0.1:11434"},
		{Kind: runtime.KindVLLM, BaseURL: "http://127.0.0.1:8000"},
		{Kind: runtime.KindTRTLLM, BaseURL: "http://127.0.0.1:8000"},
		{Kind: runtime.KindTokenSpeed, BaseURL: "http://127.0.0.1:8000"},
		{Kind: runtime.KindSGLang, BaseURL: "http://127.0.0.1:30000"},
	}
}

// Config configures a Scanner. Manager is required; every other field has a
// working default.
type Config struct {
	Manager runtime.Manager

	// Candidates is what each scan probes. Nil uses DefaultCandidates(); a
	// test overrides it with an httptest.Server URL, since that server picks
	// a random port and a fixed 11434/8000/30000 cannot be bound reliably in a
	// test process (it may already be held by a real Ollama on the
	// developer's machine, or collide across parallel test runs).
	Candidates []Candidate

	// Interval is how often a scan runs. Zero uses DefaultInterval.
	Interval time.Duration

	// Clock supplies time and the scan timer. Nil uses the system clock;
	// tests inject a fake so successive scans are exercised without
	// sleeping.
	Clock runtime.Clock

	// Logger receives one line per scan outcome. Nil discards them.
	Logger *slog.Logger
}

// Scanner periodically probes Config.Candidates and registers whichever ones
// answer with runtime.Manager. The zero value is not usable; construct one
// with New.
type Scanner struct {
	manager    runtime.Manager
	candidates []Candidate
	interval   time.Duration
	clock      runtime.Clock
	logger     *slog.Logger
}

// New returns a Scanner ready to Run.
func New(cfg Config) (*Scanner, error) {
	if cfg.Manager == nil {
		return nil, fmt.Errorf("localdiscovery: Manager is required")
	}
	candidates := cfg.Candidates
	if candidates == nil {
		candidates = DefaultCandidates()
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Scanner{
		manager:    cfg.Manager,
		candidates: candidates,
		interval:   interval,
		clock:      clock,
		logger:     logger,
	}, nil
}

// Run scans immediately, then again every interval, until ctx is canceled.
// It never returns an error on its own: a candidate that fails to register
// is the expected common case (nothing is listening there yet), not a
// reason to stop scanning for the others.
func (s *Scanner) Run(ctx context.Context) error {
	s.scanOnce(ctx)

	ch, stop := s.clock.NewTimer(s.interval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ch:
			s.scanOnce(ctx)
			ch, stop = s.clock.NewTimer(s.interval)
		}
	}
}

// scanOnce probes every candidate whose address is not already registered,
// under any kind, and registers whichever ones answer. It reads
// Manager.Snapshot() fresh each time rather than remembering what it added
// itself: that way a manually configured instance is skipped for the same
// reason a previously discovered one is, and an instance someone removed by
// hand gets picked back up on the next scan, which is exactly what
// "discovery" should mean.
//
// The key is the address alone because one address is one server: several
// kinds share port 8000, and a server an operator declared as trtllm must
// not also be registered as vllm by discovery.
//
// scanOnce 探测每一个地址尚未以任何种类注册的候选，并注册应答者。它每次都重新读取
// Manager.Snapshot()，而不是记住自己加过什么：这样手工配置的实例与之前发现的实例
// 出于同一理由被跳过，被人手工移除的实例会在下一轮扫描被重新发现，这正是「发现」
// 应有的含义。
//
// 只以地址为键，是因为一个地址就是一台服务器：多个种类共用 8000 端口，运维声明为
// trtllm 的服务器不能再被发现流程注册成 vllm。
func (s *Scanner) scanOnce(ctx context.Context) {
	known := make(map[string]struct{})
	for _, snap := range s.manager.Snapshot() {
		known[snap.Descriptor.BaseURL] = struct{}{}
	}

	for _, c := range s.candidates {
		if _, ok := known[c.BaseURL]; ok {
			continue
		}
		id := candidateID(c)
		if err := s.manager.Add(ctx, runtime.Config{ID: id, Kind: c.Kind, BaseURL: c.BaseURL}); err != nil {
			// The overwhelmingly common case is "nothing is listening
			// there" — Probe rejects it and Add returns that as an error.
			// That is not a fault in this process, so it is logged quietly
			// rather than surfaced as one.
			s.logger.Debug("local discovery: candidate did not register",
				slog.String("kind", string(c.Kind)), slog.String("base_url", c.BaseURL), slog.String("error", err.Error()))
			continue
		}
		known[c.BaseURL] = struct{}{} // later candidates for this address are now taken / 该地址后续的候选就此跳过
		s.logger.Info("auto-discovered a local runtime instance",
			slog.String("id", id), slog.String("kind", string(c.Kind)), slog.String("base_url", c.BaseURL))
	}
}

// candidateID derives a deterministic, human-readable runtime id from a
// candidate so repeated scans always propose the same id for the same
// address — "ollama-11434" for http://127.0.0.1:11434, for instance — and so
// it reads distinctly from the short ids ("ollama") an operator's own
// -ollama-id flag would typically use.
func candidateID(c Candidate) string {
	port := "0"
	if u, err := url.Parse(c.BaseURL); err == nil && u.Port() != "" {
		port = u.Port()
	}
	return fmt.Sprintf("%s-%s", c.Kind, port)
}
