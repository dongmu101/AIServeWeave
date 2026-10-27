//go:build darwin || linux

package codexcli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// TestProbeProtocol tests actual child-process RPC with no model service.
// TestProbeProtocol 使用真实子进程 RPC 测试，不调用模型服务。
func TestProbeProtocol(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{"ok", nil}, {"auth", ErrAuthentication}, {"rpc-error", ErrRPC}, {"cross-thread", ErrProtocol}, {"cross-turn", ErrProtocol}, {"instructions", ErrIsolation},
		{"global-instructions", nil},
		{"unknown-tool", ErrProtocol}, {"duplicate", ErrProtocol}, {"native", ErrUnexpectedTool}, {"approval", ErrUnexpectedTool},
		{"collab", ErrUnexpectedTool}, {"unknown-item", ErrUnexpectedTool},
		{"eof", ErrProtocol}, {"malformed", ErrProtocol}, {"oversized", ErrProtocol}, {"failed-turn", ErrTurn}, {"wrong-result", ErrTurn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := probeWithEnv(t.Context(), Config{Executable: os.Args[0]}, append(os.Environ(), "AISW_CODEX_FIXTURE="+tc.name, "CODEX_HOME=/operator/codex"))
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal("error contains input, want constant error")
			}
			if tc.want == nil && (!r.Passed || r.ToolCalls != 2 || !r.ClientTokenSeen || r.Model != "fixture-model") {
				t.Fatalf("report = %+v, want completed two-call probe", r)
			}
		})
	}
}

// TestProbeCancellation rejects work after its owner cancels.
// TestProbeCancellation 在所属请求取消后拒绝启动工作。
func TestProbeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := Probe(ctx, Config{Executable: os.Args[0]})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled", err)
	}
}

// TestProbeEnvironment strips provider keys and recursive gateway overrides.
// TestProbeEnvironment 移除提供商 Key 及可能形成网关递归的地址覆盖。
func TestProbeEnvironment(t *testing.T) {
	env := probeEnv([]string{"HOME=/home/example", "CODEX_HOME=/existing", "PATH=/bin", "OPENAI_API_KEY=secret", "OPENAI_BASE_URL=http://gateway", "CODEX_API_KEY=secret", "BASH_ENV=/private"})
	joined := strings.Join(env, "|")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "gateway") || strings.Contains(joined, "BASH_ENV") || !strings.Contains(joined, "CODEX_HOME=/existing") {
		t.Fatalf("env = %q, want original login context without overrides", joined)
	}
}
