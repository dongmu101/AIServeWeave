package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"

	"AIServeWeave/common/runtime"
)

// This file is the Responses API, translated at the boundary into the same
// canonical chat request every other endpoint produces.
//
// Translating rather than adding a tunnel operation is README's "外部协议只存在
// 于系统边界" applied literally, and it buys something concrete: a backend that
// only speaks Chat Completions — Ollama, for one — serves a Responses request
// without knowing the API exists. vLLM's own /v1/responses is not used, and
// the cost of that is stated below in what this endpoint refuses.
//
// 本文件是 Responses API，在边界处被转换成与其他每个端点相同的 canonical 聊天请求。
//
// 选择转换而不是新增一个隧道操作，是 README「外部协议只存在于系统边界」的字面落实，
// 而且它换来一件具体的好处：一个只会 Chat Completions 的后端——比如 Ollama——在不知道
// 这个 API 存在的情况下也能服务 Responses 请求。vLLM 自己的 /v1/responses 没有被使用，
// 其代价体现在下面这个端点所拒绝的东西里。

// -----------------------------------------------------------------------
// Wire types
// -----------------------------------------------------------------------

// responsesRequest is the subset of POST /v1/responses this Gateway serves.
// Fields it cannot honour are present so they can be refused by name rather
// than silently dropped — README is explicit that a deployment must not
// discard a parameter it does not support.
//
// responsesRequest 是本 Gateway 所服务的 POST /v1/responses 子集。那些它无法兑现的
// 字段也列在这里，是为了能指名拒绝而不是默默丢弃——README 明确要求部署不得丢弃它不
// 支持的参数。
type responsesRequest struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Instructions    string          `json:"instructions,omitempty"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
	Tools           []responsesTool `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
	Text            *responsesText  `json:"text,omitempty"`

	// Refused below. Each one needs something this Gateway does not have.
	//
	// 以下字段会被拒绝。每一个都需要本 Gateway 不具备的东西。
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	Store              *bool  `json:"store,omitempty"`
	Background         *bool  `json:"background,omitempty"`
}

// responsesTool is a tool definition. Responses flattens the function's fields
// onto the tool itself, where Chat Completions nests them under "function".
//
// responsesTool 是一个工具定义。Responses 把函数的字段平铺在工具本身上，而 Chat
// Completions 把它们嵌在 "function" 之下。
type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type responsesText struct {
	Format *responsesTextFormat `json:"format,omitempty"`
}

type responsesTextFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name,omitempty"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

// responseObject is the Responses API's own result shape. Its token counts are
// named differently from Chat Completions' — input_tokens rather than
// prompt_tokens — and a client reading one must not find the other.
//
// responseObject 是 Responses API 自己的结果形状。它的 token 计数命名与 Chat
// Completions 不同——是 input_tokens 而不是 prompt_tokens——读其中一个的客户端不能在
// 那里找到另一个。
type responseObject struct {
	ID                string              `json:"id"`
	Object            string              `json:"object"`
	CreatedAt         int64               `json:"created_at"`
	Status            string              `json:"status"`
	Model             string              `json:"model"`
	Output            []responseOutputRaw `json:"output"`
	Usage             *responsesUsage     `json:"usage,omitempty"`
	Error             *responsesError     `json:"error"`
	IncompleteDetails *incompleteDetails  `json:"incomplete_details"`
	Instructions      *string             `json:"instructions"`
	MaxOutputTokens   *int                `json:"max_output_tokens,omitempty"`
	Temperature       *float64            `json:"temperature,omitempty"`
	TopP              *float64            `json:"top_p,omitempty"`
	ParallelToolCalls bool                `json:"parallel_tool_calls"`
	Tools             []responsesTool     `json:"tools"`
	ToolChoice        any                 `json:"tool_choice"`
}

// responseOutputRaw is one output item. Responses puts messages and function
// calls in one array with different shapes, so the fields of both live here
// and the unused ones are omitted.
//
// responseOutputRaw 是一个输出项。Responses 把消息与函数调用放进同一个数组、各有不同
// 形状，因此两者的字段都在这里，未使用的会被省略。
type responseOutputRaw struct {
	Type    string                `json:"type"`
	ID      string                `json:"id"`
	Status  string                `json:"status,omitempty"`
	Role    string                `json:"role,omitempty"`
	Content []responseContentPart `json:"content,omitempty"`

	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type responseContentPart struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type responsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type incompleteDetails struct {
	Reason string `json:"reason"`
}

// -----------------------------------------------------------------------
// Input translation
// -----------------------------------------------------------------------

// responsesInputItem is one element of an "input" array. Content is either a
// bare string or an array of typed parts, and both forms mean the same thing
// once the text is pulled out of them.
//
// responsesInputItem 是 "input" 数组的一个元素。Content 要么是裸字符串，要么是一组
// 带类型的部件；把文本取出来之后，两种形式表达的是同一件事。
type responsesInputItem struct {
	Type    string          `json:"type,omitempty"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// messages converts the request's input into the canonical message list,
// prefixed by instructions when there are any.
//
// messages 把请求的 input 转换成 canonical 消息列表；有 instructions 时置于最前。
func (req responsesRequest) messages() ([]runtime.ChatMessage, error) {
	var out []runtime.ChatMessage
	if req.Instructions != "" {
		// Responses calls it "instructions" and puts it outside the input;
		// a chat backend knows it as the leading system message. Same thing,
		// different place on the wire.
		//
		// Responses 管它叫 "instructions" 并放在 input 之外；聊天后端认识的是打头的
		// system 消息。同一件事，只是在线上的位置不同。
		out = append(out, runtime.ChatMessage{Role: "system", Content: req.Instructions})
	}

	if len(req.Input) == 0 {
		return nil, fmt.Errorf("input is required")
	}
	var single string
	if err := json.Unmarshal(req.Input, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			return nil, fmt.Errorf("input must not be empty")
		}
		return append(out, runtime.ChatMessage{Role: "user", Content: single}), nil
	}

	var items []responsesInputItem
	if err := json.Unmarshal(req.Input, &items); err != nil {
		return nil, fmt.Errorf("input must be a string or an array of input items")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("input must not be empty")
	}
	for _, item := range items {
		if item.Type != "" && item.Type != "message" {
			// Function call outputs and the other item types belong to the
			// stateful, tool-running half of this API, which this endpoint
			// does not implement. Refusing by name beats translating half of
			// one into a message that means something else.
			//
			// 函数调用输出以及其他项目类型属于本 API 中有状态、跑工具的那一半，本端点
			// 并未实现。指名拒绝，好过把其中一半翻译成一条意思已经变了的消息。
			return nil, fmt.Errorf("input item type %q is not supported", item.Type)
		}
		text, err := inputItemText(item.Content)
		if err != nil {
			return nil, err
		}
		role := item.Role
		if role == "" {
			role = "user"
		}
		out = append(out, runtime.ChatMessage{Role: role, Content: text})
	}
	return out, nil
}

// inputItemText flattens an item's content to text, accepting both the bare
// string form and the typed-part array.
//
// inputItemText 把一个项目的内容压平为文本，两种形式都接受：裸字符串与带类型的部件
// 数组。
func inputItemText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("an input item has no content")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("an input item's content must be a string or an array of parts")
	}
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text", "":
			b.WriteString(p.Text)
		default:
			// Images, audio and file parts need a canonical representation
			// this repository does not have yet; accepting them as empty text
			// would silently answer a question nobody asked.
			//
			// 图像、音频与文件部件需要一种本仓库尚不具备的 canonical 表示；把它们当作
			// 空文本接受，等于默默回答一个没人问过的问题。
			return "", fmt.Errorf("input content part type %q is not supported", p.Type)
		}
	}
	return b.String(), nil
}

// toRuntime builds the canonical request. It is the same type chat.go produces,
// which is the point: past this function nothing downstream knows which public
// API the request arrived on.
//
// toRuntime 构造 canonical 请求。它与 chat.go 产出的是同一个类型，而这正是关键：过了
// 本函数之后，下游没有任何东西知道这次请求是从哪个公开 API 进来的。
func (req responsesRequest) toRuntime() (runtime.ChatRequest, error) {
	messages, err := req.messages()
	if err != nil {
		return runtime.ChatRequest{}, err
	}
	out := runtime.ChatRequest{
		Model:       req.Model,
		Messages:    messages,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxOutputTokens,
	}
	for _, t := range req.Tools {
		if t.Type != "function" {
			// Built-in tools (web_search, file_search, code_interpreter, mcp)
			// are executed by OpenAI's own service. This Gateway forwards to a
			// model and runs nothing, so accepting one would promise a
			// capability that does not exist here.
			//
			// 内置工具（web_search、file_search、code_interpreter、mcp）由 OpenAI 自己
			// 的服务执行。本 Gateway 只把请求转给模型、不运行任何东西，因此接受它等于
			// 承诺一项这里并不存在的能力。
			return runtime.ChatRequest{}, fmt.Errorf("tool type %q is not supported by this gateway", t.Type)
		}
		out.Tools = append(out.Tools, runtime.Tool{
			Type: "function",
			Function: runtime.FunctionDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	if choice, ok := decodeToolChoice(req.ToolChoice); ok {
		out.ToolChoice = choice
	}
	if req.Text != nil && req.Text.Format != nil {
		format := &runtime.ResponseFormat{Type: req.Text.Format.Type}
		if req.Text.Format.Type == "json_schema" {
			format.JSONSchema = &runtime.JSONSchemaFormat{
				Name:   req.Text.Format.Name,
				Strict: req.Text.Format.Strict,
				Schema: req.Text.Format.Schema,
			}
		}
		out.ResponseFormat = format
	}
	return out, nil
}

// unsupported names the field this Gateway cannot honour, or empty when the
// request only asks for things it can do.
//
// unsupported 指出本 Gateway 无法兑现的那个字段；请求只要求它做得到的事情时返回空。
func (req responsesRequest) unsupported() string {
	switch {
	case req.PreviousResponseID != "":
		// Continuing a stored conversation requires the Gateway to hold that
		// conversation and to send the follow-up to the same node. It holds
		// neither: responses are not stored, and the scheduler picks a node
		// per request.
		//
		// 续接一次已存储的会话，要求 Gateway 既持有那次会话，又把后续请求发给同一个
		// 节点。两者它都没有：响应不被存储，而调度器是按请求选节点的。
		return "previous_response_id"
	case req.Store != nil && *req.Store:
		return "store"
	case req.Background != nil && *req.Background:
		return "background"
	}
	return ""
}
