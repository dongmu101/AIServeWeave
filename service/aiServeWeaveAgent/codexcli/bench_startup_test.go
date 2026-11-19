//go:build darwin || linux

package codexcli

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/internal/cliphase"
)

// TestCLIStartupBenchmark measures, with the installed codex and the Agent's
// own command line and seed handling, how long an app-server takes to start,
// answer initialize and account/read, start a thread and take a history,
// which is what a warm process could save at most. It needs no login and
// sends nothing: CODEX_HOME is a temporary directory and every connection
// goes to a proxy that refuses it.
//
// AISW_CLI_BENCH=1 enables it. AISW_CLI_BENCH_RUNS sets the number of runs
// (default 20), AISW_CODEX_PATH the CLI, AISW_CODEX_BENCH_HISTORY_KB the
// injected history (default 256), and AISW_CODEX_BENCH_SESSIONS a sessions
// directory to copy in, so start-up includes indexing a real history.
//
// TestCLIStartupBenchmark 使用已安装的 codex 以及 Agent 自己的命令行与种子处理，测量
// app-server 启动、答复 initialize 与 account/read、开启线程并接收历史需要多久，这就是预热
// 进程最多能省下的时间。它不需要登录，也不发送任何数据：CODEX_HOME 是临时目录，所有连接都
// 发往拒绝连接的代理。
//
// 设置 AISW_CLI_BENCH=1 启用。AISW_CLI_BENCH_RUNS 设置运行次数（默认 20），AISW_CODEX_PATH
// 指定 CLI，AISW_CODEX_BENCH_HISTORY_KB 指定注入的历史大小（默认 256），AISW_CODEX_BENCH_SESSIONS
// 指定要复制进来的 sessions 目录，使启动包含为真实历史建索引。
func TestCLIStartupBenchmark(t *testing.T) {
	if os.Getenv("AISW_CLI_BENCH") != "1" {
		t.Skip("set AISW_CLI_BENCH=1 to measure the installed codex start-up (no login, no network)")
	}
	program := os.Getenv("AISW_CODEX_PATH")
	if program == "" {
		program = "codex"
	}
	if _, err := exec.LookPath(program); err != nil {
		t.Fatal("codex is unavailable")
	}
	runs := 20
	if n, err := strconv.Atoi(os.Getenv("AISW_CLI_BENCH_RUNS")); err == nil && n > 0 {
		runs = n
	}
	historyKB := 256
	if n, err := strconv.Atoi(os.Getenv("AISW_CODEX_BENCH_HISTORY_KB")); err == nil && n >= 0 {
		historyKB = n
	}
	home := t.TempDir()
	if sessions := os.Getenv("AISW_CODEX_BENCH_SESSIONS"); sessions != "" {
		if err := os.CopyFS(filepath.Join(home, "sessions"), os.DirFS(sessions)); err != nil {
			t.Fatal(err)
		}
	}
	cache := t.TempDir()
	original := userCacheDir
	userCacheDir = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDir = original })
	proxy := refusingProxy(t)
	env := withAccountDir(probeEnv(os.Environ()), home)
	env = append(env, "HTTPS_PROXY="+proxy, "HTTP_PROXY="+proxy, "ALL_PROXY="+proxy)
	items := make([]any, 0, historyKB)
	for i := 0; i < historyKB; i++ {
		items = append(items, map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": strings.Repeat("x", 1000)}}})
	}
	session := func(p *protocol, dir string) error {
		if err := p.request(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "aisw-codex-bench", "version": "0.1.0"}, "capabilities": map[string]bool{"experimentalApi": true}}, new(map[string]any)); err != nil {
			return err
		}
		if err := p.write(map[string]string{"method": "initialized"}); err != nil {
			return err
		}
		if err := p.request(2, "account/read", map[string]bool{"refreshToken": false}, new(map[string]any)); err != nil {
			return err
		}
		var thread struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := p.request(3, "thread/start", map[string]any{"cwd": dir, "ephemeral": true, "sandbox": "read-only", "approvalPolicy": "never", "model": "gpt-5"}, &thread); err != nil {
			return err
		}
		return p.request(4, "thread/inject_items", map[string]any{"threadId": thread.Thread.ID, "items": items}, new(map[string]any))
	}
	// A read process first, so later starts copy a seed as they do in production.
	// 先运行一个读取进程，使之后的启动像生产环境一样复制种子。
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := withReadServer(ctx, program, env, func(p *protocol, _ string) error {
		return p.request(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "aisw-codex-bench", "version": "0.1.0"}}, new(map[string]any))
	}); err != nil {
		t.Fatalf("seed process: %v", err)
	}
	samples := map[string][]time.Duration{}
	for i := 0; i < runs; i++ {
		phases := cliphase.New(runtime.NewSystemClock())
		if err := withServer(ctx, program, env, phases, session); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		attrs := phases.LogAttrs()
		for j := 0; j+1 < len(attrs); j += 2 {
			if key, ok := attrs[j].(string); ok {
				if ms, ok := attrs[j+1].(int64); ok {
					samples[key] = append(samples[key], time.Duration(ms)*time.Millisecond)
				}
			}
		}
	}
	t.Logf("codex %s: %d runs, history %d KB, sessions copied: %t", program, runs, historyKB, os.Getenv("AISW_CODEX_BENCH_SESSIONS") != "")
	for _, key := range []string{"process_started_ms", "cli_ready_ms", "login_checked_ms", "thread_started_ms", "history_injected_ms", "exited_ms", "cli_cpu_ms"} {
		t.Logf("%-20s %s", key, percentiles(samples[key]))
	}
	if rss := samples["cli_max_rss_kb"]; len(rss) > 0 {
		slices.Sort(rss)
		t.Logf("%-20s p50=%dKB max=%dKB", "cli_max_rss_kb", rss[len(rss)/2].Milliseconds(), rss[len(rss)-1].Milliseconds())
	}
}

// refusingProxy is an HTTP proxy on loopback that answers every request,
// CONNECT included, with 403, so nothing it is asked for leaves the machine.
// refusingProxy 是回环上的 HTTP 代理，对所有请求（包括 CONNECT）答复 403，因此请求的任何内容
// 都不会离开本机。
func refusingProxy(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = bufio.NewReader(conn).ReadString('\n')
				_, _ = conn.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
			}()
		}
	}()
	return "http://" + listener.Addr().String()
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
