package tunnel_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel/internal/tunneltest"
)

// The connection-table tests run a real Manager over a fleet of in-memory
// replicas. Every tunnel is a real Client with a real state machine and a
// real slot pool; what the fleet removes is the listener, the TLS handshake
// and the network, not any of the logic under test.

const (
	gw1 = "gw-1.example.com:8443"
	gw2 = "gw-2.example.com:8443"
	gw3 = "gw-3.example.com:8443"
)

// replicaConn is one accepted Control stream plus the replica it belongs to.
type replicaConn struct {
	endpoint string
	sess     *tunneltest.ControlSession
}

// managerFixture is one Manager over a fleet of fake replicas.
type managerFixture struct {
	t     *testing.T
	fleet *tunneltest.Fleet
	clock *tunneltest.Clock
	mgr   *tunnel.Manager
	ids   *fakeIdentities

	cancel    context.CancelFunc
	done      chan error
	collected bool
	result    error
}

// fakeIdentities hands out node identities on demand, so a test can rotate
// the certificate without a CA, a Registry or a file on disk.
type fakeIdentities struct {
	mu       sync.Mutex
	current  *tunnel.Identity
	err      error
	requests int
}

func newFakeIdentities(nodeID string) *fakeIdentities {
	return &fakeIdentities{current: &tunnel.Identity{
		NodeID:    nodeID,
		NotBefore: tunnelNow,
		NotAfter:  tunnelNow.Add(30 * 24 * time.Hour),
	}}
}

func (f *fakeIdentities) Ensure(context.Context) (*tunnel.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if f.err != nil {
		return nil, f.err
	}
	return f.current, nil
}

// rotate installs a new certificate, as a renewal would.
func (f *fakeIdentities) rotate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current = &tunnel.Identity{
		NodeID:    f.current.NodeID,
		NotBefore: f.current.NotBefore.Add(time.Hour),
		NotAfter:  f.current.NotAfter.Add(30 * 24 * time.Hour),
	}
}

func (f *fakeIdentities) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func newManagerFixture(t *testing.T, seeds []string, mutate func(*tunnel.ManagerConfig)) *managerFixture {
	t.Helper()

	f := &managerFixture{
		t:     t,
		fleet: tunneltest.NewFleet(),
		clock: tunneltest.NewClock(tunnelNow),
		ids:   newFakeIdentities("mac-mini-01"),
		done:  make(chan error, 1),
	}

	cfg := tunnel.ManagerConfig{
		Client: tunnel.ClientConfig{
			NodeID:       "mac-mini-01",
			AgentVersion: "test",
			Manager:      tunneltest.NewManager(),
			// A fixed jitter fraction makes every backoff delay exact.
			Jitter:         func() float64 { return 0.5 },
			BackoffInitial: time.Second,
			BackoffMax:     30 * time.Second,
		},
		SeedEndpoints:    seeds,
		Identities:       f.ids,
		TransportFactory: f.fleet.Transport,
		Clock:            f.clock,
		Logger:           slog.New(slog.DiscardHandler),
	}
	if mutate != nil {
		mutate(&cfg)
	}

	mgr, err := tunnel.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	f.mgr = mgr
	return f
}

// start runs the manager and guarantees it is stopped and collected before
// the test ends, so TestMain's goroutine-leak assertion means something.
func (f *managerFixture) start() {
	f.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	go func() { f.done <- f.mgr.Run(ctx) }()
	f.t.Cleanup(func() {
		cancel()
		if f.collected {
			return
		}
		select {
		case <-f.done:
		case <-time.After(testTimeout):
			f.t.Error("Run did not return after the context was cancelled")
		}
	})
}

func (f *managerFixture) runResult() error {
	f.t.Helper()
	if f.collected {
		return f.result
	}
	select {
	case err := <-f.done:
		f.result, f.collected = err, true
		return err
	case <-time.After(testTimeout):
		f.t.Fatal("Run did not return")
		return nil
	}
}

// handshake accepts one replica's Control stream, answers Hello and consumes
// the initial status report, leaving that tunnel connected.
func (f *managerFixture) handshake(endpoint string) *replicaConn {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	sess, err := f.fleet.Get(endpoint).AcceptControl(ctx)
	if err != nil {
		f.t.Fatalf("%s: no control stream was opened: %v", endpoint, err)
	}
	frame, err := sess.RecvFromAgent(ctx)
	if err != nil {
		f.t.Fatalf("%s: no frame from the agent: %v", endpoint, err)
	}
	if frame.GetHello() == nil {
		f.t.Fatalf("%s: the first frame was %T, want Hello", endpoint, frame.GetBody())
	}
	if err := sess.SendToAgent(&tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Ack{
		Ack: &tunnelv1.HelloAck{ReplicaId: endpoint, ServerUnixMs: tunnelNow.UnixMilli()},
	}}); err != nil {
		f.t.Fatalf("%s: send HelloAck: %v", endpoint, err)
	}
	if _, err := sess.RecvFromAgent(ctx); err != nil {
		f.t.Fatalf("%s: no status report after connecting: %v", endpoint, err)
	}
	return &replicaConn{endpoint: endpoint, sess: sess}
}

// handshakeAll connects several replicas, in whatever order they dial.
func (f *managerFixture) handshakeAll(endpoints ...string) map[string]*replicaConn {
	f.t.Helper()
	conns := make(map[string]*replicaConn, len(endpoints))
	for _, endpoint := range endpoints {
		conns[endpoint] = f.handshake(endpoint)
	}
	return conns
}

// sendRoster plays a roster broadcast down one replica's Control stream.
func (f *managerFixture) sendRoster(conn *replicaConn, pb *tunnelv1.GatewayRoster) {
	f.t.Helper()
	if err := conn.sess.SendToAgent(&tunnelv1.GatewayControl{
		Body: &tunnelv1.GatewayControl_Roster{Roster: pb},
	}); err != nil {
		f.t.Fatalf("%s: send roster: %v", conn.endpoint, err)
	}
}

// waitStates polls until the connection table satisfies pred.
func (f *managerFixture) waitStates(want string, pred func(map[string]tunnel.State) bool) map[string]tunnel.State {
	f.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		states := f.mgr.TunnelStates()
		if pred(states) {
			return states
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("the connection table never reached %s; states = %v", want, states)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitConnected waits until exactly n tunnels can take a request.
func (f *managerFixture) waitConnected(n int) {
	f.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		got := f.mgr.ConnectedReplicas()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("connected replicas = %d, want %d (states %v)", got, n, f.mgr.TunnelStates())
		}
		time.Sleep(time.Millisecond)
	}
}

func connectedIn(states map[string]tunnel.State) []string {
	var out []string
	for endpoint, state := range states {
		if state == tunnel.StateConnected || state == tunnel.StateServing {
			out = append(out, endpoint)
		}
	}
	return out
}

// -----------------------------------------------------------------------
// Seeds
// -----------------------------------------------------------------------

func TestManagerStartsWithOneReachableSeed(t *testing.T) {
	f := newManagerFixture(t, []string{gw1, gw2, gw3}, nil)
	// Two of the three seeds are down, which is the normal state of a seed
	// list somebody wrote months ago.
	f.fleet.Unreachable(gw1)
	f.fleet.Unreachable(gw3)
	f.start()

	f.handshake(gw2)
	f.waitConnected(1)

	// The unreachable seeds do not fail the manager: their Clients keep
	// backing off, so a replica coming back needs no restart.
	states := f.mgr.TunnelStates()
	if len(states) != 3 {
		t.Errorf("tunnels = %d, want %d: an unreachable seed still gets a supervised tunnel", len(states), 3)
	}
	if err, done := f.tryRunResult(50 * time.Millisecond); done {
		t.Fatalf("Run returned %v while a replica was still connected", err)
	}
}

func (f *managerFixture) tryRunResult(wait time.Duration) (error, bool) {
	f.t.Helper()
	if f.collected {
		return f.result, true
	}
	select {
	case err := <-f.done:
		f.result, f.collected = err, true
		return err, true
	case <-time.After(wait):
		return nil, false
	}
}

func TestManagerConnectsEveryReplicaInTheRoster(t *testing.T) {
	f := newManagerFixture(t, []string{gw1}, nil)
	f.start()

	conn := f.handshake(gw1)
	f.waitConnected(1)

	// Scaling the Gateway out reaches the Agent through the roster alone: no
	// restart, no configuration change.
	f.sendRoster(conn, replicas(1, "gw-1@"+gw1, "gw-2@"+gw2, "gw-3@"+gw3))
	f.handshakeAll(gw2, gw3)
	f.waitConnected(3)

	// Each replica is a separate connection: there is no forwarding between
	// them, so each one had to be dialled.
	for _, endpoint := range []string{gw1, gw2, gw3} {
		if got := f.fleet.Dials(endpoint); got != 1 {
			t.Errorf("%s dials = %d, want %d", endpoint, got, 1)
		}
	}
}

// -----------------------------------------------------------------------
// Roster diffing
// -----------------------------------------------------------------------

func TestManagerRetiresAReplicaTheRosterDropped(t *testing.T) {
	f := newManagerFixture(t, []string{gw1}, nil)
	f.start()

	conn := f.handshake(gw1)
	f.sendRoster(conn, replicas(1, "gw-1@"+gw1, "gw-2@"+gw2))
	f.handshake(gw2)
	f.waitConnected(2)

	// A removed replica's tunnel is closed outright: the roster is the
	// authority on which replicas exist.
	f.sendRoster(conn, &tunnelv1.GatewayRoster{Version: 2, Replicas: []*tunnelv1.GatewayReplica{
		{ReplicaId: "gw-1", Endpoint: gw1, State: tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE},
		{ReplicaId: "gw-2", Endpoint: gw2, State: tunnelv1.ReplicaState_REPLICA_STATE_REMOVED},
	}})

	f.waitStates("gw-2 retired", func(states map[string]tunnel.State) bool {
		_, present := states[gw2]
		return !present
	})
	f.waitConnected(1)
}

func TestManagerStopsRefillingADrainingReplica(t *testing.T) {
	handler := newScriptedHandler()
	release := make(chan struct{})
	releaseRequest := sync.OnceFunc(func() { close(release) })
	handler.set(func(ctx context.Context, _ *tunnel.Request, _ tunnel.ResponseSink) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	// The request is released at the end of the test, and again here in case
	// an assertion above ends the test first. A request left in flight makes
	// shutdown correct but slow: draining waits out the full grace period for
	// something that will never finish, and the resulting teardown failure
	// buries the assertion that actually failed.
	defer releaseRequest()

	f := newManagerFixture(t, []string{gw1}, func(cfg *tunnel.ManagerConfig) {
		cfg.Client.Handler = handler
		cfg.Client.Slots = tunnel.SlotConfig{MinSlots: 1, LowWatermark: 1, BulkSlots: -1, NodeTotalSlots: 4}
	})
	f.start()

	conn := f.handshake(gw1)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// One warmed slot, one request dispatched into it, and the refill that
	// the low watermark then asks for.
	busy := f.acceptWarmSlot(ctx, gw1)
	if err := busy.SendToAgent(&tunnelv1.GatewayFrame{
		RequestId: "req-1",
		Body: &tunnelv1.GatewayFrame_Headers{Headers: &tunnelv1.RequestHeaders{
			RuntimeId: testRuntimeID,
			Operation: tunnelv1.Operation_OPERATION_CHAT,
		}},
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	handler.waitCall(t)
	idle := f.acceptWarmSlot(ctx, gw1)

	// Draining means "no new requests", not "drop what is running": the
	// parked slot goes back, nothing replaces it, and the busy one stays.
	f.sendRoster(conn, &tunnelv1.GatewayRoster{Version: 1, Replicas: []*tunnelv1.GatewayReplica{
		{ReplicaId: "gw-1", Endpoint: gw1, State: tunnelv1.ReplicaState_REPLICA_STATE_DRAINING},
	}})

	f.waitSlots("no idle slots and one busy", gw1, func(s tunnel.PoolStats) bool {
		return s.Inference.Idle == 0 && s.Inference.Busy == 1
	})
	// The pool drops a reaped slot from its counters before the slot's own
	// goroutine has finished unwinding, so the stream closing is a separate,
	// slightly later event than the count reaching zero. Poll for it rather
	// than reading it in the same instant.
	f.waitClosed("the parked slot on a draining replica", idle)
	if _, live := f.mgr.TunnelStates()[gw1]; !live {
		t.Fatal("a draining replica's tunnel was closed; its in-flight request would have been cut short")
	}
	if got := f.fleet.Get(gw1).ServeDials(); got != 2 {
		t.Errorf("serve streams = %d, want %d: a draining replica must not be refilled", got, 2)
	}

	// The in-flight request still finishes on the slot it started on.
	releaseRequest()
	for {
		frame, err := busy.RecvFromAgent(ctx)
		if err != nil {
			t.Fatalf("the in-flight request did not finish while draining: %v", err)
		}
		if end := frame.GetEnd(); end != nil {
			if end.GetError() != nil {
				t.Errorf("the in-flight request failed while draining: %v", end.GetError())
			}
			break
		}
	}

	// ResponseEnd going out and the pool's in-flight tally dropping are two
	// moments, not one. Shutdown must not begin between them: draining polls
	// the tally on the injected clock, so a drain that starts with the count
	// still at one parks on a timer this test never advances.
	f.waitSlots("the drained request to be counted as finished", gw1, func(s tunnel.PoolStats) bool {
		return s.Inference.Busy == 0
	})
}

// acceptWarmSlot takes the next slot a tunnel opens and consumes its Ready.
func (f *managerFixture) acceptWarmSlot(ctx context.Context, endpoint string) *tunneltest.ServeSession {
	f.t.Helper()
	slot, err := f.fleet.Get(endpoint).AcceptServe(ctx)
	if err != nil {
		f.t.Fatalf("%s: no slot was opened: %v", endpoint, err)
	}
	frame, err := slot.RecvFromAgent(ctx)
	if err != nil {
		f.t.Fatalf("%s: no frame on a new slot: %v", endpoint, err)
	}
	if frame.GetReady() == nil {
		f.t.Fatalf("%s: the first frame on a slot was %T, want Ready", endpoint, frame.GetBody())
	}
	return slot
}

// waitSlots polls one tunnel's slot occupancy until it satisfies pred.
// waitClosed waits for a slot stream to actually end. Reaping is asynchronous:
// the pool stops counting a slot as soon as it decides to close it, and the
// stream ends once the slot's goroutine unwinds.
func (f *managerFixture) waitClosed(what string, session *tunneltest.ServeSession) {
	f.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		if session.Closed() {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("%s never closed", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *managerFixture) waitSlots(want, endpoint string, pred func(tunnel.PoolStats) bool) {
	f.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		stats, ok := f.mgr.SlotStats(endpoint)
		if ok && pred(stats) {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("%s never reached %s; inference=%+v", endpoint, want, stats.Inference)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerIgnoresAStaleRosterFromAnotherReplica(t *testing.T) {
	f := newManagerFixture(t, []string{gw1}, nil)
	f.start()

	conn1 := f.handshake(gw1)
	f.sendRoster(conn1, replicas(5, "gw-1@"+gw1, "gw-2@"+gw2))
	conn2 := f.handshake(gw2)
	f.waitConnected(2)

	f.sendRoster(conn1, replicas(6, "gw-1@"+gw1))
	f.waitStates("gw-2 retired", func(states map[string]tunnel.State) bool {
		_, present := states[gw2]
		return !present
	})

	// Replicas broadcast independently, so gw-2's copy of version 5 can
	// arrive after gw-1's version 6. Applying it would resurrect a replica
	// that has just been closed.
	f.sendRoster(conn2, replicas(5, "gw-1@"+gw1, "gw-2@"+gw2))

	if version, _ := f.mgr.RosterVersion(); version != 6 {
		t.Errorf("roster version = %d, want %d", version, 6)
	}
	// Give the manager a moment to act on the stale frame if it were going to.
	time.Sleep(20 * time.Millisecond)
	if _, present := f.mgr.TunnelStates()[gw2]; present {
		t.Error("a stale roster brought a retired replica back")
	}
}

func TestManagerTruncatesAnOversizedRoster(t *testing.T) {
	f := newManagerFixture(t, []string{gw1}, func(cfg *tunnel.ManagerConfig) {
		cfg.MaxGateways = 2
	})
	f.start()

	conn := f.handshake(gw1)
	f.sendRoster(conn, replicas(1, "gw-1@"+gw1, "gw-2@"+gw2, "gw-3@"+gw3))

	// max_gateways is the backstop against a poisoned or misconfigured
	// roster exhausting a home Mac's file descriptors.
	f.waitStates("a truncated table", func(states map[string]tunnel.State) bool {
		return len(states) == 2
	})
	time.Sleep(20 * time.Millisecond)
	if got := len(f.mgr.TunnelStates()); got != 2 {
		t.Errorf("tunnels = %d, want %d", got, 2)
	}
}

// -----------------------------------------------------------------------
// Isolation
// -----------------------------------------------------------------------

func TestManagerIsolatesOneReplicaFailure(t *testing.T) {
	f := newManagerFixture(t, []string{gw1}, nil)
	f.start()

	conn := f.handshake(gw1)
	f.sendRoster(conn, replicas(1, "gw-1@"+gw1, "gw-2@"+gw2, "gw-3@"+gw3))
	conns := f.handshakeAll(gw2, gw3)
	f.waitConnected(3)

	// Kill one replica outright, the way a rolling upgrade does.
	f.fleet.Unreachable(gw2)
	conns[gw2].sess.Break(io.EOF)

	f.waitConnected(2)
	states := f.mgr.TunnelStates()
	if got := connectedIn(states); len(got) != 2 {
		t.Fatalf("connected replicas = %v, want gw-1 and gw-3", got)
	}
	for _, endpoint := range []string{gw1, gw3} {
		if state := states[endpoint]; state != tunnel.StateConnected && state != tunnel.StateServing {
			t.Errorf("%s is %s after a sibling died; the failure was not isolated", endpoint, state)
		}
	}

	// The dead replica is still supervised, so it rejoins on its own once it
	// comes back — no restart, no roster change.
	if _, present := states[gw2]; !present {
		t.Error("the failed replica was dropped from the table instead of being retried")
	}
}

func TestManagerGivesUpWhenEveryReplicaRejectsTheNode(t *testing.T) {
	f := newManagerFixture(t, []string{gw1}, nil)
	f.start()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	sess, err := f.fleet.Get(gw1).AcceptControl(ctx)
	if err != nil {
		t.Fatalf("no control stream: %v", err)
	}
	if _, err := sess.RecvFromAgent(ctx); err != nil {
		t.Fatalf("no Hello: %v", err)
	}

	// A replica that answers Hello with the wrong frame is a protocol dead
	// end, not something a reconnect fixes.
	if err := sess.SendToAgent(&tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_HbAck{
		HbAck: &tunnelv1.HeartbeatAck{},
	}}); err != nil {
		t.Fatalf("send: %v", err)
	}

	err = f.runResult()
	if !tunnel.IsFatal(err) {
		t.Fatalf("Run returned %v, want a fatal error once every replica had rejected the node", err)
	}
}

// -----------------------------------------------------------------------
// Slot budget
// -----------------------------------------------------------------------

func TestManagerResharesTheSlotBudget(t *testing.T) {
	f := newManagerFixture(t, []string{gw1}, func(cfg *tunnel.ManagerConfig) {
		cfg.Client.Handler = newScriptedHandler()
		cfg.Client.Slots = tunnel.SlotConfig{MinSlots: 1, LowWatermark: 1, BulkSlots: -1, NodeTotalSlots: 12}
	})
	f.start()

	conn := f.handshake(gw1)
	waitCeiling := func(want int) {
		t.Helper()
		deadline := time.Now().Add(testTimeout)
		for {
			if got := f.mgr.ConnectedReplicas(); got > 0 {
				if stats := f.slotCeiling(gw1); stats == want {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("gw-1 slot ceiling = %d, want %d", f.slotCeiling(gw1), want)
			}
			time.Sleep(time.Millisecond)
		}
	}

	// One replica owns the whole node budget.
	waitCeiling(12)

	// Three active replicas each get a third, rounded up: the shares may add
	// up to more than the node can run, which the runtime limiter absorbs.
	f.sendRoster(conn, replicas(2, "gw-1@"+gw1, "gw-2@"+gw2, "gw-3@"+gw3))
	f.handshakeAll(gw2, gw3)
	f.waitConnected(3)
	waitCeiling(4)

	// Shrinking back hands the budget back rather than leaving it stranded.
	f.sendRoster(conn, replicas(3, "gw-1@"+gw1))
	f.waitStates("a single replica", func(states map[string]tunnel.State) bool {
		return len(states) == 1
	})
	waitCeiling(12)
}

// slotCeiling reports the inference slot ceiling currently in force on one
// tunnel, which is how the shared budget is observed from outside.
func (f *managerFixture) slotCeiling(endpoint string) int {
	f.t.Helper()
	stats, ok := f.mgr.SlotStats(endpoint)
	if !ok {
		return -1
	}
	return stats.Inference.Max
}

// -----------------------------------------------------------------------
// Certificate rotation
// -----------------------------------------------------------------------

func TestManagerRotatesTunnelsOneAtATime(t *testing.T) {
	f := newManagerFixture(t, []string{gw1}, func(cfg *tunnel.ManagerConfig) {
		cfg.IdentityInterval = time.Hour
	})
	f.start()

	conn := f.handshake(gw1)
	f.sendRoster(conn, replicas(1, "gw-1@"+gw1, "gw-2@"+gw2, "gw-3@"+gw3))
	f.handshakeAll(gw2, gw3)
	f.waitConnected(3)

	// A rotation must never make the node unreachable to every replica at
	// once, so the manager stops one tunnel, waits for its replacement to
	// connect, and only then moves on. The test plays the replicas in the
	// order the manager dials them and asserts the others stayed up.
	f.ids.rotate()
	f.clock.Advance(time.Hour)

	reconnected := map[string]bool{}
	for range 3 {
		endpoint := f.acceptRedial(reconnected)
		reconnected[endpoint] = true
		if got := f.mgr.ConnectedReplicas(); got < 2 {
			t.Fatalf("connected replicas = %d during rotation; the whole node went dark", got)
		}
	}

	f.waitConnected(3)
	for _, endpoint := range []string{gw1, gw2, gw3} {
		if got := f.fleet.Dials(endpoint); got != 2 {
			t.Errorf("%s dials = %d, want %d: the tunnel was not rebuilt on the new certificate", endpoint, got, 2)
		}
	}
}

// acceptRedial completes the handshake for whichever replica is dialled next,
// skipping those already reconnected.
func (f *managerFixture) acceptRedial(done map[string]bool) string {
	f.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for {
		for _, endpoint := range []string{gw1, gw2, gw3} {
			if done[endpoint] {
				continue
			}
			if f.fleet.Dials(endpoint) < 2 {
				continue
			}
			f.handshake(endpoint)
			return endpoint
		}
		if time.Now().After(deadline) {
			f.t.Fatal("no tunnel was rebuilt after the certificate rotated")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerKeepsRunningWhenTheRegistryIsUnreachable(t *testing.T) {
	f := newManagerFixture(t, []string{gw1}, func(cfg *tunnel.ManagerConfig) {
		cfg.IdentityInterval = time.Hour
	})
	f.start()

	f.handshake(gw1)
	f.waitConnected(1)

	// A Registry outage must not take a node with a valid certificate out of
	// service: only a fatal identity error does that.
	f.ids.fail(&runtime.RuntimeError{
		Code:      runtime.ErrorConnection,
		Operation: "renew_certificate",
		Message:   "registry unavailable",
		Retryable: true,
	})
	f.clock.Advance(time.Hour)

	if err, done := f.tryRunResult(100 * time.Millisecond); done {
		t.Fatalf("Run returned %v after a transient identity failure", err)
	}
	if got := f.mgr.ConnectedReplicas(); got != 1 {
		t.Errorf("connected replicas = %d, want %d", got, 1)
	}
}

func TestManagerStopsOnAFatalIdentityFailure(t *testing.T) {
	f := newManagerFixture(t, []string{gw1}, nil)
	f.ids.fail(&runtime.RuntimeError{
		Code:      runtime.ErrorUnauthorized,
		Operation: "register",
		Message:   "the bootstrap token was rejected",
		Cause:     tunnel.ErrFatal,
	})
	f.start()

	// The certificate is node-wide, so this is reported once rather than
	// once per replica, and no tunnel is opened at all.
	err := f.runResult()
	if !tunnel.IsFatal(err) {
		t.Fatalf("Run returned %v, want a fatal identity error", err)
	}
	if got := f.fleet.Dials(gw1); got != 0 {
		t.Errorf("dials = %d, want %d: a node with no certificate has nothing to say to a gateway", got, 0)
	}
}

// -----------------------------------------------------------------------
// Configuration
// -----------------------------------------------------------------------

func TestNewManagerRejectsAnInvalidConfiguration(t *testing.T) {
	base := func() tunnel.ManagerConfig {
		return tunnel.ManagerConfig{
			Client:        tunnel.ClientConfig{NodeID: "mac-mini-01", Manager: tunneltest.NewManager()},
			SeedEndpoints: []string{gw1},
			Logger:        slog.New(slog.DiscardHandler),
		}
	}

	tests := []struct {
		name   string
		mutate func(*tunnel.ManagerConfig)
	}{
		{name: "no node id", mutate: func(c *tunnel.ManagerConfig) { c.Client.NodeID = "" }},
		{name: "no runtime manager", mutate: func(c *tunnel.ManagerConfig) { c.Client.Manager = nil }},
		{name: "no seed endpoints", mutate: func(c *tunnel.ManagerConfig) { c.SeedEndpoints = nil }},
		{name: "a seed with a scheme", mutate: func(c *tunnel.ManagerConfig) { c.SeedEndpoints = []string{"https://" + gw1} }},
		{
			name: "max_gateways below the seed count",
			mutate: func(c *tunnel.ManagerConfig) {
				c.SeedEndpoints = []string{gw1, gw2, gw3}
				c.MaxGateways = 2
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)
			_, err := tunnel.NewManager(cfg)
			if err == nil {
				t.Fatal("NewManager accepted an invalid configuration")
			}
			var re *runtime.RuntimeError
			if !errors.As(err, &re) || re.Code != runtime.ErrorInvalidConfig {
				t.Errorf("error = %v, want a %s RuntimeError", err, runtime.ErrorInvalidConfig)
			}
			if !tunnel.IsFatal(err) {
				t.Error("a configuration error must be fatal; retrying it changes nothing")
			}
		})
	}
}

// -----------------------------------------------------------------------
// Integration
// -----------------------------------------------------------------------

func TestManagerServesThroughSurvivingReplicasAfterAKill(t *testing.T) {
	// The full stack over three replicas: connection table, state machines,
	// slot pools and the dispatcher, all in memory. One replica is killed
	// mid-flight and the other two have to keep answering.
	runtimes := tunneltest.NewManager()
	backend := &tunneltest.InferenceRuntime{BaseRuntime: tunneltest.BaseRuntime{Desc: runtime.Descriptor{
		ID: testRuntimeID, Kind: runtime.KindOllama, BaseURL: "http://127.0.0.1:11434",
	}}}
	backend.ChatFunc = func(_ context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
		return runtime.ChatResponse{ID: "cmpl-1", Model: req.Model,
			Message: runtime.ChatMessage{Role: "assistant", Content: "pong"}}, nil
	}
	runtimes.SetRuntime(testRuntimeID, backend)

	dispatcher, err := tunnel.NewDispatcher(tunnel.DispatchConfig{
		Manager:         runtimes,
		AllowedRuntimes: []string{testRuntimeID},
		Logger:          slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	f := newManagerFixture(t, []string{gw1}, func(cfg *tunnel.ManagerConfig) {
		cfg.Client.Manager = runtimes
		cfg.Client.Handler = dispatcher
		cfg.Client.Slots = tunnel.SlotConfig{MinSlots: 1, LowWatermark: 1, BulkSlots: -1, NodeTotalSlots: 3}
	})
	f.start()

	conn := f.handshake(gw1)
	f.sendRoster(conn, replicas(1, "gw-1@"+gw1, "gw-2@"+gw2, "gw-3@"+gw3))
	conns := f.handshakeAll(gw2, gw3)
	f.waitConnected(3)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	slots := map[string]*tunneltest.ServeSession{}
	for _, endpoint := range []string{gw1, gw2, gw3} {
		slots[endpoint] = f.acceptWarmSlot(ctx, endpoint)
	}

	chat := func(endpoint, requestID string) {
		t.Helper()
		slot := slots[endpoint]
		payload := mustMarshal(t, tunnelwire.MarshalChatRequest, runtime.ChatRequest{Model: "llama3"})
		if err := slot.SendToAgent(&tunnelv1.GatewayFrame{
			RequestId: requestID,
			Body: &tunnelv1.GatewayFrame_Headers{Headers: &tunnelv1.RequestHeaders{
				RuntimeId: testRuntimeID,
				Operation: tunnelv1.Operation_OPERATION_CHAT,
				Payload:   payload,
			}},
		}); err != nil {
			t.Fatalf("%s: dispatch: %v", endpoint, err)
		}
		for {
			frame, err := slot.RecvFromAgent(ctx)
			if err != nil {
				t.Fatalf("%s: no response: %v", endpoint, err)
			}
			if end := frame.GetEnd(); end != nil {
				if end.GetError() != nil {
					t.Fatalf("%s: request failed: %v", endpoint, end.GetError())
				}
				return
			}
		}
	}

	// Every replica reaches the backend on its own: no request is forwarded
	// between them.
	for i, endpoint := range []string{gw1, gw2, gw3} {
		chat(endpoint, "req-"+strconv.Itoa(i))
	}

	// Kill the middle replica, connection and slots together.
	f.fleet.Unreachable(gw2)
	slots[gw2].Break(io.EOF)
	conns[gw2].sess.Break(io.EOF)

	f.waitConnected(2)

	// The survivors keep their connections, their slots and their ability to
	// answer. Their slot streams were never touched.
	chat(gw1, "req-after-1")
	chat(gw3, "req-after-2")
}
