package configui

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/cliusage"
)

// TestCLIUsagePageShowsActualQuota keeps rendering separate from remote quota queries.
// TestCLIUsagePageShowsActualQuota 验证页面呈现真实额度，且渲染不触发外部查询。
func TestCLIUsagePageShowsActualQuota(t *testing.T) {
	calls := 0
	monitor := cliusage.NewMonitor(runtime.NewSystemClock(), []cliusage.Provider{{Kind: runtime.KindCodex, Read: func(context.Context) (cliusage.Account, error) {
		calls++
		return cliusage.Account{Windows: []cliusage.Window{{Name: "primary", Bucket: "codex", UsedPercent: 25, ResetsAt: time.Now().Add(time.Hour)}}}, nil
	}}})
	handler := newIndexHandler(discardLogger(), filepath.Join(t.TempDir(), "agent.yaml"), Sources{CLIUsage: monitor})
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("GET", "/", nil))
	if calls != 0 || !strings.Contains(w.Body.String(), `id="cli-usage"`) {
		t.Fatalf("queries=%d, section present=%t; want zero queries and quota section", calls, strings.Contains(w.Body.String(), `id="cli-usage"`))
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/status/cli-usage", nil)
	r.Header.Set("Accept-Language", "zh-CN")
	newUsageHandler(monitor)(w, r)
	if calls != 1 || !strings.Contains(w.Body.String(), "25%") || !strings.Contains(w.Body.String(), "75%") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("queries=%d body=%s, want one query and 25%% used / 75%% remaining with no-store", calls, w.Body.String())
	}
}

// TestCLIUsageFailuresAndMethods rejects mutations and never invents zero usage.
// TestCLIUsageFailuresAndMethods 拒绝写方法，且不会将查询失败伪装为零用量。
func TestCLIUsageFailuresAndMethods(t *testing.T) {
	for _, tc := range []struct {
		name, method        string
		wantCode, wantCalls int
	}{
		{"head reads no CLI", "HEAD", 200, 0},
		{"post rejected", "POST", 405, 0},
		{"get safe unavailable", "GET", 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			monitor := cliusage.NewMonitor(runtime.NewSystemClock(), []cliusage.Provider{{Kind: runtime.KindClaude, Read: func(context.Context) (cliusage.Account, error) {
				calls++
				return cliusage.Account{}, cliusage.ErrUnauthorized
			}}})
			w := httptest.NewRecorder()
			newUsageHandler(monitor)(w, httptest.NewRequest(tc.method, "/status/cli-usage", nil))
			if w.Code != tc.wantCode || calls != tc.wantCalls || strings.Contains(w.Body.String(), "0%") {
				t.Fatalf("status=%d queries=%d body=%s, want status=%d queries=%d and no invented percentage", w.Code, calls, w.Body.String(), tc.wantCode, tc.wantCalls)
			}
		})
	}
}

// TestCLIUsagePercentagesAvoidFloatingPointArtifacts preserves readable remaining percentages.
// TestCLIUsagePercentagesAvoidFloatingPointArtifacts 保证余量比例不会出现浮点计算尾差。
func TestCLIUsagePercentagesAvoidFloatingPointArtifacts(t *testing.T) {
	monitor := cliusage.NewMonitor(runtime.NewSystemClock(), []cliusage.Provider{{Kind: runtime.KindCodex, Read: func(context.Context) (cliusage.Account, error) {
		return cliusage.Account{Windows: []cliusage.Window{{Name: "primary", UsedPercent: 68.9, ResetsAt: time.Now().Add(time.Hour)}}}, nil
	}}})
	monitor.Refresh(t.Context())
	window := currentCLIUsage(monitor, "zh-CN").Accounts[0].Windows[0]
	if window.Used != "68.9%" || window.Remaining != "31.1%" {
		t.Fatalf("used=%s remaining=%s, want 68.9%% and 31.1%%", window.Used, window.Remaining)
	}
}
