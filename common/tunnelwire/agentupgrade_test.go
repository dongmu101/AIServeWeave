package tunnelwire

import (
	"testing"
	"time"

	"AIServeWeave/common/agentupgradestatus"
)

func TestAgentUpgradeActionRoundTrip(t *testing.T) {
	cases := []struct {
		name          string
		action        agentupgradestatus.Action
		targetVersion string
	}{
		{"unspecified", agentupgradestatus.ActionUnspecified, ""},
		{"check with no target", agentupgradestatus.ActionCheck, ""},
		{"upgrade with target", agentupgradestatus.ActionUpgrade, "v1.2.3"},
		{"rollback with target", agentupgradestatus.ActionRollback, "v1.2.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAction, gotTarget := AgentUpgradeActionFromProto(AgentUpgradeActionToProto(tc.action, tc.targetVersion))
			if gotAction != tc.action || gotTarget != tc.targetVersion {
				t.Fatalf("round trip: want (%v, %q), got (%v, %q)", tc.action, tc.targetVersion, gotAction, gotTarget)
			}
		})
	}
}

func TestAgentUpgradeActionFromProtoNilIsUnspecified(t *testing.T) {
	gotAction, gotTarget := AgentUpgradeActionFromProto(nil)
	if gotAction != agentupgradestatus.ActionUnspecified || gotTarget != "" {
		t.Fatalf("nil proto: want (ActionUnspecified, \"\"), got (%v, %q)", gotAction, gotTarget)
	}
}

func TestAgentUpgradeReportRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		st   agentupgradestatus.Status
	}{
		{"idle", agentupgradestatus.Status{
			CurrentVersion: "v1.2.2",
			State:          agentupgradestatus.StateIdle,
			UpdatedAt:      time.UnixMilli(1_700_000_000_000).UTC(),
		}},
		{"checking", agentupgradestatus.Status{
			CurrentVersion: "v1.2.2",
			State:          agentupgradestatus.StateChecking,
			UpdatedAt:      time.UnixMilli(1_700_000_001_000).UTC(),
		}},
		{"failed with reason", agentupgradestatus.Status{
			CurrentVersion: "v1.2.2",
			State:          agentupgradestatus.StateFailed,
			Reason:         agentupgradestatus.ReasonUnknownVersion,
			UpdatedAt:      time.UnixMilli(1_700_000_002_000).UTC(),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentUpgradeReportFromProto(AgentUpgradeReportToProto(tc.st))
			if got.CurrentVersion != tc.st.CurrentVersion || got.State != tc.st.State || got.Reason != tc.st.Reason || !got.UpdatedAt.Equal(tc.st.UpdatedAt) {
				t.Fatalf("round trip: want %+v, got %+v", tc.st, got)
			}
		})
	}
}

func TestAgentUpgradeReportFromProtoNil(t *testing.T) {
	got := AgentUpgradeReportFromProto(nil)
	if got.CurrentVersion != "" || got.State != agentupgradestatus.StateIdle || got.Reason != agentupgradestatus.ReasonUnspecified {
		t.Fatalf("nil proto: want zero Status, got %+v", got)
	}
}

func TestAgentUpgradeStateRoundTrip(t *testing.T) {
	states := []agentupgradestatus.State{
		agentupgradestatus.StateIdle,
		agentupgradestatus.StateChecking,
		agentupgradestatus.StateDownloading,
		agentupgradestatus.StateVerifying,
		agentupgradestatus.StateDraining,
		agentupgradestatus.StateRestarting,
		agentupgradestatus.StateFailed,
	}
	for _, s := range states {
		t.Run(s.String(), func(t *testing.T) {
			got := agentUpgradeStateFromProto(agentUpgradeStateToProto(s))
			if got != s {
				t.Fatalf("round trip: want %v, got %v", s, got)
			}
		})
	}
}

func TestAgentUpgradeReasonRoundTrip(t *testing.T) {
	reasons := []agentupgradestatus.FailureReason{
		agentupgradestatus.ReasonUnspecified,
		agentupgradestatus.ReasonUnknownVersion,
		agentupgradestatus.ReasonNotImplemented,
	}
	for _, r := range reasons {
		t.Run(r.String(), func(t *testing.T) {
			got := agentUpgradeReasonFromProto(agentUpgradeReasonToProto(r))
			if got != r {
				t.Fatalf("round trip: want %v, got %v", r, got)
			}
		})
	}
}
