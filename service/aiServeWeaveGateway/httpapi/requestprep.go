package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// requestprep.go holds the two things every JSON front door reads from a
// request before its own decode: the conversation it belongs to, for session
// affinity, and a thinking level written into the model name as a suffix,
// "model(high)" or "model(16384)", for clients that can set nothing but the
// model.
//
// requestprep.go 存放每个 JSON 前门在自己解码之前从请求里读取的两件事：请求所属的对话
// （用于会话亲和），以及以后缀形式写进模型名的思考档位——"model(high)" 或
// "model(16384)"——供只能设置模型名的客户端使用。

// sessionHeaders are the request headers that name a conversation, in the
// order they are consulted: a generic one any client can send, then the ones
// Codex and Claude Code send on their own. Each value is used only as a hash
// input and is never logged.
//
// sessionHeaders 是点名一段对话的请求头，按查阅顺序排列：任何客户端都可以发的通用头，
// 然后是 Codex 与 Claude Code 自己会发的头。每个值只作为哈希输入，从不写进日志。
var sessionHeaders = []string{"X-Session-Id", "Session_id", "X-Claude-Code-Session-Id", "Conversation_id"}

// maxSessionKeyBytes bounds a session key; a longer one is not a session name
// but some other payload, and is ignored.
//
// maxSessionKeyBytes 限定会话 key 的长度；更长的值不是会话名而是别的什么载荷，直接忽略。
const maxSessionKeyBytes = 512

// requestProbe is the handful of body fields prepareRequest reads. A field
// of an unexpected type is simply left empty: the handler's own decode
// decides whether the body is valid.
//
// requestProbe 是 prepareRequest 读取的少数几个请求体字段。类型不符的字段只是留空：
// 请求体是否合法由处理器自己的解码判断。
type requestProbe struct {
	Model          string `json:"model"`
	PromptCacheKey string `json:"prompt_cache_key"`
	Metadata       struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`
}

// suffixDialect names the body field a front door reads thinking from, which
// is where a model suffix is written to.
//
// suffixDialect 指明一个前门从哪个请求体字段读取思考设置，模型后缀就写到那里。
type suffixDialect int

const (
	dialectChat      suffixDialect = iota // reasoning_effort
	dialectResponses                      // reasoning.effort
	dialectAnthropic                      // thinking
)

// prepareRequest returns r with the request's session key on its context
// (see scheduler.WithSessionKey) and raw with any model suffix applied. The
// caller decodes the returned raw, so the native paths that forward the body
// verbatim see exactly what the converted paths see. A malformed suffix value
// is not an error — the model name is then taken as written — but a suffix
// the body cannot take is.
//
// prepareRequest 返回在 context 上带有请求会话 key 的 r（见 scheduler.WithSessionKey），
// 以及应用了模型后缀的 raw。调用方解码返回的 raw，因此原样转发请求体的原生路径看到的，
// 与经过转换的路径完全一致。后缀取值不合法不算错误——此时模型名按原样使用——但请求体
// 无法承载的后缀算错误。
func prepareRequest(r *http.Request, raw json.RawMessage, dialect suffixDialect) (*http.Request, json.RawMessage, error) {
	var probe requestProbe
	_ = json.Unmarshal(raw, &probe)
	r = r.WithContext(scheduler.WithSessionKey(r.Context(), sessionKey(r, probe)))
	base, reasoning, ok := parseModelSuffix(probe.Model)
	if !ok {
		return r, raw, nil
	}
	rewritten, err := applyModelSuffix(raw, base, reasoning, dialect)
	if err != nil {
		return r, raw, err
	}
	return r, rewritten, nil
}

// sessionKey returns the tenant-scoped session key a request carries: the
// first session header present, else the body's prompt_cache_key, else an
// Anthropic metadata.user_id (which Claude Code fills per session). It returns
// "" when there is none.
//
// sessionKey 返回请求携带的、按租户限定的会话 key：第一个出现的会话请求头，否则是请求体的
// prompt_cache_key，再否则是 Anthropic 的 metadata.user_id（Claude Code 按会话填写）。
// 都没有时返回 ""。
func sessionKey(r *http.Request, probe requestProbe) string {
	key := ""
	for _, name := range sessionHeaders {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			key = value
			break
		}
	}
	if key == "" {
		key = probe.PromptCacheKey
	}
	if key == "" {
		key = probe.Metadata.UserID
	}
	if key == "" || len(key) > maxSessionKeyBytes {
		return ""
	}
	tenant := ""
	if identity, ok := IdentityFrom(r.Context()); ok {
		tenant = identity.TenantID
	}
	return tenant + "\x00" + key
}

// maxSuffixBudget bounds a numeric suffix; the largest thinking budget any
// supported backend accepts is well below it.
//
// maxSuffixBudget 限定数字后缀的上限；任何受支持后端接受的最大思考预算都远低于它。
const maxSuffixBudget = 1_000_000

// parseModelSuffix splits "model(value)" into the model and the reasoning the
// value asks for. value is "none", one of the runtime effort levels, or a
// thinking budget in tokens, where 0 means none. Anything else — no
// parentheses, an empty model, an unknown word — is not a suffix, so a model
// whose real name ends in parentheses keeps working unless what they hold is
// one of the values above.
//
// parseModelSuffix 把 "model(value)" 拆成模型与 value 所要求的思考设置。value 是
// "none"、某个运行时思考档位，或以 token 计的思考预算（0 表示关闭）。其他情况——没有
// 括号、模型为空、不认识的词——都不算后缀，因此真实名字以括号结尾的模型照常可用，除非
// 括号里恰好是上述取值之一。
func parseModelSuffix(model string) (string, runtime.ReasoningConfig, bool) {
	open := strings.LastIndexByte(model, '(')
	if open <= 0 || !strings.HasSuffix(model, ")") {
		return "", runtime.ReasoningConfig{}, false
	}
	base, value := model[:open], strings.ToLower(model[open+1:len(model)-1])
	if strings.TrimSpace(base) != base {
		return "", runtime.ReasoningConfig{}, false
	}
	switch {
	case value == "none":
		return base, runtime.ReasoningConfig{}, true
	case runtime.ValidReasoningEffort(value):
		return base, runtime.ReasoningConfig{Enabled: true, Effort: value}, true
	}
	budget, err := strconv.Atoi(value)
	if err != nil || budget < 0 || budget > maxSuffixBudget || strconv.Itoa(budget) != value {
		return "", runtime.ReasoningConfig{}, false
	}
	if budget == 0 {
		return base, runtime.ReasoningConfig{}, true
	}
	return base, runtime.ReasoningConfig{Enabled: true, BudgetTokens: budget}, true
}

// minAnthropicBudget is the smallest thinking budget the Messages API accepts.
// minAnthropicBudget 是 Messages API 接受的最小思考预算。
const minAnthropicBudget = 1024

// applyModelSuffix rewrites raw so model is base and the dialect's thinking
// field says what reasoning asks for, overriding whatever the body said: the
// suffix is the more specific request, and a client that can set the field
// has no reason to write a suffix too. Every other field is kept as sent.
//
// For Anthropic the budget is raised to the API's 1024-token minimum and kept
// below max_tokens as the API requires; a max_tokens too small to leave room
// for any thinking is an error rather than a silent "thinking off".
//
// applyModelSuffix 改写 raw，使 model 为 base，并让该方言的思考字段表达 reasoning 的
// 要求，覆盖请求体原有的设置：后缀是更具体的要求，能设置该字段的客户端也没有理由再写
// 后缀。其他字段原样保留。
//
// 对 Anthropic，预算被提高到 API 的 1024 token 下限，并按 API 要求保持在 max_tokens
// 之下；max_tokens 小到容不下任何思考时报错，而不是悄悄关闭思考。
func applyModelSuffix(raw json.RawMessage, base string, reasoning runtime.ReasoningConfig, dialect suffixDialect) (json.RawMessage, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("the request body is not a JSON object")
	}
	set := func(key string, value any) {
		data, _ := json.Marshal(value)
		body[key] = data
	}
	set("model", base)
	switch dialect {
	case dialectChat:
		set("reasoning_effort", suffixEffort(reasoning))
	case dialectResponses:
		object := map[string]json.RawMessage{}
		if existing, ok := body["reasoning"]; ok && !bytes.Equal(bytes.TrimSpace(existing), []byte("null")) {
			if json.Unmarshal(existing, &object) != nil {
				object = map[string]json.RawMessage{}
			}
		}
		object["effort"], _ = json.Marshal(suffixEffort(reasoning))
		set("reasoning", object)
	case dialectAnthropic:
		if !reasoning.Enabled {
			set("thinking", map[string]string{"type": "disabled"})
			break
		}
		var maxTokens int
		_ = json.Unmarshal(body["max_tokens"], &maxTokens)
		if maxTokens <= minAnthropicBudget {
			return nil, fmt.Errorf("max_tokens must be greater than %d to think at the level the model suffix asks for", minAnthropicBudget)
		}
		budget := min(max(reasoning.Budget(), minAnthropicBudget), maxTokens-1)
		set("thinking", map[string]any{"type": "enabled", "budget_tokens": budget})
	}
	return json.Marshal(body)
}

// suffixEffort renders reasoning as an OpenAI reasoning_effort value: "none"
// for off, otherwise the level, a numeric budget mapped to the nearest level
// not below it.
//
// suffixEffort 把 reasoning 渲染为 OpenAI 的 reasoning_effort 取值：关闭时为 "none"，
// 否则为档位；数字预算映射到不低于它的最近档位。
func suffixEffort(reasoning runtime.ReasoningConfig) string {
	if !reasoning.Enabled {
		return "none"
	}
	return reasoning.EffortLevel()
}

// ollamaModelSuffix applies a model suffix to an Ollama request's model and
// think fields. Ollama's think takes a boolean or "low"/"medium"/"high", so
// minimal maps to low and xhigh to high. Without a suffix both are returned
// unchanged.
//
// ollamaModelSuffix 把模型后缀应用到 Ollama 请求的 model 与 think 字段。Ollama 的 think
// 取布尔值或 "low"/"medium"/"high"，因此 minimal 映射为 low、xhigh 映射为 high。没有后缀时
// 两者原样返回。
func ollamaModelSuffix(model string, think json.RawMessage) (string, json.RawMessage) {
	base, reasoning, ok := parseModelSuffix(model)
	if !ok {
		return model, think
	}
	if !reasoning.Enabled {
		return base, json.RawMessage("false")
	}
	level := reasoning.EffortLevel()
	switch level {
	case runtime.ReasoningEffortMinimal:
		level = runtime.ReasoningEffortLow
	case runtime.ReasoningEffortXHigh:
		level = runtime.ReasoningEffortHigh
	}
	data, _ := json.Marshal(level)
	return base, data
}
