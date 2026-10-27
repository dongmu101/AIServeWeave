package tunnelwire

import (
	"reflect"
	"testing"
	"time"

	"AIServeWeave/common/cliusagestatus"
	"AIServeWeave/common/runtime"
)

func TestCLIUsageReportRoundTrip(t *testing.T) {
	checked := time.UnixMilli(1_700_000_000_000).UTC()
	resets := time.UnixMilli(1_700_018_000_000).UTC()
	cases := []struct {
		name string
		in   []cliusagestatus.Account
		want []cliusagestatus.Account
	}{
		{"no accounts", nil, []cliusagestatus.Account{}},
		{
			"pending account keeps zero check time",
			[]cliusagestatus.Account{{Kind: runtime.KindCodex, Status: "pending"}},
			[]cliusagestatus.Account{{Kind: runtime.KindCodex, Status: "pending", Windows: []cliusagestatus.Window{}}},
		},
		{
			"ready account sorted by kind, derived flags dropped",
			[]cliusagestatus.Account{
				{Kind: runtime.KindCodex, Status: "ready", CheckedAt: checked, Stale: true, Windows: []cliusagestatus.Window{
					{Name: "primary", Bucket: "codex", UsedPercent: 0, DurationMinutes: 10080, ResetsAt: resets, Expired: true},
				}},
				{Kind: runtime.KindClaude, Status: "ready", CheckedAt: checked, Windows: []cliusagestatus.Window{
					{Name: "five_hour", UsedPercent: 6.5, ResetsAt: resets},
				}},
			},
			[]cliusagestatus.Account{
				{Kind: runtime.KindClaude, Status: "ready", CheckedAt: checked, Windows: []cliusagestatus.Window{
					{Name: "five_hour", UsedPercent: 6.5, ResetsAt: resets},
				}},
				{Kind: runtime.KindCodex, Status: "ready", CheckedAt: checked, Windows: []cliusagestatus.Window{
					{Name: "primary", Bucket: "codex", UsedPercent: 0, DurationMinutes: 10080, ResetsAt: resets},
				}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CLIUsageReportFromProto(CLIUsageReportToProto(tc.in))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("round trip:\nwant %+v\n got %+v", tc.want, got)
			}
		})
	}
}
