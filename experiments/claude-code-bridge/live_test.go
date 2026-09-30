package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveProbe requires an explicit opt-in and an existing CLI login.
// TestLiveProbe 必须显式启用且 CLI 已有登录状态。
func TestLiveProbe(t *testing.T) {
	if os.Getenv("AISW_CLAUDE_LIVE_TEST") != "1" {
		t.Skip("set AISW_CLAUDE_LIVE_TEST=1 to use the local subscription")
	}
	cli := os.Getenv("AISW_CLAUDE_PATH")
	if cli == "" {
		cli = "claude"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err := checkAuth(ctx, cli, probeEnv(os.Environ())); err != nil {
		t.Fatal(err)
	}
	report, err := runProbe(ctx, cli, "sonnet", "live-test", "sequential")
	if err != nil {
		t.Fatalf("probe = %v, want successful tool round trip", err)
	}
	if !report.Passed {
		t.Fatalf("report = %+v, want passed", report)
	}
}
