package tunnelserver_test

import (
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// TestSetRosterMarksAnAlreadyConnectedNodeUnderMaintenanceWithoutDisconnecting
// exercises the core distinction STATUS.md's P01 draws against S03's
// DisableNode: a node_id appearing in a roster's maintenance_node_ids must be
// excluded from new dispatch, but its Control stream must be left alone.
func TestSetRosterMarksAnAlreadyConnectedNodeUnderMaintenanceWithoutDisconnecting(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("node-1")

	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 1, MaintenanceNodeIds: []string{"node-1"}})

	select {
	case err := <-c.errc:
		t.Fatalf("Control handler exited unexpectedly with error = %v, want it to stay connected", err)
	default:
	}
	info, ok := h.srv.Node("node-1")
	if !ok {
		t.Fatal("Node(\"node-1\") = not found, want it still connected")
	}
	if !info.Live {
		t.Error("Node(\"node-1\").Live = false, want true: maintenance must not affect liveness")
	}
	if !info.Maintenance {
		t.Error("Node(\"node-1\").Maintenance = false, want true")
	}
}

// TestSetRosterClearsMaintenanceWhenANodeIDLeavesTheSet is the control on the
// previous test: a roster update that drops a node_id from
// maintenance_node_ids must clear the flag on that node.
func TestSetRosterClearsMaintenanceWhenANodeIDLeavesTheSet(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("node-1")
	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 1, MaintenanceNodeIds: []string{"node-1"}})

	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 2})

	info, ok := h.srv.Node("node-1")
	if !ok {
		t.Fatal("Node(\"node-1\") = not found, want it still connected")
	}
	if info.Maintenance {
		t.Error("Node(\"node-1\").Maintenance = true after leaving the roster's set, want false")
	}
}

// TestSetRosterLeavesANonMaintenanceNodeUnaffected is the control confirming
// maintenance status is per-node_id, not fleet-wide.
func TestSetRosterLeavesANonMaintenanceNodeUnaffected(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("node-1")

	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 1, MaintenanceNodeIds: []string{"node-2"}})

	info, ok := h.srv.Node("node-1")
	if !ok {
		t.Fatal("Node(\"node-1\") = not found, want it still connected")
	}
	if info.Maintenance {
		t.Error("Node(\"node-1\").Maintenance = true, want false: it was not named in maintenance_node_ids")
	}
}

// TestControlHandshakePicksUpMaintenanceFromTheCurrentRoster covers a fresh
// Control handshake arriving after a node_id was already put under
// maintenance: the new node entry must start out marked, not wait for the
// next roster broadcast.
func TestControlHandshakePicksUpMaintenanceFromTheCurrentRoster(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.srv.SetRoster(&tunnelv1.GatewayRoster{Version: 1, MaintenanceNodeIds: []string{"node-1"}})

	h.connect("node-1")

	info, ok := h.srv.Node("node-1")
	if !ok {
		t.Fatal("Node(\"node-1\") = not found")
	}
	if !info.Maintenance {
		t.Error("Node(\"node-1\").Maintenance = false on a handshake arriving after the roster already named it, want true")
	}
}
