package grokcli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"AIServeWeave/common/runtime"
)

// streamEvent is one line of `--output-format streaming-json`.
// streamEvent 是 `--output-format streaming-json` 输出的一行。
type streamEvent struct {
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data"`
	Usage json.RawMessage `json:"usage"`
}

// runTurn runs one headless turn and forwards text deltas through emit as
// they arrive. Thought events are dropped, any tool event aborts the turn,
// and the terminal stop event is emitted only after the CLI exits cleanly.
//
// runTurn 运行一次 headless 回合，文本增量随到随经 emit 转发。thought 事件
// 被丢弃，任何工具事件都会中止回合，结束事件只在 CLI 正常退出后发送。
func runTurn(ctx context.Context, executable string, env []string, model, prompt string, touch func(), emit func(runtime.ChatEvent) error) error {
	return withHome(executable, env, func(dir, exe string, childEnv []string) error {
		promptFile := filepath.Join(dir, "prompt.txt")
		if os.WriteFile(promptFile, []byte(prompt), 0o600) != nil {
			return ErrProcess
		}
		var usage *runtime.Usage
		textBytes := 0
		noise := &headCapture{limit: maxDiagBytes}
		diag, err := runCLI(ctx, exe, childEnv, dir, inferenceArgs(promptFile, model), nil, func(line []byte) (bool, error) {
			touch()
			var event streamEvent
			if json.Unmarshal(line, &event) != nil || event.Type == "" {
				// Not an event; kept only for sign-in classification.
				// 不是事件；只保留用于判断是否未登录。
				_, _ = noise.Write(line)
				return false, nil
			}
			switch event.Type {
			case "text":
				var text string
				if json.Unmarshal(event.Data, &text) != nil {
					return false, ErrProtocol
				}
				if textBytes += len(text); textBytes > maxTextBytes {
					return false, ErrProtocol
				}
				if text == "" {
					return false, nil
				}
				return false, emit(runtime.ChatEvent{Delta: runtime.ChatMessageDelta{Content: text}})
			case "tool_call", "tool_call_update":
				return false, ErrUnexpectedTool
			case "end":
				var parseErr error
				usage, parseErr = parseUsage(event.Usage)
				return false, parseErr
			case "error":
				if mentionsSignIn(line) {
					return false, ErrAuthentication
				}
				return false, ErrTurn
			}
			// thought, plan, usage, available_commands and future types carry no reply text.
			// thought、plan、usage、available_commands 及未来新增类型都不含回复文本。
			return false, nil
		})
		if errors.Is(err, errExit) {
			if mentionsSignIn(diag) || mentionsSignIn(noise.buffer.Bytes()) {
				return ErrAuthentication
			}
			return ErrTurn
		}
		if err != nil {
			return err
		}
		return emit(runtime.ChatEvent{FinishReason: "stop", Usage: usage})
	})
}

// parseUsage reads the end event's token counts; absent or all-zero usage is nil.
// parseUsage 读取 end 事件的 token 计数；缺失或全为零时返回 nil。
func parseUsage(raw json.RawMessage) (*runtime.Usage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, ErrProtocol
	}
	count := func(keys ...string) (int, error) {
		value := lookup(fields, keys...)
		if value == nil {
			return 0, nil
		}
		var n int
		if json.Unmarshal(value, &n) != nil || n < 0 {
			return 0, ErrProtocol
		}
		return n, nil
	}
	input, err := count("input_tokens", "prompt_tokens", "inputTokens")
	if err != nil {
		return nil, err
	}
	output, err := count("output_tokens", "completion_tokens", "outputTokens")
	if err != nil {
		return nil, err
	}
	if input == 0 && output == 0 {
		return nil, nil
	}
	return &runtime.Usage{PromptTokens: input, CompletionTokens: output, TotalTokens: input + output}, nil
}
