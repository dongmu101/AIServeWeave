//go:build darwin || linux

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/codexcli"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// TestLiveCodexClientThroughTunnel runs an original Codex client against the Gateway.
// TestLiveCodexClientThroughTunnel 使用原版 Codex 客户端访问 Gateway。
func TestLiveCodexClientThroughTunnel(t *testing.T) {
	if os.Getenv("AISW_CODEX_LIVE") != "1" {
		t.Skip("set AISW_CODEX_LIVE=1 to use local account quota")
	}
	f := newFleetWithBackend(t, 1, runtime.KindCodex, codexcli.New)
	f.awaitReady(f.replicas...)
	api := httpapi.New(scheduler.New(f.replicas[0].server, scheduler.Config{}), httpapi.Config{APIKeys: []string{"live-fixture-key"}})
	defer api.Close()
	server := httptest.NewServer(api)
	defer server.Close()
	models := f.manager.Snapshot()[0].Discovery.Models
	if len(models) == 0 {
		t.Fatal("no model advertised by local Codex")
	}
	dir := t.TempDir()
	var nonce [16]byte
	rand.Read(nonce[:])
	token := hex.EncodeToString(nonce[:])
	if err := os.WriteFile(filepath.Join(dir, "caller-marker.txt"), []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	args := []string{"exec", "--json", "--ignore-user-config", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "-C", dir, "-m", models[0].ID}
	args = append(args, "--enable", "shell_tool", "--enable", "unified_exec")
	for _, feature := range []string{"multi_agent", "apps", "plugins", "hooks", "code_mode"} {
		args = append(args, "--disable", feature)
	}
	for _, setting := range []string{
		`model_provider="aisw_test"`, `model_providers.aisw_test.name="AIServeWeave test"`,
		`model_providers.aisw_test.base_url=` + strconv.Quote(server.URL+"/v1"),
		`model_providers.aisw_test.env_key="AISW_TEST_KEY"`, `model_providers.aisw_test.wire_api="responses"`,
		`mcp_servers={}`, `web_search="disabled"`, `history.persistence="none"`, `project_doc_max_bytes=0`,
		`log_dir=` + strconv.Quote(dir), `sqlite_home=` + strconv.Quote(dir),
	} {
		args = append(args, "-c", setting)
	}
	args = append(args, "Use your command tool to read caller-marker.txt in the current directory. Output only the file contents. Do not guess the contents and do not modify any file.")
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Dir = dir
	allowedEnv := map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "LANG": true, "LC_ALL": true, "TMPDIR": true, "CODEX_HOME": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true}
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowedEnv[key] {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "AISW_TEST_KEY=live-fixture-key")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Cancel()
		}
	}()
	var output liveClientOutput
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("original Codex client failed: %T (details withheld)", err)
	}
	var final string
	for _, line := range bytes.Split(output.Bytes(), []byte("\n")) {
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "item.completed" && event.Item.Type == "agent_message" {
			final = event.Item.Text
		}
	}
	if !strings.Contains(final, token) {
		t.Fatal("original client final answer did not consume its local command result")
	}
	t.Logf("original Codex client tool loop passed through HTTP and mTLS; model=%s", models[0].ID)
}

// liveClientOutput bounds subprocess JSON output without printing its contents.
// liveClientOutput 限制子进程 JSON 输出大小，不打印其内容。
type liveClientOutput struct{ bytes.Buffer }

func (b *liveClientOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}
