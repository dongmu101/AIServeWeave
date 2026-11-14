package tunnelserver_test

import (
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

func f64(v float64) *float64 { return &v }

func schedPressure(t *testing.T, srv *tunnelserver.Server, nodeID string) bool {
	t.Helper()
	for _, n := range srv.AppendSchedNodes(nil) {
		if n.NodeID == nodeID {
			return n.Pressure
		}
	}
	t.Fatalf("node %s missing from the scheduler view", nodeID)
	return false
}

func TestControlHeartbeatLoadIsStoredAndCleared(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{HeartbeatTimeout: 45 * time.Second})
	c := h.connect("mac-mini-01")

	heartbeat := func(load *tunnelv1.NodeLoad) {
		t.Helper()
		c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Heartbeat{Heartbeat: &tunnelv1.Heartbeat{
			SentUnixMs: 1, Load: load,
		}}})
		if c.expect(t).GetHbAck() == nil {
			t.Fatal("heartbeat not acknowledged")
		}
	}

	heartbeat(&tunnelv1.NodeLoad{
		CpuUtilization: f64(0.9),
		Pressure:       true,
		PressureReasons: []tunnelv1.LoadPressureReason{
			tunnelv1.LoadPressureReason_LOAD_PRESSURE_REASON_CPU,
		},
	})
	info, _ := h.srv.Node("mac-mini-01")
	if info.Load == nil || !info.Load.Pressure || info.Load.CPUUtilization == nil || *info.Load.CPUUtilization != 0.9 {
		t.Fatalf("load not stored: %+v", info.Load)
	}
	if !schedPressure(t, h.srv, "mac-mini-01") {
		t.Fatal("scheduler view must carry the reported pressure")
	}

	// A heartbeat without load (an older Agent) clears it back to "not
	// reported", which is never treated as pressure.
	heartbeat(nil)
	info, _ = h.srv.Node("mac-mini-01")
	if info.Load != nil {
		t.Fatalf("a heartbeat without load must clear it, got %+v", info.Load)
	}
	if schedPressure(t, h.srv, "mac-mini-01") {
		t.Fatal("an unreported load must not count as pressure")
	}
}

func TestStalePressureIsIgnoredOnceTheNodeIsNotLive(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{HeartbeatTimeout: 45 * time.Second})
	c := h.connect("mac-mini-01")
	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_Heartbeat{Heartbeat: &tunnelv1.Heartbeat{
		SentUnixMs: 1, Load: &tunnelv1.NodeLoad{Pressure: true},
	}}})
	c.expect(t)
	h.clock.Advance(46 * time.Second)
	if schedPressure(t, h.srv, "mac-mini-01") {
		t.Fatal("pressure from a node that is no longer live must not be reported")
	}
}
