//go:build darwin || linux

package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

func liveMessagesBridge(t *testing.T, model string) *messagesBridge {
	t.Helper()
	if os.Getenv("AISW_CLAUDE_LIVE_TEST") != "1" {
		t.Skip("set AISW_CLAUDE_LIVE_TEST=1 to use the local subscription")
	}
	program := os.Getenv("AISW_CLAUDE_PATH")
	if program == "" {
		program = "claude"
	}
	if err := checkAuth(t.Context(), program, probeEnv(os.Environ())); err != nil {
		t.Fatal(err)
	}
	b := newMessagesBridge(program, model, map[string]principal{"live-first": {"t1", "k1"}, "live-second": {"t2", "k2"}}, runtime.NewSystemClock())
	t.Cleanup(b.close)
	return b
}

// TestLiveMessagesRoundTrip holds a real CLI across two separate HTTP calls.
// TestLiveMessagesRoundTrip 在两个独立 HTTP 请求之间保留真实 CLI。
func TestLiveMessagesRoundTrip(t *testing.T) {
	b := liveMessagesBridge(t, "sonnet")
	server := httptest.NewServer(b)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	post := func(req messagesRequest) *httptest.ResponseRecorder {
		data, _ := json.Marshal(req)
		r, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/messages", bytes.NewReader(data))
		r.Header.Set("X-Api-Key", "live-first")
		r.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatalf("HTTP request failed: %T", err)
		}
		defer response.Body.Close()
		w := httptest.NewRecorder()
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
		return w
	}
	req := testMessagesRequest()
	req.Messages[0].Content = json.RawMessage(`"Call read_file once with path caller-only.txt. After getting its result, output that result verbatim. Do not use other tools."`)
	req.Tools[0].Description = "Return a random token held only by the remote caller."
	token := randomID()
	req = continuation(t, req, post(req), token)
	w := post(req)
	var shape struct {
		Type       string
		StopReason string `json:"stop_reason"`
		Content    []messageBlock
	}
	_ = json.Unmarshal(w.Body.Bytes(), &shape)
	t.Logf("Final response shape: type=%s stop=%s blocks=%d bytes=%d", shape.Type, shape.StopReason, len(shape.Content), w.Body.Len())
	if w.Code != 200 || !strings.Contains(w.Body.String(), token) {
		t.Fatalf("HTTP status/token = %d/%t, want 200/true", w.Code, strings.Contains(w.Body.String(), token))
	}
}

// TestLiveMessagesOriginalClient reads and edits a file using an unmodified client.
// TestLiveMessagesOriginalClient 使用未修改的原版客户端读取并修改文件。
func TestLiveMessagesOriginalClient(t *testing.T) {
	b := liveMessagesBridge(t, "claude-sonnet-4-6")
	server := httptest.NewServer(b)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	token := randomID()
	path := filepath.Join(dir, "caller-marker.txt")
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "client-config")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--bare", "--print", "--model", "claude-sonnet-4-6", "--output-format", "stream-json", "--verbose", "--tools", "Read,Edit", "--allowedTools", "Read,Edit", "--permission-mode", "acceptEdits", "--setting-sources", "", "--settings", `{"disableAllHooks":true,"autoMemoryEnabled":false}`, "--disable-slash-commands", "--no-chrome", "--no-session-persistence", "--system-prompt", "Use only the provided client tools. Do not delegate or use shell tools.", "Read caller-marker.txt with Read, then use Edit to append a newline followed by verified. Do not change its original contents. Output the original file content after editing."}
	cmd := exec.CommandContext(ctx, b.program, args...)
	cmd.Dir, cmd.Stderr = dir, io.Discard
	for _, entry := range probeEnv(os.Environ()) {
		if !strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+config, "ANTHROPIC_BASE_URL="+server.URL, "ANTHROPIC_API_KEY=live-first", "MAX_THINKING_TOKENS=0", "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=1", "CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1", "DISABLE_PROMPT_CACHING=1", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=4096", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "CLAUDE_CODE_SIMPLE=1")
	output := boundedCapture{limit: maxRequestBytes}
	cmd.Stdout = &output
	if err := configureProcess(cmd); err != nil {
		t.Fatal(err)
	}
	cmd.WaitDelay = 2 * time.Second
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Cancel()
		}
	}()
	err := cmd.Run()
	data, readErr := os.ReadFile(path)
	if err != nil {
		t.Fatalf("original client failed: %T; output withheld", err)
	}
	if readErr != nil || string(data) != token+"\nverified" || !bytes.Contains(output.buffer.Bytes(), []byte(token)) {
		t.Fatalf("client file/final result = %t/%t, want true/true", string(data) == token+"\nverified", bytes.Contains(output.buffer.Bytes(), []byte(token)))
	}
	var finalSuccess bool
	for _, line := range bytes.Split(output.buffer.Bytes(), []byte("\n")) {
		var result struct {
			Type, Subtype string
			IsError       bool `json:"is_error"`
		}
		_ = json.Unmarshal(line, &result)
		if result.Type == "result" {
			finalSuccess = result.Subtype == "success" && !result.IsError
		}
	}
	if !finalSuccess {
		t.Fatal("original client final success = false, want true")
	}
}
