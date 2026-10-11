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

// TestLiveClaudeChatThroughTunnel verifies text, SSE, history and cross-replica caller tools.
// TestLiveClaudeChatThroughTunnel 验证真实文本、SSE、历史上下文与跨副本调用方工具。
func TestLiveClaudeChatThroughTunnel(t *testing.T) {
	if os.Getenv("AISW_CLAUDE_LIVE_TEST") != "1" || os.Getenv("AISW_MESSAGES_REDIS_ADDR") == "" {
		t.Skip("set AISW_CLAUDE_LIVE_TEST=1 and AISW_MESSAGES_REDIS_ADDR to use local subscription and Redis")
	}
	program := os.Getenv("AISW_CLAUDE_PATH")
	if program == "" {
		program = "claude"
	}
	model := os.Getenv("AISW_CLAUDE_MODEL")
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	f := newFleetWithBackend(t, 2, runtime.KindClaude, claudecode.NewFactory(program))
	f.awaitReady(f.replicas...)
	var urls []string
	for _, replica := range f.replicas {
		client := redis.NewClient(&redis.Options{Addr: os.Getenv("AISW_MESSAGES_REDIS_ADDR")})
		t.Cleanup(func() { _ = client.Close() })
		api := httpapi.New(scheduler.New(replica.server, scheduler.Config{}), httpapi.Config{Verifier: claudeLiveVerifier{}, MessagesSessions: messagesession.NewRedis(client)})
		t.Cleanup(api.Close)
		server := httptest.NewServer(api)
		t.Cleanup(server.Close)
		urls = append(urls, server.URL)
	}
	for _, tc := range []struct {
		name   string
		stream bool
	}{{"JSON", false}, {"SSE", true}} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []claudeChatMessage{{Role: "user", Content: "Reply exactly OK."}}
			message, finish := postClaudeChat(t, urls[0], "claude-live-first", model, messages, nil, tc.stream, 200)
			if strings.TrimSpace(message.Content) != "OK" || finish != "stop" {
				t.Fatalf("text=%q finish=%q, want OK and stop", message.Content, finish)
			}
			messages = []claudeChatMessage{{Role: "user", Content: "Remember the code AISW-5729."}, {Role: "assistant", Content: "I will remember it."}, {Role: "user", Content: "Reply with only the code from our earlier conversation."}}
			message, finish = postClaudeChat(t, urls[1], "claude-live-first", model, messages, nil, tc.stream, 200)
			if !strings.Contains(message.Content, "AISW-5729") || finish != "stop" {
				t.Fatalf("history text=%q finish=%q, want AISW-5729 and stop", message.Content, finish)
			}
			runClaudeChatTools(t, urls, model, tc.stream, false)
		})
	}
}

// TestLiveClaudeModelCatalogueThroughTunnel checks CLI display metadata without inference.
// TestLiveClaudeModelCatalogueThroughTunnel 不执行推理，验证真实 CLI 目录元数据经过隧道。
func TestLiveClaudeModelCatalogueThroughTunnel(t *testing.T) {
	if os.Getenv("AISW_CLAUDE_LIVE_TEST") != "1" {
		t.Skip("set AISW_CLAUDE_LIVE_TEST=1 to query the local CLI catalogue without inference")
	}
	program := os.Getenv("AISW_CLAUDE_PATH")
	if program == "" {
		program = "claude"
	}
	f := newFleetWithBackend(t, 1, runtime.KindClaude, claudecode.NewFactory(program))
	f.awaitReady(f.replicas...)
	api := httpapi.New(scheduler.New(f.replicas[0].server, scheduler.Config{}), httpapi.Config{Verifier: claudeLiveVerifier{}})
	defer api.Close()
	server := httptest.NewServer(api)
	defer server.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer claude-live-first")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var catalog struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	want := f.manager.Snapshot()[0].Discovery.Models
	if resp.StatusCode != http.StatusOK || len(want) == 0 || len(catalog.Data) != len(want) {
		t.Fatalf("status=%d models=%d, want 200 and %d nonempty models", resp.StatusCode, len(catalog.Data), len(want))
	}
	for i, model := range catalog.Data {
		name := want[i].DisplayName
		if name == "" {
			name = want[i].ID
		}
		if model.ID != want[i].ID || model.Name != name || model.DisplayName != name {
			t.Fatalf("position=%d model=%+v, want ID=%q name=%q", i, model, want[i].ID, name)
		}
		t.Logf("position=%d id=%s display_name=%s", i+1, model.ID, model.DisplayName)
	}
}

func (claudeLiveVerifier) Verify(_ context.Context, key string) (httpapi.Identity, error) {
	switch key {
	case "claude-live-first", "claude-live-second":
		return httpapi.Identity{TenantID: key, KeyID: key}, nil
	default:
		return httpapi.Identity{}, httpapi.ErrKeyRejected
	}
}

// TestLiveClaudeClientsThroughTunnel verifies /v1 and legacy root base URLs across Redis and mTLS replicas.
// TestLiveClaudeClientsThroughTunnel 验证 /v1 和旧根地址的原版客户端跨 Redis 与 mTLS 副本完成任务。
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
	table, err := routing.New([]routing.Route{{Model: "claude-sonnet-4-6", Targets: []routing.Target{{RuntimeModel: "claude-sonnet-4-6"}}}})
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
	for _, tc := range []struct{ key, basePath string }{{"claude-live-first", "/v1"}, {"claude-live-second", ""}} {
		wg.Go(func() {
			t.Run(tc.key, func(t *testing.T) { runClaudeClient(t, program, front.URL+tc.basePath, tc.key) })
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
