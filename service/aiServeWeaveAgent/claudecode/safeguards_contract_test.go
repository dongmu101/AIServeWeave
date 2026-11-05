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
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

type nativeContractRequest struct {
	document map[string]json.RawMessage
	header   http.Header
}

func installedContractCLI(t *testing.T) string {
	t.Helper()
	if os.Getenv("AISW_CLAUDE_CONTRACT_TEST") != "1" {
		t.Skip("set AISW_CLAUDE_CONTRACT_TEST=1 to test the installed CLI against a loopback API")
	}
	program := os.Getenv("AISW_CLAUDE_PATH")
	if program == "" {
		program = "claude"
	}
	if _, err := exec.LookPath(program); err != nil {
		t.Fatal("Claude CLI is unavailable")
	}
	return program
}

// writeNativeContractRound simulates upstream SSE, not a local CLI process.
// writeNativeContractRound 模拟上游 SSE，CLI 本身使用未修改的真实程序。
func writeNativeContractRound(w http.ResponseWriter, round, rounds int) {
	w.Header().Set("Content-Type", "text/event-stream")
	emit := func(event any) {
		data, _ := json.Marshal(event)
		var envelope struct{ Type string }
		_ = json.Unmarshal(data, &envelope)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", envelope.Type, data)
	}
	emit(map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_contract", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "content": []any{}, "usage": map[string]int{"input_tokens": 3, "output_tokens": 0}}})
	stop := "end_turn"
	calls := map[string]any{}
	if round+1 < rounds {
		id := fmt.Sprintf("native-call-%d", round)
		stop = "tool_use"
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": id, "name": "read_file", "input": map[string]any{}, "caller": map[string]string{"type": "direct"}}})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": fmt.Sprintf(`{"path":"round-%d.txt"}`, round)}})
		calls[id] = map[string]string{"type": "evaluated", "outcome": "not_flagged"}
	} else {
		emit(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
		emit(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "contract-complete"}})
	}
	emit(map[string]any{"type": "content_block_stop", "index": 0})
	emit(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "safeguard_results": []any{map[string]any{"type": "dangerous_tool_use", "status": map[string]any{"type": "available", "tool_uses": calls}}}}, "context_management": map[string]any{"applied_edits": []any{}}, "usage": map[string]int{"output_tokens": 7}})
	emit(map[string]string{"type": "message_stop"})
}

// TestNativeCLISafeguardsContract compares the actual HTTP document sent by the installed CLI.
// TestNativeCLISafeguardsContract 比对真实 CLI 发出的 HTTP 文档，覆盖大历史、工具续接及客户端压缩。
func TestNativeCLISafeguardsContract(t *testing.T) {
	program := installedContractCLI(t)
	const rounds = 8
	captured := make(chan nativeContractRequest, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
			return
		}
		var document map[string]json.RawMessage
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&document) != nil {
			http.Error(w, "invalid document", 400)
			return
		}
		var guards []struct {
			Context struct{ Round int } `json:"classifier_context"`
		}
		if json.Unmarshal(document["safeguards"], &guards) != nil || len(guards) != 1 {
			http.Error(w, "missing safeguards", 400)
			return
		}
		select {
		case captured <- nativeContractRequest{document: document, header: r.Header.Clone()}:
		case <-r.Context().Done():
			return
		}
		writeNativeContractRound(w, guards[0].Context.Round, rounds)
	}))
	defer api.Close()
	account := t.TempDir()
	if err := os.WriteFile(filepath.Join(account, "CLAUDE.md"), []byte("AGENT_LOCAL_INSTRUCTIONS_MUST_NOT_LEAK"), 0600); err != nil {
		t.Fatal(err)
	}
	b := newMessagesBridge(program, "claude-opus-5-5", map[string]principal{"fixture": {"t", "k"}}, runtime.NewSystemClock())
	defer b.close()
	b.env = append(withAccountDir(probeEnv(os.Environ()), account), "ANTHROPIC_API_KEY=loopback-fixture-only", "ANTHROPIC_BASE_URL="+api.URL, "DISABLE_TELEMETRY=1", "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1")
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	t.Logf("CLI version: %s", cliprocess.Version(ctx, program, b.env))
	document := map[string]any{
		"model": b.model, "max_tokens": 4096, "stream": false,
		"system":   []any{map[string]any{"type": "text", "text": "CALLER_SYSTEM_MARKER", "cache_control": map[string]string{"type": "ephemeral"}}},
		"tools":    []any{map[string]any{"name": "read_file", "description": "Caller tool only", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]string{"type": "string"}}}, "defer_loading": false}},
		"thinking": map[string]string{"type": "adaptive"}, "output_config": map[string]string{"effort": "high"},
		"context_management": map[string]any{"edits": []any{}}, "metadata": map[string]string{"user_id": "caller-metadata"},
	}
	messages := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": strings.Repeat("large-context ", 28<<10), "cache_control": map[string]string{"type": "ephemeral"}}, pdfTestBlock()}},
		map[string]any{"role": "system", "content": "CALLER_ENVIRONMENT_MARKER"},
		map[string]any{"role": "assistant", "content": []any{map[string]string{"type": "thinking", "thinking": "CALLER_THINKING_MARKER", "signature": "caller-signature"}, map[string]any{"type": "tool_use", "id": "prior-native-id", "name": "read_file", "input": map[string]string{"path": "prior.txt"}}}},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "prior-native-id", "content": []any{pdfTestBlock(), map[string]string{"type": "text", "text": "CALLER_TOOL_RESULT_MARKER"}}}}},
	}
	for round := 0; round < rounds; round++ {
		if round == rounds/2 {
			// The client supplies a new compacted transcript; no backend session may restore old turns.
			// 客户端主动替换压缩后的历史，后端不得恢复旧会话中的回合。
			messages = []any{map[string]string{"role": "user", "content": "CALLER_COMPACTED_SUMMARY_MARKER"}}
		}
		document["messages"] = messages
		document["safeguards"] = []any{map[string]any{"type": "dangerous_tool_use", "classifier_context": map[string]any{"v": 1, "round": round, "opaque": "CALLER_POLICY_MARKER"}}}
		data, _ := json.Marshal(document)
		request := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(data)).WithContext(ctx)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Api-Key", "fixture")
		request.Header.Set("Anthropic-Version", "2023-06-01")
		request.Header.Set("Anthropic-Beta", "dangerous-tool-use-2026-09-03,future-feature-2099-01-01")
		response := httptest.NewRecorder()
		b.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("round %d: status %d; CLI output withheld", round, response.Code)
		}
		var actual nativeContractRequest
		select {
		case actual = <-captured:
		default:
			t.Fatalf("round %d: CLI did not call the loopback API", round)
		}
		var expected map[string]json.RawMessage
		_ = json.Unmarshal(data, &expected)
		expected["stream"] = json.RawMessage(`true`)
		for key, value := range expected {
			if canonical(value) != canonical(actual.document[key]) {
				t.Fatalf("round %d: CLI changed %s", round, key)
			}
		}
		encoded, _ := json.Marshal(actual.document)
		if bytes.Contains(encoded, []byte("AGENT_LOCAL_INSTRUCTIONS_MUST_NOT_LEAK")) || bytes.Contains(encoded, []byte("Continue the supplied native Messages request.")) {
			t.Fatal("local instructions or compatibility input reached the native document")
		}
		if actual.header.Get("Anthropic-Version") != "2023-06-01" || !slices.Contains(strings.Split(actual.header.Get("Anthropic-Beta"), ","), "future-feature-2099-01-01") {
			t.Fatal("CLI changed native protocol selectors")
		}
		var result struct {
			Content json.RawMessage
			Stop    string          `json:"stop_reason"`
			Results json.RawMessage `json:"safeguard_results"`
			Context json.RawMessage `json:"context_management"`
		}
		if json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Results) == 0 || len(result.Context) == 0 {
			t.Fatalf("round %d: lost response extensions", round)
		}
		if round+1 == rounds {
			if result.Stop != "end_turn" || !bytes.Contains(result.Content, []byte("contract-complete")) {
				t.Fatal("native conversation did not complete")
			}
			break
		}
		id := fmt.Sprintf("native-call-%d", round)
		if result.Stop != "tool_use" || !bytes.Contains(result.Content, []byte(id)) {
			t.Fatalf("round %d: tool identity lost", round)
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": result.Content}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": fmt.Sprintf("caller-result-%d", round)}}})
	}
	t.Logf("verified %d native rounds, large history, caller compaction, tool IDs, thinking, caching and protocol selectors", rounds)
}
