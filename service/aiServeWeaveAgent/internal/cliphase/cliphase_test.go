package cliphase_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/internal/cliphase"
)

type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { return c.now }
func (c *stepClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	return make(chan time.Time), func() bool { return true }
}

// TestTimelineRecordsPhasesOnce keeps each phase's first mark, in order, and
// exposes it in logs and metrics.
// TestTimelineRecordsPhasesOnce 按顺序保留每个阶段的首次标记，并在日志与指标中呈现。
func TestTimelineRecordsPhasesOnce(t *testing.T) {
	clock := &stepClock{now: time.Unix(1_800_000_000, 0)}
	timeline := cliphase.New(clock)
	clock.now = clock.now.Add(150 * time.Millisecond)
	timeline.Mark(cliphase.ProcessStarted)
	clock.now = clock.now.Add(600 * time.Millisecond)
	timeline.Mark(cliphase.RequestSent)
	clock.now = clock.now.Add(time.Second)
	timeline.Mark(cliphase.RequestSent) // a later mark does not move it / 之后的标记不会改变它
	timeline.Connected(120*time.Millisecond, false)
	timeline.Connected(0, true)
	attrs := timeline.LogAttrs()
	want := []any{"process_started_ms", int64(150), "request_sent_ms", int64(750), "upstream_connect_ms", int64(120), "upstream_conn_reused", false}
	if len(attrs) != len(want) {
		t.Fatalf("attrs=%v, want %v", attrs, want)
	}
	for i := range want {
		if attrs[i] != want[i] {
			t.Fatalf("attrs=%v, want %v", attrs, want)
		}
	}
	registry := metrics.New(cliphase.Descriptions())
	timeline.Observe(registry, "codex-local", runtime.KindCodex)
	var out strings.Builder
	if err := registry.Render(&out); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`agent_cli_phase_seconds_count{kind="codex",phase="request_sent",runtime_id="codex-local"} 1`,
		`agent_cli_upstream_connect_seconds_count{kind="codex",reused="false",runtime_id="codex-local"} 1`,
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("exposition lacks %s:\n%s", line, out.String())
		}
	}
}

// TestTimelineProcessUsage records a reaped process's CPU time, and a nil
// timeline accepts every call.
// TestTimelineProcessUsage 记录已回收进程的 CPU 时间；nil 时间线接受所有调用。
func TestTimelineProcessUsage(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	timeline := cliphase.New(runtime.NewSystemClock())
	timeline.Exited(cmd.ProcessState)
	attrs := timeline.LogAttrs()
	if len(attrs) < 4 || attrs[0] != "exited_ms" || attrs[2] != "cli_cpu_ms" {
		t.Fatalf("attrs=%v, want exited_ms then cli_cpu_ms", attrs)
	}
	var none *cliphase.Timeline
	none.Mark(cliphase.Done)
	none.Connected(time.Second, true)
	none.Exited(cmd.ProcessState)
	none.Observe(metrics.New(), "x", runtime.KindClaude)
	if none.LogAttrs() != nil {
		t.Fatal("nil timeline recorded something")
	}
}
