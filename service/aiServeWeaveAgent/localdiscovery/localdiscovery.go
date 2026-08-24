// Package localdiscovery finds Ollama and vLLM instances already running on
// this machine and registers them with runtime.Manager, so an operator does
// not have to hand-configure a backend that is already sitting on its
// well-known port.
//
// It deliberately probes only 127.0.0.1: this machine's own loopback
// interface, on the two default ports. Nothing here scans a subnet or
// speaks mDNS — an Agent that started reaching out to arbitrary hosts on its
// own would be exactly the kind of "general-purpose proxy" behavior
// AGENTS.md's security rules rule out for the tunnel, and the same
// restraint applies to this feature. Discovering an instance on another
// host is a job for an operator explicitly configuring its address, not for
// this package.
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

// DefaultCandidates returns the two well-known local endpoints this feature
// targets: Ollama and vLLM on their default ports, loopback only.
func DefaultCandidates() []Candidate {
	return []Candidate{
		{Kind: runtime.KindOllama, BaseURL: "http://127.0.0.1:11434"},
		{Kind: runtime.KindVLLM, BaseURL: "http://127.0.0.1:8000"},
	}
}

// Config configures a Scanner. Manager is required; every other field has a
// working default.
type Config struct {
	Manager runtime.Manager

	// Candidates is what each scan probes. Nil uses DefaultCandidates(); a
	// test overrides it with an httptest.Server URL, since that server picks
	// a random port and a fixed 11434/8000 cannot be bound reliably in a
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

// scanOnce probes every candidate not already registered under a matching
// (Kind, BaseURL) and registers whichever ones answer. It reads
// Manager.Snapshot() fresh each time rather than remembering what it added
// itself: that way a manually configured instance is skipped for the same
// reason a previously discovered one is, and an instance someone removed by
// hand gets picked back up on the next scan, which is exactly what
// "discovery" should mean.
func (s *Scanner) scanOnce(ctx context.Context) {
	known := make(map[Candidate]struct{})
	for _, snap := range s.manager.Snapshot() {
		known[Candidate{Kind: snap.Descriptor.Kind, BaseURL: snap.Descriptor.BaseURL}] = struct{}{}
	}

	for _, c := range s.candidates {
		if _, ok := known[c]; ok {
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
