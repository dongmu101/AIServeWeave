//go:build darwin || linux

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/claudecode"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/messagesession"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"github.com/redis/go-redis/v9"
)

type claudeLiveVerifier struct{}

func (claudeLiveVerifier) Verify(_ context.Context, key string) (httpapi.Identity, error) {
	switch key {
	case "claude-live-first", "claude-live-second":
		return httpapi.Identity{TenantID: key, KeyID: key}, nil
	default:
		return httpapi.Identity{}, httpapi.ErrKeyRejected
	}
}

// TestLiveClaudeClientsThroughTunnel verifies two original clients across Redis and mTLS replicas.
// TestLiveClaudeClientsThroughTunnel 验证两个原版客户端跨 Redis 与 mTLS 副本完成任务。
func TestLiveClaudeClientsThroughTunnel(t *testing.T) {
	if os.Getenv("AISW_CLAUDE_LIVE_TEST") != "1" || os.Getenv("AISW_MESSAGES_REDIS_ADDR") == "" {
		t.Skip("set AISW_CLAUDE_LIVE_TEST=1 and AISW_MESSAGES_REDIS_ADDR to use local subscription and Redis")
	}
	program := os.Getenv("AISW_CLAUDE_PATH")
	if program == "" {
		program = "claude"
	}
	f := newFleetWithBackend(t, 2, runtime.KindClaude, claudecode.NewFactory(program))
	f.awaitReady(f.replicas...)
	table, err := routing.New([]routing.Route{{Model: "claude-sonnet-4-6", Targets: []routing.Target{{RuntimeModel: "sonnet"}}}})
	if err != nil {
		t.Fatal(err)
	}
	var endpoints []*url.URL
	for _, replica := range f.replicas {
		client := redis.NewClient(&redis.Options{Addr: os.Getenv("AISW_MESSAGES_REDIS_ADDR")})
		t.Cleanup(func() { _ = client.Close() })
		if client.Ping(t.Context()).Err() != nil {
			t.Fatal("test Redis unavailable")
		}
		sched := scheduler.New(replica.server, scheduler.Config{})
		sched.SetRoutes(table)
		api := httpapi.New(sched, httpapi.Config{Verifier: claudeLiveVerifier{}, MessagesSessions: messagesession.NewRedis(client)})
		t.Cleanup(func() { api.Close() })
		server := httptest.NewServer(api)
		t.Cleanup(server.Close)
		endpoint, _ := url.Parse(server.URL)
		endpoints = append(endpoints, endpoint)
	}
	var rounds atomic.Uint64
	proxy := httputil.NewSingleHostReverseProxy(endpoints[0])
	proxy.Director = func(r *http.Request) {
		endpoint := endpoints[int(rounds.Add(1)-1)%len(endpoints)]
		r.URL.Scheme, r.URL.Host, r.Host = endpoint.Scheme, endpoint.Host, endpoint.Host
	}
	proxy.FlushInterval = -1
	proxy.ModifyResponse = func(r *http.Response) error {
		t.Logf("Gateway response path=%s status=%d", r.Request.URL.Path, r.StatusCode)
		return nil
	}
	front := httptest.NewServer(proxy)
	defer front.Close()
	var wg sync.WaitGroup
	for _, key := range []string{"claude-live-first", "claude-live-second"} {
		wg.Go(func() {
			t.Run(key, func(t *testing.T) { runClaudeClient(t, program, front.URL, key) })
		})
	}
	wg.Wait()
	if rounds.Load() < 6 {
		t.Errorf("HTTP round count=%d, want at least 6 caller-tool rounds", rounds.Load())
	}
}

func runClaudeClient(t *testing.T, program, baseURL, key string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	dir := t.TempDir()
	token := "caller-" + filepath.Base(dir)
	path := filepath.Join(dir, "caller-marker.txt")
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "client-config")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, program, "--bare", "--print", "--model", "claude-sonnet-4-6", "--output-format", "stream-json", "--verbose", "--tools", "Read,Edit", "--allowedTools", "Read,Edit", "--permission-mode", "acceptEdits", "--setting-sources", "", "--settings", `{"disableAllHooks":true,"autoMemoryEnabled":false}`, "--disable-slash-commands", "--no-chrome", "--no-session-persistence", "--system-prompt", "Use only the provided client tools. Do not delegate or use shell tools.", "Read caller-marker.txt with Read, then use Edit to append a newline followed by verified. Do not change its original contents. Output the original file content after editing.")
	cmd.Dir, cmd.Stderr = dir, io.Discard
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR":
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+config, "ANTHROPIC_BASE_URL="+baseURL, "ANTHROPIC_API_KEY="+key, "MAX_THINKING_TOKENS=0", "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=1", "CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1", "DISABLE_PROMPT_CACHING=1", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=4096", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_SIMPLE=1")
	output := &liveClientOutput{}
	cmd.Stdout, cmd.WaitDelay = output, 2*time.Second
	if err := cmd.Run(); err != nil {
		t.Fatalf("client failed: %T; subprocess output withheld", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != token+"\nverified" || !bytes.Contains(output.Bytes(), []byte(token)) {
		t.Fatalf("file/final token matched=%t/%t, want true/true", string(data) == token+"\nverified", bytes.Contains(output.Bytes(), []byte(token)))
	}
	var success bool
	for _, line := range bytes.Split(output.Bytes(), []byte("\n")) {
		var result struct {
			Type, Subtype string
			IsError       bool `json:"is_error"`
		}
		_ = json.Unmarshal(line, &result)
		if result.Type == "result" {
			success = result.Subtype == "success" && !result.IsError
		}
	}
	if !success {
		t.Fatal("final success=false, want true")
	}
}
