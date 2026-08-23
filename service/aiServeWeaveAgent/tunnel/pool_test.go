package tunnel_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel/internal/tunneltest"
)

// The slot pool tests drive a real Pool against the in-memory Gateway: every
// slot is a real Serve stream carrying real frames, but no port is opened, no
// TLS is negotiated and no test sleeps for a watermark. The pool's own timers
// run on the fake clock; the short polls in waitStats are only ever traversed
// while a goroutine is on its way to a state it is already headed for.

// slotConn is one accepted slot: the replica side of the stream plus the
// Ready frame the Agent parked it with.
type slotConn struct {
	sess  *tunneltest.ServeSession
	ready *tunnelv1.Ready
}

// handlerCall records one invocation of the pool's Handler.
type handlerCall struct {
	req *tunnel.Request
	ctx context.Context
}

// scriptedHandler is the tunnel.Handler the pool dispatches into. By default
// every request succeeds immediately; a test installs its own function to
// hold a request open, fail it, or write a streaming response.
type scriptedHandler struct {
	mu    sync.Mutex
	fn    func(context.Context, *tunnel.Request, tunnel.ResponseSink) error
	calls chan *handlerCall
}

func newScriptedHandler() *scriptedHandler {
	return &scriptedHandler{calls: make(chan *handlerCall, 64)}
}

// set installs the behaviour for subsequent requests.
func (h *scriptedHandler) set(fn func(context.Context, *tunnel.Request, tunnel.ResponseSink) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fn = fn
}

// Handle implements tunnel.Handler.
func (h *scriptedHandler) Handle(ctx context.Context, req *tunnel.Request, sink tunnel.ResponseSink) error {
	h.mu.Lock()
	fn := h.fn
	h.mu.Unlock()

	select {
	case h.calls <- &handlerCall{req: req, ctx: ctx}:
	default:
	}
	if fn == nil {
		return nil
	}
	return fn(ctx, req, sink)
}

// waitCall returns the next request the pool dispatched.
func (h *scriptedHandler) waitCall(t *testing.T) *handlerCall {
	t.Helper()
	select {
	case call := <-h.calls:
		return call
	case <-time.After(testTimeout):
		t.Fatal("the pool never dispatched a request to the handler")
		return nil
	}
}

// poolFixture is one Pool wired to an in-memory Gateway and a fake clock.
type poolFixture struct {
	t       *testing.T
	gw      *tunneltest.Gateway
	clock   *tunneltest.Clock
	handler *scriptedHandler
	pool    *tunnel.Pool
	serving chan bool
}

func newPoolFixture(t *testing.T, mutate func(*tunnel.PoolConfig)) *poolFixture {
	t.Helper()

	f := &poolFixture{
		t:       t,
		gw:      tunneltest.NewGateway(),
		clock:   tunneltest.NewClock(tunnelNow),
		handler: newScriptedHandler(),
		serving: make(chan bool, 16),
	}

	cfg := tunnel.PoolConfig{
		NodeID:    "mac-mini-01",
		ReplicaID: "gw-1",
		Handler:   f.handler,
		Clock:     f.clock,
		Logger:    slog.New(slog.DiscardHandler),
		OnServing: func(serving bool) { f.serving <- serving },
		Slots: tunnel.SlotConfig{
			MinSlots:       2,
			LowWatermark:   1,
			BulkSlots:      1,
			NodeTotalSlots: 8,
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}

	pool, err := tunnel.NewPool(cfg, f.gw)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	f.pool = pool
	return f
}

// start runs the pool and guarantees it is stopped before the test ends, so
// TestMain's goroutine-leak assertion means something.
func (f *poolFixture) start() {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := f.pool.Start(ctx); err != nil {
		cancel()
		f.t.Fatalf("Start: %v", err)
	}
	f.t.Cleanup(func() {
		f.pool.Stop()
		cancel()
	})
}

// acceptSlots collects the next n slots the pool opened, each already parked
// with Ready.
func (f *poolFixture) acceptSlots(n int) []slotConn {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	conns := make([]slotConn, 0, n)
	for range n {
		sess, err := f.gw.AcceptServe(ctx)
		if err != nil {
			f.t.Fatalf("only %d of %d slots were opened: %v", len(conns), n, err)
		}
		frame, err := sess.RecvFromAgent(ctx)
		if err != nil {
			f.t.Fatalf("a new slot sent no frame: %v", err)
		}
		ready := frame.GetReady()
		if ready == nil {
			f.t.Fatalf("the first frame on a slot was %T, want Ready", frame.GetBody())
		}
		conns = append(conns, slotConn{sess: sess, ready: ready})
	}
	return conns
}

// ofClass returns the accepted slots of one class. Slots of both classes open
// concurrently, so a test must select rather than assume an order.
func ofClass(conns []slotConn, class tunnelv1.SlotClass) []slotConn {
	var out []slotConn
	for _, c := range conns {
		if c.ready.GetClass() == class {
			out = append(out, c)
		}
	}
	return out
}

// waitStats polls until the pool's occupancy satisfies pred, failing with the
// occupancy it got stuck at.
func (f *poolFixture) waitStats(want string, pred func(tunnel.PoolStats) bool) tunnel.PoolStats {
	f.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		stats := f.pool.Stats()
		if pred(stats) {
			return stats
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("the slot pool never reached %s; inference=%+v bulk=%+v",
				want, stats.Inference, stats.Bulk)
		}
		time.Sleep(time.Millisecond)
	}
}

// dispatch plays a RequestHeaders frame into a slot, the way a replica does.
func (f *poolFixture) dispatch(c slotConn, requestID string) {
	f.t.Helper()
	f.sendFrame(c, &tunnelv1.GatewayFrame{
		RequestId: requestID,
		Body: &tunnelv1.GatewayFrame_Headers{Headers: &tunnelv1.RequestHeaders{
			RuntimeId: "ollama-local",
			Operation: tunnelv1.Operation_OPERATION_CHAT,
		}},
	})
}

func (f *poolFixture) sendFrame(c slotConn, frame *tunnelv1.GatewayFrame) {
	f.t.Helper()
	if err := c.sess.SendToAgent(frame); err != nil {
		f.t.Fatalf("send to slot %s: %v", c.ready.GetSlotId(), err)
	}
}

func (f *poolFixture) recvFrame(c slotConn) *tunnelv1.AgentFrame {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	frame, err := c.sess.RecvFromAgent(ctx)
	if err != nil {
		f.t.Fatalf("no frame from slot %s: %v", c.ready.GetSlotId(), err)
	}
	return frame
}

// expectEnd reads until the request terminates, returning its ResponseEnd and
// the payloads of the DataChunks that preceded it.
func (f *poolFixture) expectEnd(c slotConn, requestID string) (*tunnelv1.ResponseEnd, [][]byte) {
	f.t.Helper()
	var chunks [][]byte
	for {
		frame := f.recvFrame(c)
		if got := frame.GetRequestId(); got != "" && got != requestID {
			f.t.Fatalf("frame for request %q on a slot serving %q", got, requestID)
		}
		switch body := frame.GetBody().(type) {
		case *tunnelv1.AgentFrame_Data:
			chunks = append(chunks, body.Data.GetPayload())
		case *tunnelv1.AgentFrame_Headers:
		case *tunnelv1.AgentFrame_End:
			return body.End, chunks
		default:
			f.t.Fatalf("frame %T while waiting for ResponseEnd of %q", frame.GetBody(), requestID)
		}
	}
}

// expectReady requires the slot to park itself again, which is how it says it
// is available for reuse.
func (f *poolFixture) expectReady(c slotConn) {
	f.t.Helper()
	frame := f.recvFrame(c)
	if frame.GetReady() == nil {
		f.t.Fatalf("frame %T after a completed request, want Ready", frame.GetBody())
	}
}

// expectClosed requires the slot's stream to end, which is how a retired,
// reaped or aborted slot appears to the replica.
func (f *poolFixture) expectClosed(c slotConn) {
	f.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for !c.sess.Closed() {
		if time.Now().After(deadline) {
			f.t.Fatalf("slot %s never closed its stream", c.ready.GetSlotId())
		}
		time.Sleep(time.Millisecond)
	}
}

// expectClosedCount requires exactly n of the given slots to have closed.
// Which one the pool picked is deliberately unspecified — the guarantee is
// how many survive, not which.
func (f *poolFixture) expectClosedCount(conns []slotConn, n int) {
	f.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		closed := 0
		for _, c := range conns {
			if c.sess.Closed() {
				closed++
			}
		}
		if closed == n {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("closed slots = %d, want %d", closed, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// -----------------------------------------------------------------------
// Watermarks
// -----------------------------------------------------------------------

func TestPoolWarmsMinSlotsOnStart(t *testing.T) {
	f := newPoolFixture(t, nil)
	f.start()

	conns := f.acceptSlots(3)
	inference := ofClass(conns, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE)
	bulk := ofClass(conns, tunnelv1.SlotClass_SLOT_CLASS_BULK)

	if len(inference) != 2 {
		t.Errorf("inference slots warmed = %d, want %d", len(inference), 2)
	}
	if len(bulk) != 1 {
		t.Errorf("bulk slots warmed = %d, want %d", len(bulk), 1)
	}
	for _, c := range conns {
		if got := c.ready.GetNodeId(); got != "mac-mini-01" {
			t.Errorf("Ready.node_id = %q, want %q", got, "mac-mini-01")
		}
		if c.ready.GetSlotId() == "" {
			t.Error("Ready.slot_id is empty; the two sides' logs cannot be lined up")
		}
	}

	f.waitStats("2 idle inference and 1 idle bulk slot", func(s tunnel.PoolStats) bool {
		return s.Inference.Idle == 2 && s.Bulk.Idle == 1
	})

	select {
	case serving := <-f.serving:
		if !serving {
			t.Error("the pool reported not serving after warming its slots")
		}
	case <-time.After(testTimeout):
		t.Error("the pool never reported that it is serving")
	}
}

func TestPoolRefillsBelowLowWatermark(t *testing.T) {
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		cfg.Slots.BulkSlots = -1
	})
	f.handler.set(func(ctx context.Context, _ *tunnel.Request, _ tunnel.ResponseSink) error {
		<-ctx.Done()
		return ctx.Err()
	})
	f.start()

	conns := f.acceptSlots(2)

	// One busy slot still leaves one idle, which is the watermark exactly:
	// nothing new is opened.
	f.dispatch(conns[0], "req-1")
	f.waitStats("one busy slot", func(s tunnel.PoolStats) bool { return s.Inference.Busy == 1 })
	if got := f.pool.Stats().Inference.Total(); got != 2 {
		t.Errorf("slots after one dispatch = %d, want %d: the low watermark was still met", got, 2)
	}

	// The second dispatch drops the idle count below the watermark, so a
	// replacement must be opened before the next request arrives.
	f.dispatch(conns[1], "req-2")
	f.acceptSlots(1)
	f.waitStats("a refilled idle slot", func(s tunnel.PoolStats) bool {
		return s.Inference.Busy == 2 && s.Inference.Idle == 1
	})
}

func TestPoolRefusesToOpenAboveTheCeiling(t *testing.T) {
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		cfg.Slots.BulkSlots = -1
		cfg.Slots.NodeTotalSlots = 3
	})
	f.handler.set(func(ctx context.Context, _ *tunnel.Request, _ tunnel.ResponseSink) error {
		<-ctx.Done()
		return ctx.Err()
	})
	f.start()

	conns := f.acceptSlots(2)
	f.dispatch(conns[0], "req-1")
	f.dispatch(conns[1], "req-2")

	third := f.acceptSlots(1)
	f.dispatch(third[0], "req-3")
	f.waitStats("every slot busy at the ceiling", func(s tunnel.PoolStats) bool {
		return s.Inference.Busy == 3 && s.Inference.Max == 3
	})

	// The pool is below its low watermark and would like a fourth slot, but
	// the per-replica share forbids it: refusing here is what makes the
	// replica pick another node instead of queueing behind this one.
	f.pool.ApplyHint(nil)
	if got := f.gw.ServeDials(); got != 3 {
		t.Errorf("serve streams opened = %d, want %d: the ceiling was exceeded", got, 3)
	}
	if got := f.pool.Stats().Inference.Total(); got != 3 {
		t.Errorf("slots = %d, want %d", got, 3)
	}
}

func TestPoolReapsIdleSlotsDownToMin(t *testing.T) {
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		cfg.Slots.BulkSlots = -1
		cfg.Slots.IdleTimeout = 5 * time.Minute
	})

	release := make(chan struct{})
	f.handler.set(func(ctx context.Context, _ *tunnel.Request, _ tunnel.ResponseSink) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	f.start()

	// Grow the pool past its floor, then hand every slot back.
	conns := f.acceptSlots(2)
	f.dispatch(conns[0], "req-1")
	f.dispatch(conns[1], "req-2")
	extra := f.acceptSlots(1)
	close(release)

	for i, c := range conns {
		if end, _ := f.expectEnd(c, []string{"req-1", "req-2"}[i]); end.GetError() != nil {
			t.Fatalf("request %d failed: %v", i+1, end.GetError())
		}
		f.expectReady(c)
	}
	f.waitStats("three idle slots", func(s tunnel.PoolStats) bool { return s.Inference.Idle == 3 })

	f.clock.Advance(6 * time.Minute)
	f.waitStats("the floor after reaping", func(s tunnel.PoolStats) bool {
		return s.Inference.Total() == 2
	})

	// The surplus slot goes, the floor stays: min_slots is a guarantee, not a
	// starting point.
	f.expectClosedCount(append(conns, extra...), 1)
	if got := f.pool.Stats().Inference.Idle; got != 2 {
		t.Errorf("idle slots after reaping = %d, want %d", got, 2)
	}
}

func TestPerReplicaMax(t *testing.T) {
	tests := []struct {
		name           string
		nodeTotal      int
		minSlots       int
		activeReplicas int
		want           int
	}{
		{name: "one replica takes the whole node budget", nodeTotal: 32, minSlots: 2, activeReplicas: 1, want: 32},
		{name: "three replicas round the share up", nodeTotal: 32, minSlots: 2, activeReplicas: 3, want: 11},
		{name: "a hundred replicas fall back to the floor", nodeTotal: 32, minSlots: 2, activeReplicas: 100, want: 2},
		{name: "an exact division needs no rounding", nodeTotal: 32, minSlots: 2, activeReplicas: 4, want: 8},
		{name: "the floor wins over a share below it", nodeTotal: 4, minSlots: 3, activeReplicas: 4, want: 3},
		{name: "a missing replica count means one", nodeTotal: 32, minSlots: 2, activeReplicas: 0, want: 32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tunnel.PerReplicaMax(tt.nodeTotal, tt.minSlots, tt.activeReplicas)
			if got != tt.want {
				t.Errorf("PerReplicaMax(%d, %d, %d) = %d, want %d",
					tt.nodeTotal, tt.minSlots, tt.activeReplicas, got, tt.want)
			}
		})
	}
}

func TestPoolRescalesWithTheActiveReplicaCount(t *testing.T) {
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		cfg.Slots.BulkSlots = -1
		cfg.Slots.NodeTotalSlots = 12
	})
	f.start()
	f.acceptSlots(2)

	if got := f.pool.Stats().Inference.Max; got != 12 {
		t.Fatalf("ceiling with one replica = %d, want %d", got, 12)
	}

	f.pool.SetActiveReplicas(3)
	f.waitStats("the rescaled ceiling", func(s tunnel.PoolStats) bool { return s.Inference.Max == 4 })

	// Shrinking the share never closes a slot the pool is required to keep.
	if got := f.pool.Stats().Inference.Total(); got != 2 {
		t.Errorf("slots after rescaling = %d, want %d", got, 2)
	}
}

func TestPoolClampsTheReplicaSlotHint(t *testing.T) {
	f := newPoolFixture(t, func(cfg *tunnel.PoolConfig) {
		cfg.Slots.BulkSlots = -1
		cfg.Slots.NodeTotalSlots = 8
	})
	f.start()
	f.acceptSlots(2)

	// A hint asking for more than the node budget allows is advice the Agent
	// declines: the local configuration is the ceiling, always.
	f.pool.ApplyHint(&tunnelv1.SlotHint{MinSlots: 64, MaxSlots: 64})
	f.acceptSlots(6)
	f.waitStats("the locally capped floor", func(s tunnel.PoolStats) bool {
		return s.Inference.Total() == 8 && s.Inference.Max == 8
	})
}

// -----------------------------------------------------------------------
// Class isolation
// -----------------------------------------------------------------------

func TestPoolBulkExhaustionLeavesInferenceAlone(t *testing.T) {
	f := newPoolFixture(t, nil)

	held := make(chan struct{})
	f.handler.set(func(ctx context.Context, req *tunnel.Request, _ tunnel.ResponseSink) error {
		if req.Class == tunnelv1.SlotClass_SLOT_CLASS_BULK {
			select {
			case <-held:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	f.start()

	conns := f.acceptSlots(3)
	inference := ofClass(conns, tunnelv1.SlotClass_SLOT_CLASS_INFERENCE)
	bulk := ofClass(conns, tunnelv1.SlotClass_SLOT_CLASS_BULK)

	// Occupy the whole bulk quota with an artifact transfer that never ends.
	f.dispatch(bulk[0], "artifact-1")
	f.waitStats("the bulk quota exhausted", func(s tunnel.PoolStats) bool {
		return s.Bulk.Busy == 1 && s.Bulk.Idle == 0
	})

	// Inference is physically separate: its slots are untouched and still
	// serve, which is the entire reason the two classes exist. The wait is
	// for the pool to register the slots it has already parked — a Ready
	// frame reaches this test slightly before the pool's own bookkeeping.
	f.waitStats("both inference slots idle", func(s tunnel.PoolStats) bool {
		return s.Inference.Idle == 2
	})
	f.dispatch(inference[0], "chat-1")
	end, _ := f.expectEnd(inference[0], "chat-1")
	if end.GetError() != nil {
		t.Errorf("an inference request failed while bulk was exhausted: %v", end.GetError())
	}
	f.expectReady(inference[0])

	// The bulk quota is its own ceiling too: an exhausted bulk class does not
	// borrow an inference slot's budget.
	if got := f.pool.Stats().Bulk.Total(); got != 1 {
		t.Errorf("bulk slots = %d, want %d", got, 1)
	}
	close(held)
}
