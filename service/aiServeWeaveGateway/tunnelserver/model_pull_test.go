package tunnelserver_test

import (
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/modelpullstatus"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// STATUS.md's P2 model distribution subtask 2: the Control-stream frames
// that carry a Gateway-triggered pull and the Agent's status of it.

func TestModelPullReportMergesIntoNode(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	want := []modelpullstatus.Status{
		{Name: "b", State: modelpullstatus.StateDone, BytesDownloaded: 100, BytesTotal: 100, UpdatedAt: time.UnixMilli(2000).UTC()},
		{Name: "a", State: modelpullstatus.StateDownloading, BytesDownloaded: 40, BytesTotal: 100, UpdatedAt: time.UnixMilli(1000).UTC()},
	}
	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_ModelPull{
		ModelPull: tunnelwire.ModelPullReportToProto(want),
	}})

	waitFor(t, "model pull report to be applied", func() bool {
		got, ok := h.srv.ModelPullStatus("mac-mini-01")
		return ok && len(got) == 2
	})

	got, ok := h.srv.ModelPullStatus("mac-mini-01")
	if !ok {
		t.Fatal("ModelPullStatus reports the node as unknown after it connected")
	}
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("ModelPullStatus = %+v, want [a, b] sorted by name", got)
	}
	if got[0].BytesDownloaded != 40 || got[1].State != modelpullstatus.StateDone {
		t.Fatalf("ModelPullStatus = %+v, did not preserve the reported fields", got)
	}
}

func TestModelPullReportReplacesWholesale(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_ModelPull{
		ModelPull: tunnelwire.ModelPullReportToProto([]modelpullstatus.Status{
			{Name: "a", State: modelpullstatus.StateDownloading},
			{Name: "b", State: modelpullstatus.StateDone},
		}),
	}})
	waitFor(t, "first report", func() bool {
		got, _ := h.srv.ModelPullStatus("mac-mini-01")
		return len(got) == 2
	})

	// A second report that only mentions "a" must drop "b" — the Agent
	// always reports its full known set, so a name missing from the latest
	// report is gone, not merely unmentioned this time.
	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_ModelPull{
		ModelPull: tunnelwire.ModelPullReportToProto([]modelpullstatus.Status{
			{Name: "a", State: modelpullstatus.StateDone},
		}),
	}})
	waitFor(t, "second report to replace the first", func() bool {
		got, _ := h.srv.ModelPullStatus("mac-mini-01")
		return len(got) == 1
	})

	got, _ := h.srv.ModelPullStatus("mac-mini-01")
	if len(got) != 1 || got[0].Name != "a" || got[0].State != modelpullstatus.StateDone {
		t.Fatalf("ModelPullStatus = %+v, want only [a:done]", got)
	}
}

func TestTriggerModelPullSendsGatewayControlFrame(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	if err := h.srv.TriggerModelPull("mac-mini-01", []string{"m1", "m2"}); err != nil {
		t.Fatalf("TriggerModelPull: %v", err)
	}

	frame := c.expect(t)
	trigger := frame.GetModelPullTrigger()
	if trigger == nil {
		t.Fatalf("frame = %T, want ModelPullTrigger", frame.GetBody())
	}
	if got := trigger.GetNames(); len(got) != 2 || got[0] != "m1" || got[1] != "m2" {
		t.Fatalf("names = %v, want [m1 m2]", got)
	}
}

func TestTriggerModelPullUnknownNode(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	if err := h.srv.TriggerModelPull("ghost", []string{"m1"}); err == nil {
		t.Fatal("TriggerModelPull on an unknown node did not error")
	}
}

func TestModelPullStatusUnknownNode(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	got, ok := h.srv.ModelPullStatus("ghost")
	if ok || got != nil {
		t.Fatalf("ModelPullStatus(%q) = (%v, %v), want (nil, false)", "ghost", got, ok)
	}
}

func TestModelPullStatusConnectedWithNoReportsYet(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")

	got, ok := h.srv.ModelPullStatus("mac-mini-01")
	if !ok {
		t.Fatal("ModelPullStatus reports a connected node as unknown")
	}
	if len(got) != 0 {
		t.Fatalf("ModelPullStatus = %v, want empty before any report arrives", got)
	}
}
