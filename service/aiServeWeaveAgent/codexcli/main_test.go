//go:build darwin || linux

package codexcli

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestMain supplies an offline app-server fixture and checks goroutine cleanup.
// TestMain 提供离线 app-server 测试桩并统一检查协程回收。
func TestMain(m *testing.M) {
	if mode := os.Getenv("AISW_CODEX_FIXTURE"); mode != "" {
		fakeServer(mode)
		os.Exit(0)
	}
	before := runtime.NumGoroutine()
	code := m.Run()
	deadline := time.Now().Add(2 * time.Second)
	for code == 0 && runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "goroutines remain, want initial count")
			code = 1
			break
		}
		runtime.Gosched()
	}
	os.Exit(code)
}

func fakeServer(mode string) {
	if mode == "tree-child" {
		waitTreeFixture(false)
		return
	}
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	send := func(v any) {
		if enc.Encode(v) != nil {
			os.Exit(2)
		}
	}
	call := func(id, label string) {
		thread, turn, tool := "thread-test", "turn-test", "aisw_probe_echo"
		if mode == "cross-thread" {
			thread = "other"
		}
		if mode == "cross-turn" {
			turn = "other"
		}
		if mode == "unknown-tool" {
			tool = "shell"
		}
		send(map[string]any{"id": id, "method": "item/tool/call", "params": map[string]any{"threadId": thread, "turnId": turn, "callId": id, "tool": tool, "arguments": map[string]string{"label": label}}})
	}
	for {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
		}
		if dec.Decode(&msg) != nil {
			return
		}
		if msg.Method == "initialized" {
			continue
		}
		switch msg.Method {
		case "initialize":
			send(map[string]any{"id": msg.ID, "result": map[string]any{"userAgent": "fixture"}})
		case "account/read":
			var account any = map[string]string{"type": "chatgpt"}
			if mode == "auth" {
				account = nil
			}
			send(map[string]any{"id": msg.ID, "result": map[string]any{"account": account}})
		case "thread/start":
			if mode == "rpc-error" {
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32000, "message": "SECRET"}})
				continue
			}
			sources := []string{}
			if mode == "instructions" {
				sources = []string{"/sensitive/instructions"}
			}
			if mode == "global-instructions" {
				sources = []string{"/operator/codex/AGENTS.md"}
			}
			send(map[string]any{"id": msg.ID, "result": map[string]any{"thread": map[string]string{"id": "thread-test"}, "model": "fixture-model", "instructionSources": sources}})
		case "turn/start":
			send(map[string]any{"id": msg.ID, "result": map[string]any{"turn": map[string]string{"id": "turn-test"}}})
			switch mode {
			case "eof":
				return
			case "wait":
				waitTreeFixture(true)
				return
			case "malformed":
				fmt.Println("SECRET")
				return
			case "oversized":
				fmt.Println(string(make([]byte, (1<<20)+1)))
				return
			case "native":
				send(map[string]any{"method": "item/started", "params": map[string]any{"item": map[string]string{"type": "commandExecution"}}})
				continue
			case "collab", "unknown-item":
				kind := "collabAgentToolCall"
				if mode == "unknown-item" {
					kind = "futureTool"
				}
				send(map[string]any{"method": "item/started", "params": map[string]any{"item": map[string]string{"type": kind}}})
				return
			case "approval":
				send(map[string]any{"id": 10, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
				continue
			}
			call("tool-1", "first")
		case "":
			var id string
			_ = json.Unmarshal(msg.ID, &id)
			if id == "tool-1" {
				if mode == "duplicate" {
					call("tool-1", "first")
				} else {
					call("tool-2", "second")
				}
				continue
			}
			if id == "tool-2" {
				var result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"contentItems"`
				}
				_ = json.Unmarshal(msg.Result, &result)
				text := "missing token"
				if len(result.Content) > 0 {
					text = result.Content[0].Text
				}
				if mode == "wrong-result" {
					text = "wrong"
				}
				send(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-test", "turnId": "turn-test", "item": map[string]string{"type": "agentMessage", "text": text}}})
				status := "completed"
				if mode == "failed-turn" {
					status = "failed"
				}
				send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-test", "turn": map[string]any{"id": "turn-test", "status": status}}})
			}
		}
	}
}
