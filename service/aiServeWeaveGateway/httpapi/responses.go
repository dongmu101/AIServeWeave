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

	// PreviousResponseID and Store are honoured only when a control plane is
	// configured to persist conversation history (STATUS.md's P2 "Responses
	// 持久会话"); see responses_handler.go. Background is always refused —
	// this Gateway has nothing resembling the asynchronous job this field
	// asks for on the Responses API's own terms.
	//
	// PreviousResponseID 与 Store 只有在配置了持久化会话历史的控制面时才会被
	// 兑现（STATUS.md 的 P2「Responses 持久会话」）；见 responses_handler.go。
	// Background 始终被拒绝——本 Gateway 没有任何近似于这个字段在 Responses
	// API 自己的语境里所要求的那种异步任务的东西。
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
	// Tools is set on a "namespace" tool, whose members are function tools.
	//
	// Tools 用于 "namespace" 工具，其成员是 function 工具。
	Tools []responsesTool `json:"tools,omitempty"`
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

	// Arguments, Input and Action are pointers or maps so a present-but-empty
	// value still serialises: a function_call whose arguments are "" must say
	// so, because Responses clients treat the field as required on that type.
	//
	// Arguments、Input 与 Action 用指针或 map，使「存在但为空」的值依旧被序列化：
	// arguments 为 "" 的 function_call 必须如实写出，因为 Responses 客户端把该字段
	// 视为这个类型的必填项。
	CallID    string         `json:"call_id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	Arguments *string        `json:"arguments,omitempty"`
	Input     *string        `json:"input,omitempty"`
	Action    map[string]any `json:"action,omitempty"`
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
//
// The remaining fields belong to the tool-loop item types (function_call,
// custom_tool_call, local_shell_call and their outputs); see
// appendResponsesItem.
//
// 其余字段属于工具循环的各个项类型（function_call、custom_tool_call、
// local_shell_call 及它们的输出）；见 appendResponsesItem。
type responsesInputItem struct {
	Type    string          `json:"type,omitempty"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`

	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Input     string          `json:"input,omitempty"`
	Action    json.RawMessage `json:"action,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
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
		var err error
		if out, err = appendResponsesItem(out, item); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// responsesContentPartJSON is one element of an input item's typed-part
// content array. ImageURL is a bare string on this API — unlike Chat
// Completions' nested image_url.url — matching the real Responses API's
// "input_image" shape. FileData/Filename mirror the real "input_file"
// shape's inline form; FileID mirrors its other form (a reference to a file
// already uploaded to OpenAI's own Files API) which this Gateway has no way
// to resolve and rejects by name — see inputItemContent.
type responsesContentPartJSON struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`
	FileID   string `json:"file_id,omitempty"`
}

// inputItemContent converts one input item's "content" field into the
// canonical chat message content (STATUS.md's P2 ChatMessage.Content
// structured rework and its multimodal-input follow-on): a bare string, or
// an array of parts that are all text-shaped
// ("input_text"/"output_text"/"text"/""), collapses to plain text —
// byte-identical to this function's pre-ContentParts behavior, so the
// overwhelmingly common text-only case is unaffected by images or files
// existing. An array containing an "input_image" or "input_file" part
// builds ContentParts instead; any other part type (input_audio, …) is
// still rejected by name: accepting it as empty text would silently answer
// a question nobody asked, and there is still nowhere in the canonical type
// to put it. An "input_file" naming file_id rather than carrying file_data
// inline is also rejected by name — this Gateway has no OpenAI Files API of
// its own to resolve that reference against.
//
// inputItemContent 把一个输入项目的 "content" 字段转换成 canonical 聊天消息
// 内容（STATUS.md 的 P2 ChatMessage.Content 结构化改造及其多模态输入后续）：
// 裸字符串，或者全部由文本形状部件（"input_text"/"output_text"/"text"/空）
// 组成的数组，都会收敛为纯文本——与本函数加入 ContentParts 之前的行为逐字节
// 一致，因此绝大多数的纯文本情形不受图片或文件存在的影响。含 "input_image"
// 或 "input_file" 部件的数组则改为构建 ContentParts；其他任何部件类型
// （input_audio……）仍按名字拒绝：把它当作空文本接受，等于默默回答一个没人
// 问过的问题，且 canonical 类型里仍然没有地方安放它。一个指名 file_id 而非
// 内联携带 file_data 的 "input_file" 同样按名字拒绝——本 Gateway 没有自己的
// OpenAI Files API 可用来解析这个引用。
func inputItemContent(raw json.RawMessage) (text string, parts []runtime.ContentPart, err error) {
	if len(raw) == 0 {
		return "", nil, fmt.Errorf("an input item has no content")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single, nil, nil
	}
	var items []responsesContentPartJSON
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", nil, fmt.Errorf("an input item's content must be a string or an array of parts")
	}

	allText := true
	for _, p := range items {
		switch p.Type {
		case "input_text", "output_text", "text", "":
		default:
			allText = false
		}
	}
	if allText {
		var b strings.Builder
		for _, p := range items {
			b.WriteString(p.Text)
		}
		return b.String(), nil, nil
	}

	for _, p := range items {
		switch p.Type {
		case "input_text", "output_text", "text", "":
			parts = append(parts, runtime.ContentPart{Type: "text", Text: p.Text})
		case "input_file":
			if p.FileID != "" {
				return "", nil, fmt.Errorf(`an "input_file" content part naming "file_id" is not supported; supply "file_data" inline`)
			}
			if p.FileData == "" {
				return "", nil, fmt.Errorf(`an "input_file" content part requires "file_data"`)
			}
			parts = append(parts, runtime.ContentPart{
				Type: "file",
				File: &runtime.ContentFile{URL: p.FileData, Filename: p.Filename},
			})
		case "input_image":
			if p.ImageURL == "" {
				return "", nil, fmt.Errorf(`an "input_image" content part requires "image_url"`)
			}
			parts = append(parts, runtime.ContentPart{
				Type:     "image_url",
				ImageURL: &runtime.ContentImageURL{URL: p.ImageURL, Detail: p.Detail},
			})
		default:
			return "", nil, fmt.Errorf("input content part type %q is not supported", p.Type)
		}
	}
	return "", parts, nil
}

// toRuntime builds the canonical request. It is the same type chat.go produces,
// which is the point: past this function nothing downstream knows which public
// API the request arrived on.
//
// toRuntime 构造 canonical 请求。它与 chat.go 产出的是同一个类型，而这正是关键：过了
// 本函数之后，下游没有任何东西知道这次请求是从哪个公开 API 进来的。
//
// The returned responsesToolset also carries what only the response side
// needs: which tools were declared as something other than a plain function,
// and which hosted tools were left out.
//
// 返回的 responsesToolset 还携带只有响应一侧才用得到的东西：哪些工具被声明成普通
// function 之外的类型，以及哪些托管工具被略去。
func (req responsesRequest) toRuntime() (runtime.ChatRequest, responsesToolset, error) {
	messages, err := req.messages()
	if err != nil {
		return runtime.ChatRequest{}, responsesToolset{}, err
	}
	out := runtime.ChatRequest{
		Model:       req.Model,
		Messages:    messages,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxOutputTokens,
	}
	toolset, err := responsesTools(req.Tools)
	if err != nil {
		return runtime.ChatRequest{}, responsesToolset{}, err
	}
	out.Tools = toolset.tools
	choice, ok, err := responsesToolChoice(req.ToolChoice)
	if err != nil {
		return runtime.ChatRequest{}, responsesToolset{}, err
	}
	if ok {
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
	return out, toolset, nil
}

// unsupported names the field this Gateway can never honour, regardless of
// configuration, or empty when the request only asks for things it can do.
// store and previous_response_id are not here: whether this Gateway can
// honour them depends on whether a control plane is configured to persist
// conversation history (STATUS.md's P2 "Responses 持久会话"), which this
// pure method has no way to know — see responses_handler.go's own check,
// gated on h.responsePersist.
//
// unsupported 指出本 Gateway 无论如何配置都无法兑现的字段；请求只要求它做得到
// 的事情时返回空。store 与 previous_response_id 不在此列：本 Gateway 能否
// 兑现它们，取决于是否配置了持久化会话历史的控制面（STATUS.md 的 P2
// 「Responses 持久会话」），而这个纯函数无从得知——判断见 responses_handler.go
// 自己的检查，以 h.responsePersist 为条件。
func (req responsesRequest) unsupported() string {
	if req.Background != nil && *req.Background {
		return "background"
	}
	return ""
}
