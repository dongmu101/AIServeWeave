package tunnelwire

import (
	"testing"
	"time"

	"AIServeWeave/common/comfyuimanagedstatus"
)

func TestComfyUIManagedActionRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		action comfyuimanagedstatus.Action
	}{
		{"unspecified", comfyuimanagedstatus.ActionUnspecified},
		{"start", comfyuimanagedstatus.ActionStart},
		{"stop", comfyuimanagedstatus.ActionStop},
		{"restart", comfyuimanagedstatus.ActionRestart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComfyUIManagedActionFromProto(ComfyUIManagedActionToProto(tc.action))
			if got != tc.action {
				t.Fatalf("round trip: want %v, got %v", tc.action, got)
			}
		})
	}
}

func TestComfyUIManagedActionFromProtoUnrecognizedIsUnspecified(t *testing.T) {
	got := ComfyUIManagedActionFromProto(nil)
	if got != comfyuimanagedstatus.ActionUnspecified {
		t.Fatalf("nil proto: want ActionUnspecified, got %v", got)
	}
}

func TestComfyUIManagedReportRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		statuses []comfyuimanagedstatus.Status
	}{
		{"empty", nil},
		{
			"one instance",
			[]comfyuimanagedstatus.Status{
				{
					ContainerName: "aiserveweave-comfyui",
					State:         comfyuimanagedstatus.StateRunning,
					UpdatedAt:     time.UnixMilli(1_700_000_000_000).UTC(),
				},
			},
		},
		{
			"sorts by container name regardless of input order",
			[]comfyuimanagedstatus.Status{
				{ContainerName: "zeta", State: comfyuimanagedstatus.StateFailed, UpdatedAt: time.UnixMilli(2000).UTC()},
				{ContainerName: "alpha", State: comfyuimanagedstatus.StatePending, UpdatedAt: time.UnixMilli(1000).UTC()},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComfyUIManagedReportFromProto(ComfyUIManagedReportToProto(tc.statuses))
			if len(got) != len(tc.statuses) {
				t.Fatalf("length: want %d, got %d", len(tc.statuses), len(got))
			}
			for i := 1; i < len(got); i++ {
				if got[i-1].ContainerName > got[i].ContainerName {
					t.Fatalf("not sorted by container name: %v", got)
				}
			}
			if len(tc.statuses) == 1 {
				want, gotOne := tc.statuses[0], got[0]
				if gotOne.ContainerName != want.ContainerName || gotOne.State != want.State || !gotOne.UpdatedAt.Equal(want.UpdatedAt) {
					t.Fatalf("round trip: want %+v, got %+v", want, gotOne)
				}
			}
		})
	}
}

func TestComfyUIManagedStateRoundTrip(t *testing.T) {
	states := []comfyuimanagedstatus.State{
		comfyuimanagedstatus.StateUnspecified,
		comfyuimanagedstatus.StatePending,
		comfyuimanagedstatus.StateStarting,
		comfyuimanagedstatus.StateRunning,
		comfyuimanagedstatus.StateStopped,
		comfyuimanagedstatus.StateFailed,
	}
	for _, s := range states {
		t.Run(s.String(), func(t *testing.T) {
			got := comfyUIManagedStateFromProto(comfyUIManagedStateToProto(s))
			if got != s {
				t.Fatalf("round trip: want %v, got %v", s, got)
			}
		})
	}
}
