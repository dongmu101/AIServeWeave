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

// TestRecordPendingBlocksAndApproveClears covers STATUS.md's P01: a fresh
// node_id RecordPending marks is reported pending until Approve clears it,
// and Approve may also pre-create a record for a node_id that never
// attempted anything.
func TestRecordPendingBlocksAndApproveClears(t *testing.T) {
	store := open(t)
	now := time.Now()

	if store.IsPending("node-1") {
		t.Fatal("IsPending() on an unknown node_id = true, want false")
	}
	if err := store.RecordPending("node-1", now); err != nil {
		t.Fatalf("RecordPending() error = %v", err)
	}
	if !store.IsPending("node-1") {
		t.Fatal("IsPending() after RecordPending() = false, want true")
	}
	if !store.HasRecord("node-1") {
		t.Fatal("HasRecord() after RecordPending() = false, want true")
	}

	if err := store.Approve("node-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if store.IsPending("node-1") {
		t.Fatal("IsPending() after Approve() = true, want false")
	}

	// Pre-approval: a node_id that never attempted anything is not pending,
	// and Approve on it is a no-op that still leaves a record behind.
	if err := store.Approve("node-2", now); err != nil {
		t.Fatalf("Approve() on an unseen node_id error = %v", err)
	}
	if store.IsPending("node-2") {
		t.Fatal("IsPending() after a pre-emptive Approve() = true, want false")
	}
	if !store.HasRecord("node-2") {
		t.Fatal("HasRecord() after a pre-emptive Approve() = false, want true")
	}
}

// TestReserveTreatsAnEmptyFingerprintAsNoPriorBinding covers the fix that
// makes pre-approval (Approve) and pre-disable (Disable) compose safely with
// a node's first real registration: both create a record with no fingerprint
// yet, and Reserve must bind the real one rather than refuse it as
// conflicting with an empty string nobody ever proved they hold.
func TestReserveTreatsAnEmptyFingerprintAsNoPriorBinding(t *testing.T) {
	store := open(t)
	now := time.Now()

	if err := store.Approve("node-1", now); err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	outcome, err := store.Reserve("node-1", "fp-a", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if outcome != identitystore.OutcomeNew {
		t.Fatalf("Reserve() after Approve() on an empty-fingerprint record = %v, want OutcomeNew", outcome)
	}

	// The binding now holds: a second, different fingerprint still conflicts.
	outcome, err = store.Reserve("node-1", "fp-b", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("second Reserve() error = %v", err)
	}
	if outcome != identitystore.OutcomeConflict {
		t.Fatalf("Reserve() with a different fingerprint after binding = %v, want OutcomeConflict", outcome)
	}
}

// TestPreP01RecordsDecodeAsApproved simulates a ledger file written before
// pending_approval existed: JSON's zero value for a missing bool is false,
// which must read as "not pending" so an upgrade does not suddenly require
// every already-registered node to be approved again.
func TestPreP01RecordsDecodeAsApproved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identities.json")
	legacy := `[{"node_id":"node-1","fingerprint":"fp-a","first_seen_at":"2025-01-01T00:00:00Z","last_seen_at":"2025-01-01T00:00:00Z"}]`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store, err := identitystore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if store.IsPending("node-1") {
		t.Fatal("IsPending() on a pre-P01 record = true, want false (grandfathered as approved)")
	}
}

// TestSetMaintenanceAndClearMaintenance covers the maintenance flag's
// lifecycle, mirroring TestDisableMarksAKnownNodeIDAndEnableClearsIt for
// STATUS.md's P01.
func TestSetMaintenanceAndClearMaintenance(t *testing.T) {
	store := open(t)
	now := time.Now()

	if err := store.SetMaintenance("node-1", now); err != nil {
		t.Fatalf("SetMaintenance() error = %v", err)
	}
	if got := store.MaintenanceNodeIDs(); len(got) != 1 || got[0] != "node-1" {
		t.Fatalf("MaintenanceNodeIDs() = %v, want [node-1]", got)
	}

	if err := store.ClearMaintenance("node-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("ClearMaintenance() error = %v", err)
	}
	if got := store.MaintenanceNodeIDs(); len(got) != 0 {
		t.Fatalf("MaintenanceNodeIDs() after ClearMaintenance() = %v, want empty", got)
	}
}

// TestStatesReportsAllThreeFacets covers the read path ListNodeStates
// (TokenAdmin) is built on.
func TestStatesReportsAllThreeFacets(t *testing.T) {
	store := open(t)
	now := time.Now()

	if err := store.RecordPending("node-pending", now); err != nil {
		t.Fatalf("RecordPending() error = %v", err)
	}
	if err := store.Disable("node-disabled", now); err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	if err := store.SetMaintenance("node-maintained", now); err != nil {
		t.Fatalf("SetMaintenance() error = %v", err)
	}

	byID := make(map[string]identitystore.State)
	for _, st := range store.States() {
		byID[st.NodeID] = st
	}
	if st, ok := byID["node-pending"]; !ok || !st.PendingApproval {
		t.Errorf("States() node-pending = %+v, want PendingApproval=true", st)
	}
	if st, ok := byID["node-disabled"]; !ok || !st.Disabled {
		t.Errorf("States() node-disabled = %+v, want Disabled=true", st)
	}
	if st, ok := byID["node-maintained"]; !ok || !st.Maintenance {
		t.Errorf("States() node-maintained = %+v, want Maintenance=true", st)
	}
}
