package e2e

import (
	"context"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

// TestRollingUpgradeKeepsAtLeastOneTunnelAvailable replaces every Gateway
// replica one at a time — stop it, start its replacement on the same
// address, wait for the Agent to find the replacement again — while a
// background monitor continuously drives real chat requests through
// whichever replica currently has a live route to the node. A rolling
// upgrade that ever drops to zero usable tunnels is a user-visible outage;
// this proves the Agent's independent per-replica tunnels make that
// impossible as long as the Gateway side replaces replicas one at a time
// rather than all together (contrast with
// TestFaultInjectionAllReplicasKilled, which replaces them all at once and
// is expected to have a gap).
func TestRollingUpgradeKeepsAtLeastOneTunnelAvailable(t *testing.T) {
	const replicaCount = 3
	f := newFleet(t, replicaCount)
	f.awaitReady(f.replicas...)

	// active tracks, per original replica slot, whichever process currently
	// holds that slot: the original while it is up, nil while its
	// replacement is still starting, the replacement once it is ready.
	var (
		mu     sync.Mutex
		active = append([]*replica(nil), f.replicas[:replicaCount]...)
	)
	setActive := func(i int, r *replica) {
		mu.Lock()
		active[i] = r
		mu.Unlock()
	}
	snapshot := func() []*replica {
		mu.Lock()
		defer mu.Unlock()
		return append([]*replica(nil), active...)
	}

	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	var (
		monitorWG            sync.WaitGroup
		attempts, successes  int
		longestGap           time.Duration
		lastSuccess          = time.Now()
		firstFailureAt       time.Time
		everyoneWasDownAtOne bool
	)
	monitorWG.Add(1)
	go func() {
		defer monitorWG.Done()
		for monitorCtx.Err() == nil {
			ok := false
			for _, r := range snapshot() {
				if r == nil {
					continue
				}
				if info, present := r.server.Node("mac-mini-01"); !present || !info.Live {
					continue
				}
				rt := r.server.Runtime("mac-mini-01", "backend-1")
				cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, err := rt.Chat(cctx, runtime.ChatRequest{
					Model:    "e2e-model",
					Messages: []runtime.ChatMessage{{Role: "user", Content: "rolling"}},
				})
				cancel()
				attempts++
				if err == nil {
					ok = true
					successes++
					break
				}
			}

			now := time.Now()
			if ok {
				if gap := now.Sub(lastSuccess); gap > longestGap {
					longestGap = gap
				}
				lastSuccess = now
			} else if firstFailureAt.IsZero() {
				firstFailureAt = now
				everyoneWasDownAtOne = true
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Roll the fleet: one replica down, its replacement up and ready, before
	// moving on to the next. Never more than one out of three down at once.
	for i, r := range f.replicas[:replicaCount] {
		addr := r.addr
		r.stop()
		setActive(i, nil)

		replacement := f.restartReplica(r, addr)
		awaitReplicaReady(t, replacement)
		setActive(i, replacement)
	}

	stopMonitor()
	monitorWG.Wait()

	if everyoneWasDownAtOne {
		t.Fatalf("no replica served a request at %s during the rolling upgrade; every tunnel was down at once",
			firstFailureAt.Format(time.RFC3339Nano))
	}
	if successes == 0 {
		t.Fatal("the monitor never observed a successful request during the rolling upgrade")
	}
	if longestGap > waitTimeout {
		t.Errorf("longest gap between successful requests was %v, want well under %v", longestGap, waitTimeout)
	}
	t.Logf("rolling upgrade: %d replicas replaced one at a time, %d/%d monitor requests served, longest gap between successes %v",
		replicaCount, successes, attempts, longestGap)
}
