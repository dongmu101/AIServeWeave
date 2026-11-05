//go:build darwin || linux || windows

package claudecode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestNativeCLIOriginalClientContract exercises both real CLI processes without subscription inference.
// TestNativeCLIOriginalClientContract 使用两个真实 CLI 验证原版客户端长工具链，不消耗订阅推理额度。
func TestNativeCLIOriginalClientContract(t *testing.T) {
	program := installedContractCLI(t)
	chunkToken := regexp.MustCompile(`chunk ([0-9]+) of 12; token: ([0-9a-f]{32})`)
	finalToken := regexp.MustCompile(`[0-9a-f]{32}`)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		if err != nil {
			http.Error(w, "request limit", 413)
			return
		}
		var request struct {
			Messages []struct {
				Content json.RawMessage
			}
			Tools []struct{ Name string }
		}
		if json.Unmarshal(data, &request) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		var readTool, submitTool, lastTool string
		var lastResult json.RawMessage
		for _, tool := range request.Tools {
			if strings.HasSuffix(tool.Name, "__read_chunk") {
				readTool = tool.Name
			}
			if strings.HasSuffix(tool.Name, "__submit_tokens") {
				submitTool = tool.Name
			}
		}
		for _, message := range request.Messages {
			var blocks []struct {
				Type, Name string
				Content    json.RawMessage
			}
			_ = json.Unmarshal(message.Content, &blocks)
			for _, block := range blocks {
				if block.Type == "tool_use" {
					lastTool = block.Name
				}
				if block.Type == "tool_result" {
					lastResult = block.Content
				}
			}
		}
		if readTool == "" || submitTool == "" {
			http.Error(w, "acceptance tools missing", 400)
			return
		}
		tokens := make(map[int]string)
		for _, match := range chunkToken.FindAllSubmatch(data, -1) {
			index, _ := strconv.Atoi(string(match[1]))
			tokens[index] = string(match[2])
		}
		tool, id, text := readTool, fmt.Sprintf("call_chunk_%d", len(tokens)), ""
		input := map[string]any{"index": len(tokens)}
		if len(tokens) == 12 {
			if lastTool == submitTool {
				text = string(finalToken.Find(lastResult))
				if text == "" {
					http.Error(w, "acceptance submission failed", 400)
					return
				}
				tool, id = "", ""
			} else {
				values := make([]string, 12)
				for i := range values {
					values[i] = tokens[i]
				}
				tool, id, input = submitTool, "call_submit", map[string]any{"tokens": values}
			}
		}
		var wire bytes.Buffer
		fixtureRound(&wire, id, text)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range bytes.Split(wire.Bytes(), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var envelope struct{ Event map[string]any }
			_ = json.Unmarshal(line, &envelope)
			event := envelope.Event
			switch event["type"] {
			case "content_block_start":
				if tool != "" {
					event["content_block"].(map[string]any)["name"] = tool
				}
			case "content_block_delta":
				if tool != "" {
					args, _ := json.Marshal(input)
					event["delta"].(map[string]any)["partial_json"] = string(args)
				}
			case "message_delta":
				calls := map[string]any{}
				if tool != "" {
					calls[id] = map[string]string{"type": "evaluated", "outcome": "not_flagged"}
				}
				event["delta"].(map[string]any)["safeguard_results"] = []any{map[string]any{"type": "dangerous_tool_use", "status": map[string]any{"type": "available", "tool_uses": calls}}}
			}
			encoded, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], encoded)
		}
	}))
	defer api.Close()
	env := append(withAccountDir(probeEnv(os.Environ()), t.TempDir()), "ANTHROPIC_API_KEY=loopback-fixture-only", "ANTHROPIC_BASE_URL="+api.URL)
	runSafeguardsAcceptance(t, program, "claude-opus-5-5", env, nil)
	target, _ := url.Parse(api.URL + "/v1")
	runSafeguardsAcceptance(t, program, "claude-opus-5-5", env, target)
}
