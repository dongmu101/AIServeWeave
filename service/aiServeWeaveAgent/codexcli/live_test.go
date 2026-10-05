//go:build darwin || linux

package codexcli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

// TestLiveCallerToolRoundTrip uses the existing local ChatGPT login only when opted in.
// TestLiveCallerToolRoundTrip 仅在显式启用时使用本机现有 ChatGPT 登录。
func TestLiveCallerToolRoundTrip(t *testing.T) {
	if os.Getenv("AISW_CODEX_LIVE") != "1" {
		t.Skip("set AISW_CODEX_LIVE=1 to use local account quota")
	}
	rt, err := New(runtime.Config{ID: "codex-live", Kind: runtime.KindCodex}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	var nonce [16]byte
	rand.Read(nonce[:])
	token := hex.EncodeToString(nonce[:])
	req := runtime.ChatRequest{Messages: []runtime.ChatMessage{{Role: "user", Content: "Call aisw_client_echo once with label test. Then return its result exactly. Do not use any other tool."}}, Tools: []runtime.Tool{{Type: "function", Function: runtime.FunctionDefinition{Name: "aisw_client_echo", Description: "Obtain a result from the remote caller.", Parameters: json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}},"required":["label"],"additionalProperties":false}`)}}}}
	first, err := rt.(runtime.InferenceRuntime).Chat(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.FinishReason != "tool_calls" || len(first.Message.ToolCalls) != 1 || first.Message.ToolCalls[0].Function.Name != "aisw_client_echo" {
		t.Fatal("first response did not contain the expected caller tool")
	}
	req.Messages = append(req.Messages, first.Message, runtime.ChatMessage{Role: "tool", ToolCallID: first.Message.ToolCalls[0].ID, Content: token})
	second, err := rt.(runtime.InferenceRuntime).Chat(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.FinishReason != "stop" || !strings.Contains(second.Message.Content, token) {
		t.Fatal("continuation did not consume the caller's random result")
	}
	t.Logf("caller tool round trip passed; model=%s", second.Model)
}
