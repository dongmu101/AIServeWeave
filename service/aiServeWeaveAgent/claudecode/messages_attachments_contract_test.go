package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

// TestNativeCLIDocumentContract uses the installed CLI with a loopback fake API.
// TestNativeCLIDocumentContract 使用已安装 CLI 与回环模拟 API，不使用真实账户或付费推理。
func TestNativeCLIDocumentContract(t *testing.T) {
	if os.Getenv("AISW_CLAUDE_CONTRACT_TEST") != "1" {
		t.Skip("set AISW_CLAUDE_CONTRACT_TEST=1 for the installed CLI transport contract")
	}
	program := os.Getenv("AISW_CLAUDE_PATH")
	if program == "" {
		program = "claude"
	}
	if _, err := exec.LookPath(program); err != nil {
		t.Fatal("Claude CLI is unavailable")
	}
	seen := make(chan bool, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/messages") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
			return
		}
		var body struct {
			Messages []bridgeMessage
			Stream   bool
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		found := false
		for _, message := range body.Messages {
			list, _ := blocks(message.Content)
			for _, block := range list {
				found = found || (block.Type == "document" && block.Source != nil && block.Source.Data == pdfTestBlock().Source.Data)
			}
		}
		select {
		case seen <- found:
		default:
		}
		if !body.Stream {
			_, _ = io.WriteString(w, `{"id":"msg_contract","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"contract-ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var fixture bytes.Buffer
		fixtureRound(&fixture, "", "contract-ok")
		for _, line := range bytes.Split(fixture.Bytes(), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var envelope struct{ Event json.RawMessage }
			_ = json.Unmarshal(line, &envelope)
			var event struct{ Type string }
			_ = json.Unmarshal(envelope.Event, &event)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, envelope.Event)
		}
	}))
	defer api.Close()
	b := newMessagesBridge(program, "claude-opus-5-5", map[string]principal{"fixture": {"t", "k"}}, runtime.NewSystemClock())
	defer b.close()
	b.env = append(withAccountDir(probeEnv(os.Environ()), t.TempDir()), "ANTHROPIC_API_KEY=loopback-fixture-only", "ANTHROPIC_BASE_URL="+api.URL, "DISABLE_TELEMETRY=1", "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1")
	content, _ := json.Marshal([]messageBlock{{Type: "text", Text: "Read this document and reply contract-ok."}, pdfTestBlock()})
	req := messagesRequest{Model: b.model, MaxTokens: 64, Messages: []bridgeMessage{{Role: "user", Content: content}}}
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	request := httptest.NewRequest("POST", "http://127.0.0.1/v1/messages", bytes.NewReader(body)).WithContext(ctx)
	request.Header.Set("X-Api-Key", "fixture")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	b.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "contract-ok") {
		t.Fatalf("native CLI contract status=%d, expected model text=%t", response.Code, strings.Contains(response.Body.String(), "contract-ok"))
	}
	select {
	case found := <-seen:
		if !found {
			t.Fatal("native CLI did not forward PDF bytes")
		}
	default:
		t.Fatal("native CLI never called the loopback API")
	}
}
