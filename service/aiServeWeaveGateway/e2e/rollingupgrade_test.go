package e2e

import (
	"context"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

// TestRollingUpgradeKeepsAtLeastOneTunnelAvailable replaces three replicas
// sequentially with the same source version. Chat probes run while each replica
// is stopped, and a background monitor checks availability throughout the roll.
// This exercises loopback replica replacement, not mixed-version compatibility.
//
// TestRollingUpgradeKeepsAtLeastOneTunnelAvailable 用同一源码版本逐个替换三个副本。
// 每个副本停机期间均执行 Chat 探测，后台监视器同时检查整个替换过程的可用性。
// 此测试验证回环网络下的副本替换，不代表混合版本兼容性验收。
func TestRollingUpgradeKeepsAtLeastOneTunnelAvailable(t *testing.T) {
	const replicaCount = 3
	f := newFleet(t, replicaCount)
	f.awaitReady(f.replicas...)

	// active names the live replica in each original position, or nil during replacement.
	// active 记录每个原始位置上的存活副本，替换期间为 nil。
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

	serveAvailable := func(ctx context.Context) (attempts int, served bool) {
		for _, r := range snapshot() {
			if r == nil {
				continue
			}
			if info, present := r.server.Node("mac-mini-01"); !present || !info.Live {
				continue
			}
			rt := r.server.Runtime("mac-mini-01", "backend-1")
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, err := rt.Chat(cctx, runtime.ChatRequest{
				Model:    "e2e-model",
				Messages: []runtime.ChatMessage{{Role: "user", Content: "rolling"}},
			})
			cancel()
			attempts++
			if err == nil {
				return attempts, true
			}
		}
		return attempts, false
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
	t.Cleanup(func() {
		stopMonitor()
		monitorWG.Wait()
	})
	monitorWG.Add(1)
	go func() {
		defer monitorWG.Done()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for monitorCtx.Err() == nil {
			n, ok := serveAvailable(monitorCtx)
			if monitorCtx.Err() != nil {
				return
			}
			attempts += n
			if ok {
				successes++
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
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	// Probe surviving replicas before restarting the stopped replica, covering every outage window.
	// 先探测存活副本，再重启已停机副本，确保覆盖每一次停机窗口。
	for i, r := range f.replicas[:replicaCount] {
		addr := r.addr
		r.stop()
		setActive(i, nil)
		if attempts, served := serveAvailable(context.Background()); !served {
			t.Fatalf("replica %s stopped: %d Chat attempts succeeded on no surviving replica, want one successful request", r.id, attempts)
		}

		replacement := f.restartReplica(r, addr)
		awaitReplicaReady(t, replacement)
		setActive(i, replacement)
	}

	stopMonitor()
	monitorWG.Wait()
	longestGap = max(longestGap, time.Since(lastSuccess))

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
	t.Logf("same-source rolling replacement: %d replicas and stopped-replica probes completed, %d/%d monitor requests served, longest gap between successes %v",
		replicaCount, successes, attempts, longestGap)
}
