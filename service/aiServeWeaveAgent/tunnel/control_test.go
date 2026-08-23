package tunnel_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel/internal/tunneltest"
)

// isolateHeartbeat pushes the status timers out of the way so a test that
// drives the clock trips exactly one timer per jump.
func isolateHeartbeat(cfg *tunnel.ClientConfig) {
	cfg.HeartbeatInterval = 15 * time.Second
	cfg.StatusPollInterval = time.Hour
	cfg.StatusFullInterval = time.Hour
}

// isolateStatus does the same for the heartbeat.
func isolateStatus(cfg *tunnel.ClientConfig) {
	cfg.HeartbeatInterval = time.Hour
	cfg.StatusPollInterval = 2 * time.Second
	cfg.StatusFullInterval = 60 * time.Second
}

// snapshot builds a runtime snapshot for the status-report tests.
func snapshot(id string, state runtime.State, version string) runtime.Snapshot {
	return runtime.Snapshot{
		Descriptor: runtime.Descriptor{
			ID:            id,
			Kind:          runtime.Kind("ollama"),
			BaseURL:       "http://127.0.0.1:11434",
			MaxConcurrent: 4,
		},
		State:     state,
		Probe:     runtime.ProbeResult{Kind: runtime.Kind("ollama"), Version: version, ProbedAt: tunnelNow},
		Health:    runtime.HealthReport{State: state, CheckedAt: tunnelNow, Latency: 3 * time.Millisecond},
		Discovery: runtime.Discovery{Version: version, Models: []runtime.Model{{ID: "llama3"}}},
		UpdatedAt: tunnelNow,
	}
}

// expectNoStatus proves nothing was reported: a Ping is sent and the very next
// frame from the Agent must be the matching Pong. Any status report would have
// been queued ahead of it.
func expectNoStatus(f *clientFixture, sess *tunneltest.ControlSession, marker int64) {
	f.t.Helper()
	f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Ping{Ping: &tunnelv1.Ping{SentUnixMs: marker}}})
	frame := f.recv(sess)
	if pong := frame.GetPong(); pong == nil || pong.GetSentUnixMs() != marker {
		f.t.Fatalf("expected only a Pong, got %v: the agent reported a status change that did not happen", frame)
	}
}

// -----------------------------------------------------------------------
// Heartbeat
// -----------------------------------------------------------------------

func TestControlHeartbeatRoundTrip(t *testing.T) {
	f := newClientFixture(t, isolateHeartbeat)
	f.start()
	sess := f.connect()

	f.advance(15*time.Second, 1)
	hb := f.recv(sess).GetHeartbeat()
	if hb == nil {
		t.Fatal("no heartbeat after one interval")
	}
	if got, want := hb.GetSentUnixMs(), tunnelNow.Add(15*time.Second).UnixMilli(); got != want {
		t.Errorf("heartbeat sent_unix_ms = %d, want %d", got, want)
	}

	// The round trip is measured from the echoed timestamp, so a slow replica
	// is visible without either side trusting the other's clock.
	f.advance(250*time.Millisecond, 0)
	f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_HbAck{HbAck: &tunnelv1.HeartbeatAck{
		SentUnixMs: hb.GetSentUnixMs(),
	}}})

	deadline := time.After(testTimeout)
	for f.client.HeartbeatRTT() == 0 {
		select {
		case <-deadline:
			t.Fatal("the heartbeat round trip was never recorded")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if got, want := f.client.HeartbeatRTT(), 250*time.Millisecond; got != want {
		t.Errorf("heartbeat rtt = %s, want %s", got, want)
	}
}

func TestControlUnansweredHeartbeatsDropTheTunnel(t *testing.T) {
	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		isolateHeartbeat(cfg)
		cfg.HeartbeatFailureThreshold = 3
	})
	f.start()
	sess := f.connect()

	// Three heartbeats go out unanswered; the fourth interval is when the
	// replica is declared dead. The Control stream is the liveness verdict,
	// so this is what takes the node out of that replica's candidates.
	for i := range 3 {
		f.advance(15*time.Second, 1)
		if hb := f.recv(sess).GetHeartbeat(); hb == nil {
			t.Fatalf("no heartbeat %d of 3", i+1)
		}
		if got := f.client.State(); got != tunnel.StateConnected {
			t.Fatalf("state after %d unanswered heartbeats = %s, want %s", i+1, got, tunnel.StateConnected)
		}
	}

	f.advance(15*time.Second, 0)
	f.waitState(tunnel.StateReconnecting)

	// A dead stream must be replaced, not abandoned.
	f.waitArmed(1) // the backoff timer
	f.advance(500*time.Millisecond, 0)
	next := f.accept()
	if hello := f.recv(next).GetHello(); hello == nil {
		t.Fatal("the tunnel did not re-handshake after the heartbeat timeout")
	}
}

func TestControlAnsweredHeartbeatsKeepTheTunnelUp(t *testing.T) {
	f := newClientFixture(t, isolateHeartbeat)
	f.start()
	sess := f.connect()

	for i := range 10 {
		f.advance(15*time.Second, 1)
		hb := f.recv(sess).GetHeartbeat()
		if hb == nil {
			t.Fatalf("no heartbeat in interval %d", i+1)
		}
		f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_HbAck{HbAck: &tunnelv1.HeartbeatAck{
			SentUnixMs: hb.GetSentUnixMs(),
		}}})
	}
	if got := f.client.State(); got != tunnel.StateConnected {
		t.Errorf("state after 10 answered heartbeats = %s, want %s", got, tunnel.StateConnected)
	}
}

func TestControlPingIsAnsweredWithPong(t *testing.T) {
	f := newClientFixture(t, isolateStatus)
	f.start()
	sess := f.connect()

	f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Ping{Ping: &tunnelv1.Ping{SentUnixMs: 1234}}})
	pong := f.recv(sess).GetPong()
	if pong == nil {
		t.Fatal("a Ping was not answered with a Pong")
	}
	if got, want := pong.GetSentUnixMs(), int64(1234); got != want {
		t.Errorf("pong sent_unix_ms = %d, want the ping's %d", got, want)
	}
}

// -----------------------------------------------------------------------
// Status reporting
// -----------------------------------------------------------------------

func TestControlReportsChangesImmediately(t *testing.T) {
	f := newClientFixture(t, isolateStatus)
	f.mgr.SetSnapshots(snapshot("ollama-local", runtime.StateHealthy, "0.5.1"))
	f.start()
	sess := f.connect()

	f.mgr.SetSnapshots(snapshot("ollama-local", runtime.StateUnhealthy, "0.5.1"))
	f.advance(2*time.Second, 1)

	st := f.recv(sess).GetStatus()
	if st == nil {
		t.Fatal("a state change was not reported")
	}
	if st.GetFull() {
		t.Error("a change-triggered report must not be marked full")
	}
	if got := len(st.GetSnapshots()); got != 1 {
		t.Fatalf("snapshots = %d, want 1: only what changed is sent", got)
	}
	if got, want := st.GetSnapshots()[0].GetState(), string(runtime.StateUnhealthy); got != want {
		t.Errorf("reported state = %q, want %q", got, want)
	}
}

func TestControlStaysQuietWhenNothingMaterialChanged(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(s runtime.Snapshot) runtime.Snapshot
	}{
		{
			name:   "nothing at all changed",
			mutate: func(s runtime.Snapshot) runtime.Snapshot { return s },
		},
		{
			name: "only the health-check timestamps moved",
			mutate: func(s runtime.Snapshot) runtime.Snapshot {
				s.UpdatedAt = s.UpdatedAt.Add(time.Minute)
				s.Health.CheckedAt = s.Health.CheckedAt.Add(time.Minute)
				s.Probe.ProbedAt = s.Probe.ProbedAt.Add(time.Minute)
				return s
			},
		},
		{
			name: "only the health-check latency moved",
			mutate: func(s runtime.Snapshot) runtime.Snapshot {
				s.Health.Latency = 47 * time.Millisecond
				return s
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := snapshot("ollama-local", runtime.StateHealthy, "0.5.1")
			f := newClientFixture(t, isolateStatus)
			f.mgr.SetSnapshots(base)
			f.start()
			sess := f.connect()

			// Every health check moves these fields; reporting on them would
			// turn the change-triggered report into a busy loop.
			f.mgr.SetSnapshots(tt.mutate(base))
			f.advance(2*time.Second, 1)
			expectNoStatus(f, sess, 7)
		})
	}
}

func TestControlReportsSetChangesInFull(t *testing.T) {
	tests := []struct {
		name  string
		after []runtime.Snapshot
		want  int
	}{
		{
			name:  "instance added",
			after: []runtime.Snapshot{snapshot("ollama-local", runtime.StateHealthy, "0.5.1"), snapshot("vllm-a", runtime.StateHealthy, "0.6.0")},
			want:  2,
		},
		{
			name:  "instance removed",
			after: nil,
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newClientFixture(t, isolateStatus)
			f.mgr.SetSnapshots(snapshot("ollama-local", runtime.StateHealthy, "0.5.1"))
			f.start()
			sess := f.connect()

			f.mgr.SetSnapshots(tt.after...)
			f.advance(2*time.Second, 1)

			st := f.recv(sess).GetStatus()
			if st == nil {
				t.Fatal("a change to the instance set was not reported")
			}
			// A removal cannot be expressed by sending a subset, so any
			// change to the set is reported in full.
			if !st.GetFull() {
				t.Error("a change to the instance set must be reported in full")
			}
			if got := len(st.GetSnapshots()); got != tt.want {
				t.Errorf("snapshots = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestControlReconcilesPeriodicallyInFull(t *testing.T) {
	f := newClientFixture(t, isolateStatus)
	f.mgr.SetSnapshots(snapshot("ollama-local", runtime.StateHealthy, "0.5.1"))
	f.start()
	sess := f.connect()

	// 60s with nothing happening: the poll ticks stay silent and the
	// reconciliation carries everything.
	f.advance(60*time.Second, 2) // the status poll and the full report
	st := f.recv(sess).GetStatus()
	if st == nil {
		t.Fatal("no periodic reconciliation after 60s")
	}
	if !st.GetFull() {
		t.Error("the periodic reconciliation must be marked full")
	}
	if got := len(st.GetSnapshots()); got != 1 {
		t.Errorf("snapshots = %d, want 1", got)
	}
}

func TestControlStatusCarriesNoCredentials(t *testing.T) {
	const apiKey = "sk-live-super-secret"
	const headerValue = "Bearer super-secret-header"

	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		isolateStatus(cfg)
		cfg.AllowedRuntimes = []string{"ollama-local"}
		cfg.Secrets = secretResolverFunc(func(context.Context, string) (string, error) { return apiKey, nil })
	})
	f.start()
	sess := f.connect()

	// Install a runtime that does hold a credential, then report on it.
	f.send(sess, configFrame(tunnelv1.ConfigAction_CONFIG_ACTION_ADD, &tunnelv1.RuntimeSpec{
		Id:        "ollama-local",
		Kind:      "ollama",
		BaseUrl:   "http://127.0.0.1:11434",
		ApiKeyRef: "secrets/ollama",
		Headers:   map[string]string{"Authorization": headerValue},
	}))

	adds := waitFor(t, func() []runtime.Config { return f.mgr.Adds() })
	if got := adds[0].APIKey; got != apiKey {
		t.Fatalf("resolved api key = %q, want the resolver's value", got)
	}
	f.mgr.SetSnapshots(snapshot("ollama-local", runtime.StateHealthy, "0.5.1"))

	// Everything the tunnel sends from here on must be free of both.
	for range 3 {
		frame := f.recv(sess)
		raw, err := proto.Marshal(frame)
		if err != nil {
			t.Fatalf("marshal frame: %v", err)
		}
		if strings.Contains(string(raw), apiKey) {
			t.Fatal("an API key crossed the tunnel")
		}
		if strings.Contains(string(raw), headerValue) {
			t.Fatal("a custom authorization header value crossed the tunnel")
		}
		if frame.GetStatus() != nil {
			return
		}
	}
	t.Fatal("no status report followed the configuration change")
}

// -----------------------------------------------------------------------
// Configuration from the control plane
// -----------------------------------------------------------------------

func configFrame(action tunnelv1.ConfigAction, spec *tunnelv1.RuntimeSpec) *tunnelv1.GatewayControl {
	cfg := &tunnelv1.RuntimeConfig{Action: action, Spec: spec}
	if spec != nil {
		cfg.RuntimeId = spec.GetId()
	}
	return &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Config{Config: cfg}}
}

// secretResolverFunc adapts a function to tunnel.SecretResolver.
type secretResolverFunc func(context.Context, string) (string, error)

func (f secretResolverFunc) Resolve(ctx context.Context, ref string) (string, error) {
	return f(ctx, ref)
}

// waitFor polls until get returns a non-empty slice, so a test does not have
// to guess how long an asynchronous apply takes.
func waitFor[T any](t *testing.T, get func() []T) []T {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		if got := get(); len(got) > 0 {
			return got
		}
		select {
		case <-deadline:
			t.Fatal("the expected manager call never happened")
			return nil
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestControlAppliesRuntimeConfiguration(t *testing.T) {
	spec := &tunnelv1.RuntimeSpec{
		Id:      "ollama-local",
		Kind:    "ollama",
		BaseUrl: "http://127.0.0.1:11434",
	}

	tests := []struct {
		name   string
		frame  *tunnelv1.GatewayControl
		assert func(t *testing.T, m *tunneltest.Manager)
	}{
		{
			name:  "add",
			frame: configFrame(tunnelv1.ConfigAction_CONFIG_ACTION_ADD, spec),
			assert: func(t *testing.T, m *tunneltest.Manager) {
				adds := waitFor(t, m.Adds)
				if got, want := adds[0].ID, "ollama-local"; got != want {
					t.Errorf("added runtime id = %q, want %q", got, want)
				}
				if got, want := adds[0].BaseURL, "http://127.0.0.1:11434"; got != want {
					t.Errorf("added base url = %q, want %q", got, want)
				}
			},
		},
		{
			name:  "replace",
			frame: configFrame(tunnelv1.ConfigAction_CONFIG_ACTION_REPLACE, spec),
			assert: func(t *testing.T, m *tunneltest.Manager) {
				if got, want := waitFor(t, m.Replaces)[0].ID, "ollama-local"; got != want {
					t.Errorf("replaced runtime id = %q, want %q", got, want)
				}
			},
		},
		{
			name: "remove",
			frame: &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Config{Config: &tunnelv1.RuntimeConfig{
				Action:    tunnelv1.ConfigAction_CONFIG_ACTION_REMOVE,
				RuntimeId: "ollama-local",
			}}},
			assert: func(t *testing.T, m *tunneltest.Manager) {
				if got, want := waitFor(t, m.Removes)[0], "ollama-local"; got != want {
					t.Errorf("removed runtime id = %q, want %q", got, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
				isolateStatus(cfg)
				cfg.AllowedRuntimes = []string{"ollama-local"}
			})
			f.start()
			sess := f.connect()

			f.send(sess, tt.frame)
			tt.assert(t, f.mgr)

			// There is no dedicated ack frame: the report that follows is how
			// the replica observes the outcome.
			if st := f.recv(sess).GetStatus(); st == nil || !st.GetFull() {
				t.Error("a configuration change must be followed by a full status report")
			}
		})
	}
}

func TestControlRefusesUnsafeConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(cfg *tunnel.ClientConfig)
		frame   *tunnelv1.GatewayControl
		wantLog string
	}{
		{
			name:   "runtime id outside the local allowlist",
			mutate: func(cfg *tunnel.ClientConfig) { cfg.AllowedRuntimes = []string{"ollama-local"} },
			frame: configFrame(tunnelv1.ConfigAction_CONFIG_ACTION_ADD, &tunnelv1.RuntimeSpec{
				Id:      "internal-scanner",
				Kind:    "ollama",
				BaseUrl: "http://10.0.0.1:8080",
			}),
		},
		{
			name:   "credential named with no resolver configured",
			mutate: func(cfg *tunnel.ClientConfig) { cfg.AllowedRuntimes = []string{"ollama-local"} },
			frame: configFrame(tunnelv1.ConfigAction_CONFIG_ACTION_ADD, &tunnelv1.RuntimeSpec{
				Id:        "ollama-local",
				Kind:      "ollama",
				BaseUrl:   "http://127.0.0.1:11434",
				ApiKeyRef: "secrets/ollama",
			}),
		},
		{
			name: "unresolvable credential",
			mutate: func(cfg *tunnel.ClientConfig) {
				cfg.AllowedRuntimes = []string{"ollama-local"}
				cfg.Secrets = secretResolverFunc(func(context.Context, string) (string, error) {
					return "", errors.New("no such secret")
				})
			},
			frame: configFrame(tunnelv1.ConfigAction_CONFIG_ACTION_ADD, &tunnelv1.RuntimeSpec{
				Id:        "ollama-local",
				Kind:      "ollama",
				BaseUrl:   "http://127.0.0.1:11434",
				ApiKeyRef: "secrets/missing",
			}),
		},
		{
			name:   "no action",
			mutate: func(cfg *tunnel.ClientConfig) {},
			frame: configFrame(tunnelv1.ConfigAction_CONFIG_ACTION_UNSPECIFIED, &tunnelv1.RuntimeSpec{
				Id:      "ollama-local",
				Kind:    "ollama",
				BaseUrl: "http://127.0.0.1:11434",
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
				isolateStatus(cfg)
				tt.mutate(cfg)
			})
			f.start()
			sess := f.connect()

			f.send(sess, tt.frame)

			// The stream survives, a status report still follows, and the
			// Manager was never touched: the local allowlist is the last line
			// of defence against a compromised control plane.
			if st := f.recv(sess).GetStatus(); st == nil {
				t.Fatal("a refused configuration must still be followed by a status report")
			}
			if got := len(f.mgr.Adds()); got != 0 {
				t.Errorf("manager adds = %d, want 0", got)
			}
			if got := f.client.State(); got != tunnel.StateConnected {
				t.Errorf("state = %s, want %s: a bad configuration must not drop a healthy tunnel", got, tunnel.StateConnected)
			}
		})
	}
}

func TestControlSurvivesAFailedConfiguration(t *testing.T) {
	f := newClientFixture(t, func(cfg *tunnel.ClientConfig) {
		isolateStatus(cfg)
		cfg.AllowedRuntimes = []string{"ollama-local"}
	})
	f.mgr.SetErrors(errors.New("probe failed"), nil, nil)
	f.start()
	sess := f.connect()

	f.send(sess, configFrame(tunnelv1.ConfigAction_CONFIG_ACTION_ADD, &tunnelv1.RuntimeSpec{
		Id:      "ollama-local",
		Kind:    "ollama",
		BaseUrl: "http://127.0.0.1:11434",
	}))
	waitFor(t, f.mgr.Adds)

	// The failure shows up as the instance's absence from the report, which
	// is exactly why the protocol has no separate ack frame.
	st := f.recv(sess).GetStatus()
	if st == nil {
		t.Fatal("a failed configuration was not followed by a status report")
	}
	if got := len(st.GetSnapshots()); got != 0 {
		t.Errorf("snapshots = %d, want 0: the runtime was never installed", got)
	}
	if got := f.client.State(); got != tunnel.StateConnected {
		t.Errorf("state = %s, want %s", got, tunnel.StateConnected)
	}
}

// -----------------------------------------------------------------------
// Roster and slot hints
// -----------------------------------------------------------------------

func TestControlForwardsRosterAndSlotHint(t *testing.T) {
	f := newClientFixture(t, isolateStatus)
	f.start()
	sess := f.connect()

	// The Client does not interpret either frame: the roster belongs to the
	// connection table (阶段 6) and the hint to the slot pool (阶段 4).
	f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_Roster{Roster: &tunnelv1.GatewayRoster{
		Version: 7,
		Replicas: []*tunnelv1.GatewayReplica{
			{ReplicaId: "gw-1", Endpoint: "gw-1.example.com:8443", State: tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE},
			{ReplicaId: "gw-2", Endpoint: "gw-2.example.com:8443", State: tunnelv1.ReplicaState_REPLICA_STATE_DRAINING},
		},
	}}})
	f.send(sess, &tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_SlotHint{SlotHint: &tunnelv1.SlotHint{
		MinSlots: 2, MaxSlots: 8, BulkSlots: 1,
	}}})

	select {
	case roster := <-f.rosters:
		if got, want := roster.GetVersion(), int64(7); got != want {
			t.Errorf("roster version = %d, want %d", got, want)
		}
		if got := len(roster.GetReplicas()); got != 2 {
			t.Errorf("roster replicas = %d, want 2", got)
		}
	case <-time.After(testTimeout):
		t.Fatal("the roster was not forwarded")
	}

	select {
	case hint := <-f.hints:
		if got, want := hint.GetMaxSlots(), int32(8); got != want {
			t.Errorf("slot hint max_slots = %d, want %d", got, want)
		}
	case <-time.After(testTimeout):
		t.Fatal("the slot hint was not forwarded")
	}

	if got := f.client.State(); got != tunnel.StateConnected {
		t.Errorf("state = %s, want %s", got, tunnel.StateConnected)
	}
}
