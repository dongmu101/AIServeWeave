package tunnel_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel/internal/tunneltest"
)

// These are 阶段 7's pressure and fault-injection tests, in the form this
// repository can actually run: an in-memory fleet, a fake clock and no
// backend. They answer the questions the phase asks that do not need a real
// Gateway — does the slot count converge under saturation, is an exhausted
// node refused rather than queued, is the loss of a replica both survivable
// and visible — and they run in the ordinary `go test -race` gate rather than
// once, by hand, before a release.
//
// What they deliberately do not answer: absolute TTFT, the behaviour of a
// real network path, and anything requiring a Gateway implementation. Those
// stay on 阶段 7's checklist until there is one to test against.

// -----------------------------------------------------------------------
// Saturation
// -----------------------------------------------------------------------

// TestStressSlotPoolConvergesUnderSaturation fills every inference slot, holds
// them all busy, and checks the two properties the phase's acceptance
// criterion names: the pool climbs to its ceiling and stops there, and it
// comes back to its floor once the load goes away.
func TestStressSlotPoolConvergesUnderSaturation(t *testing.T) {
	const (
		nodeTotal = 8
		minSlots  = 2
	)

	mx := tunneltest.NewMetrics()
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		cfg.Metrics = mx
		cfg.Slots = tunnel.SlotConfig{
			MinSlots:       minSlots,
			LowWatermark:   1,
			BulkSlots:      1,
			NodeTotalSlots: nodeTotal,
			IdleTimeout:    time.Minute,
		}
	})
	f.start()

	// Every request blocks until the test releases it, so a slot that takes
	// one stays busy and the pool has to open another to hold its watermark.
	release := make(chan struct{})
	var running sync.WaitGroup
	f.handler.set(func(ctx context.Context, _ *tunnel.Request, _ tunnel.ResponseSink) error {
		running.Done()
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	// Take slots as fast as the pool opens them. The ceiling is what stops
	// this loop: once nodeTotal inference slots are busy, no more open.
	busy := make([]slotConn, 0, nodeTotal)
	for len(busy) < nodeTotal {
		conns := f.acceptSlots(1)
		if conns[0].ready.GetClass() != tunnelv1.SlotClass_SLOT_CLASS_INFERENCE {
			continue
		}
		running.Add(1)
		dispatchOp(f, conns[0], "req-"+conns[0].ready.GetSlotId(), tunnelv1.Operation_OPERATION_CHAT)
		busy = append(busy, conns[0])
	}
	running.Wait()

	// The ceiling holds: with every slot busy the pool wants more, and the
	// per-replica share is the only thing that says no.
	stats := f.waitStats("saturation", func(s tunnel.PoolStats) bool {
		return s.Inference.Busy == nodeTotal
	})
	if got := stats.Inference.Total(); got != nodeTotal {
		t.Errorf("inference slots at saturation = %d, want %d", got, nodeTotal)
	}
	if got := mx.Sum(tunnel.MetricSlotAcquireFailuresTotal, nil); got != 0 {
		t.Errorf("slot acquire failures under load = %v, want 0", got)
	}
	// The bulk quota was never touched: artifact transfers stay isolated from
	// a node that is fully committed to inference.
	if got := stats.Bulk.Idle; got != 1 {
		t.Errorf("idle bulk slots under inference saturation = %d, want 1", got)
	}

	close(release)
	for _, c := range busy {
		f.expectEnd(c, "req-"+c.ready.GetSlotId())
	}

	// Draining back to the floor needs the idle timeout to pass; on the fake
	// clock that is one jump, not a minute of waiting.
	f.waitStats("all slots idle", func(s tunnel.PoolStats) bool {
		return s.Inference.Idle == nodeTotal
	})
	f.clock.Advance(2 * time.Minute)
	stats = f.waitStats("convergence back to the floor", func(s tunnel.PoolStats) bool {
		return s.Inference.Total() == minSlots
	})
	if got := stats.Inference.Idle; got != minSlots {
		t.Errorf("idle inference slots after the load = %d, want %d", got, minSlots)
	}
	if got := mx.Sum(tunnel.MetricRequestsTotal, map[string]string{
		tunnel.LabelResult: string(tunnel.ResultSuccess),
	}); got != nodeTotal {
		t.Errorf("successful requests = %v, want %d", got, nodeTotal)
	}
}

// TestStressExhaustedNodeRefusesRatherThanQueues is the limiter half of the
// same question. The slot soft quota deliberately overshoots the node's real
// concurrency, so the hard quota is what a saturated node answers with — and
// it must answer immediately: a replica holding a request in a queue cannot
// send it to another node.
func TestStressExhaustedNodeRefusesRatherThanQueues(t *testing.T) {
	const (
		permits = 2
		extra   = 6
	)

	mx := tunneltest.NewMetrics()
	f := newDispatchFixture(t, func(cfg *tunnel.DispatchConfig) {
		cfg.NodeID = "mac-mini-01"
		cfg.Metrics = mx
	})

	entered := make(chan struct{}, permits)
	release := make(chan struct{})
	f.inference(permits).ChatFunc = func(ctx context.Context, _ runtime.ChatRequest) (runtime.ChatResponse, error) {
		entered <- struct{}{}
		<-release
		return runtime.ChatResponse{ID: "cmpl"}, nil
	}

	payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
	admitted := make(chan error, permits)
	for range permits {
		go func() {
			_, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, nil)
			admitted <- err
		}()
	}
	for range permits {
		<-entered
	}

	// Every further request comes back now, not when a permit frees up. The
	// assertion is the absence of blocking: these calls are made while all
	// permits are held.
	for i := range extra {
		_, err := f.dispatch(tunnelv1.Operation_OPERATION_CHAT, payload, nil)
		re := wantCode(t, err, runtime.ErrorBackpressure)
		if !errors.Is(re, runtime.ErrConcurrencyLimit) {
			t.Fatalf("request %d: error does not wrap ErrConcurrencyLimit: %v", i, re)
		}
		// The refusal carries no Retryable flag: runtime.Limiter does not set
		// one, so a replica has to branch on the code. That asymmetry belongs
		// to the Gateway side of the contract and is recorded in README.md's
		// 待决问题 rather than papered over here.
	}

	// The counter is the calibration signal README.md's 待决问题 6 asks for:
	// this many rejections against this node_total is what says the soft
	// quota is too generous.
	if got := mx.Sum(tunnel.MetricLimiterRejectionsTotal, map[string]string{
		tunnel.LabelRuntimeID: testRuntimeID,
	}); got != extra {
		t.Errorf("limiter rejections = %v, want %d", got, extra)
	}

	close(release)
	for range permits {
		if err := <-admitted; err != nil {
			t.Errorf("an admitted request failed: %v", err)
		}
	}
}

// -----------------------------------------------------------------------
// Fault injection
// -----------------------------------------------------------------------

// TestStressLosingOneReplicaLeavesTheOthersServing is the "kill one replica"
// scenario, measured the way an operator would: tunnel_connected_replicas
// drops by one and the surviving tunnels keep taking requests.
func TestStressLosingOneReplicaLeavesTheOthersServing(t *testing.T) {
	mx := tunneltest.NewMetrics()
	f := newManagerFixture(t, []string{gw1, gw2, gw3}, func(cfg *tunnel.ManagerConfig) {
		cfg.Client.Metrics = mx
	})
	f.start()

	conns := f.handshakeAll(gw1, gw2, gw3)
	f.waitConnected(3)
	waitMetric(t, mx, tunnel.MetricConnectedReplicas, nil,
		func(s *tunneltest.Series) bool { return s.Value() == 3 })

	// One replica dies mid-flight: its process is gone, so both its streams
	// break at once.
	f.fleet.Unreachable(gw2)
	conns[gw2].sess.Break(errors.New("tunneltest: replica killed"))

	waitMetric(t, mx, tunnel.MetricConnectedReplicas, nil,
		func(s *tunneltest.Series) bool { return s.Value() == 2 })
	states := f.mgr.TunnelStates()
	for _, endpoint := range []string{gw1, gw3} {
		switch states[endpoint] {
		case tunnel.StateConnected, tunnel.StateServing:
		default:
			t.Errorf("%s = %s after a different replica died, want connected", endpoint, states[endpoint])
		}
	}

	// The dead replica's tunnel counts its own reconnect and nobody else's.
	waitMetric(t, mx, tunnel.MetricReconnectsTotal,
		map[string]string{
			tunnel.LabelReplicaID: gw2,
			tunnel.LabelReason:    string(tunnel.ReconnectTransport),
		},
		func(s *tunneltest.Series) bool { return s.Value() >= 1 })
	for _, endpoint := range []string{gw1, gw3} {
		if got := mx.Sum(tunnel.MetricReconnectsTotal, map[string]string{
			tunnel.LabelReplicaID: endpoint,
		}); got != 0 {
			t.Errorf("%s reconnects = %v while a different replica died, want 0", endpoint, got)
		}
	}

	// And it comes back on its own once the replica does.
	f.fleet.Reachable(gw2)
	f.clock.Advance(time.Minute)
	f.handshake(gw2)
	f.waitConnected(3)
	waitMetric(t, mx, tunnel.MetricConnectedReplicas, nil,
		func(s *tunneltest.Series) bool { return s.Value() == 3 })
}

// TestStressAReplicaRefusingSlotsDoesNotSpin is the "backend half dead"
// scenario on the tunnel's own terms: a replica that accepts Control but
// refuses every Serve stream. The pool must report the failure and then wait,
// rather than retrying as fast as the failures come back.
func TestStressAReplicaRefusingSlotsDoesNotSpin(t *testing.T) {
	mx := tunneltest.NewMetrics()
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) { cfg.Metrics = mx })
	f.gw.SetServeError(errors.New("tunneltest: no stream quota left"))
	f.start()

	waitMetric(t, mx, tunnel.MetricSlotAcquireFailuresTotal, nil,
		func(s *tunneltest.Series) bool { return s.Value() >= 1 })

	// The clock does not move, so the open backoff never expires: whatever
	// attempts were already in flight finish and no new ones start. A pool
	// that retried on every failure would climb without bound here.
	settled := mx.Sum(tunnel.MetricSlotAcquireFailuresTotal, nil)
	time.Sleep(20 * time.Millisecond)
	if got := mx.Sum(tunnel.MetricSlotAcquireFailuresTotal, nil); got > settled+1 {
		t.Errorf("slot open attempts kept climbing while the clock stood still: %v then %v", settled, got)
	}

	// Once the replica accepts streams again the pool refills on the next
	// maintenance tick, without an operator touching anything.
	f.gw.SetServeError(nil)
	f.clock.Advance(5 * time.Minute)
	f.acceptSlots(1)
	f.waitStats("recovery after the replica accepted streams again", func(s tunnel.PoolStats) bool {
		return s.Inference.Idle >= 1
	})
}
