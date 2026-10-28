package cliagent

import (
	"encoding/json"
	"errors"
	"strings"

	"AIServeWeave/common/runtime"
)

const (
	piPromptID = "aisw_prompt"
	piModelsID = "aisw_model_discovery"
)

// piArgs disables sessions, extensions, skills, prompt templates, context
// files and every built-in tool.
// piArgs 关闭会话、扩展、技能、提示词模板、上下文文件及全部内建工具。
var piArgs = []string{"--mode", "rpc", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates",
	"--no-context-files", "--no-builtin-tools", "--no-approve"}

func init() {
	register(&cliSpec{
		Kind:            runtime.KindPi,
		Label:           "Pi",
		Executables:     []string{"pi"},
		PassEnv:         []string{"PI_CODING_AGENT_DIR"},
		PassEnvSuffixes: []string{"_API_KEY"},
		SignIn:          []string{"not authenticated", "authentication required", "no api key", "please login"},
		Discover:        piDiscover,
		Turn:            piTurn,
		TurnRPC:         true,
		ReasoningArgs:   piReasoningArgs,
		Parser:          func() parser { return &piParser{} },
	})
}

// piTurn splits "provider/model" on the first slash and sends the prompt as
// one RPC command.
// piTurn 在第一个斜杠处拆分 "provider/model"，并把提示词作为一条 RPC 命令发送。
func piTurn(_, _, prompt, model string) ([]string, []byte) {
	args := append([]string{}, piArgs...)
	if provider, name, ok := strings.Cut(model, "/"); ok {
		args = append(args, "--provider", provider, "--model", name)
	} else {
		args = append(args, "--model", model)
	}
	command, _ := json.Marshal(map[string]string{"id": piPromptID, "type": "prompt", "message": prompt})
	return args, append(command, '\n')
}

// piReasoningArgs maps ChatRequest.Reasoning onto Pi's --thinking level:
// "off" when thinking is turned off, the requested level otherwise (Pi
// accepts minimal through xhigh, the same vocabulary), and "medium" when the
// caller asked to think without naming a level. Pi itself clamps a level to
// what the selected model supports and ignores it for a model that cannot
// think, so this never makes a turn fail.
//
// piReasoningArgs 把 ChatRequest.Reasoning 映射为 Pi 的 --thinking 档位：关闭思考时
// 为 "off"，否则为请求的档位（Pi 接受 minimal 到 xhigh，词汇相同）；调用方要求思考
// 却没给档位时为 "medium"。Pi 自己会把档位收敛到所选模型支持的范围，对不会思考的
// 模型则忽略它，因此这不会让回合失败。
func piReasoningArgs(reasoning runtime.ReasoningConfig) []string {
	if !reasoning.Enabled {
		return []string{"--thinking", "off"}
	}
	level := reasoning.EffortLevel()
	if !runtime.ValidReasoningEffort(level) {
		level = runtime.ReasoningEffortMedium
	}
	return []string{"--thinking", level}
}

// piDiscover asks the RPC server for get_available_models.
// piDiscover 通过 RPC 请求 get_available_models。
func piDiscover(c *call) ([]string, error) {
	command, err := json.Marshal(map[string]string{"id": piModelsID, "type": "get_available_models"})
	if err != nil {
		return nil, ErrProtocol
	}
	var ids []string
	answered := false
	diag, err := c.rpc(piArgs, append(command, '\n'), func(line []byte) (bool, error) {
		obj := object(line)
		if str(obj, "type") != "response" || str(obj, "id") != piModelsID {
			return false, nil
		}
		answered = true
		var success bool
		if json.Unmarshal(obj["success"], &success) != nil || !success {
			if c.signedOut(line) {
				return true, ErrAuthentication
			}
			return true, ErrRPC
		}
		models := lookup(obj, "models")
		if data := object(obj["data"]); data != nil {
			models = lookup(data, "models")
		}
		var list []map[string]json.RawMessage
		if json.Unmarshal(models, &list) != nil {
			return true, ErrProtocol
		}
		for _, model := range list {
			// Each half becomes its own argv value, so each must be safe alone.
			// 两段各自成为独立的 argv 值，因此每段都必须单独安全。
			provider, id := str(model, "provider"), str(model, "id")
			if modelID.MatchString(provider) && modelID.MatchString(id) && !strings.Contains(provider, "/") {
				ids = append(ids, provider+"/"+id)
			}
		}
		return true, nil
	})
	if errors.Is(err, errExit) && c.signedOut(diag) {
		return nil, ErrAuthentication
	}
	if err == nil && !answered {
		if c.signedOut(diag) {
			return nil, ErrAuthentication
		}
		return nil, ErrProtocol
	}
	return ids, err
}

// piParser reads the RPC event stream. agent_settled ends the turn; agent_end
// does not, following dbx. A failed attempt (an error event or stopReason
// "error") aborts the turn at once rather than waiting for a retry, because
// a retry would repeat text already forwarded. An assistant thinking_delta
// carries one fragment of thinking text in "delta" and is forwarded as
// thinking; thinking_start and thinking_end carry none.
// piParser 读取 RPC 事件流。按 dbx 的做法，agent_settled 结束回合，agent_end
// 不结束。一次失败的尝试（error 事件或 stopReason 为 "error"）立即中止回合，
// 不等待重试，因为重试会重复已经转发出去的文本。assistant 的 thinking_delta 在
// "delta" 中携带一段思考文本，作为思考转发；thinking_start 与 thinking_end 不含文本。
type piParser struct {
	input, output int
	settled       bool
}

func (p *piParser) line(event []byte, out sink) (bool, error) {
	obj := object(event)
	switch str(obj, "type") {
	case "response":
		var success bool
		if str(obj, "id") == piPromptID && (json.Unmarshal(obj["success"], &success) != nil || !success) {
			return false, ErrTurn
		}
	case "message_update":
		if role := str(object(obj["message"]), "role"); role != "" && role != "assistant" {
			return false, nil
		}
		update := object(obj["assistantMessageEvent"])
		switch str(update, "type") {
		case "text_delta":
			return false, out.text(str(update, "delta"))
		case "thinking_delta":
			return false, out.reasoning(str(update, "delta"))
		case "error":
			return false, ErrTurn
		case "toolcall_start", "toolcall_delta", "toolcall_end":
			return false, ErrUnexpectedTool
		}
	case "message_end":
		message := object(obj["message"])
		if role := str(message, "role"); role != "" && role != "assistant" {
			return false, nil
		}
		if str(message, "stopReason") == "error" {
			return false, ErrTurn
		}
		usage := object(message["usage"])
		input, err := sumCounts(usage, []string{"input", "inputTokens", "input_tokens"}, []string{"cacheRead"}, []string{"cacheWrite"})
		if err != nil {
			return false, err
		}
		output, err := count(usage, "output", "outputTokens", "output_tokens")
		if err != nil {
			return false, err
		}
		p.input, p.output = p.input+input, p.output+output
	case "tool_execution_start", "tool_execution_end", "tool_execution_update":
		return false, ErrUnexpectedTool
	case "agent_settled":
		p.settled = true
		return true, nil
	case "error":
		return false, ErrTurn
	}
	// agent_start, turn_* and future types carry neither reply nor thinking text.
	// agent_start、turn_* 及未来新增类型既不含回复文本，也不含思考文本。
	return false, nil
}

func (p *piParser) finish() (*runtime.Usage, error) {
	if !p.settled {
		return nil, ErrProtocol
	}
	return tokenUsage(p.input, p.output), nil
}
