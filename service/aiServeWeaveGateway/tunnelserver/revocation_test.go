package tunnelserver_test

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// TestSetRosterKillsAnAlreadyConnectedRevokedNode exercises the "existing
// connection" half of STATUS.md's S03: a node_id appearing in a roster's
// revoked_node_ids must lose its Control stream immediately, not merely be
// refused on its next reconnect attempt.
func TestSetRosterKillsAnAlreadyConnectedRevokedNode(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("node-1")

	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 1, RevokedNodeIds: []string{"node-1"}})

	err := c.wait(t)
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("Control error code after revocation = %v (%v), want PermissionDenied", got, err)
	}
	waitFor(t, "the revoked node to drop out of Nodes()", func() bool {
		return len(h.srv.Nodes()) == 0
	})
}

// TestSetRosterLeavesAnUnrevokedNodeConnected is the control on the previous
// test: a roster update that revokes some other node_id must not disturb a
// node that was not named.
func TestSetRosterLeavesAnUnrevokedNodeConnected(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("node-1")

	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 1, RevokedNodeIds: []string{"node-2"}})

	select {
	case err := <-c.errc:
		t.Fatalf("Control handler exited unexpectedly with error = %v, want it to stay connected", err)
	default:
	}
	if _, ok := h.srv.Node("node-1"); !ok {
		t.Error("Node(\"node-1\") = not found, want it still connected")
	}
}

// TestControlRejectsARevokedNodeIDBeforeCreatingANodeEntry covers the
// "reconnect" half of S03: once a node_id is revoked, even a brand-new
// Control handshake presenting a still cryptographically valid certificate
// for it must be refused, and must not leave a node entry behind.
func TestControlRejectsARevokedNodeIDBeforeCreatingANodeEntry(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 1, RevokedNodeIds: []string{"node-1"}})

	c := h.startControl("node-1", &tunnelv1.Hello{NodeId: "node-1", AgentVersion: "test"})
	err := c.wait(t)
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("Control error code = %v (%v), want PermissionDenied", got, err)
	}
	if nodes := h.srv.Nodes(); len(nodes) != 0 {
		t.Errorf("Nodes() = %v, want none: a rejected handshake must not register a node", nodes)
	}
}

// TestServeRejectsARevokedNodeID covers the data-plane path directly, in
// case a revoked node_id opens a slot stream without ever completing
// Control's handshake.
func TestServeRejectsARevokedNodeID(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 1, RevokedNodeIds: []string{"node-1"}})

	slot := h.openSlot("node-1", tunnelv1.SlotClass_SLOT_CLASS_INFERENCE, "slot-1", nil)
	err := slot.wait(t)
	if got := status.Code(err); got != codes.PermissionDenied {
		t.Fatalf("Serve error code = %v (%v), want PermissionDenied", got, err)
	}
}
