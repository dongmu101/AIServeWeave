package openai

import (
	"strings"

	"AIServeWeave/common/runtime"
)

// thinkingSwitchDTO is the {"type":"enabled"|"disabled"} object Zhipu's
// GLM API takes as "thinking".
//
// thinkingSwitchDTO 是智谱 GLM API 以 "thinking" 接收的
// {"type":"enabled"|"disabled"} 对象。
type thinkingSwitchDTO struct {
	Type string `json:"type"`
}

// applyReasoning maps req.Reasoning onto the switch c's backend kind speaks.
// OpenAI-compatible servers disagree on this one parameter more than on any
// other, so the mapping is per kind:
//
//   - vLLM, SGLang, TensorRT-LLM and TokenSpeed render the chat template
//     themselves, and open-weight thinking models are switched there:
//     chat_template_kwargs carries enable_thinking (Qwen3) and thinking
//     (DeepSeek-V3.1); a template ignores the one it does not use.
//     reasoning_effort is added for models such as gpt-oss that read it,
//     limited to the three levels vLLM accepts.
//   - Ollama reads reasoning_effort, with "none" for off and only low,
//     medium and high for on.
//   - Qwen (DashScope) takes enable_thinking and thinking_budget; Zhipu takes
//     a thinking object.
//   - Every other hosted provider gets OpenAI's own reasoning_effort, with
//     "none" for off; with thinking on but no level given nothing is sent,
//     leaving the provider's default.
//
// applyReasoning 把 req.Reasoning 映射到 c 的后端种类所用的开关。OpenAI 兼容服务器在
// 这个参数上的分歧比任何其他参数都大，因此按种类映射：
//
//   - vLLM、SGLang、TensorRT-LLM 与 TokenSpeed 自己渲染聊天模板，开放权重的思考模型在
//     模板里切换：chat_template_kwargs 同时携带 enable_thinking（Qwen3）与 thinking
//     （DeepSeek-V3.1），模板忽略自己不用的那个。另外为 gpt-oss 这类读取
//     reasoning_effort 的模型加上该字段，只限 vLLM 接受的三档。
//   - Ollama 读取 reasoning_effort，关闭用 "none"，开启只认 low、medium、high。
//   - 通义千问（DashScope）接收 enable_thinking 与 thinking_budget；智谱接收 thinking
//     对象。
//   - 其余托管供应商使用 OpenAI 自己的 reasoning_effort，关闭用 "none"；开启但没给档位
//     时什么都不发，沿用供应商的默认值。
func (c *Client) applyReasoning(dto *chatCompletionRequest, r *runtime.ReasoningConfig) {
	if r == nil {
		return
	}
	switch c.kind {
	case runtime.KindVLLM, runtime.KindSGLang, runtime.KindTRTLLM, runtime.KindTokenSpeed:
		dto.ChatTemplateKwargs = map[string]bool{"enable_thinking": r.Enabled, "thinking": r.Enabled}
		if effort := threeLevelEffort(r.EffortLevel()); r.Enabled && r.EffortLevel() != "" {
			dto.ReasoningEffort = &effort
		}
	case runtime.KindOllama:
		effort := "none"
		if r.Enabled {
			effort = threeLevelEffort(r.EffortLevel())
		}
		dto.ReasoningEffort = &effort
	case runtime.KindQwen:
		enabled := r.Enabled
		dto.EnableThinking = &enabled
		if r.Enabled && (r.BudgetTokens > 0 || r.Effort != "") {
			budget := r.Budget()
			dto.ThinkingBudget = &budget
		}
	case runtime.KindZhipu:
		dto.Thinking = &thinkingSwitchDTO{Type: "disabled"}
		if r.Enabled {
			dto.Thinking.Type = "enabled"
		}
	default:
		switch effort := r.EffortLevel(); {
		case !r.Enabled:
			none := "none"
			dto.ReasoningEffort = &none
		case effort != "":
			dto.ReasoningEffort = &effort
		}
	}
}

// threeLevelEffort folds an effort level onto the low/medium/high scale
// vLLM and Ollama accept: minimal rounds up to low, xhigh down to high, and
// no level becomes medium.
//
// threeLevelEffort 把思考强度折算到 vLLM 与 Ollama 接受的 low/medium/high 三档：
// minimal 向上取 low，xhigh 向下取 high，没有档位时取 medium。
func threeLevelEffort(effort string) string {
	switch effort {
	case runtime.ReasoningEffortMinimal, runtime.ReasoningEffortLow:
		return runtime.ReasoningEffortLow
	case runtime.ReasoningEffortHigh, runtime.ReasoningEffortXHigh:
		return runtime.ReasoningEffortHigh
	}
	return runtime.ReasoningEffortMedium
}

const (
	thinkOpenTag  = "<think>"
	thinkCloseTag = "</think>"
	// maxThinkLead bounds the leading whitespace held while deciding
	// whether a reply opens with <think>, so the decision never waits on an
	// unbounded prefix.
	//
	// maxThinkLead 限制判断回复是否以 <think> 开头时扣住的前导空白，使判断从不等待一个
	// 无界的前缀。
	maxThinkLead = 64
)

type thinkState int

const (
	thinkDeciding thinkState = iota // before the reply has shown whether it opens with <think> / 尚未看出回复是否以 <think> 开头
	thinkInside                     // inside the reasoning, until </think> / 在思考内部，直到 </think>
	thinkAnswer                     // after the reasoning, everything is answer / 思考之后，全部是回答
)

// thinkSplitter is the fallback reasoning parser (runtime.Config's
// ReasoningParser): it splits a reply that carries its reasoning inline, as
// <think>…</think>, into reasoning and answer, chunk by chunk. It holds back
// at most a partial tag or a short whitespace prefix, never the reasoning
// itself, so a stream keeps flowing. Only the first <think> section is
// split; a tag inside the answer is answer text.
//
// thinkSplitter 是兜底思考解析器（runtime.Config 的 ReasoningParser）：把以
// <think>…</think> 内联携带思考的回复逐块切分为思考与回答。它最多扣住一个不完整的标签
// 或一小段前导空白，从不扣住思考本身，因此流式输出照常流动。只切分第一个 <think> 段；
// 回答里出现的标签算回答文本。
type thinkSplitter struct {
	state thinkState
	// fallback is the state a reply that does not open with <think> goes
	// to: the answer for ReasoningParserThinkTags, the reasoning for
	// ReasoningParserThinkTagsPrefilled.
	//
	// fallback 是不以 <think> 开头的回复所进入的状态：ReasoningParserThinkTags 进入
	// 回答，ReasoningParserThinkTagsPrefilled 进入思考。
	fallback thinkState
	held     string
	trimLead bool // drop the newlines that separate </think> from the answer / 丢掉 </think> 与回答之间的换行
}

// newThinkSplitter returns the splitter for parser, or nil when parser
// names none.
//
// newThinkSplitter 返回 parser 对应的切分器；parser 未指定解析器时返回 nil。
func newThinkSplitter(parser string) *thinkSplitter {
	switch parser {
	case runtime.ReasoningParserThinkTags:
		return &thinkSplitter{fallback: thinkAnswer}
	case runtime.ReasoningParserThinkTagsPrefilled:
		return &thinkSplitter{fallback: thinkInside}
	}
	return nil
}

// push feeds one chunk of reply text and returns the reasoning and answer
// text that can be released so far.
//
// push 送入一块回复文本，返回目前可以放出的思考与回答文本。
func (s *thinkSplitter) push(text string) (reasoning, answer string) {
	s.held += text
	var r, a strings.Builder
	for {
		switch s.state {
		case thinkDeciding:
			lead := strings.TrimLeft(s.held, " \t\r\n")
			if strings.HasPrefix(lead, thinkOpenTag) {
				s.held, s.state = lead[len(thinkOpenTag):], thinkInside
				continue
			}
			if strings.HasPrefix(thinkOpenTag, lead) && len(s.held) <= maxThinkLead {
				return r.String(), a.String()
			}
			s.state = s.fallback
		case thinkInside:
			if i := strings.Index(s.held, thinkCloseTag); i >= 0 {
				r.WriteString(s.held[:i])
				s.held, s.state, s.trimLead = s.held[i+len(thinkCloseTag):], thinkAnswer, true
				continue
			}
			keep := partialSuffix(s.held, thinkCloseTag)
			r.WriteString(s.held[:len(s.held)-keep])
			s.held = s.held[len(s.held)-keep:]
			return r.String(), a.String()
		case thinkAnswer:
			if s.trimLead {
				s.held = strings.TrimLeft(s.held, "\r\n")
				if s.held == "" {
					return r.String(), a.String()
				}
				s.trimLead = false
			}
			a.WriteString(s.held)
			s.held = ""
			return r.String(), a.String()
		}
	}
}

// flush releases whatever push was still holding at the end of the reply.
//
// flush 在回复结束时放出 push 仍扣着的内容。
func (s *thinkSplitter) flush() (reasoning, answer string) {
	held := s.held
	s.held = ""
	state := s.state
	if state == thinkDeciding {
		state = s.fallback
	}
	if state == thinkInside {
		return held, ""
	}
	if s.trimLead {
		held = strings.TrimLeft(held, "\r\n")
	}
	return "", held
}

// partialSuffix returns the length of the longest proper prefix of tag that
// text ends with: the bytes that might be the start of a tag split across
// two chunks.
//
// partialSuffix 返回 text 结尾处与 tag 的某个真前缀重合的最大长度：这些字节可能是被
// 拆在两块之间的标签的开头。
func partialSuffix(text, tag string) int {
	for n := min(len(tag)-1, len(text)); n > 0; n-- {
		if strings.HasSuffix(text, tag[:n]) {
			return n
		}
	}
	return 0
}

// splitterFor returns the fallback splitter for one request, or nil. The
// prefilled parser is skipped when the request turns thinking off: the
// template then closes the reasoning itself, no </think> follows, and the
// whole answer would be taken for reasoning.
//
// splitterFor 返回单个请求使用的兜底切分器，或 nil。请求关闭思考时跳过 prefilled
// 解析器：此时模板自己就关闭了思考，后面不会再有 </think>，整段回答会被当成思考。
func (c *Client) splitterFor(req runtime.ChatRequest) *thinkSplitter {
	if c.reasoningParser == runtime.ReasoningParserThinkTagsPrefilled && req.Reasoning != nil && !req.Reasoning.Enabled {
		return nil
	}
	return newThinkSplitter(c.reasoningParser)
}

// splitMessage applies the fallback parser to a whole non-streamed reply,
// unless the backend already separated the reasoning itself.
//
// splitMessage 对一整条非流式回复应用兜底解析器，除非后端自己已经分离出了思考。
func (c *Client) splitMessage(req runtime.ChatRequest, m *runtime.ChatMessage) {
	s := c.splitterFor(req)
	if s == nil || m.ReasoningContent != "" {
		return
	}
	r1, a1 := s.push(m.Content)
	r2, a2 := s.flush()
	m.ReasoningContent, m.Content = r1+r2, a1+a2
}
