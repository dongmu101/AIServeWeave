package tunnelserver_test

import (
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/comfyuimanagedstatus"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// STATUS.md's P2 ComfyUI Managed Docker deployment subtask 2: the
// Control-stream frames that carry a Gateway-triggered lifecycle action and
// the Agent's report of the one Managed instance's container state.

func TestComfyUIManagedReportMergesIntoNode(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	want := []comfyuimanagedstatus.Status{
		{ContainerName: "aiserveweave-comfyui", State: comfyuimanagedstatus.StateRunning, UpdatedAt: time.UnixMilli(2000).UTC()},
	}
	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_ComfyuiManaged{
		ComfyuiManaged: tunnelwire.ComfyUIManagedReportToProto(want),
	}})

	waitFor(t, "comfyui managed report to be applied", func() bool {
		got, ok := h.srv.ComfyUIManagedStatus("mac-mini-01")
		return ok && len(got) == 1
	})

	got, ok := h.srv.ComfyUIManagedStatus("mac-mini-01")
	if !ok {
		t.Fatal("ComfyUIManagedStatus reports the node as unknown after it connected")
	}
	if len(got) != 1 || got[0].ContainerName != "aiserveweave-comfyui" || got[0].State != comfyuimanagedstatus.StateRunning {
		t.Fatalf("ComfyUIManagedStatus = %+v, did not preserve the reported fields", got)
	}
}

func TestComfyUIManagedReportReplacesWholesale(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_ComfyuiManaged{
		ComfyuiManaged: tunnelwire.ComfyUIManagedReportToProto([]comfyuimanagedstatus.Status{
			{ContainerName: "aiserveweave-comfyui", State: comfyuimanagedstatus.StateStarting},
		}),
	}})
	waitFor(t, "first report", func() bool {
		got, _ := h.srv.ComfyUIManagedStatus("mac-mini-01")
		return len(got) == 1 && got[0].State == comfyuimanagedstatus.StateStarting
	})

	// A second report replaces the first wholesale, matching modelPulls'
	// existing precedent — an Agent's report is always its complete current
	// view, not an incremental delta.
	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_ComfyuiManaged{
		ComfyuiManaged: tunnelwire.ComfyUIManagedReportToProto([]comfyuimanagedstatus.Status{
			{ContainerName: "aiserveweave-comfyui", State: comfyuimanagedstatus.StateRunning},
		}),
	}})
	waitFor(t, "second report to replace the first", func() bool {
		got, _ := h.srv.ComfyUIManagedStatus("mac-mini-01")
		return len(got) == 1 && got[0].State == comfyuimanagedstatus.StateRunning
	})
}

func TestTriggerComfyUIManagedActionSendsGatewayControlFrame(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	if err := h.srv.TriggerComfyUIManagedAction("mac-mini-01", comfyuimanagedstatus.ActionRestart); err != nil {
		t.Fatalf("TriggerComfyUIManagedAction: %v", err)
	}

	frame := c.expect(t)
	action := frame.GetComfyuiManagedAction()
	if action == nil {
		t.Fatalf("frame = %T, want ComfyUIManagedAction", frame.GetBody())
	}
	if got := action.GetAction(); got != tunnelv1.ComfyUIManagedActionType_COMFYUI_MANAGED_ACTION_RESTART {
		t.Fatalf("action = %v, want RESTART", got)
	}
}

func TestTriggerComfyUIManagedActionUnknownNode(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	if err := h.srv.TriggerComfyUIManagedAction("ghost", comfyuimanagedstatus.ActionStart); err == nil {
		t.Fatal("TriggerComfyUIManagedAction on an unknown node did not error")
	}
}

func TestTriggerComfyUIManagedCustomNodeInstallSendsGatewayControlFrame(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	if err := h.srv.TriggerComfyUIManagedCustomNodeInstall("mac-mini-01", "my-node"); err != nil {
		t.Fatalf("TriggerComfyUIManagedCustomNodeInstall: %v", err)
	}

	frame := c.expect(t)
	trigger := frame.GetComfyuiManagedCustomNodeInstall()
	if trigger == nil {
		t.Fatalf("frame = %T, want ComfyUIManagedCustomNodeInstallTrigger", frame.GetBody())
	}
	if got := trigger.GetName(); got != "my-node" {
		t.Fatalf("name = %q, want %q", got, "my-node")
	}
}

func TestTriggerComfyUIManagedCustomNodeInstallUnknownNode(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	if err := h.srv.TriggerComfyUIManagedCustomNodeInstall("ghost", "my-node"); err == nil {
		t.Fatal("TriggerComfyUIManagedCustomNodeInstall on an unknown node did not error")
	}
}

func TestComfyUIManagedReportCarriesCustomNodes(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	want := []comfyuimanagedstatus.Status{
		{
			ContainerName: "aiserveweave-comfyui",
			State:         comfyuimanagedstatus.StateRunning,
			CustomNodes:   []comfyuimanagedstatus.CustomNodeStatus{{Name: "my-node", Version: "v1"}},
		},
	}
	c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_ComfyuiManaged{
		ComfyuiManaged: tunnelwire.ComfyUIManagedReportToProto(want),
	}})

	waitFor(t, "comfyui managed report with custom nodes to be applied", func() bool {
		got, ok := h.srv.ComfyUIManagedStatus("mac-mini-01")
		return ok && len(got) == 1 && len(got[0].CustomNodes) == 1
	})

	got, _ := h.srv.ComfyUIManagedStatus("mac-mini-01")
	if got[0].CustomNodes[0].Name != "my-node" || got[0].CustomNodes[0].Version != "v1" {
		t.Fatalf("CustomNodes = %+v, want [{my-node v1}]", got[0].CustomNodes)
	}
}

func TestComfyUIManagedStatusUnknownNode(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	got, ok := h.srv.ComfyUIManagedStatus("ghost")
	if ok || got != nil {
		t.Fatalf("ComfyUIManagedStatus(%q) = (%v, %v), want (nil, false)", "ghost", got, ok)
	}
}

func TestComfyUIManagedStatusConnectedWithNoReportsYet(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	h.connect("mac-mini-01")

	got, ok := h.srv.ComfyUIManagedStatus("mac-mini-01")
	if !ok {
		t.Fatal("ComfyUIManagedStatus reports a connected node as unknown")
	}
	if len(got) != 0 {
		t.Fatalf("ComfyUIManagedStatus = %v, want empty before any report arrives", got)
	}
}
