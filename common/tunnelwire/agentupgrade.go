package tunnelwire

import (
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/agentupgradestatus"
)

// AgentUpgradeActionToProto encodes the action a Gateway asks an Agent to
// apply to its own upgrade process (STATUS.md's P2 Agent auto-upgrade
// subtask 1). There is deliberately no field for a download URL or
// signature: see agentupgradestatus's package doc for why.
func AgentUpgradeActionToProto(a agentupgradestatus.Action, targetVersion string) *tunnelv1.AgentUpgradeAction {
	return &tunnelv1.AgentUpgradeAction{
		Action:        agentUpgradeActionTypeToProto(a),
		TargetVersion: targetVersion,
	}
}

// AgentUpgradeActionFromProto restores the requested action and target
// version.
func AgentUpgradeActionFromProto(pb *tunnelv1.AgentUpgradeAction) (a agentupgradestatus.Action, targetVersion string) {
	return agentUpgradeActionTypeFromProto(pb.GetAction()), pb.GetTargetVersion()
}

// AgentUpgradeReportToProto encodes an Agent's current upgrade status.
// Unlike ModelPullReportToProto or ComfyUIManagedReportToProto there is no
// slice to sort: an Agent has exactly one running version.
func AgentUpgradeReportToProto(st agentupgradestatus.Status) *tunnelv1.AgentUpgradeReport {
	return &tunnelv1.AgentUpgradeReport{
		CurrentVersion: st.CurrentVersion,
		State:          agentUpgradeStateToProto(st.State),
		Reason:         agentUpgradeReasonToProto(st.Reason),
		UpdatedUnixMs:  st.UpdatedAt.UnixMilli(),
	}
}

// AgentUpgradeReportFromProto restores an Agent's reported upgrade status.
func AgentUpgradeReportFromProto(pb *tunnelv1.AgentUpgradeReport) agentupgradestatus.Status {
	return agentupgradestatus.Status{
		CurrentVersion: pb.GetCurrentVersion(),
		State:          agentUpgradeStateFromProto(pb.GetState()),
		Reason:         agentUpgradeReasonFromProto(pb.GetReason()),
		UpdatedAt:      time.UnixMilli(pb.GetUpdatedUnixMs()).UTC(),
	}
}

func agentUpgradeActionTypeToProto(a agentupgradestatus.Action) tunnelv1.AgentUpgradeActionType {
	switch a {
	case agentupgradestatus.ActionCheck:
		return tunnelv1.AgentUpgradeActionType_AGENT_UPGRADE_ACTION_CHECK
	case agentupgradestatus.ActionUpgrade:
		return tunnelv1.AgentUpgradeActionType_AGENT_UPGRADE_ACTION_UPGRADE
	case agentupgradestatus.ActionRollback:
		return tunnelv1.AgentUpgradeActionType_AGENT_UPGRADE_ACTION_ROLLBACK
	default:
		return tunnelv1.AgentUpgradeActionType_AGENT_UPGRADE_ACTION_UNSPECIFIED
	}
}

func agentUpgradeActionTypeFromProto(pb tunnelv1.AgentUpgradeActionType) agentupgradestatus.Action {
	switch pb {
	case tunnelv1.AgentUpgradeActionType_AGENT_UPGRADE_ACTION_CHECK:
		return agentupgradestatus.ActionCheck
	case tunnelv1.AgentUpgradeActionType_AGENT_UPGRADE_ACTION_UPGRADE:
		return agentupgradestatus.ActionUpgrade
	case tunnelv1.AgentUpgradeActionType_AGENT_UPGRADE_ACTION_ROLLBACK:
		return agentupgradestatus.ActionRollback
	default:
		return agentupgradestatus.ActionUnspecified
	}
}

func agentUpgradeStateToProto(s agentupgradestatus.State) tunnelv1.AgentUpgradeState {
	switch s {
	case agentupgradestatus.StateChecking:
		return tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_CHECKING
	case agentupgradestatus.StateDownloading:
		return tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_DOWNLOADING
	case agentupgradestatus.StateVerifying:
		return tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_VERIFYING
	case agentupgradestatus.StateDraining:
		return tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_DRAINING
	case agentupgradestatus.StateRestarting:
		return tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_RESTARTING
	case agentupgradestatus.StateFailed:
		return tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_FAILED
	default:
		return tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_UNSPECIFIED
	}
}

func agentUpgradeStateFromProto(pb tunnelv1.AgentUpgradeState) agentupgradestatus.State {
	switch pb {
	case tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_CHECKING:
		return agentupgradestatus.StateChecking
	case tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_DOWNLOADING:
		return agentupgradestatus.StateDownloading
	case tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_VERIFYING:
		return agentupgradestatus.StateVerifying
	case tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_DRAINING:
		return agentupgradestatus.StateDraining
	case tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_RESTARTING:
		return agentupgradestatus.StateRestarting
	case tunnelv1.AgentUpgradeState_AGENT_UPGRADE_STATE_FAILED:
		return agentupgradestatus.StateFailed
	default:
		return agentupgradestatus.StateIdle
	}
}

func agentUpgradeReasonToProto(r agentupgradestatus.FailureReason) tunnelv1.AgentUpgradeFailureReason {
	switch r {
	case agentupgradestatus.ReasonUnknownVersion:
		return tunnelv1.AgentUpgradeFailureReason_AGENT_UPGRADE_FAILURE_REASON_UNKNOWN_VERSION
	case agentupgradestatus.ReasonNotImplemented:
		return tunnelv1.AgentUpgradeFailureReason_AGENT_UPGRADE_FAILURE_REASON_NOT_IMPLEMENTED
	default:
		return tunnelv1.AgentUpgradeFailureReason_AGENT_UPGRADE_FAILURE_REASON_UNSPECIFIED
	}
}

func agentUpgradeReasonFromProto(pb tunnelv1.AgentUpgradeFailureReason) agentupgradestatus.FailureReason {
	switch pb {
	case tunnelv1.AgentUpgradeFailureReason_AGENT_UPGRADE_FAILURE_REASON_UNKNOWN_VERSION:
		return agentupgradestatus.ReasonUnknownVersion
	case tunnelv1.AgentUpgradeFailureReason_AGENT_UPGRADE_FAILURE_REASON_NOT_IMPLEMENTED:
		return agentupgradestatus.ReasonNotImplemented
	default:
		return agentupgradestatus.ReasonUnspecified
	}
}
