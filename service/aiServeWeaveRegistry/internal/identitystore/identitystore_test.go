package identitystore_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveRegistry/internal/identitystore"
)

func open(t *testing.T) *identitystore.Store {
	t.Helper()
	store, err := identitystore.Open(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}

func TestReserveBindsANewNodeIDAndConfirmsAMatchingReRegistration(t *testing.T) {
	store := open(t)
	now := time.Now()

	outcome, err := store.Reserve("node-1", "fp-a", now)
	if err != nil {
		t.Fatalf("first Reserve() error = %v", err)
	}
	if outcome != identitystore.OutcomeNew {
		t.Fatalf("first Reserve() outcome = %v, want OutcomeNew", outcome)
	}

	outcome, err = store.Reserve("node-1", "fp-a", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second Reserve() error = %v", err)
	}
	if outcome != identitystore.OutcomeMatch {
		t.Fatalf("second Reserve() with the same fingerprint outcome = %v, want OutcomeMatch", outcome)
	}
}

func TestReserveRefusesADifferentFingerprintForAKnownNodeID(t *testing.T) {
	store := open(t)
	now := time.Now()

	if _, err := store.Reserve("node-1", "fp-a", now); err != nil {
		t.Fatalf("first Reserve() error = %v", err)
	}

	outcome, err := store.Reserve("node-1", "fp-b", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("conflicting Reserve() error = %v", err)
	}
	if outcome != identitystore.OutcomeConflict {
		t.Fatalf("conflicting Reserve() outcome = %v, want OutcomeConflict", outcome)
	}

	// The ledger must be left exactly as it was: a conflicting request never
	// overwrites the prior binding.
	outcome, err = store.Reserve("node-1", "fp-a", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Reserve() with the original fingerprint after a conflict error = %v", err)
	}
	if outcome != identitystore.OutcomeMatch {
		t.Fatalf("Reserve() with the original fingerprint after a conflict outcome = %v, want OutcomeMatch (unchanged binding)", outcome)
	}
}

func TestSetOverwritesUnconditionallyAndPreservesFirstSeenAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identities.json")
	store, err := identitystore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Now()

	if _, err := store.Reserve("node-1", "fp-a", now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}

	// Set must succeed even though fp-b differs from what Reserve recorded —
	// this is RenewCertificate's path, gated by proof of possession rather
	// than a fingerprint match.
	if err := store.Set("node-1", "fp-b", now.Add(time.Hour)); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// A Reserve carrying the new fingerprint must now read as a match, and
	// the old one as a conflict — Set really did overwrite the binding.
	outcome, err := store.Reserve("node-1", "fp-b", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("Reserve() after Set() error = %v", err)
	}
	if outcome != identitystore.OutcomeMatch {
		t.Fatalf("Reserve() with the Set fingerprint outcome = %v, want OutcomeMatch", outcome)
	}

	// first_seen_at must survive the overwrite: Set's contract is to update
	// the current binding, not to treat a rotated key as a brand new node
	// history. The persisted file is read directly since Store exposes no
	// getter for it — first_seen_at exists for an operator reading the file,
	// not for this package's own logic.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var records []struct {
		NodeID      string    `json:"node_id"`
		FirstSeenAt time.Time `json:"first_seen_at"`
	}
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if !records[0].FirstSeenAt.Equal(now) {
		t.Errorf("first_seen_at = %v, want unchanged at %v", records[0].FirstSeenAt, now)
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identities.json")
	now := time.Now()

	store, err := identitystore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Reserve("node-1", "fp-a", now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}

	reopened, err := identitystore.Open(path)
	if err != nil {
		t.Fatalf("re-Open() error = %v", err)
	}
	outcome, err := reopened.Reserve("node-1", "fp-b", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Reserve() on the reopened store error = %v", err)
	}
	if outcome != identitystore.OutcomeConflict {
		t.Fatalf("Reserve() on the reopened store outcome = %v, want OutcomeConflict (the binding survived reopen)", outcome)
	}
}

func TestReserveSerializesConcurrentRegistrationsForTheSameNodeID(t *testing.T) {
	store := open(t)
	now := time.Now()

	const attempts = 50
	var wg sync.WaitGroup
	outcomes := make([]identitystore.Outcome, attempts)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Every goroutine claims the same node_id with the same
			// fingerprint, the way a retried registration from the same
			// Agent process would: exactly one may create the record, the
			// rest must observe it already matches — never a conflict
			// against itself.
			outcome, err := store.Reserve("node-1", "fp-a", now)
			if err != nil {
				t.Errorf("Reserve() goroutine %d error = %v", i, err)
				return
			}
			outcomes[i] = outcome
		}(i)
	}
	wg.Wait()

	newCount, matchCount, conflictCount := 0, 0, 0
	for _, o := range outcomes {
		switch o {
		case identitystore.OutcomeNew:
			newCount++
		case identitystore.OutcomeMatch:
			matchCount++
		case identitystore.OutcomeConflict:
			conflictCount++
		}
	}
	if newCount != 1 {
		t.Errorf("OutcomeNew count = %d, want exactly 1", newCount)
	}
	if matchCount != attempts-1 {
		t.Errorf("OutcomeMatch count = %d, want %d", matchCount, attempts-1)
	}
	if conflictCount != 0 {
		t.Errorf("OutcomeConflict count = %d, want 0 (same fingerprint racing itself is never a conflict)", conflictCount)
	}
}

func TestReserveDetectsAConflictAmongConcurrentDifferentKeys(t *testing.T) {
	store := open(t)
	now := time.Now()

	const attempts = 20
	var wg sync.WaitGroup
	outcomes := make([]identitystore.Outcome, attempts)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine claims the same node_id with its own distinct
			// fingerprint — the concurrent-spoofing/reinstall-race shape
			// S01 exists to detect.
			outcome, err := store.Reserve("node-1", fingerprintFor(i), now)
			if err != nil {
				t.Errorf("Reserve() goroutine %d error = %v", i, err)
				return
			}
			outcomes[i] = outcome
		}(i)
	}
	wg.Wait()

	newCount, conflictCount := 0, 0
	for _, o := range outcomes {
		switch o {
		case identitystore.OutcomeNew:
			newCount++
		case identitystore.OutcomeConflict:
			conflictCount++
		}
	}
	if newCount != 1 {
		t.Errorf("OutcomeNew count = %d, want exactly 1 (exactly one distinct key wins the race)", newCount)
	}
	if conflictCount != attempts-1 {
		t.Errorf("OutcomeConflict count = %d, want %d", conflictCount, attempts-1)
	}
}

func fingerprintFor(i int) string {
	return "fp-" + string(rune('a'+i))
}

func TestDisableMarksAKnownNodeIDAndEnableClearsIt(t *testing.T) {
	store := open(t)
	now := time.Now()

	if _, err := store.Reserve("node-1", "fp-a", now); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if store.IsDisabled("node-1") {
		t.Fatalf("IsDisabled() = true before Disable(), want false")
	}

	if err := store.Disable("node-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if !store.IsDisabled("node-1") {
		t.Fatalf("IsDisabled() = false after Disable(), want true")
	}

	if err := store.Enable("node-1", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if store.IsDisabled("node-1") {
		t.Fatalf("IsDisabled() = true after Enable(), want false")
	}
}

func TestDisableCreatesARecordForANodeIDThatNeverRegistered(t *testing.T) {
	store := open(t)
	now := time.Now()

	// An operator may disable a node_id pre-emptively, before it has ever
	// registered, to block a bootstrap token that leaked before it was
	// consumed.
	if err := store.Disable("node-never-seen", now); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if !store.IsDisabled("node-never-seen") {
		t.Fatalf("IsDisabled() = false, want true")
	}
}

func TestIsDisabledIsFalseForAnUnknownNodeID(t *testing.T) {
	store := open(t)
	if store.IsDisabled("node-unknown") {
		t.Fatalf("IsDisabled() = true for a node_id with no record, want false")
	}
}

func TestDisableAndEnableAreIdempotent(t *testing.T) {
	store := open(t)
	now := time.Now()

	if err := store.Disable("node-1", now); err != nil {
		t.Fatalf("first Disable() error = %v", err)
	}
	if err := store.Disable("node-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("second Disable() error = %v", err)
	}
	if !store.IsDisabled("node-1") {
		t.Fatalf("IsDisabled() = false, want true")
	}

	if err := store.Enable("node-2", now); err != nil {
		t.Fatalf("Enable() on a node_id that was never disabled, error = %v, want nil", err)
	}
	if store.IsDisabled("node-2") {
		t.Fatalf("IsDisabled() = true for node-2, want false")
	}
}

func TestDisabledNodeIDsReportsEveryCurrentlyDisabledNode(t *testing.T) {
	store := open(t)
	now := time.Now()

	if err := store.Disable("node-1", now); err != nil {
		t.Fatalf("Disable(node-1) error = %v", err)
	}
	if err := store.Disable("node-2", now); err != nil {
		t.Fatalf("Disable(node-2) error = %v", err)
	}
	if _, err := store.Reserve("node-3", "fp-c", now); err != nil {
		t.Fatalf("Reserve(node-3) error = %v", err)
	}

	got := store.DisabledNodeIDs()
	want := map[string]bool{"node-1": true, "node-2": true}
	if len(got) != len(want) {
		t.Fatalf("DisabledNodeIDs() = %v, want exactly %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("DisabledNodeIDs() included unexpected %q", id)
		}
	}

	if err := store.Enable("node-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("Enable(node-1) error = %v", err)
	}
	got = store.DisabledNodeIDs()
	if len(got) != 1 || got[0] != "node-2" {
		t.Fatalf("DisabledNodeIDs() after Enable(node-1) = %v, want [node-2]", got)
	}
}

func TestDisabledStatePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identities.json")
	now := time.Now()

	store, err := identitystore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Disable("node-1", now); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}

	reopened, err := identitystore.Open(path)
	if err != nil {
		t.Fatalf("re-Open() error = %v", err)
	}
	if !reopened.IsDisabled("node-1") {
		t.Fatalf("IsDisabled() on the reopened store = false, want true")
	}
}
