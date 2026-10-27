package geminitest

import (
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/geminicli"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// Fixture constructs a real adapter against an isolated offline extension.
// Fixture 使用隔离的离线扩展创建真实适配器。
func Fixture(t *testing.T, mode string, clock runtime.Clock) (*geminicli.Runtime, string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini", "extensions", "nanobanana", "mcp-server", "dist")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"nanobanana","mcpServers":{"nanobanana":{"command":"node","args":["${extensionPath}/mcp-server/dist/index.js"],"env":{}}}}`
	if err := os.WriteFile(filepath.Join(dir, "..", "..", "gemini-extension.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(mode), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_CLI_HOME", home)
	t.Setenv("NANOBANANA_API_KEY", "fixture-key")
	if mode == "missing key" {
		for _, name := range []string{"NANOBANANA_API_KEY", "NANOBANANA_GEMINI_API_KEY", "NANOBANANA_GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
			t.Setenv(name, "")
		}
	}
	t.Setenv("NODE_OPTIONS", "must-not-reach-child")
	t.Setenv("UNRELATED_API_KEY", "must-not-reach-child")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	rt, err := geminicli.NewFactory(exe)(runtime.Config{ID: "gemini-test", Kind: runtime.Kind("gemini-cli"), MaxConcurrent: 1}, runtime.Dependencies{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*geminicli.Runtime)
	t.Cleanup(func() { _ = r.Close() })
	return r, dir
}

// WantCode checks the public error category and secret redaction.
// WantCode 检查公开错误类别与秘密脱敏。
func WantCode(t *testing.T, err error, want runtime.ErrorCode) {
	t.Helper()
	var e *runtime.RuntimeError
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("error=%v, want code=%s", err, want)
	}
	for _, secret := range []string{"SECRET_PROMPT", "fixture-key", "/operator/private"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposes %q, want fixed category", secret)
		}
	}
}

// AwaitReady waits for the subprocess to receive its generation request.
// AwaitReady 等待子进程收到生图请求。
func AwaitReady(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("process did not reach tools/call, want ready")
		}
		goruntime.Gosched()
	}
}
