package tunnelserver_test

import (
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/cliusagestatus"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// TestCLIUsageReportReplacesWholesale checks the node view carries the Agent's
// latest CLI quota and that a later report replaces the earlier one.
// TestCLIUsageReportReplacesWholesale 检查节点视图带有 Agent 最新的 CLI 额度，且后一次
// 上报会整份替换前一次。
func TestCLIUsageReportReplacesWholesale(t *testing.T) {
	h := newHarness(t, tunnelserver.Config{})
	c := h.connect("mac-mini-01")

	send := func(accounts []cliusagestatus.Account) {
		c.send(t, &tunnelv1.AgentControl{Body: &tunnelv1.AgentControl_CliUsage{
			CliUsage: tunnelwire.CLIUsageReportToProto(accounts),
		}})
	}
	checked := time.UnixMilli(1_700_000_000_000).UTC()
	send([]cliusagestatus.Account{
		{Kind: runtime.KindCodex, Status: "ready", CheckedAt: checked},
		{Kind: runtime.KindClaude, Status: "pending"},
	})
	waitFor(t, "first cli usage report", func() bool {
		info, ok := h.srv.Node("mac-mini-01")
		return ok && len(info.CLIUsage) == 2
	})
	info, _ := h.srv.Node("mac-mini-01")
	if info.CLIUsage[0].Kind != runtime.KindClaude || info.CLIUsage[1].Kind != runtime.KindCodex {
		t.Fatalf("CLIUsage = %+v, want claude then codex (sorted by kind)", info.CLIUsage)
	}

	send([]cliusagestatus.Account{{Kind: runtime.KindClaude, Status: "ready"}})
	waitFor(t, "second cli usage report", func() bool {
		info, _ := h.srv.Node("mac-mini-01")
		return len(info.CLIUsage) == 1 && info.CLIUsage[0].Status == "ready"
	})
}
