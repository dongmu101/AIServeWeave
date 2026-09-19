package agentupgrade

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// signManifest builds a Manifest over entries and signs it with priv,
// returning the JSON bytes ready to write to a manifest file.
func signManifest(t *testing.T, priv ed25519.PrivateKey, entries []Entry) []byte {
	t.Helper()
	sig := ed25519.Sign(priv, SignedContent(entries))
	data, err := json.Marshal(Manifest{Entries: entries, Signature: base64.StdEncoding.EncodeToString(sig)})
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return data
}

func TestLoadManifest(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	entries := []Entry{
		{Version: "v1.2.3", SourceURL: "https://example.invalid/agent-v1.2.3", SHA256: "abc"},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, signManifest(t, priv, entries), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	got, err := LoadManifest(path, pub)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(got) != 1 || got[0] != entries[0] {
		t.Fatalf("want %+v, got %+v", entries, got)
	}
}

func TestLoadManifest_MissingFile(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "missing.json"), pub); err == nil {
		t.Fatal("want error for a missing manifest file, got nil")
	}
}

func TestLoadManifest_NoPublicKeyConfigured(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	entries := []Entry{{Version: "v1.2.3", SHA256: "abc"}}
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, signManifest(t, priv, entries), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if _, err := LoadManifest(path, nil); err == nil {
		t.Fatal("want error when no public key is configured, got nil")
	}
}

func TestLoadManifest_TamperedEntryFailsVerification(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	entries := []Entry{{Version: "v1.2.3", SHA256: "abc"}}
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, signManifest(t, priv, entries), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Tamper with the signed manifest after signing, as a compromised
	// distribution host might: swap the declared SHA256 for a different
	// one. The signature no longer covers this content.
	tampered := []byte(`{"entries":[{"version":"v1.2.3","sha256":"evil"}],"signature":"` +
		base64.StdEncoding.EncodeToString(ed25519.Sign(priv, SignedContent(entries))) + `"}`)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("write tampered manifest: %v", err)
	}

	if _, err := LoadManifest(path, pub); err == nil {
		t.Fatal("want error for a manifest whose signed content was tampered with, got nil")
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

func TestChecker_UpgradeActionReportsNotImplementedByDefault(t *testing.T) {
	c := NewChecker([]Entry{{Version: "v1.1.0"}}, "v1.0.0", newFakeClock())
	c.Trigger(agentupgradestatus.ActionUpgrade, "v1.1.0")
	st := c.Status()
	if st.State != agentupgradestatus.StateFailed || st.Reason != agentupgradestatus.ReasonNotImplemented {
		t.Fatalf("want StateFailed/ReasonNotImplemented before EnableExecution, got %v/%v", st.State, st.Reason)
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

func TestChecker_UpgradeActionReportsNotImplementedEvenForUnknownVersionByDefault(t *testing.T) {
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

// -----------------------------------------------------------------------
// EnableExecution: the real download/verify/drain/exec chain
// -----------------------------------------------------------------------

// fakeDrainer records that DrainAll was called, in a shared order log so
// tests can assert it ran before exec.
type fakeDrainer struct {
	mu    *sync.Mutex
	order *[]string
}

func (d *fakeDrainer) DrainAll(timeout time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	*d.order = append(*d.order, "drain")
}

// waitForState polls c.Status() until it reports want or timeout passes.
func waitForState(t *testing.T, c *Checker, want agentupgradestatus.State, timeout time.Duration) agentupgradestatus.Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st := c.Status()
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for state %v; last status %+v", want, st)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestChecker_UpgradeDownloadsVerifiesDrainsAndExecs(t *testing.T) {
	payload := []byte("pretend agent binary bytes")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	entry := Entry{Version: "v1.1.0", SourceURL: srv.URL, SHA256: hex.EncodeToString(sum[:])}
	c := NewChecker([]Entry{entry}, "v1.0.0", newFakeClock())
	c.EnableExecution(t.TempDir(), srv.Client(), time.Second)

	var mu sync.Mutex
	var order []string
	c.SetDrainer(&fakeDrainer{mu: &mu, order: &order})

	var execArgv0 string
	execDone := make(chan struct{})
	c.execFn = func(argv0 string, argv, envv []string) error {
		mu.Lock()
		order = append(order, "exec")
		execArgv0 = argv0
		mu.Unlock()
		close(execDone)
		return nil
	}

	c.Trigger(agentupgradestatus.ActionUpgrade, "v1.1.0")

	select {
	case <-execDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exec to be called")
	}
	waitForState(t, c, agentupgradestatus.StateRestarting, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "drain" || order[1] != "exec" {
		t.Fatalf("want drain before exec, got %v", order)
	}
	wantPath := filepath.Join(c.workDir, "agent-v1.1.0")
	if execArgv0 != wantPath {
		t.Fatalf("want exec argv0 %q, got %q", wantPath, execArgv0)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("want downloaded binary at %q: %v", wantPath, err)
	}
}

func TestChecker_UpgradeUnknownVersionNeverMakesANetworkRequest(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
	}))
	defer srv.Close()

	c := NewChecker([]Entry{{Version: "v1.1.0", SourceURL: srv.URL, SHA256: "abc"}}, "v1.0.0", newFakeClock())
	c.EnableExecution(t.TempDir(), srv.Client(), time.Second)

	c.Trigger(agentupgradestatus.ActionUpgrade, "v9.9.9")
	st := c.Status()
	if st.State != agentupgradestatus.StateFailed || st.Reason != agentupgradestatus.ReasonUnknownVersion {
		t.Fatalf("want StateFailed/ReasonUnknownVersion, got %v/%v", st.State, st.Reason)
	}
	if n := atomic.LoadInt32(&requests); n != 0 {
		t.Fatalf("want no network request for an unknown target_version, got %d", n)
	}
}

func TestChecker_UpgradeReportsDownloadFailedOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewChecker([]Entry{{Version: "v1.1.0", SourceURL: srv.URL, SHA256: "abc"}}, "v1.0.0", newFakeClock())
	c.EnableExecution(t.TempDir(), srv.Client(), time.Second)

	c.Trigger(agentupgradestatus.ActionUpgrade, "v1.1.0")
	st := waitForState(t, c, agentupgradestatus.StateFailed, 2*time.Second)
	if st.Reason != agentupgradestatus.ReasonDownloadFailed {
		t.Fatalf("want ReasonDownloadFailed, got %v", st.Reason)
	}
}

func TestChecker_UpgradeReportsVerificationFailedOnChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not what the manifest declared"))
	}))
	defer srv.Close()

	c := NewChecker([]Entry{{Version: "v1.1.0", SourceURL: srv.URL, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}}, "v1.0.0", newFakeClock())
	c.EnableExecution(t.TempDir(), srv.Client(), time.Second)

	c.Trigger(agentupgradestatus.ActionUpgrade, "v1.1.0")
	st := waitForState(t, c, agentupgradestatus.StateFailed, 2*time.Second)
	if st.Reason != agentupgradestatus.ReasonVerificationFailed {
		t.Fatalf("want ReasonVerificationFailed, got %v", st.Reason)
	}
}

func TestChecker_UpgradeReportsExecFailed(t *testing.T) {
	payload := []byte("pretend agent binary bytes")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	entry := Entry{Version: "v1.1.0", SourceURL: srv.URL, SHA256: hex.EncodeToString(sum[:])}
	c := NewChecker([]Entry{entry}, "v1.0.0", newFakeClock())
	c.EnableExecution(t.TempDir(), srv.Client(), time.Second)
	c.execFn = func(argv0 string, argv, envv []string) error {
		return os.ErrPermission
	}

	c.Trigger(agentupgradestatus.ActionUpgrade, "v1.1.0")
	st := waitForState(t, c, agentupgradestatus.StateFailed, 2*time.Second)
	if st.Reason != agentupgradestatus.ReasonExecFailed {
		t.Fatalf("want ReasonExecFailed, got %v", st.Reason)
	}
}

func TestChecker_UpgradeIgnoresDuplicateTriggerWhileInProgress(t *testing.T) {
	var requests int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	payload := []byte("pretend agent binary bytes")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		started <- struct{}{}
		<-release
		w.Write(payload)
	}))
	defer srv.Close()

	entry := Entry{Version: "v1.1.0", SourceURL: srv.URL, SHA256: hex.EncodeToString(sum[:])}
	c := NewChecker([]Entry{entry}, "v1.0.0", newFakeClock())
	c.EnableExecution(t.TempDir(), srv.Client(), time.Second)
	execDone := make(chan struct{})
	c.execFn = func(argv0 string, argv, envv []string) error {
		close(execDone)
		return nil
	}

	c.Trigger(agentupgradestatus.ActionUpgrade, "v1.1.0")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the first download to start")
	}

	// A second trigger while the first is still in flight must not start a
	// second download.
	c.Trigger(agentupgradestatus.ActionUpgrade, "v1.1.0")

	close(release)
	select {
	case <-execDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exec to be called")
	}
	waitForState(t, c, agentupgradestatus.StateRestarting, 2*time.Second)

	if n := atomic.LoadInt32(&requests); n != 1 {
		t.Fatalf("want exactly one download request, got %d", n)
	}
}
