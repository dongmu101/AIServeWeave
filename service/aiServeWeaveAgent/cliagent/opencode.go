package cliagent

import (
	"errors"
	"strings"

	"AIServeWeave/common/runtime"
)

func init() {
	register(&cliSpec{
		Kind:            runtime.KindOpenCode,
		Label:           "OpenCode",
		Executables:     []string{"opencode"},
		HomeBin:         ".opencode/bin",
		PassEnv:         []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"},
		PassEnvSuffixes: []string{"_API_KEY"},
		SignIn:          []string{"not authenticated", "authentication required", "unauthorized", "please login", "please sign in"},
		Setup:           openCodeSetup,
		Discover:        openCodeDiscover,
		Turn: func(_, _, prompt, model string) ([]string, []byte) {
			return []string{"run", "--format", "json", "--pure", "--model", model}, []byte(prompt)
		},
		Parser: func() parser { return &openCodeParser{} },
	})
}

// openCodeSetup keeps sessions in memory, ignores project config and denies
// every permission, so no tool or MCP server can act.
// openCodeSetup 让会话只存于内存、忽略项目配置并拒绝全部权限，任何工具或
// MCP 服务都无法执行。
func openCodeSetup(_ string, env, _ []string) ([]string, error) {
	env = setEnv(env, "OPENCODE_DB", ":memory:")
	env = setEnv(env, "OPENCODE_DISABLE_PROJECT_CONFIG", "1")
	return setEnv(env, "OPENCODE_CONFIG_CONTENT", `{"permission":{"*":"deny"}}`), nil
}

// openCodeDiscover parses `models --verbose --pure`, which prints each
// "provider/model" ID on its own unindented line, optionally followed by an
// indented JSON description.
//
// openCodeDiscover 解析 `models --verbose --pure`，每个 "provider/model" ID
// 独占一行且不缩进，其后可能跟着缩进的 JSON 描述。
func openCodeDiscover(c *call) ([]string, error) {
	var ids []string
	total, signedOut := 0, false
	diag, err := c.run([]string{"models", "--verbose", "--pure"}, nil, func(line []byte) (bool, error) {
		if total += len(line); total > maxListBytes {
			return false, ErrProtocol
		}
		signedOut = signedOut || c.signedOut(line)
		text := ansi.ReplaceAllString(string(line), "")
		if text == "" || text[0] == ' ' || text[0] == '\t' || !strings.Contains(text, "/") || !modelID.MatchString(strings.TrimSpace(text)) {
			return false, nil
		}
		if len(ids) < maxRawModels {
			ids = append(ids, strings.TrimSpace(text))
		}
		return false, nil
	})
	// An empty listing after a clean exit may be a sign-in message on stderr.
	// 正常退出却列表为空，可能是 stderr 上的未登录提示。
	if signedOut || ((errors.Is(err, errExit) || (err == nil && len(ids) == 0)) && c.signedOut(diag)) {
		return nil, ErrAuthentication
	}
	if errors.Is(err, errExit) {
		return nil, ErrRPC
	}
	return ids, err
}

// openCodeParser reads `run --format json`. There is no end event: a clean
// exit with some reply text is success, so a turn that only thought fails.
// A "reasoning" event carries one finished reasoning part, forwarded whole as
// thinking text. Token counts are summed over steps. OpenCode's reasoning
// variants are provider-specific names, so ChatRequest.Reasoning is not
// mapped onto any flag.
//
// openCodeParser 读取 `run --format json`。没有结束事件：正常退出且有回复文本即为
// 成功，因此只有思考没有回答的回合按失败处理。"reasoning" 事件携带一段已完成的思考
// part，整段作为思考文本转发。token 数按步骤累加。OpenCode 的思考档位（variant）是
// 各供应商自己的名字，因此 ChatRequest.Reasoning 不映射到任何参数。
type openCodeParser struct {
	emitted       bool
	input, output int
}

func (p *openCodeParser) line(event []byte, out sink) (bool, error) {
	obj := object(event)
	part := object(obj["part"])
	switch str(obj, "type") {
	case "text":
		s := str(part, "text")
		p.emitted = p.emitted || s != ""
		return false, out.text(s)
	case "reasoning":
		return false, out.reasoning(str(part, "text"))
	case "tool_use", "tool":
		return false, ErrUnexpectedTool
	case "step_finish":
		// Cached input and reasoning output are reported apart from input
		// and output; both count toward quota.
		// 缓存输入与推理输出独立于 input、output 报告，两者都计入配额。
		tokens := object(part["tokens"])
		cache := object(tokens["cache"])
		input, err := sumCounts(tokens, []string{"input"})
		if err != nil {
			return false, err
		}
		cached, err := sumCounts(cache, []string{"read"}, []string{"write"})
		if err != nil {
			return false, err
		}
		output, err := sumCounts(tokens, []string{"output"}, []string{"reasoning"})
		if err != nil {
			return false, err
		}
		input += cached
		p.input, p.output = p.input+input, p.output+output
	case "error":
		return false, ErrTurn
	}
	// step_start and future types carry neither reply nor thinking text.
	// step_start 及未来新增类型既不含回复文本，也不含思考文本。
	return false, nil
}

func (p *openCodeParser) finish() (*runtime.Usage, error) {
	if !p.emitted {
		return nil, ErrTurn
	}
	return tokenUsage(p.input, p.output), nil
}
