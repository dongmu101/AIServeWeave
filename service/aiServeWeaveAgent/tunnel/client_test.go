package tunnel_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel/internal/tunneltest"
)

// tunnelNow is the instant every tunnel test starts from; all timing is
// driven by the fake clock from there, so no test sleeps for a heartbeat.
var tunnelNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// testTimeout bounds every wait on a real channel. It is generous because it
// is only ever reached when something is actually broken.
const testTimeout = 5 * time.Second

// armedAfterConnect is how many fake timers exist once a tunnel is connected:
// the Hello deadline plus the heartbeat, status-poll and status-full timers.
const armedAfterConnect = 4

// clientFixture is one Client wired to an in-memory Gateway and a fake clock.
type clientFixture struct {
	t     *testing.T
	gw    *tunneltest.Gateway
	clock *tunneltest.Clock
	mgr   *tunneltest.Manager

	client  *tunnel.Client
	states  chan tunnel.State
	rosters chan *tunnelv1.GatewayRoster
	hints   chan *tunnelv1.SlotHint

	cancel    context.CancelFunc
	done      chan error
	result    error
	collected bool
	armed     int
}

func newClientFixture(t *testing.T, mutate func(*tunnel.ClientConfig)) *clientFixture {
	t.Helper()

	f := &clientFixture{
		t:       t,
		gw:      tunneltest.NewGateway(),
		clock:   tunneltest.NewClock(tunnelNow),
		mgr:     tunneltest.NewManager(),
		states:  make(chan tunnel.State, 64),
		rosters: make(chan *tunnelv1.GatewayRoster, 8),
		hints:   make(chan *tunnelv1.SlotHint, 8),
		done:    make(chan error, 1),
	}

	cfg := tunnel.ClientConfig{
		NodeID:       "mac-mini-01",
		Endpoint:     "gw-1.example.com:8443",
		AgentVersion: "test",
		Manager:      f.mgr,
		Clock:        f.clock,
		Logger:       slog.New(slog.DiscardHandler),
		// A fixed jitter fraction makes every backoff delay exact.
		Jitter:         func() float64 { return 0.5 },
		BackoffInitial: time.Second,
		BackoffMax:     30 * time.Second,
		OnState:        func(s tunnel.State) { f.states <- s },
		OnRoster:       func(r *tunnelv1.GatewayRoster) { f.rosters <- r },
		OnSlotHint:     func(h *tunnelv1.SlotHint) { f.hints <- h },
	}
	if mutate != nil {
		mutate(&cfg)
	}

	client, err := tunnel.NewClient(cfg, f.gw)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	f.client = client
	return f
}

// start runs the tunnel and guarantees it is stopped and collected before the
// test ends, so TestMain's goroutine-leak assertion means something.
func (f *clientFixture) start() {
	f.t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	go func() { f.done <- f.client.Run(ctx) }()
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

// runResult waits for Run to return and remembers the outcome, so a test that
// already collected it does not make the cleanup wait for a second value.
func (f *clientFixture) runResult() error {
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

// tryResult reports whether Run has already returned, waiting at most wait.
// Like runResult it remembers the outcome, so the cleanup does not wait for a
// second value that will never come.
func (f *clientFixture) tryResult(wait time.Duration) (error, bool) {
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

func (f *clientFixture) accept() *tunneltest.ControlSession {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	sess, err := f.gw.AcceptControl(ctx)
	if err != nil {
		f.t.Fatalf("no control stream was opened: %v", err)
	}
	return sess
}

func (f *clientFixture) recv(sess *tunneltest.ControlSession) *tunnelv1.AgentControl {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	frame, err := sess.RecvFromAgent(ctx)
	if err != nil {
		f.t.Fatalf("no frame from the agent: %v", err)
	}
	return frame
}

func (f *clientFixture) send(sess *tunneltest.ControlSession, frame *tunnelv1.GatewayControl) {
	f.t.Helper()
	if err := sess.SendToAgent(frame); err != nil {
		f.t.Fatalf("send to agent: %v", err)
	}
}

// connect performs the handshake and consumes the initial full status report,
// leaving the tunnel connected and its three loop timers armed.
func (f *clientFixture) connect() *tunneltest.ControlSession {
	f.t.Helper()

	sess := f.accept()
	if hello := f.recv(sess).GetHello(); hello == nil {
		f.t.Fatal("the first frame was not Hello")
	}
	f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Ack{Ack: &tunnelv1.HelloAck{
		ReplicaId:    "gw-1",
		ServerUnixMs: tunnelNow.UnixMilli(),
	}}})
	if status := f.recv(sess).GetStatus(); status == nil {
		f.t.Fatal("the agent did not report runtime status right after connecting")
	}
	f.waitState(tunnel.StateConnected)
	f.waitArmed(armedAfterConnect)
	return sess
}

// waitState waits for the tunnel to report want, failing with the states it
// passed through instead.
func (f *clientFixture) waitState(want tunnel.State) {
	f.t.Helper()
	var seen []tunnel.State
	deadline := time.After(testTimeout)
	for {
		select {
		case s := <-f.states:
			if s == want {
				return
			}
			seen = append(seen, s)
		case <-deadline:
			f.t.Fatalf("state %s never reached; saw %v", want, seen)
		}
	}
}

// waitArmed blocks until the tunnel has armed n more timers than the fixture
// has already accounted for. Waiting on the count rather than on wall time is
// what keeps an Advance from slipping past a timer that the loop registers a
// moment later, which would hang the test rather than fail it.
func (f *clientFixture) waitArmed(n int) {
	f.t.Helper()
	want := f.armed + n
	if !f.clock.WaitArmed(want, testTimeout) {
		f.t.Fatalf("armed timers = %d, want %d: the tunnel has not reached its select loop", f.clock.Armed(), want)
	}
	f.armed = want
}

// advance moves the fake clock and then waits for the tunnel to re-arm the
// timers the jump tripped. Waiting afterwards rather than before is what makes
// the next advance safe: a timer that has not been armed yet would be
// registered with a deadline beyond the jump and never fire.
func (f *clientFixture) advance(d time.Duration, fires int) {
	f.t.Helper()
	f.clock.Advance(d)
	if fires > 0 {
		f.waitArmed(fires)
	}
}

// -----------------------------------------------------------------------
// Handshake and connection state
// -----------------------------------------------------------------------

func TestClientHandshakeReachesConnected(t *testing.T) {
	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		cfg.AllowedRuntimes = []string{"ollama-local", "vllm-a"}
		cfg.Resources = &tunnelv1.NodeResources{CpuCores: 10, GpuCount: 1, Os: "darwin"}
	})
	f.start()

	sess := f.accept()
	hello := f.recv(sess).GetHello()
	if hello == nil {
		t.Fatal("the first frame on a Control stream must be Hello")
	}
	if got, want := hello.GetNodeId(), "mac-mini-01"; got != want {
		t.Errorf("hello node_id = %q, want %q", got, want)
	}
	if got, want := hello.GetAgentVersion(), "test"; got != want {
		t.Errorf("hello agent_version = %q, want %q", got, want)
	}
	if got, want := len(hello.GetRuntimeIds()), 2; got != want {
		t.Errorf("hello runtime_ids = %v, want the local allowlist of %d entries", hello.GetRuntimeIds(), want)
	}
	if got := hello.GetResources().GetCpuCores(); got != 10 {
		t.Errorf("hello cpu_cores = %d, want 10", got)
	}
	if got := f.client.State(); got != tunnel.StateConnecting {
		t.Errorf("state before HelloAck = %s, want %s", got, tunnel.StateConnecting)
	}

	f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Ack{Ack: &tunnelv1.HelloAck{ReplicaId: "gw-1"}}})
	f.waitState(tunnel.StateConnected)

	if got, want := f.client.ReplicaID(), "gw-1"; got != want {
		t.Errorf("replica id = %q, want %q", got, want)
	}
	if st := f.recv(sess).GetStatus(); st == nil || !st.GetFull() {
		t.Error("a connected tunnel must open with a full runtime status report")
	}
}

func TestClientFatalHandshakeFailures(t *testing.T) {
	tests := []struct {
		name     string
		dialErr  error
		respond  func(f *clientFixture, sess *tunneltest.ControlSession)
		wantCode runtime.ErrorCode
	}{
		{
			name:     "gateway rejects the node certificate",
			dialErr:  status.Error(codes.Unauthenticated, "unknown node"),
			wantCode: runtime.ErrorUnauthorized,
		},
		{
			name:     "gateway rejects this node id",
			dialErr:  status.Error(codes.PermissionDenied, "node_id does not match the certificate SAN"),
			wantCode: runtime.ErrorUnauthorized,
		},
		{
			name: "gateway answers Hello with the wrong frame",
			respond: func(f *clientFixture, sess *tunneltest.ControlSession) {
				f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_HbAck{HbAck: &tunnelv1.HeartbeatAck{}}})
			},
			// A replica that does not answer Hello with HelloAck is not
			// speaking this protocol; dialling again cannot fix that.
			wantCode: runtime.ErrorProtocol,
		},
		{
			name: "gateway never answers Hello",
			respond: func(f *clientFixture, sess *tunneltest.ControlSession) {
				f.waitArmed(1) // the Hello deadline
				f.advance(30*time.Second, 0)
			},
			wantCode: runtime.ErrorTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newClientFixture(t, nil)
			if tt.dialErr != nil {
				f.gw.SetDialError(tt.dialErr)
			}
			f.start()

			if tt.respond != nil {
				sess := f.accept()
				if hello := f.recv(sess).GetHello(); hello == nil {
					t.Fatal("the first frame was not Hello")
				}
				tt.respond(f, sess)
			}

			err := f.runResult()
			if !tunnel.IsFatal(err) {
				t.Fatalf("Run error = %v, want a fatal tunnel error", err)
			}
			var rerr *runtime.RuntimeError
			if !errors.As(err, &rerr) {
				t.Fatalf("error type = %T, want *runtime.RuntimeError", err)
			}
			if rerr.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", rerr.Code, tt.wantCode)
			}
			if got := f.client.State(); got != tunnel.StateFailed {
				t.Errorf("state = %s, want %s: a fatal failure must not be retried", got, tunnel.StateFailed)
			}
			if got := f.gw.Dials(); got != 1 {
				t.Errorf("dials = %d, want 1: a fatal failure must not be retried", got)
			}
		})
	}
}

func TestClientReconnectsAfterTransientFailures(t *testing.T) {
	tests := []struct {
		name    string
		breakIt func(f *clientFixture)
	}{
		{
			name: "replica unreachable",
			breakIt: func(f *clientFixture) {
				f.gw.SetDialError(status.Error(codes.Unavailable, "connection refused"))
			},
		},
		{
			name: "control stream dropped",
			breakIt: func(f *clientFixture) {
				sess := f.connect()
				sess.Break(io.EOF)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newClientFixture(t, nil)
			f.start()
			tt.breakIt(f)

			f.waitState(tunnel.StateReconnecting)
			dialsBefore := f.gw.Dials()

			// The delay is full jitter over [0, 1s) with the fixture's fixed
			// 0.5 fraction: 500ms of fake time, not a second of real time.
			f.gw.SetDialError(nil)
			f.waitArmed(1) // the backoff timer
			f.advance(500*time.Millisecond, 0)

			deadline := time.After(testTimeout)
			for f.gw.Dials() <= dialsBefore {
				select {
				case <-deadline:
					t.Fatalf("dials = %d, want more than %d: the tunnel never retried", f.gw.Dials(), dialsBefore)
				default:
					time.Sleep(time.Millisecond)
				}
			}
			if got := f.client.State(); got == tunnel.StateFailed {
				t.Error("a transient failure must not reach the failed state")
			}
		})
	}
}

func TestClientBackoffResetsAfterASuccessfulHandshake(t *testing.T) {
	f := newClientFixture(t, nil)
	f.start()

	// First break: the tunnel had connected, so the delay is the first
	// window, [0, 1s) — 500ms with the fixture's fixed jitter.
	sess := f.connect()
	sess.Break(io.EOF)
	f.waitState(tunnel.StateReconnecting)

	f.waitArmed(1) // the backoff timer
	f.advance(499*time.Millisecond, 0)
	if got := f.gw.Dials(); got != 1 {
		t.Fatalf("dials = %d, want 1: the tunnel retried before its backoff elapsed", got)
	}
	f.advance(time.Millisecond, 0)

	// Reconnecting successfully must reset the sequence, so a second break
	// waits the same 500ms rather than 1s.
	sess = f.connect()
	sess.Break(io.EOF)
	f.waitState(tunnel.StateReconnecting)

	f.waitArmed(1) // the backoff timer, drawn from the reset window
	f.advance(500*time.Millisecond, 0)

	third := f.accept()
	if hello := f.recv(third).GetHello(); hello == nil {
		t.Fatal("the third attempt did not start with Hello")
	}
}

// -----------------------------------------------------------------------
// Draining
// -----------------------------------------------------------------------

func TestClientDrainsOnShutdown(t *testing.T) {
	var inflight atomic.Int64
	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		cfg.DrainTimeout = time.Minute
		cfg.InFlight = func() int { return int(inflight.Load()) }
	})
	f.start()
	sess := f.connect()

	f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Shutdown{Shutdown: &tunnelv1.Shutdown{
		Reason:      "rolling upgrade",
		GracePeriod: durationpb.New(20 * time.Second),
	}}})

	draining := f.recv(sess).GetDraining()
	if draining == nil {
		t.Fatal("the agent did not announce draining")
	}
	if got, want := draining.GetReason(), "rolling upgrade"; got != want {
		t.Errorf("draining reason = %q, want %q", got, want)
	}
	if got, want := draining.GetDeadlineUnixMs(), tunnelNow.Add(20*time.Second).UnixMilli(); got != want {
		t.Errorf("draining deadline = %d, want %d: the grace period is the replica's, clamped locally", got, want)
	}

	err := f.runResult()
	if !errors.Is(err, tunnel.ErrShutdownRequested) {
		t.Fatalf("Run error = %v, want ErrShutdownRequested", err)
	}
	if !sess.SendClosed() {
		t.Error("the agent did not half-close the control stream after draining")
	}
	if got := f.gw.Dials(); got != 1 {
		t.Errorf("dials = %d, want 1: a replica that asked us to leave must not be re-dialled", got)
	}
	if tunnel.IsFatal(err) {
		t.Error("a requested shutdown is a normal outcome, not a fatal failure")
	}
}

func TestClientDrainWaitsForInFlightRequests(t *testing.T) {
	var inflight atomic.Int64
	inflight.Store(2)
	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		cfg.DrainTimeout = 30 * time.Second
		cfg.StatusPollInterval = time.Second
		cfg.InFlight = func() int { return int(inflight.Load()) }
	})
	f.start()
	sess := f.connect()

	f.cancel()
	if draining := f.recv(sess).GetDraining(); draining == nil {
		t.Fatal("cancelling the context must drain rather than cut the stream")
	}
	f.waitState(tunnel.StateDraining)

	// Still busy: the tunnel must wait rather than return.
	f.waitArmed(1)
	f.advance(time.Second, 0)
	if err, done := f.tryResult(50 * time.Millisecond); done {
		t.Fatalf("Run returned while %d requests were still in flight (err = %v)", inflight.Load(), err)
	}

	inflight.Store(0)
	f.waitArmed(1)
	f.advance(time.Second, 0)
	if err := f.runResult(); err != nil {
		t.Fatalf("Run after a clean drain = %v, want nil", err)
	}
}

func TestClientDrainGivesUpAtTheDeadline(t *testing.T) {
	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		cfg.DrainTimeout = 10 * time.Second
		cfg.StatusPollInterval = time.Second
		cfg.InFlight = func() int { return 1 } // never finishes
	})
	f.start()
	sess := f.connect()

	f.cancel()
	if draining := f.recv(sess).GetDraining(); draining == nil {
		t.Fatal("the agent did not announce draining")
	}

	// A request that never ends must not keep the Agent alive forever.
	for range 12 {
		f.waitArmed(1)
		f.advance(time.Second, 0)
		if err, done := f.tryResult(10 * time.Millisecond); done {
			if err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
			return
		}
	}
	t.Fatal("Run never returned although the drain deadline passed")
}

// -----------------------------------------------------------------------
// Configuration validation
// -----------------------------------------------------------------------

func TestNewClientRejectsInvalidConfig(t *testing.T) {
	base := func() tunnel.ClientConfig {
		return tunnel.ClientConfig{
			NodeID:   "mac-mini-01",
			Endpoint: "gw-1.example.com:8443",
			Manager:  tunneltest.NewManager(),
			Logger:   slog.New(slog.DiscardHandler),
		}
	}
	tests := []struct {
		name    string
		mutate  func(cfg *tunnel.ClientConfig)
		wantErr bool
	}{
		{"valid", func(cfg *tunnel.ClientConfig) {}, false},
		{"no node id", func(cfg *tunnel.ClientConfig) { cfg.NodeID = "" }, true},
		{"endpoint with scheme", func(cfg *tunnel.ClientConfig) { cfg.Endpoint = "https://gw-1.example.com:8443" }, true},
		{"endpoint without port", func(cfg *tunnel.ClientConfig) { cfg.Endpoint = "gw-1.example.com" }, true},
		{"no manager", func(cfg *tunnel.ClientConfig) { cfg.Manager = nil }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)
			_, err := tunnel.NewClient(cfg, tunneltest.NewGateway())
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("NewClient error = %v, want error = %t", err, tt.wantErr)
			}
			if tt.wantErr && !tunnel.IsFatal(err) {
				t.Errorf("error %v is not fatal; a bad configuration is never fixed by retrying", err)
			}
		})
	}
}

func TestNewClientRequiresTransport(t *testing.T) {
	_, err := tunnel.NewClient(tunnel.ClientConfig{
		NodeID:   "mac-mini-01",
		Endpoint: "gw-1.example.com:8443",
		Manager:  tunneltest.NewManager(),
		Logger:   slog.New(slog.DiscardHandler),
	}, nil)
	if !tunnel.IsFatal(err) {
		t.Fatalf("error = %v, want a fatal tunnel error", err)
	}
}

func TestStateString(t *testing.T) {
	tests := []struct {
		state tunnel.State
		want  string
	}{
		{tunnel.StateDisconnected, "disconnected"},
		{tunnel.StateConnecting, "connecting"},
		{tunnel.StateConnected, "connected"},
		{tunnel.StateServing, "serving"},
		{tunnel.StateReconnecting, "reconnecting"},
		{tunnel.StateDraining, "draining"},
		{tunnel.StateFailed, "failed"},
		{tunnel.StateRetired, "retired"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", int(tt.state), got, tt.want)
		}
	}
}

// -----------------------------------------------------------------------
// Slot pool integration
// -----------------------------------------------------------------------

func TestClientEntersServingOnceItsSlotsAreWarm(t *testing.T) {
	handler := newScriptedHandler()
	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		cfg.Handler = handler
		cfg.Slots = tunnel.SlotConfig{MinSlots: 2, LowWatermark: 1, BulkSlots: -1, NodeTotalSlots: 4}
	})
	f.start()
	sess := f.connect()

	// A connected tunnel warms its slots and only then counts as serving:
	// StateConnected means the replica can talk to this node, StateServing
	// means it can dispatch to it.
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	for range 2 {
		slot, err := f.gw.AcceptServe(ctx)
		if err != nil {
			t.Fatalf("the connected tunnel did not warm its slots: %v", err)
		}
		frame, err := slot.RecvFromAgent(ctx)
		if err != nil {
			t.Fatalf("no frame on a warmed slot: %v", err)
		}
		if frame.GetReady() == nil {
			t.Fatalf("the first frame on a slot was %T, want Ready", frame.GetBody())
		}
	}
	f.waitState(tunnel.StateServing)

	// The slots belong to this connection. Breaking the Control stream voids
	// them, so no request can survive on a link the replica has lost sight of.
	sess.Break(io.EOF)
	f.waitState(tunnel.StateReconnecting)
	if got := f.client.SlotStats().Inference.Total(); got != 0 {
		t.Errorf("slots after the control stream broke = %d, want %d", got, 0)
	}
}

func TestClientDrainWaitsForItsSlots(t *testing.T) {
	handler := newScriptedHandler()
	release := make(chan struct{})
	handler.set(func(ctx context.Context, _ *tunnel.Request, _ tunnel.ResponseSink) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		cfg.Handler = handler
		cfg.Slots = tunnel.SlotConfig{MinSlots: 1, LowWatermark: 1, BulkSlots: -1, NodeTotalSlots: 1}
	})
	f.start()
	f.connect()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	slot, err := f.gw.AcceptServe(ctx)
	if err != nil {
		t.Fatalf("the connected tunnel did not warm a slot: %v", err)
	}
	if _, err := slot.RecvFromAgent(ctx); err != nil {
		t.Fatalf("no Ready on the warmed slot: %v", err)
	}
	if err := slot.SendToAgent(&tunnelv1.GatewayFrame{
		RequestId: "req-1",
		Body: &tunnelv1.GatewayFrame_Headers{Headers: &tunnelv1.RequestHeaders{
			RuntimeId: "ollama-local",
			Operation: tunnelv1.Operation_OPERATION_CHAT,
		}},
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	handler.waitCall(t)

	// A shutdown must not cut the request short: the drain waits for the slot
	// pool's own in-flight count to reach zero, polling on the injected clock
	// rather than on wall time.
	f.cancel()
	f.waitState(tunnel.StateDraining)
	for range 3 {
		f.clock.Advance(2 * time.Second)
	}
	if _, done := f.tryResult(50 * time.Millisecond); done {
		t.Fatal("Run returned while a request was still in flight")
	}

	close(release)
	deadline := time.Now().Add(testTimeout)
	for {
		if _, done := f.tryResult(10 * time.Millisecond); done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Run did not return after the in-flight request finished")
		}
		f.clock.Advance(2 * time.Second)
	}
	if err := f.runResult(); err != nil {
		t.Errorf("Run after a graceful drain = %v, want nil", err)
	}
}
