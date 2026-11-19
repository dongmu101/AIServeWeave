package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/internal/cliphase"
)

// TestNativeRoundLogsPhases runs a native round through a real subprocess
// and finds its phases in the round log and the metrics.
// TestNativeRoundLogsPhases 经真实子进程运行一个原生回合，并在回合日志与指标中找到其各阶段。
func TestNativeRoundLogsPhases(t *testing.T) {
	var logs bytes.Buffer
	registry := metrics.New(cliphase.Descriptions())
	rt, err := NewFactory(os.Args[0])(runtime.Config{ID: "guarded", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Metrics: registry})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	defer r.Close()
	r.models = map[string]struct{}{"claude-opus-5-5": {}}
	r.bridge.env = append(r.bridge.cliEnv(), "AISW_CLAUDE_SAFEGUARDS_FIXTURE=1")
	// The request shape the safeguards fixture checks (see TestSafeguardsFreshContext).
	// safeguards 夹具所校验的请求形状（见 TestSafeguardsFreshContext）。
	req := guardedRequest()
	temperature := .25
	req.Temperature = &temperature
	data, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	stream, err := r.Messages(ctx, runtime.MessagesRequest{Model: "claude-opus-5-5", TenantID: "tenant", KeyID: "key", JSON: data, APIVersion: "2023-06-01", BetaFeatures: "future-feature-2099-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := stream.Recv(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	_ = stream.Close()
	var record map[string]any
	scanner := bufio.NewScanner(&logs)
	for scanner.Scan() {
		var line map[string]any
		if json.Unmarshal(scanner.Bytes(), &line) == nil && line["msg"] == "Claude Messages round finished" {
			record = line
		}
	}
	for _, key := range []string{"process_started_ms", "cli_ready_ms", "first_event_ms", "done_ms", "exited_ms", "cli_cpu_ms"} {
		if _, ok := record[key]; !ok {
			t.Fatalf("round log=%v, want %s", record, key)
		}
	}
	var out strings.Builder
	if err := registry.Render(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `agent_cli_phase_seconds_count{kind="claude",phase="first_event",runtime_id="guarded"} 1`) {
		t.Fatalf("exposition lacks the first_event phase:\n%s", out.String())
	}
}

// TestTimeConnectRecordsFirstRequest marks the model request as sent and
// keeps its first wait for an upstream connection, not a retry's reuse.
// TestTimeConnectRecordsFirstRequest 把模型请求标记为已发出，并保留其第一次等待上游连接的
// 时间，而不是重试时的复用。
func TestTimeConnectRecordsFirstRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	t.Cleanup(server.Close)
	capture := &upstreamCapture{phases: cliphase.New(runtime.NewSystemClock())}
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, nil)
		response, err := client.Do(capture.timeConnect(req))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	attrs := capture.phases.LogAttrs()
	got := map[any]any{}
	for i := 0; i+1 < len(attrs); i += 2 {
		got[attrs[i]] = attrs[i+1]
	}
	if _, ok := got["request_sent_ms"]; !ok || got["upstream_conn_reused"] != false {
		t.Fatalf("attrs=%v, want request_sent and the first, new connection", attrs)
	}
	var untimed upstreamCapture
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	if untimed.timeConnect(req) != req {
		t.Fatal("a capture without phases changed the request")
	}
}
