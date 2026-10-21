package codexcli

import (
	"testing"
	"time"
)

// TestParseAccountUsage distinguishes real zero usage from absent quota data.
// TestParseAccountUsage 区分真实零用量与未提供的额度数据。
func TestParseAccountUsage(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		wantCount  int
		wantUsed   float64
		wantError  bool
	}{
		{"primary and secondary", `{"rateLimits":{"primary":{"usedPercent":25,"windowDurationMins":300,"resetsAt":1800003600},"secondary":{"usedPercent":0,"windowDurationMins":10080,"resetsAt":1800600000}}}`, 2, 25, false},
		{"multi bucket supersedes legacy", `{"rateLimits":{"primary":{"usedPercent":99,"windowDurationMins":300,"resetsAt":1800003600}},"rateLimitsByLimitId":{"codex":{"primary":{"usedPercent":20,"windowDurationMins":300,"resetsAt":1800003600}}}}`, 1, 20, false},
		{"missing usage is unknown", `{"rateLimits":{"primary":{"resetsAt":1800003600}}}`, 0, 0, true},
		{"invalid percentage", `{"rateLimits":{"primary":{"usedPercent":-1,"resetsAt":1800003600}}}`, 0, 0, true},
		{"over limit stays observable", `{"rateLimits":{"primary":{"usedPercent":109,"resetsAt":1800003600}}}`, 1, 109, false},
		{"missing quota", `{"rateLimits":null}`, 0, 0, true},
		{"opaque bucket with punctuation", `{"rateLimitsByLimitId":{"codex.gpt-6":{"primary":{"usedPercent":25,"resetsAt":1800003600}}}}`, 1, 25, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAccountUsage([]byte(tc.data))
			if (err != nil) != tc.wantError || len(got.Windows) != tc.wantCount {
				t.Fatalf("account=%+v error=%v, want %d windows and error=%t", got, err, tc.wantCount, tc.wantError)
			}
			if tc.wantCount > 0 && (got.Windows[0].UsedPercent != tc.wantUsed || !got.Windows[0].ResetsAt.Equal(time.Unix(1800003600, 0))) {
				t.Fatalf("window=%+v, want used=%v and reset=1800003600", got.Windows[0], tc.wantUsed)
			}
		})
	}
}
