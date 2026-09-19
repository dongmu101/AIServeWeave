package agentupgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/agentupgradestatus"
)

// fakeClock is a minimal runtime.Clock: Checker only reads Now(), it never
// arms a timer, so NewTimer is unused but must still exist to satisfy the
// interface.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	panic("agentupgrade: Checker does not use Clock.NewTimer")
}

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	entries := []Entry{
		{Version: "v1.2.3", SourceURL: "https://example.invalid/agent-v1.2.3", SHA256: "abc", Signature: "sig"},
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	got, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(got) != 1 || got[0] != entries[0] {
		t.Fatalf("want %+v, got %+v", entries, got)
	}
}

func TestLoadManifest_MissingFile(t *testing.T) {
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("want error for a missing manifest file, got nil")
	}
}

func TestChecker_ReportsNewerVersionAvailable(t *testing.T) {
	c := NewChecker([]Entry{{Version: "v1.0.0"}, {Version: "v1.1.0"}}, "v1.0.0", newFakeClock())
	if !c.HasUpdate() {
		t.Fatal("want HasUpdate true when the manifest names a version other than current")
	}
}

func TestChecker_NoNewerVersionWhenCurrentIsLatest(t *testing.T) {
	c := NewChecker([]Entry{{Version: "v1.0.0"}}, "v1.0.0", newFakeClock())
	if c.HasUpdate() {
		t.Fatal("want HasUpdate false when the manifest names only the current version")
	}
}

func TestChecker_EmptyManifestHasNoUpdate(t *testing.T) {
	c := NewChecker(nil, "v1.0.0", newFakeClock())
	if c.HasUpdate() {
		t.Fatal("want HasUpdate false for an empty manifest")
	}
}

func TestChecker_CheckActionReportsIdle(t *testing.T) {
	c := NewChecker([]Entry{{Version: "v1.1.0"}}, "v1.0.0", newFakeClock())
	c.Trigger(agentupgradestatus.ActionCheck, "")
	st := c.Status()
	if st.State != agentupgradestatus.StateIdle {
		t.Fatalf("want StateIdle after ActionCheck, got %v", st.State)
	}
	if st.CurrentVersion != "v1.0.0" {
		t.Fatalf("want CurrentVersion v1.0.0, got %q", st.CurrentVersion)
	}
}

func TestChecker_UpgradeActionReportsNotImplemented(t *testing.T) {
	c := NewChecker([]Entry{{Version: "v1.1.0"}}, "v1.0.0", newFakeClock())
	c.Trigger(agentupgradestatus.ActionUpgrade, "v1.1.0")
	st := c.Status()
	if st.State != agentupgradestatus.StateFailed || st.Reason != agentupgradestatus.ReasonNotImplemented {
		t.Fatalf("want StateFailed/ReasonNotImplemented, got %v/%v", st.State, st.Reason)
	}
}

func TestChecker_RollbackActionReportsNotImplemented(t *testing.T) {
	c := NewChecker([]Entry{{Version: "v0.9.0"}}, "v1.0.0", newFakeClock())
	c.Trigger(agentupgradestatus.ActionRollback, "v0.9.0")
	st := c.Status()
	if st.State != agentupgradestatus.StateFailed || st.Reason != agentupgradestatus.ReasonNotImplemented {
		t.Fatalf("want StateFailed/ReasonNotImplemented, got %v/%v", st.State, st.Reason)
	}
}

func TestChecker_UpgradeActionReportsNotImplementedEvenForUnknownVersion(t *testing.T) {
	c := NewChecker([]Entry{{Version: "v1.1.0"}}, "v1.0.0", newFakeClock())
	c.Trigger(agentupgradestatus.ActionUpgrade, "v9.9.9")
	st := c.Status()
	if st.State != agentupgradestatus.StateFailed || st.Reason != agentupgradestatus.ReasonNotImplemented {
		t.Fatalf("want StateFailed/ReasonNotImplemented regardless of whether target_version is known, got %v/%v", st.State, st.Reason)
	}
}

func TestChecker_UnspecifiedActionIsNoOp(t *testing.T) {
	c := NewChecker([]Entry{{Version: "v1.1.0"}}, "v1.0.0", newFakeClock())
	before := c.Status()
	c.Trigger(agentupgradestatus.ActionUnspecified, "")
	after := c.Status()
	if before != after {
		t.Fatalf("want ActionUnspecified to leave Status unchanged: before %+v, after %+v", before, after)
	}
}

func TestChecker_InitialStatusIsIdle(t *testing.T) {
	c := NewChecker(nil, "v1.0.0", newFakeClock())
	st := c.Status()
	if st.State != agentupgradestatus.StateIdle || st.CurrentVersion != "v1.0.0" {
		t.Fatalf("want initial Status{State: StateIdle, CurrentVersion: v1.0.0}, got %+v", st)
	}
}

func TestChecker_DuplicateVersionsCollapse(t *testing.T) {
	c := NewChecker([]Entry{{Version: "v1.1.0"}, {Version: "v1.1.0"}}, "v1.0.0", newFakeClock())
	if !c.HasUpdate() {
		t.Fatal("want HasUpdate true; duplicate manifest entries should not change the answer")
	}
}
