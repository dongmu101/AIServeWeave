package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

// TestCLIStartupBenchmark runs native rounds through the installed CLI and
// the Agent's own code path (temporary CA, per-process proxy, settings file)
// against a loopback stand-in for api.anthropic.com, and reports each phase
// of the round (see cliphase) as p50/p95/max. It also lists the requests the
// CLI sends before its model request: those are on every round's critical
// path. Nothing leaves the machine and no quota is spent: the CLI uses a
// placeholder key and a temporary configuration directory, and the stand-in
// answers every other request with an empty JSON object. Loopback hides
// network round trips, so the numbers are the Agent's and the CLI's own
// cost; production adds one upstream round trip per request listed.
//
// AISW_CLI_BENCH=1 enables it; AISW_CLI_BENCH_RUNS sets the number of runs
// (default 20) and AISW_CLAUDE_PATH the CLI.
//
// TestCLIStartupBenchmark 使用已安装的 CLI 与 Agent 自己的代码路径（临时 CA、每进程代理、
// 设置文件），对着本机冒充的 api.anthropic.com 运行原生回合，并以 p50/p95/最大值报告回合的
// 各阶段（见 cliphase）。它还列出 CLI 在模型请求之前发出的请求：这些请求位于每个回合的关键
// 路径上。没有任何数据离开本机，也不消耗额度：CLI 使用占位 Key 与临时配置目录，冒充的上游对
// 其他请求一律答复空 JSON 对象。回环没有网络往返，因此这些数字是 Agent 与 CLI 自身的开销；
// 生产环境中每个列出的请求还要再加一次上游往返。
//
// 设置 AISW_CLI_BENCH=1 启用；AISW_CLI_BENCH_RUNS 设置运行次数（默认 20），AISW_CLAUDE_PATH
// 指定 CLI。
func TestCLIStartupBenchmark(t *testing.T) {
	if os.Getenv("AISW_CLI_BENCH") != "1" {
		t.Skip("set AISW_CLI_BENCH=1 to measure the installed CLI's round phases (loopback, no quota)")
	}
	program := os.Getenv("AISW_CLAUDE_PATH")
	if program == "" {
		program = "claude"
	}
	if _, err := exec.LookPath(program); err != nil {
		t.Fatal("Claude CLI is unavailable")
	}
	runs := 20
	if n, err := strconv.Atoi(os.Getenv("AISW_CLI_BENCH_RUNS")); err == nil && n > 0 {
		runs = n
	}
	var mu sync.Mutex
	before := map[string]int{} // requests seen before the model request, per run / 每次运行中模型请求之前出现的请求
	modelSeen := false
	env := benchFixtureEnvironment(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/v1/messages") {
			mu.Lock()
			if !modelSeen {
				before[req.Method+" "+req.URL.Path]++
			}
			mu.Unlock()
			// The CLI waits for its organisation policy and remote settings
			// before it starts, and retries a reply it cannot read; 404 is
			// "none", which it accepts at once.
			// CLI 启动前要等组织策略与远程设置，读不懂的答复会重试；404 表示「没有」，CLI 会立即接受。
			if strings.HasPrefix(req.URL.Path, "/api/claude_code/") {
				http.NotFound(w, req)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
			return
		}
		mu.Lock()
		modelSeen = true
		mu.Unlock()
		_, _ = io.Copy(io.Discard, req.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		var events bytes.Buffer
		fixtureRound(&events, "", "bench-ok")
		for _, line := range strings.Split(events.String(), "\n") {
			var item struct{ Event json.RawMessage }
			if json.Unmarshal([]byte(line), &item) != nil {
				continue
			}
			var kind struct{ Type string }
			_ = json.Unmarshal(item.Event, &kind)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind.Type, item.Event)
		}
	}))
	body := json.RawMessage(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	samples := map[string][]time.Duration{}
	completed := 0
	for i := 0; i < runs; i++ {
		mu.Lock()
		modelSeen = false
		mu.Unlock()
		var logs bytes.Buffer
		rt, err := NewFactory(program)(runtime.Config{ID: "bench", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
		if err != nil {
			t.Fatal(err)
		}
		r := rt.(*Runtime)
		r.models = map[string]struct{}{"claude-opus-5-5": {}}
		r.bridge.env = env
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		stream, err := r.Messages(ctx, runtime.MessagesRequest{Model: "claude-opus-5-5", TenantID: "bench", KeyID: "bench", JSON: body, Native: true})
		if err == nil {
			for {
				if _, err = stream.Recv(); err != nil {
					break
				}
			}
			_ = stream.Close()
		}
		cancel()
		_ = r.Close()
		if err == io.EOF {
			completed++
		}
		scanner := bufio.NewScanner(&logs)
		for scanner.Scan() {
			var record map[string]any
			if json.Unmarshal(scanner.Bytes(), &record) != nil || record["msg"] != "Claude Messages round finished" {
				continue
			}
			for key, value := range record {
				// Every value is kept as milliseconds; cli_max_rss_kb is read back as a number.
				// 所有值都按毫秒保存；cli_max_rss_kb 读回时当作数字。
				if ms, ok := value.(float64); ok && (strings.HasSuffix(key, "_ms") || key == "cli_max_rss_kb") {
					samples[key] = append(samples[key], time.Duration(ms)*time.Millisecond)
				}
			}
		}
	}
	t.Logf("CLI %s: %d runs, %d completed", program, runs, completed)
	for _, key := range []string{"process_started_ms", "cli_ready_ms", "request_sent_ms", "upstream_connect_ms", "upstream_headers_ms", "first_event_ms", "done_ms", "exited_ms", "cli_cpu_ms"} {
		t.Logf("%-20s %s", key, percentiles(samples[key]))
	}
	if rss := samples["cli_max_rss_kb"]; len(rss) > 0 {
		slices.Sort(rss)
		t.Logf("%-20s p50=%dKB max=%dKB", "cli_max_rss_kb", rss[len(rss)/2].Milliseconds(), rss[len(rss)-1].Milliseconds())
	}
	mu.Lock()
	paths := make([]string, 0, len(before))
	for path := range before {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		t.Logf("before the model request: %-50s %.1f per run", path, float64(before[path])/float64(runs))
	}
	mu.Unlock()
}

// benchFixtureEnvironment is nativeFixtureEnvironment without its telemetry
// switches, so the CLI starts as it does in production.
// benchFixtureEnvironment 是去掉遥测开关的 nativeFixtureEnvironment，使 CLI 像生产环境一样启动。
func benchFixtureEnvironment(t *testing.T, handler http.Handler) []string {
	t.Helper()
	ca, leaf, err := upstreamCertificates()
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewUnstartedServer(handler)
	api.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	api.StartTLS()
	t.Cleanup(api.Close)
	dir := t.TempDir()
	caFile := filepath.Join(dir, "fixture-ca.pem")
	if err := os.WriteFile(caFile, ca, 0600); err != nil {
		t.Fatal(err)
	}
	base := append(withAccountDir(probeEnv(os.Environ()), t.TempDir()), "ANTHROPIC_API_KEY=loopback-fixture-only", "NODE_EXTRA_CA_CERTS="+caFile)
	proxy, err := startUpstreamProxy(&upstreamCapture{}, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.close)
	proxy.transport.Proxy = nil
	dial := proxy.transport.DialContext
	proxy.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != upstreamAPIHost+":443" {
			return nil, errors.New("fixture refuses external destination")
		}
		return dial(ctx, network, api.Listener.Addr().String())
	}
	env, err := proxy.environ(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// percentiles formats p50, p95 and max of samples.
// percentiles 格式化 samples 的 p50、p95 与最大值。
func percentiles(samples []time.Duration) string {
	if len(samples) == 0 {
		return "no samples"
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	at := func(q float64) time.Duration { return sorted[min(len(sorted)-1, int(q*float64(len(sorted))))] }
	return "p50=" + at(0.50).String() + " p95=" + at(0.95).String() + " max=" + sorted[len(sorted)-1].String() + " n=" + strconv.Itoa(len(sorted))
}
