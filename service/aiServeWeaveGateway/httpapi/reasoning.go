package httpapi

import (
	"encoding/json"
	"fmt"

	"AIServeWeave/common/runtime"
)

// reasoning.go turns each front door's own way of asking a model to think
// into the one runtime.ReasoningConfig every adapter reads. The request is a
// hint all the way down (see runtime.ChatRequest.Reasoning), so a value is
// refused here only when it is malformed, never because some backend might
// not honour it.
//
// reasoning.go 把各个前门各自要求模型思考的写法，转成每个适配器都读取的同一个
// runtime.ReasoningConfig。这个请求自始至终都是提示（见 runtime.ChatRequest.Reasoning），
// 因此这里只拒绝格式错误的值，从不因为某个后端可能不兑现而拒绝。

// reasoningFromEffort reads an OpenAI reasoning_effort value: "none" turns
// thinking off, a level turns it on at that level, and an empty value means
// the caller did not ask. field names the parameter in the error.
//
// reasoningFromEffort 读取 OpenAI 的 reasoning_effort 值："none" 关闭思考，一个档位以该
// 档位开启思考，空值表示调用方没有要求。field 是错误信息里的参数名。
func reasoningFromEffort(effort, field string) (*runtime.ReasoningConfig, error) {
	switch {
	case effort == "":
		return nil, nil
	case effort == "none":
		return &runtime.ReasoningConfig{}, nil
	case runtime.ValidReasoningEffort(effort):
		return &runtime.ReasoningConfig{Enabled: true, Effort: effort}, nil
	}
	return nil, fmt.Errorf("%s must be one of none, minimal, low, medium, high, xhigh", field)
}

// responsesReasoning is the Responses API's "reasoning" object. Summary is
// accepted and not acted on: a chat backend hands up its chain of thought
// verbatim, and it is rendered as reasoning_text, not summarised.
//
// responsesReasoning 是 Responses API 的 "reasoning" 对象。Summary 被接受但不起作用：
// 聊天后端递上来的是逐字的思考链，它被渲染为 reasoning_text，而不是摘要。
type responsesReasoning struct {
	Effort  *string         `json:"effort"`
	Summary json.RawMessage `json:"summary,omitempty"`
}

// reasoningFromResponses reads a Responses "reasoning" object. An object
// without an effort asks for the provider's default level, which on the
// Responses API means thinking on.
//
// reasoningFromResponses 读取 Responses 的 "reasoning" 对象。没有 effort 的对象要求
// 供应商的默认档位，在 Responses API 上这意味着开启思考。
func reasoningFromResponses(r *responsesReasoning) (*runtime.ReasoningConfig, error) {
	if r == nil {
		return nil, nil
	}
	if r.Effort == nil || *r.Effort == "" {
		return &runtime.ReasoningConfig{Enabled: true}, nil
	}
	return reasoningFromEffort(*r.Effort, "reasoning.effort")
}

// reasoningFromAnthropic reads an Anthropic "thinking" object: "enabled"
// with its required budget_tokens, "adaptive" (thinking on, the model picks
// the depth), or "disabled".
//
// reasoningFromAnthropic 读取 Anthropic 的 "thinking" 对象："enabled" 及其必填的
// budget_tokens、"adaptive"（开启思考，由模型决定深度）或 "disabled"。
func reasoningFromAnthropic(raw json.RawMessage) (*runtime.ReasoningConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var thinking struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	}
	if err := json.Unmarshal(raw, &thinking); err != nil {
		return nil, fmt.Errorf(`thinking must be an object with a "type"`)
	}
	switch thinking.Type {
	case "enabled":
		if thinking.BudgetTokens <= 0 {
			return nil, fmt.Errorf(`thinking.budget_tokens is required and must be greater than zero when thinking.type is "enabled"`)
		}
		return &runtime.ReasoningConfig{Enabled: true, BudgetTokens: thinking.BudgetTokens}, nil
	case "adaptive":
		return &runtime.ReasoningConfig{Enabled: true}, nil
	case "disabled":
		return &runtime.ReasoningConfig{}, nil
	}
	return nil, fmt.Errorf(`thinking.type must be "enabled", "adaptive" or "disabled"`)
}

// reasoningFromOllamaThink reads Ollama's "think": a boolean, or one of the
// levels "low", "medium" and "high" that Ollama accepts for models such as
// gpt-oss.
//
// reasoningFromOllamaThink 读取 Ollama 的 "think"：布尔值，或 Ollama 为 gpt-oss 这类
// 模型接受的 "low"、"medium"、"high" 三档之一。
func reasoningFromOllamaThink(raw json.RawMessage) (*runtime.ReasoningConfig, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var on bool
	if json.Unmarshal(raw, &on) == nil {
		return &runtime.ReasoningConfig{Enabled: on}, nil
	}
	var level string
	if json.Unmarshal(raw, &level) == nil {
		switch level {
		case runtime.ReasoningEffortLow, runtime.ReasoningEffortMedium, runtime.ReasoningEffortHigh:
			return &runtime.ReasoningConfig{Enabled: true, Effort: level}, nil
		}
	}
	return nil, fmt.Errorf(`think must be true, false, "low", "medium" or "high"`)
}
