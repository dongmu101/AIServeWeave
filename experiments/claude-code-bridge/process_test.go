package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// TestCLIHelper is a subprocess fixture, never a real model invocation.
// TestCLIHelper 是子进程测试桩，不会调用真实模型。
func TestCLIHelper(t *testing.T) {
	mode := os.Getenv("AISW_PROBE_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "success":
		fmt.Println(`{"type":"result","subtype":"success","result":"sentinel"}`)
	case "failure":
		fmt.Fprintln(os.Stderr, "SECRET-PROMPT-CREDENTIAL")
		fmt.Println(`{"type":"result","subtype":"success"}`)
		os.Exit(7)
	case "invalid":
		fmt.Println("SECRET-PROMPT-CREDENTIAL")
	case "oversized":
		fmt.Println(strings.Repeat("x", maxEventBytes+1))
	case "auth-output":
		fmt.Print(strings.Repeat("x", 128*1024))
	case "wait":
		for {
			var value [1]byte
			_, _ = os.Stdin.Read(value[:])
		}
	}
	os.Exit(0)
}

// TestRunCLI checks process exit and parser errors without leaking stderr.
// TestRunCLI 检查进程退出和解析错误不会泄漏 stderr。
func TestRunCLI(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{"success", nil}, {"failure", errCLIExit}, {"invalid", errCLIProtocol}, {"oversized", errCLIProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := runCLI(t.Context(), os.Args[0], []string{"-test.run=^TestCLIHelper$"}, t.TempDir(), append(os.Environ(), "AISW_PROBE_HELPER="+tc.name), strings.NewReader(""), "sentinel")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal("stderr leaked, want constant error")
			}
			if tc.want == nil && !s.SentinelSeen {
				t.Fatalf("sentinel = %v, want true", s.SentinelSeen)
			}
		})
	}
}

type readerFunc func([]byte) (int, error)

// Read delegates to a deterministic test-controlled input source.
// Read 委托给由测试确定性控制的输入源。
func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// TestRunCLICancel cancels once the started process begins consuming stdin.
// TestRunCLICancel 在已启动进程开始读取 stdin 时取消。
func TestRunCLICancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := runCLI(ctx, os.Args[0], []string{"-test.run=^TestCLIHelper$"}, t.TempDir(), append(os.Environ(), "AISW_PROBE_HELPER=wait"), readerFunc(func([]byte) (int, error) { cancel(); return 0, io.EOF }), "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v, want canceled", err)
	}
}

// TestProbeConfiguration keeps local execution and credential overrides closed.
// TestProbeConfiguration 保证本地执行入口和凭据覆盖关闭。
func TestProbeConfiguration(t *testing.T) {
	args := probeArgs("sonnet", "/tmp/mcp.json")
	for _, pair := range [][2]string{{"--tools", ""}, {"--permission-mode", "dontAsk"}, {"--setting-sources", ""}, {"--allowedTools", "mcp__aisw_probe__probe_echo"}} {
		found := false
		for i := 0; i+1 < len(args); i++ {
			if args[i] == pair[0] && args[i+1] == pair[1] {
				found = true
			}
		}
		if !found {
			t.Fatalf("args missing %q, want explicit setting", pair)
		}
	}
	joined := strings.Join(args, "|")
	if strings.Contains(joined, "--bare") || !strings.Contains(joined, "--restricted") || !strings.Contains(joined, "--strict-mcp-config") {
		t.Fatalf("args = %q, want restricted subscription-compatible launch", joined)
	}
	env := probeEnv([]string{"PATH=/bin", "HOME=/home/example", "CLAUDE_CONFIG_DIR=/config", "ANTHROPIC_API_KEY=secret", "ANTHROPIC_AUTH_TOKEN=secret", "ANTHROPIC_BASE_URL=http://gateway", "CLAUDECODE=1", "BASH_ENV=/secret"})
	got := strings.Join(env, "|")
	if strings.Contains(got, "secret") || strings.Contains(got, "gateway") || strings.Contains(got, "CLAUDECODE") || !strings.Contains(got, "CLAUDE_CONFIG_DIR=/config") {
		t.Fatalf("env = %q, want login context only", got)
	}
}
