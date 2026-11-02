package tunnelwire

import (
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/remoteprovider"
)

// RemoteProvidersToProto wraps a sealed bundle for the Control stream. The
// payload stays opaque here: only the Agent holding the node key can open it.
//
// RemoteProvidersToProto 把已封装的打包装进 Control 流消息。负载在这里保持不透明：只有
// 持有节点密钥的 Agent 能打开它。
func RemoteProvidersToProto(version uint64, sealed []byte) *tunnelv1.RemoteProviders {
	return &tunnelv1.RemoteProviders{Version: version, Sealed: sealed}
}

// RemoteProvidersFromProto unwraps a RemoteProviders message.
//
// RemoteProvidersFromProto 拆开一条 RemoteProviders 消息。
func RemoteProvidersFromProto(pb *tunnelv1.RemoteProviders) (version uint64, sealed []byte) {
	return pb.GetVersion(), pb.GetSealed()
}

// RemoteProvidersReportToProto encodes the Agent's remote provider report.
//
// RemoteProvidersReportToProto 编码 Agent 的远程供应商回报。
func RemoteProvidersReportToProto(r remoteprovider.Report) *tunnelv1.RemoteProvidersReport {
	out := &tunnelv1.RemoteProvidersReport{
		AppliedVersion:   r.AppliedVersion,
		AttemptedVersion: r.AttemptedVersion,
		Outcome:          remoteOutcomeToProto(r.Outcome),
		UpdatedUnixMs:    r.UpdatedAt.UnixMilli(),
	}
	for _, rt := range r.Runtimes {
		out.Runtimes = append(out.Runtimes, &tunnelv1.RemoteRuntimeResult{Id: rt.ID, State: remoteRuntimeStateToProto(rt.State)})
	}
	return out
}

// RemoteProvidersReportFromProto restores an Agent's remote provider report.
// Unknown enum values map to the zero state rather than failing.
//
// RemoteProvidersReportFromProto 还原 Agent 的远程供应商回报。未知枚举值映射为零值状态，
// 而不是报错。
func RemoteProvidersReportFromProto(pb *tunnelv1.RemoteProvidersReport) remoteprovider.Report {
	out := remoteprovider.Report{
		AppliedVersion:   pb.GetAppliedVersion(),
		AttemptedVersion: pb.GetAttemptedVersion(),
		Outcome:          remoteOutcomeFromProto(pb.GetOutcome()),
	}
	if ms := pb.GetUpdatedUnixMs(); ms != 0 {
		out.UpdatedAt = time.UnixMilli(ms).UTC()
	}
	for _, rt := range pb.GetRuntimes() {
		out.Runtimes = append(out.Runtimes, remoteprovider.RuntimeResult{ID: rt.GetId(), State: remoteRuntimeStateFromProto(rt.GetState())})
	}
	return out
}

func remoteOutcomeToProto(o remoteprovider.Outcome) tunnelv1.RemoteProvidersOutcome {
	switch o {
	case remoteprovider.OutcomeApplied:
		return tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_APPLIED
	case remoteprovider.OutcomeDisabled:
		return tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_DISABLED
	case remoteprovider.OutcomeOpenFailed:
		return tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_OPEN_FAILED
	case remoteprovider.OutcomeInvalid:
		return tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_INVALID
	case remoteprovider.OutcomeStale:
		return tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_STALE
	case remoteprovider.OutcomeStoreFailed:
		return tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_STORE_FAILED
	}
	return tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_UNSPECIFIED
}

func remoteOutcomeFromProto(pb tunnelv1.RemoteProvidersOutcome) remoteprovider.Outcome {
	switch pb {
	case tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_APPLIED:
		return remoteprovider.OutcomeApplied
	case tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_DISABLED:
		return remoteprovider.OutcomeDisabled
	case tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_OPEN_FAILED:
		return remoteprovider.OutcomeOpenFailed
	case tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_INVALID:
		return remoteprovider.OutcomeInvalid
	case tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_STALE:
		return remoteprovider.OutcomeStale
	case tunnelv1.RemoteProvidersOutcome_REMOTE_PROVIDERS_OUTCOME_STORE_FAILED:
		return remoteprovider.OutcomeStoreFailed
	}
	return remoteprovider.OutcomeNone
}

func remoteRuntimeStateToProto(s remoteprovider.RuntimeState) tunnelv1.RemoteRuntimeState {
	switch s {
	case remoteprovider.RuntimeActive:
		return tunnelv1.RemoteRuntimeState_REMOTE_RUNTIME_STATE_ACTIVE
	case remoteprovider.RuntimeConflict:
		return tunnelv1.RemoteRuntimeState_REMOTE_RUNTIME_STATE_CONFLICT
	case remoteprovider.RuntimeFailed:
		return tunnelv1.RemoteRuntimeState_REMOTE_RUNTIME_STATE_FAILED
	}
	return tunnelv1.RemoteRuntimeState_REMOTE_RUNTIME_STATE_UNSPECIFIED
}

func remoteRuntimeStateFromProto(pb tunnelv1.RemoteRuntimeState) remoteprovider.RuntimeState {
	switch pb {
	case tunnelv1.RemoteRuntimeState_REMOTE_RUNTIME_STATE_ACTIVE:
		return remoteprovider.RuntimeActive
	case tunnelv1.RemoteRuntimeState_REMOTE_RUNTIME_STATE_CONFLICT:
		return remoteprovider.RuntimeConflict
	case tunnelv1.RemoteRuntimeState_REMOTE_RUNTIME_STATE_FAILED:
		return remoteprovider.RuntimeFailed
	}
	return remoteprovider.RuntimeUnknown
}
