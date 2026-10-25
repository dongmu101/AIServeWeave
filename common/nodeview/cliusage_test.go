package nodeview

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/cliusagestatus"
	"AIServeWeave/common/runtime"
)

func TestCLIUsageFromAccounts(t *testing.T) {
	checked := time.Date(2026, 10, 25, 7, 59, 11, 0, time.UTC)
	resets := checked.Add(5 * time.Hour)
	long := strings.Repeat("x", 200)

	cases := []struct {
		name string
		in   []cliusagestatus.Account
		want []CLIUsage
	}{
		{"nothing reported", nil, nil},
		{
			"unsupported kind is dropped",
			[]cliusagestatus.Account{{Kind: runtime.KindOllama, Status: "ready"}},
			nil,
		},
		{
			"unknown status becomes error",
			[]cliusagestatus.Account{{Kind: runtime.KindCodex, Status: "<script>"}},
			[]CLIUsage{{Kind: "codex", Status: "error"}},
		},
		{
			"pending keeps no check time",
			[]cliusagestatus.Account{{Kind: runtime.KindClaude, Status: "pending"}},
			[]CLIUsage{{Kind: "claude", Status: "pending"}},
		},
		{
			"real zero is kept and results are sorted by kind",
			[]cliusagestatus.Account{
				{Kind: runtime.KindCodex, Status: "ready", CheckedAt: checked, Windows: []cliusagestatus.Window{
					{Name: "primary", Bucket: "codex", UsedPercent: 0, DurationMinutes: 10080, ResetsAt: resets},
				}},
				{Kind: runtime.KindClaude, Status: "ready", CheckedAt: checked, Windows: []cliusagestatus.Window{
					{Name: "five_hour", UsedPercent: 6, ResetsAt: resets},
				}},
			},
			[]CLIUsage{
				{Kind: "claude", Status: "ready", CheckedAt: &checked, Windows: []CLIUsageWindow{{Name: "five_hour", UsedPercent: 6, ResetsAt: &resets}}},
				{Kind: "codex", Status: "ready", CheckedAt: &checked, Windows: []CLIUsageWindow{{Name: "primary", Bucket: "codex", UsedPercent: 0, DurationMinutes: 10080, ResetsAt: &resets}}},
			},
		},
		{
			"percent is clamped and labels are bounded",
			[]cliusagestatus.Account{{Kind: runtime.KindClaude, Status: "ready", Windows: []cliusagestatus.Window{
				{Name: long, UsedPercent: 250, DurationMinutes: -5},
				{Name: "nan", UsedPercent: math.NaN()},
				{Name: "negative", UsedPercent: -3},
			}}},
			[]CLIUsage{{Kind: "claude", Status: "ready", Windows: []CLIUsageWindow{
				{Name: strings.Repeat("x", maxCLIUsageLabel), UsedPercent: 100},
				{Name: "nan", UsedPercent: 0},
				{Name: "negative", UsedPercent: 0},
			}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CLIUsageFromAccounts(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("CLIUsageFromAccounts:\nwant %+v\n got %+v", tc.want, got)
			}
		})
	}
}

func TestCLIUsageFromAccountsCapsCounts(t *testing.T) {
	windows := make([]cliusagestatus.Window, 20)
	accounts := []cliusagestatus.Account{{Kind: runtime.KindCodex, Status: "ready", Windows: windows}}
	got := CLIUsageFromAccounts(accounts)
	if len(got) != 1 || len(got[0].Windows) != maxCLIUsageWindows {
		t.Fatalf("windows = %d, want %d", len(got[0].Windows), maxCLIUsageWindows)
	}
}
