package tunnelserver_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

func TestControlRejectsHandshakesThatDoNotAuthenticate(t *testing.T) {
	tests := []struct {
		name string
		// certNodeID is the identity in the client certificate; hello is the
		// first frame the Agent sends.
		certNodeID string
		hello      *tunnelv1.Hello
		notHello   *tunnelv1.AgentControl
		wantCode   codes.Code
	}{
		{
			name:       "node_id disagrees with the certificate",
			certNodeID: "mac-mini-01",
			hello:      &tunnelv1.Hello{NodeId: "gpu-box-07"},
			wantCode:   codes.PermissionDenied,
		},
		{
			name:       "Hello carries no node_id",
			certNodeID: "mac-mini-01",
			hello:      &tunnelv1.Hello{},
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "first frame is not Hello",
			certNodeID: "mac-mini-01",
			notHello: &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Heartbeat{
				Heartbeat: &tunnelv1.Heartbeat{},
			}},
			wantCode: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tunnelserver.Config{})
			c := h.startControl(tt.certNodeID, tt.hello)
			if tt.notHello != nil {
				c.send(t, tt.notHello)
			}

			err := c.wait(t)
			if got := status.Code(err); got != tt.wantCode {
				t.Fatalf("Control error code = %v (%v), want %v", got, err, tt.wantCode)
			}
			if nodes := h.srv.Nodes(); len(nodes) != 0 {
				t.Errorf("Nodes() = %v, want none: a rejected handshake must not register a node", nodes)
			}
		})
	}
}

func TestControlRequiresAVerifiedClientCertificate(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	ca := gatewaytest.NewCA(t)

	tests := []struct {
		name string
		ctx  func(context.Context) context.Context
	}{
		{
			name: "no peer information at all",
			ctx:  func(ctx context.Context) context.Context { return ctx },
		},
		{
			name: "certificate presented but not verified",
			ctx: func(ctx context.Context) context.Context {
				return gatewaytest.UnverifiedPeerContext(ctx, ca.NodeCertificate(t, "mac-mini-01"))
			},
		},
		{
			name: "verified certificate that names no node",
			ctx: func(ctx context.Context) context.Context {
				return gatewaytest.VerifiedPeerContext(ctx, ca.AnonymousCertificate(t))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := gatewaytest.NewControlStreamWithContext(tt.ctx(context.Background()))
			defer stream.Break(nil)
			err := h.srv.Control(stream)
			if got := status.Code(err); got != codes.Unauthenticated {
				t.Fatalf("Control error code = %v (%v), want Unauthenticated", got, err)
			}
		})
	}
}

func TestControlAcceptsADNSNamedNode(t *testing.T) {
	// A CA that cannot issue URI SANs names the node with a single DNS SAN.
	// Accepting it is what keeps such a CA usable; accepting a certificate
	// with several DNS SANs would not, because it would assert more than one
	// identity.
	h := newHarness(t, tunnelserver.Config{})
	ca := gatewaytest.NewCA(t)
	ctx := gatewaytest.VerifiedPeerContext(context.Background(), ca.NodeCertificateWithDNS(t, "mac-mini-01"))
	stream := gatewaytest.NewControlStreamWithContext(ctx)
	defer stream.Break(nil)

	done := make(chan error, 1)
	go func() { done <- h.srv.Control(stream) }()

	if err := stream.FromAgent(&tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Hello{
		Hello: &tunnelv1.Hello{NodeId: "mac-mini-01"},
	}}); err != nil {
		t.Fatalf("sending Hello: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	frame, err := stream.ToAgent(waitCtx)
	if err != nil {
		t.Fatalf("waiting for HelloAck: %v", err)
	}
	if frame.GetAck() == nil {
		t.Fatalf("first frame = %T, want HelloAck", frame.GetBody())
	}
	stream.Break(nil)
	<-done
}

func TestControlHandshakeDeliversReplicaIdentityHintAndRoster(t *testing.T) {
	hint := &tunnelv1.SlotHint{MinSlots: 2, MaxSlots: 8, BulkSlots: 1}
	roster := &tunnelv1.GatewayRoster{
		Version: 7,
		Replicas: []*tunnelv1.GatewayReplica{
			{ReplicaId: "replica-a", Endpoint: "a.example:443", State: tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE},
		},
	}
	h := newHarness(t, tunnelserver.Config{ReplicaID: "replica-a", SlotHint: hint})
	h.srv.SetRoster(roster)

	c := h.connect("mac-mini-01")
	if got := c.ack.GetReplicaId(); got != "replica-a" {
		t.Errorf("HelloAck.replica_id = %q, want %q", got, "replica-a")
	}
	if got := c.ack.GetServerUnixMs(); got != h.clock.Now().UnixMilli() {
		t.Errorf("HelloAck.server_unix_ms = %d, want %d", got, h.clock.Now().UnixMilli())
	}

	gotHint := c.expect(t).GetSlotHint()
	if gotHint.GetMinSlots() != hint.GetMinSlots() || gotHint.GetMaxSlots() != hint.GetMaxSlots() {
		t.Errorf("SlotHint = %v, want %v", gotHint, hint)
	}
	gotRoster := c.expect(t).GetRoster()
	if gotRoster.GetVersion() != roster.GetVersion() || len(gotRoster.GetReplicas()) != 1 {
		t.Errorf("roster = %v, want version %d with one replica", gotRoster, roster.GetVersion())
	}
}

func TestControlBroadcastsRosterChangesToEveryNode(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	first := h.connect("mac-mini-01")
	second := h.connect("gpu-box-07")

	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 3})

	for name, c := range map[string]*control{"mac-mini-01": first, "gpu-box-07": second} {
		roster := c.expect(t).GetRoster()
		if roster.GetVersion() != 3 {
			t.Errorf("%s received roster version %d, want 3", name, roster.GetVersion())
		}
	}
}

func TestControlAnswersHeartbeatsAndTracksLiveness(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{HeartbeatTimeout: 45 * time.Second})
	c := h.connect("mac-mini-01")

	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Heartbeat{Heartbeat: &tunnelv1.Heartbeat{
		SentUnixMs:       1234,
		InflightRequests: 2,
		IdleSlots:        3,
	}}})

	ack := c.expect(t).GetHbAck()
	if ack == nil {
		t.Fatalf("frame after Heartbeat = %v, want HeartbeatAck", c)
	}
	if ack.GetSentUnixMs() != 1234 {
		t.Errorf("HeartbeatAck.sent_unix_ms = %d, want 1234 echoed back so the Agent can compute RTT", ack.GetSentUnixMs())
	}

	info, ok := h.srv.Node("mac-mini-01")
	if !ok {
		t.Fatal("Node(mac-mini-01) not found after a heartbeat")
	}
	if !info.Live {
		t.Error("node is not Live right after a heartbeat")
	}
	if info.InflightRequests != 2 {
		t.Errorf("InflightRequests = %d, want 2", info.InflightRequests)
	}

	// One missed heartbeat is tolerated; a silence longer than the timeout is
	// not, because the scheduler must stop choosing a node it cannot reach.
	h.clock.Advance(44 * time.Second)
	if info, _ := h.srv.Node("mac-mini-01"); !info.Live {
		t.Error("node stopped being Live before the heartbeat timeout elapsed")
	}
	h.clock.Advance(2 * time.Second)
	if info, _ := h.srv.Node("mac-mini-01"); info.Live {
		t.Error("node is still Live after the heartbeat timeout elapsed")
	}
}

func TestControlStatusReplacesInventoryOnlyOnAFullReport(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	report := func(full bool, ids ...string) {
		t.Helper()
		snaps := make([]runtime.Snapshot, 0, len(ids))
		for _, id := range ids {
			snaps = append(snaps, runtime.Snapshot{
				Descriptor: runtime.Descriptor{ID: id, Kind: runtime.KindOllama, MaxConcurrent: 2},
				State:      runtime.StateHealthy,
			})
		}
		c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Status{Status: &tunnelv1.RuntimeStatus{
			Snapshots:  tunnelwire.SnapshotsToProto(snaps),
			Full:       full,
			ReportedAt: timestamppb.New(h.clock.Now()),
		}}})
	}

	runtimeIDs := func() []string {
		info, _ := h.srv.Node("mac-mini-01")
		ids := make([]string, 0, len(info.Runtimes))
		for _, snap := range info.Runtimes {
			ids = append(ids, snap.Descriptor.ID)
		}
		return ids
	}

	report(true, "ollama-1", "comfy-1")
	waitFor(t, "the first full report to land", func() bool { return len(runtimeIDs()) == 2 })

	// An incremental report is a change notice, not a new world: an instance
	// that simply stopped changing must not disappear from the inventory.
	report(false, "ollama-1")
	time.Sleep(10 * time.Millisecond)
	if got := runtimeIDs(); len(got) != 2 {
		t.Errorf("after an incremental report inventory = %v, want both instances retained", got)
	}

	report(true, "ollama-1")
	waitFor(t, "the second full report to land", func() bool { return len(runtimeIDs()) == 1 })
	if got := runtimeIDs(); len(got) != 1 || got[0] != "ollama-1" {
		t.Errorf("after a full report inventory = %v, want [ollama-1]", got)
	}
}

func TestControlDrainingStopsDispatchAndClosesIdleSlots(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")
	slot := h.openSlot("mac-mini-01", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1", echoHandler)
	waitFor(t, "the slot to park", func() bool { return idleCount(h, "mac-mini-01") == 1 })

	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Draining{Draining: &tunnelv1.Draining{
		Reason: "agent shutting down",
	}}})

	waitFor(t, "the node to report draining", func() bool {
		info, _ := h.srv.Node("mac-mini-01")
		return info.Draining
	})
	if info, _ := h.srv.Node("mac-mini-01"); info.Live {
		t.Error("a draining node is still reported Live; the scheduler would keep sending it work")
	}
	if got := idleCount(h, "mac-mini-01"); got != 0 {
		t.Errorf("idle slots after draining = %d, want 0: a slot that may not be dispatched to is not spare capacity", got)
	}

	_, err := h.srv.Dispatch(context.Background(), tunnelserver.Request{
		NodeID:    "mac-mini-01",
		RuntimeID: "ollama-1",
		Operation: tunnelv1.Operation_OPERATION_LIST_MODELS,
	})
	if !errors.Is(err, tunnelserver.ErrNoIdleSlot) {
		t.Errorf("Dispatch to a draining node = %v, want a backpressure error wrapping ErrNoIdleSlot", err)
	}
	_ = slot
}

func TestControlHalfCloseEndsTheHandlerCleanly(t *testing.T) {
	// An Agent that has finished draining half-closes its Control stream.
	// That is a normal goodbye, not a failure, so the handler must not report
	// an error the operator would then go looking for.
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	c.stream.CloseSend()
	if err := c.wait(t); err != nil {
		t.Fatalf("Control after a clean half-close = %v, want nil", err)
	}
	waitFor(t, "the node entry to be forgotten", func() bool { return len(h.srv.Nodes()) == 0 })
}

// idleCount reports how many slots of any class are parked on a node.
func idleCount(h *harness, nodeID string) int {
	info, ok := h.srv.Node(nodeID)
	if !ok {
		return 0
	}
	total := 0
	for _, n := range info.IdleSlots {
		total += n
	}
	return total
}

// echoHandler answers any request with its own payload, which is enough for
// tests that only care about slot mechanics.
func echoHandler(req *tunnelv1.RequestHeaders, _ [][]byte, reply func(*tunnelv1.AgentFrame) error) error {
	return reply(dataFrame(req.GetPayload()))
}
