//go:build darwin || linux

package codexcli

import (
	"encoding/json"
	"os"
)

func runtimeFixture() {
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	send := func(v any) {
		if enc.Encode(v) != nil {
			os.Exit(2)
		}
	}
	model := "fixture-model"
	var items []struct {
		Type   string `json:"type"`
		Output string `json:"output"`
	}
	for {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if dec.Decode(&msg) != nil {
			return
		}
		switch msg.Method {
		case "initialized":
			continue
		case "account/read":
			send(map[string]any{"id": msg.ID, "result": map[string]any{"account": map[string]string{"type": "chatgpt"}}})
		case "model/list":
			send(map[string]any{"id": msg.ID, "result": map[string]any{"data": []any{map[string]string{"model": "fixture-model"}}, "nextCursor": nil}})
		case "thread/start":
			var params struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			if params.Model != "" {
				model = params.Model
			}
			send(map[string]any{"id": msg.ID, "result": map[string]any{"thread": map[string]string{"id": "thread1"}, "model": model, "instructionSources": []string{}}})
		case "thread/inject_items":
			var params struct {
				Items json.RawMessage `json:"items"`
			}
			_ = json.Unmarshal(msg.Params, &params)
			_ = json.Unmarshal(params.Items, &items)
			send(map[string]any{"id": msg.ID, "result": map[string]any{}})
		case "turn/start":
			send(map[string]any{"id": msg.ID, "result": map[string]any{"turn": map[string]string{"id": "turn1"}}})
			result := "Hello world"
			for _, item := range items {
				if item.Type == "function_call_output" {
					result = item.Output
				}
			}
			if model == "tool-model" && result == "Hello world" {
				send(map[string]any{"id": "request1", "method": "item/tool/call", "params": map[string]any{"threadId": "thread1", "turnId": "turn1", "callId": "call1", "tool": "echo", "arguments": map[string]any{}}})
				continue
			}
			for _, text := range []string{result[:len(result)/2], result[len(result)/2:]} {
				send(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "thread1", "turnId": "turn1", "itemId": "message1", "delta": text}})
			}
			send(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "thread1", "turnId": "turn1", "tokenUsage": map[string]any{"last": map[string]int{"inputTokens": 5, "outputTokens": 3, "totalTokens": 8}}}})
			send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread1", "turn": map[string]string{"id": "turn1", "status": "completed"}}})
		default:
			send(map[string]any{"id": msg.ID, "result": map[string]any{}})
		}
	}
}
