package comfyui

import (
	"container/list"
	"strconv"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/runtimetest"
)

func newSubmitTable(clock runtime.Clock) *Runtime {
	return &Runtime{
		clock:          clock,
		submitted:      make(map[string]*submitEntry),
		submittedOrder: list.New(),
	}
}

// complete claims key and resolves it as a success, returning whether the
// caller owned the claim.
func complete(r *Runtime, key, runID string) (owned bool) {
	entry, owner := r.claimSubmitted(key)
	if owner {
		r.resolveSubmitted(key, entry, runtime.WorkflowRun{ID: runID}, true)
	}
	return owner
}

func TestSubmittedEntriesExpireAfterTheTTL(t *testing.T) {
	clock := runtimetest.NewClock(time.Unix(1700000000, 0))
	r := newSubmitTable(clock)

	if !complete(r, "job", "run-1") {
		t.Fatal("the first claim of a key must own it")
	}

	clock.Advance(submittedTTL - time.Second)
	entry, owner := r.claimSubmitted("job")
	if owner || !entry.ok || entry.run.ID != "run-1" {
		t.Fatalf("within the TTL the key must answer with the original run, got owner=%v ok=%v run=%q", owner, entry.ok, entry.run.ID)
	}

	clock.Advance(2 * time.Second)
	if _, owner := r.claimSubmitted("job"); !owner {
		t.Fatal("past the TTL the key must be forgotten so a new submission can own it")
	}
	// The expired entry was dropped, not left behind next to its replacement.
	if got := r.submittedOrder.Len(); got != 0 {
		t.Errorf("completed queue holds %d entries after expiry, want 0", got)
	}
}

func TestSubmittedTableIsBounded(t *testing.T) {
	clock := runtimetest.NewClock(time.Unix(1700000000, 0))
	r := newSubmitTable(clock)

	total := maxSubmitted + 500
	for i := range total {
		if !complete(r, "key-"+strconv.Itoa(i), "run-"+strconv.Itoa(i)) {
			t.Fatalf("key %d was already known", i)
		}
		// Each call may add one entry; the table must never exceed the cap
		// even transiently between calls.
		if got := len(r.submitted); got > maxSubmitted {
			t.Fatalf("after %d keys the table holds %d entries, cap is %d", i+1, got, maxSubmitted)
		}
	}
	if got := len(r.submitted); got != maxSubmitted {
		t.Errorf("table holds %d entries, want exactly the cap %d", got, maxSubmitted)
	}
	if got := r.submittedOrder.Len(); got != len(r.submitted) {
		t.Errorf("expiry queue has %d entries but the table has %d: they must stay in step", got, len(r.submitted))
	}

	// Oldest out: the first keys are forgotten, the latest are kept.
	if entry, owner := r.claimSubmitted("key-" + strconv.Itoa(total-1)); owner || entry.run.ID != "run-"+strconv.Itoa(total-1) {
		t.Error("the most recent key was evicted")
	}
	if _, owner := r.claimSubmitted("key-0"); !owner {
		t.Error("the oldest key survived past the cap")
	}
}

func TestSubmittedTableDropsEverythingOnceTheTTLPasses(t *testing.T) {
	clock := runtimetest.NewClock(time.Unix(1700000000, 0))
	r := newSubmitTable(clock)
	for i := range 1000 {
		complete(r, strconv.Itoa(i), "run")
		clock.Advance(time.Second)
	}
	clock.Advance(2 * submittedTTL)
	// Any claim sweeps the expired entries.
	r.claimSubmitted("fresh")
	if got := len(r.submitted); got != 1 {
		t.Errorf("table holds %d entries after the TTL, want only the fresh claim", got)
	}
}

func TestSubmittedFailureReleasesTheKey(t *testing.T) {
	clock := runtimetest.NewClock(time.Unix(1700000000, 0))
	r := newSubmitTable(clock)

	entry, owner := r.claimSubmitted("job")
	if !owner {
		t.Fatal("first claim must own the key")
	}
	r.resolveSubmitted("job", entry, runtime.WorkflowRun{}, false)

	select {
	case <-entry.done:
	default:
		t.Fatal("a failed submission must still wake its waiters")
	}
	if entry.ok {
		t.Error("a failed submission was recorded as a success")
	}
	if len(r.submitted) != 0 || r.submittedOrder.Len() != 0 {
		t.Errorf("a failure left %d entries behind", len(r.submitted))
	}
	if _, owner := r.claimSubmitted("job"); !owner {
		t.Error("after a failure the next caller must be able to submit for real")
	}
}

func TestSubmittedInFlightEntriesAreNeverEvicted(t *testing.T) {
	clock := runtimetest.NewClock(time.Unix(1700000000, 0))
	r := newSubmitTable(clock)

	inflight, owner := r.claimSubmitted("slow")
	if !owner {
		t.Fatal("first claim must own the key")
	}
	for i := range 2 * maxSubmitted {
		complete(r, "k"+strconv.Itoa(i), "run")
	}
	clock.Advance(2 * submittedTTL)
	got, owner := r.claimSubmitted("slow")
	if owner || got != inflight {
		t.Fatal("an in-flight submission was evicted: a second Submit would have been sent to ComfyUI")
	}
}
