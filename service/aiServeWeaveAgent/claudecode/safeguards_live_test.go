//go:build darwin || linux || windows

package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

// acceptanceCapture copies a bounded response while forwarding every write and flush immediately.
// acceptanceCapture 有界复制验收响应，不缓冲或延迟向客户端的流式传输。
type acceptanceCapture struct {
	http.ResponseWriter
	output boundedCapture
}

func (w *acceptanceCapture) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *acceptanceCapture) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	_, _ = w.output.Write(data[:n])
	return n, err
}

// TestLiveSafeguardsDirectComparison runs the same bounded tool/context task directly and through Agent.
// TestLiveSafeguardsDirectComparison 用同一受控工具与上下文任务对照直连和 Agent，不作为智力评分。
func TestLiveSafeguardsDirectComparison(t *testing.T) {
	if os.Getenv("AISW_CLAUDE_LIVE_TEST") != "1" {
		t.Skip("set AISW_CLAUDE_LIVE_TEST=1 to use a real Claude subscription")
	}
	program := os.Getenv("AISW_CLAUDE_PATH")
	if program == "" {
		program = "claude"
	}
	accountEnv := probeEnv(os.Environ())
	if err := checkAuth(t.Context(), program, accountEnv); err != nil {
		t.Fatal("live acceptance requires the Agent account's claude.ai login; no token is exported or substituted")
	}
	model := os.Getenv("AISW_CLAUDE_MODEL")
	if model == "" {
		model = "claude-opus-5-5"
	}
	runSafeguardsAcceptance(t, program, model, accountEnv, nil)
}

func runSafeguardsAcceptance(t *testing.T, program, model string, accountEnv []string, gateway *url.URL) {
	t.Helper()
	const chunks = 12
	tokens := make([]string, chunks)
	for i := range tokens {
		tokens[i] = randomID()
	}
	finalToken := randomID()
	routes := []bool{false, true}
	if gateway != nil {
		routes = []bool{true}
	}
	for _, throughAgent := range routes {
		name := "direct"
		if throughAgent {
			name = "through_agent"
			if gateway != nil {
				name = "gateway"
			}
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
			defer cancel()
			var mu sync.Mutex
			next, submitted, guarded, available := 0, false, 0, 0
			statuses := make(map[string]int)
			mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				var request struct {
					ID     json.RawMessage
					Method string
					Params struct {
						Name      string
						Arguments struct {
							Index  int
							Tokens []string
						}
					}
				}
				if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request) != nil {
					http.Error(w, "invalid MCP request", 400)
					return
				}
				if len(request.ID) == 0 {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				var result any
				switch request.Method {
				case "initialize":
					result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "aisw-acceptance", "version": "1"}}
				case "tools/list":
					result = map[string]any{"tools": []any{
						map[string]any{"name": "read_chunk", "description": "Return the next numbered chunk and its token; indices start at zero and must be sequential.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"index": map[string]any{"type": "integer"}}, "required": []string{"index"}}},
						map[string]any{"name": "submit_tokens", "description": "Validate all twelve chunk tokens in their original order after reading every chunk.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"tokens": map[string]any{"type": "array", "items": map[string]string{"type": "string"}}}, "required": []string{"tokens"}}},
					}}
				case "tools/call":
					mu.Lock()
					text, failed := "invalid operation", true
					if request.Params.Name == "read_chunk" && request.Params.Arguments.Index == next && next < chunks {
						text = fmt.Sprintf("chunk %d of %d; token: %s\n%s", next, chunks, tokens[next], strings.Repeat("Context filler for this chunk. ", 512))
						next++
						failed = false
					} else if request.Params.Name == "submit_tokens" && next == chunks && reflect.DeepEqual(request.Params.Arguments.Tokens, tokens) {
						submitted, failed, text = true, false, finalToken
					}
					mu.Unlock()
					result = map[string]any{"isError": failed, "content": []any{map[string]string{"type": "text", "text": text}}}
				default:
					result = map[string]any{}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
			}))
			defer mcp.Close()
			dir := t.TempDir()
			configPath := filepath.Join(dir, "mcp.json")
			config, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"acceptance": map[string]string{"type": "http", "url": mcp.URL}}})
			if err := os.WriteFile(configPath, config, 0600); err != nil {
				t.Fatal(err)
			}
			env := append([]string(nil), accountEnv...)
			if throughAgent {
				var handler http.Handler
				if gateway != nil {
					handler = acceptanceGatewayProxy(gateway)
				} else {
					b := newMessagesBridge(program, model, map[string]principal{"live-caller": {"t", "k"}}, runtime.NewSystemClock())
					b.env = append([]string(nil), accountEnv...)
					defer b.close()
					handler = b
				}
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
					if err != nil {
						http.Error(w, "request limit", 413)
						return
					}
					var hint struct{ Safeguards []json.RawMessage }
					_ = json.Unmarshal(body, &hint)
					mu.Lock()
					if len(hint.Safeguards) > 0 {
						guarded++
					}
					mu.Unlock()
					r.Body = io.NopCloser(bytes.NewReader(body))
					writer := &acceptanceCapture{ResponseWriter: w, output: boundedCapture{limit: 1 << 20}}
					handler.ServeHTTP(writer, r)
					diagnostics := newMessagesRoundDiagnostics(true)
					for _, line := range bytes.Split(writer.output.buffer.Bytes(), []byte("\n")) {
						if bytes.HasPrefix(line, []byte("data: ")) {
							diagnostics.observe(line[6:])
						}
					}
					mu.Lock()
					if len(hint.Safeguards) > 0 {
						statuses[diagnostics.status]++
						if diagnostics.status == "available" {
							available++
						}
					}
					mu.Unlock()
				}))
				defer api.Close()
				env = append(withAccountDir(env, t.TempDir()), "ANTHROPIC_BASE_URL="+api.URL)
				if gateway == nil {
					env = append(env, "ANTHROPIC_API_KEY=live-caller")
				}
			}
			env = append(env, "CLAUDE_CODE_AUTO_MODE_SERVER=1", "DISABLE_TELEMETRY=1", "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1", "ANTHROPIC_DEFAULT_SONNET_MODEL="+model, "ANTHROPIC_DEFAULT_HAIKU_MODEL="+model)
			args := []string{"--restricted", "--print", "--model", model, "--output-format", "stream-json", "--verbose", "--tools", "", "--allowedTools", "mcp__acceptance__read_chunk,mcp__acceptance__submit_tokens", "--permission-mode", "auto", "--strict-mcp-config", "--mcp-config", configPath, "--setting-sources", "", "--settings", `{"disableAllHooks":true,"autoMemoryEnabled":false}`, "--disable-slash-commands", "--no-chrome", "--no-session-persistence", "--system-prompt", "Use only the provided acceptance MCP tools. Complete the user's task. Do not delegate or request other tools.", "Read chunks 0 through 11 sequentially with read_chunk, exactly one at a time. Keep every token from the tool results in order. Then call submit_tokens with the twelve original tokens. Finally output only the text returned by submit_tokens."}
			cmd := exec.CommandContext(ctx, program, args...)
			cmd.Dir, cmd.Env, cmd.Stderr = dir, env, io.Discard
			output := boundedCapture{limit: 2 << 20}
			cmd.Stdout = &output
			cmd.WaitDelay = 2 * time.Second
			if err := cliprocess.Configure(cmd); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			err := cliprocess.Run(cmd)
			finalSuccess := false
			for _, line := range bytes.Split(output.buffer.Bytes(), []byte("\n")) {
				var result struct {
					Type, Subtype, Result string
					IsError               bool `json:"is_error"`
				}
				if json.Unmarshal(line, &result) == nil && result.Type == "result" {
					finalSuccess = result.Subtype == "success" && !result.IsError && strings.TrimSpace(result.Result) == finalToken
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil || !submitted || next != chunks || !finalSuccess {
				t.Fatalf("task failed: cli_ok=%t chunks=%d/%d submitted=%t final_success=%t; raw CLI output withheld", err == nil, next, chunks, submitted, finalSuccess)
			}
			if throughAgent && (guarded < chunks || available < chunks) {
				t.Fatalf("server review not accepted across tool rounds: guarded=%d available=%d", guarded, available)
			}
			t.Logf("model=%s chunks=%d submitted=%t guarded=%d available=%d statuses=%v elapsed=%s", model, next, submitted, guarded, available, statuses, time.Since(started).Round(time.Second))
		})
	}
}
