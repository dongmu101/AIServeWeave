package tunnelwire

import (
	"testing"
	"time"

	"AIServeWeave/common/modelpullstatus"
)

func TestModelPullReportRoundTrip_Reasons(t *testing.T) {
	tests := []modelpullstatus.FailureReason{
		modelpullstatus.ReasonUnspecified,
		modelpullstatus.ReasonUnknownName,
		modelpullstatus.ReasonInvalidSpec,
		modelpullstatus.ReasonNotAllowlisted,
		modelpullstatus.ReasonQuotaExceeded,
		modelpullstatus.ReasonFetchFailed,
		modelpullstatus.ReasonUnexpectedStatus,
		modelpullstatus.ReasonChecksumMismatch,
		modelpullstatus.ReasonStorageError,
		modelpullstatus.ReasonOllamaUnconfigured,
		modelpullstatus.ReasonOllamaPullFailed,
		modelpullstatus.ReasonLedgerQuotaExceeded,
		modelpullstatus.ReasonDiskSpaceLow,
	}

	for _, reason := range tests {
		t.Run(reason.String(), func(t *testing.T) {
			status := modelpullstatus.Status{
				Name:      "m1",
				State:     modelpullstatus.StateFailed,
				Reason:    reason,
				UpdatedAt: time.UnixMilli(1000).UTC(),
			}
			pb := ModelPullReportToProto([]modelpullstatus.Status{status})
			got := ModelPullReportFromProto(pb)
			if len(got) != 1 || got[0].Reason != reason {
				t.Fatalf("round trip reason = %+v, want %v", got, reason)
			}
		})
	}
}
