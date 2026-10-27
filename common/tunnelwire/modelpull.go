package tunnelwire

import (
	"sort"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/modelpullstatus"
)

// ModelPullTriggerToProto encodes the names a Gateway asks an Agent to start
// pulling (STATUS.md's P2 model distribution subtask 2). There is
// deliberately no equivalent for a URL or any other fetch instruction: see
// modelpullstatus's package doc for why.
func ModelPullTriggerToProto(names []string) *tunnelv1.ModelPullTrigger {
	return &tunnelv1.ModelPullTrigger{Names: append([]string(nil), names...)}
}

// ModelPullTriggerFromProto restores the triggered names.
func ModelPullTriggerFromProto(pb *tunnelv1.ModelPullTrigger) []string {
	return append([]string(nil), pb.GetNames()...)
}

// ModelPullReportToProto encodes an Agent's current known pull statuses. The
// caller is expected to have sorted statuses by name already (Puller.Snapshot
// does); this function does not re-sort, so two calls with the same input
// produce byte-identical output, which is what the Agent's change-detection
// relies on.
func ModelPullReportToProto(statuses []modelpullstatus.Status) *tunnelv1.ModelPullReport {
	pulls := make([]*tunnelv1.ModelPullStatus, len(statuses))
	for i, st := range statuses {
		pulls[i] = modelPullStatusToProto(st)
	}
	return &tunnelv1.ModelPullReport{Pulls: pulls}
}

// ModelPullReportFromProto restores an Agent's reported pull statuses, sorted
// by name regardless of wire order, so a Gateway-side consumer never depends
// on the Agent having sent them in a particular sequence.
func ModelPullReportFromProto(pb *tunnelv1.ModelPullReport) []modelpullstatus.Status {
	pulls := pb.GetPulls()
	out := make([]modelpullstatus.Status, len(pulls))
	for i, p := range pulls {
		out[i] = modelPullStatusFromProto(p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func modelPullStatusToProto(st modelpullstatus.Status) *tunnelv1.ModelPullStatus {
	return &tunnelv1.ModelPullStatus{
		Name:            st.Name,
		State:           modelPullStateToProto(st.State),
		BytesDownloaded: st.BytesDownloaded,
		BytesTotal:      st.BytesTotal,
		Reason:          modelPullReasonToProto(st.Reason),
		UpdatedUnixMs:   st.UpdatedAt.UnixMilli(),
	}
}

func modelPullStatusFromProto(pb *tunnelv1.ModelPullStatus) modelpullstatus.Status {
	return modelpullstatus.Status{
		Name:            pb.GetName(),
		State:           modelPullStateFromProto(pb.GetState()),
		BytesDownloaded: pb.GetBytesDownloaded(),
		BytesTotal:      pb.GetBytesTotal(),
		Reason:          modelPullReasonFromProto(pb.GetReason()),
		UpdatedAt:       time.UnixMilli(pb.GetUpdatedUnixMs()).UTC(),
	}
}

func modelPullStateToProto(s modelpullstatus.State) tunnelv1.ModelPullState {
	switch s {
	case modelpullstatus.StatePending:
		return tunnelv1.ModelPullState_MODEL_PULL_STATE_PENDING
	case modelpullstatus.StateDownloading:
		return tunnelv1.ModelPullState_MODEL_PULL_STATE_DOWNLOADING
	case modelpullstatus.StateDone:
		return tunnelv1.ModelPullState_MODEL_PULL_STATE_DONE
	case modelpullstatus.StateFailed:
		return tunnelv1.ModelPullState_MODEL_PULL_STATE_FAILED
	default:
		return tunnelv1.ModelPullState_MODEL_PULL_STATE_UNSPECIFIED
	}
}

func modelPullStateFromProto(pb tunnelv1.ModelPullState) modelpullstatus.State {
	switch pb {
	case tunnelv1.ModelPullState_MODEL_PULL_STATE_PENDING:
		return modelpullstatus.StatePending
	case tunnelv1.ModelPullState_MODEL_PULL_STATE_DOWNLOADING:
		return modelpullstatus.StateDownloading
	case tunnelv1.ModelPullState_MODEL_PULL_STATE_DONE:
		return modelpullstatus.StateDone
	case tunnelv1.ModelPullState_MODEL_PULL_STATE_FAILED:
		return modelpullstatus.StateFailed
	default:
		return modelpullstatus.StateUnspecified
	}
}

func modelPullReasonToProto(r modelpullstatus.FailureReason) tunnelv1.ModelPullFailureReason {
	switch r {
	case modelpullstatus.ReasonUnknownName:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_UNKNOWN_NAME
	case modelpullstatus.ReasonInvalidSpec:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_INVALID_SPEC
	case modelpullstatus.ReasonNotAllowlisted:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_NOT_ALLOWLISTED
	case modelpullstatus.ReasonQuotaExceeded:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_QUOTA_EXCEEDED
	case modelpullstatus.ReasonFetchFailed:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_FETCH_FAILED
	case modelpullstatus.ReasonUnexpectedStatus:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_UNEXPECTED_STATUS
	case modelpullstatus.ReasonChecksumMismatch:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_CHECKSUM_MISMATCH
	case modelpullstatus.ReasonStorageError:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_STORAGE_ERROR
	case modelpullstatus.ReasonOllamaUnconfigured:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_OLLAMA_UNCONFIGURED
	case modelpullstatus.ReasonOllamaPullFailed:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_OLLAMA_PULL_FAILED
	case modelpullstatus.ReasonLedgerQuotaExceeded:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_LEDGER_QUOTA_EXCEEDED
	case modelpullstatus.ReasonDiskSpaceLow:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_DISK_SPACE_LOW
	default:
		return tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_UNSPECIFIED
	}
}

func modelPullReasonFromProto(pb tunnelv1.ModelPullFailureReason) modelpullstatus.FailureReason {
	switch pb {
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_UNKNOWN_NAME:
		return modelpullstatus.ReasonUnknownName
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_INVALID_SPEC:
		return modelpullstatus.ReasonInvalidSpec
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_NOT_ALLOWLISTED:
		return modelpullstatus.ReasonNotAllowlisted
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_QUOTA_EXCEEDED:
		return modelpullstatus.ReasonQuotaExceeded
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_FETCH_FAILED:
		return modelpullstatus.ReasonFetchFailed
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_UNEXPECTED_STATUS:
		return modelpullstatus.ReasonUnexpectedStatus
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_CHECKSUM_MISMATCH:
		return modelpullstatus.ReasonChecksumMismatch
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_STORAGE_ERROR:
		return modelpullstatus.ReasonStorageError
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_OLLAMA_UNCONFIGURED:
		return modelpullstatus.ReasonOllamaUnconfigured
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_OLLAMA_PULL_FAILED:
		return modelpullstatus.ReasonOllamaPullFailed
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_LEDGER_QUOTA_EXCEEDED:
		return modelpullstatus.ReasonLedgerQuotaExceeded
	case tunnelv1.ModelPullFailureReason_MODEL_PULL_FAILURE_REASON_DISK_SPACE_LOW:
		return modelpullstatus.ReasonDiskSpaceLow
	default:
		return modelpullstatus.ReasonUnspecified
	}
}
