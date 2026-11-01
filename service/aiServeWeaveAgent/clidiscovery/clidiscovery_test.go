package clidiscovery

import (
	"context"
	"errors"
	"sync"
	"testing"

	"AIServeWeave/common/runtime"
)

// fakeManager records Add calls and answers Snapshot from a scripted list.
//
// fakeManager 记录 Add 调用，并用预设列表回答 Snapshot。
type fakeManager struct {
	mu       sync.Mutex
	existing []runtime.Snapshot
	addErr   error
	added    []runtime.Config
}

func (m *fakeManager) Add(_ context.Context, cfg runtime.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.added = append(m.added, cfg)
	return m.addErr
}
func (m *fakeManager) Replace(context.Context, runtime.Config) error { return nil }
func (m *fakeManager) Remove(context.Context, string) error          { return nil }
func (m *fakeManager) Get(string) (runtime.Runtime, bool)            { return nil, false }
func (m *fakeManager) Close(context.Context) error                   { return nil }
func (m *fakeManager) Snapshot() []runtime.Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]runtime.Snapshot(nil), m.existing...)
}

type fixture struct {
	manager   *fakeManager
	declared  map[string]runtime.Config
	lookups   int
	lookupErr error
	allowed   []string
}

func (f *fixture) scanner(t *testing.T) *Scanner {
	t.Helper()
	s, err := New(Config{
		Manager:         f.manager,
		Declare:         func(cfg runtime.Config) { f.declared[cfg.ID] = cfg },
		Undeclare:       func(id string) { delete(f.declared, id) },
		AllowedRuntimes: f.allowed,
		LookPath:        func(string) (string, error) { f.lookups++; return "/usr/bin/codex", f.lookupErr },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func newFixture() *fixture {
	return &fixture{manager: &fakeManager{}, declared: map[string]runtime.Config{}}
}

func TestScanOnce(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(f *fixture)
		wantAdded    int
		wantDeclared bool
		wantLookups  int
	}{
		{name: "installed and logged in registers codex", setup: func(f *fixture) {}, wantAdded: 1, wantDeclared: true, wantLookups: 1},
		{name: "codex not on PATH adds nothing and starts no probe", setup: func(f *fixture) { f.lookupErr = errors.New("not found") }, wantAdded: 0, wantLookups: 1},
		{name: "probe failure leaves nothing declared", setup: func(f *fixture) { f.manager.addErr = errors.New("not logged in") }, wantAdded: 1, wantDeclared: false, wantLookups: 1},
		{name: "a declared codex runtime is left alone", setup: func(f *fixture) {
			f.manager.existing = []runtime.Snapshot{{Descriptor: runtime.Descriptor{ID: "my-codex", Kind: runtime.KindCodex}}}
		}, wantAdded: 0, wantLookups: 0},
		{name: "a claude runtime does not block codex", setup: func(f *fixture) {
			f.manager.existing = []runtime.Snapshot{{Descriptor: runtime.Descriptor{ID: "claude-local", Kind: runtime.KindClaude}}}
		}, wantAdded: 1, wantDeclared: true, wantLookups: 1},
		{name: "allowlist without codex-local registers nothing", setup: func(f *fixture) { f.allowed = []string{"ollama-11434"} }, wantAdded: 0, wantLookups: 0},
		{name: "allowlist with codex-local registers", setup: func(f *fixture) { f.allowed = []string{"ollama-11434", CodexID} }, wantAdded: 1, wantDeclared: true, wantLookups: 1},
		{name: "empty allowlist means no narrowing", setup: func(f *fixture) { f.allowed = []string{} }, wantAdded: 1, wantDeclared: true, wantLookups: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			tc.setup(f)
			f.scanner(t).scanOnce(context.Background())

			if got := len(f.manager.added); got != tc.wantAdded {
				t.Fatalf("Add calls = %d, want %d", got, tc.wantAdded)
			}
			if tc.wantAdded > 0 {
				got := f.manager.added[0]
				if got.ID != CodexID || got.Kind != runtime.KindCodex || got.BaseURL != "" {
					t.Fatalf("added %+v, want id %q kind codex with no base URL", got, CodexID)
				}
			}
			if _, ok := f.declared[CodexID]; ok != tc.wantDeclared {
				t.Fatalf("declared = %v, want %v", ok, tc.wantDeclared)
			}
			if f.lookups != tc.wantLookups {
				t.Fatalf("PATH lookups = %d, want %d", f.lookups, tc.wantLookups)
			}
		})
	}
}

// TestScanRetriesAfterLogin checks that a codex that was installed but not logged
// in registers on a later scan once the login exists.
//
// TestScanRetriesAfterLogin 检查已安装但未登录的 codex，在登录后的下一轮扫描中完成注册。
func TestScanRetriesAfterLogin(t *testing.T) {
	f := newFixture()
	f.manager.addErr = errors.New("not logged in")
	s := f.scanner(t)

	s.scanOnce(context.Background())
	if _, ok := f.declared[CodexID]; ok {
		t.Fatal("a failed probe left codex declared")
	}

	f.manager.addErr = nil
	s.scanOnce(context.Background())
	if _, ok := f.declared[CodexID]; !ok {
		t.Fatal("codex was not registered once the login worked")
	}
	if got := len(f.manager.added); got != 2 {
		t.Fatalf("Add calls = %d, want 2 (one failed, one successful)", got)
	}
}

func TestRunScansOnceAndStopsOnCancel(t *testing.T) {
	f := newFixture()
	s := f.scanner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatalf("Run returned %v, want nil on cancel", err)
	}
	if got := len(f.manager.added); got != 1 {
		t.Fatalf("Add calls = %d, want 1 (the immediate scan)", got)
	}
}

func TestNewRequiresDependencies(t *testing.T) {
	noop := func(runtime.Config) {}
	undo := func(string) {}
	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{"no manager", Config{Declare: noop, Undeclare: undo}},
		{"no declare", Config{Manager: &fakeManager{}, Undeclare: undo}},
		{"no undeclare", Config{Manager: &fakeManager{}, Declare: noop}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil {
				t.Fatal("New accepted an incomplete config")
			}
		})
	}
}
