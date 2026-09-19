package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"AIServeWeave/common/runtime"
)

// chatMessageDTO's Content marshals as a plain string when Parts is empty
// and as the content-array form when it is not — see MarshalJSON. Parts is
// never populated on a decoded response: no backend these adapters talk to
// returns array-form content in a chat completion.
type chatMessageDTO struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	Parts      []contentPartDTO `json:"-"`
	Name       string           `json:"name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCallDTO    `json:"tool_calls,omitempty"`
}

// chatMessageDTOWire mirrors chatMessageDTO's JSON shape with Content typed
// as any, so MarshalJSON can substitute either a string or []contentPartDTO
// without hand-writing the rest of the object's fields twice.
type chatMessageDTOWire struct {
	Role       string        `json:"role"`
	Content    any           `json:"content"`
	Name       string        `json:"name,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCallDTO `json:"tool_calls,omitempty"`
}

// MarshalJSON emits Content as an array of content parts (mirroring the
// OpenAI-compatible vision content-array shape vLLM/SGLang/Ollama's
// OpenAI-compatible endpoints accept) when Parts is set, or as the plain
// string every existing caller and test already expects otherwise.
func (m chatMessageDTO) MarshalJSON() ([]byte, error) {
	wire := chatMessageDTOWire{
		Role: m.Role, Name: m.Name, ToolCallID: m.ToolCallID, ToolCalls: m.ToolCalls,
	}
	if len(m.Parts) > 0 {
		wire.Content = m.Parts
	} else {
		wire.Content = m.Content
	}
	return json.Marshal(wire)
}

type contentPartDTO struct {
	Type       string           `json:"type"`
	Text       string           `json:"text,omitempty"`
	ImageURL   *imageURLPartDTO `json:"image_url,omitempty"`
	InputAudio *inputAudioDTO   `json:"input_audio,omitempty"`
	// File carries a "file" part's source (STATUS.md's P2 multimodal
	// input). Unlike image_url/input_audio, this is not a literal OpenAI
	// Chat Completions wire shape — OpenAI defines no chat-completions file
	// block, only the Responses API's differently-shaped "input_file". This
	// repository's own convention stands in, same as Rerank's non-OpenAI
	// endpoint; no adapter today actually sends it, since nothing publishes
	// CapabilityDocumentInput yet.
	//
	// File 承载一个 "file" 部件的来源（STATUS.md 的 P2 多模态输入）。与
	// image_url/input_audio 不同，这不是 OpenAI Chat Completions 的字面 wire
	// 形状——OpenAI 未定义 chat completions 的文件块，只有形状不同的
	// Responses API "input_file"。本仓库自己的约定在此代为承载，与 Rerank
	// 那个非 OpenAI 端点同一先例；今天没有任何适配器会真的发出它，因为还
	// 没有谁发布 CapabilityDocumentInput。
	File *filePartDTO `json:"file,omitempty"`
}

type imageURLPartDTO struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// inputAudioDTO mirrors OpenAI Chat Completions' input_audio shape exactly:
// base64 data plus a format string.
type inputAudioDTO struct {
	Data   string `json:"data"`
	Format string `json:"format,omitempty"`
}

type filePartDTO struct {
	FileData string `json:"file_data"`
	Filename string `json:"filename,omitempty"`
}

type toolCallDTO struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function functionCallDTO `json:"function"`
}

type functionCallDTO struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type usageDTO struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type toolDTO struct {
	Type     string         `json:"type"`
	Function functionDefDTO `json:"function"`
}

type functionDefDTO struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type responseFormatDTO struct {
	Type       string               `json:"type"`
	JSONSchema *jsonSchemaFormatDTO `json:"json_schema,omitempty"`
}

type jsonSchemaFormatDTO struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

type chatCompletionRequest struct {
	Model    string           `json:"model"`
	Messages []chatMessageDTO `json:"messages"`

	// Pointer/slice fields use omitempty so a field the caller left unset
	// on runtime.ChatRequest is absent from the wire request rather than
	// sent as an explicit zero value; a non-nil pointer to a zero value
	// (e.g. Temperature pointing at 0.0) is still marshaled, since
	// omitempty on a pointer only checks nil-ness.
	Temperature    *float64           `json:"temperature,omitempty"`
	TopP           *float64           `json:"top_p,omitempty"`
	MaxTokens      *int               `json:"max_tokens,omitempty"`
	Stop           []string           `json:"stop,omitempty"`
	Seed           *int64             `json:"seed,omitempty"`
	Tools          []toolDTO          `json:"tools,omitempty"`
	ToolChoice     string             `json:"tool_choice,omitempty"`
	ResponseFormat *responseFormatDTO `json:"response_format,omitempty"`
}

// modeledChatFields is the fixed set of wire field names the request DTOs
// in this file and stream.go already model. A runtime.ChatRequest.Extra key
// matching one of these is rejected regardless of whether the modeled field
// happens to be set on this particular request — the collision is against
// the field name, not against the current marshal output.
var modeledChatFields = map[string]bool{
	"model": true, "messages": true, "temperature": true, "top_p": true,
	"max_tokens": true, "stop": true, "seed": true, "tools": true,
	"tool_choice": true, "response_format": true, "stream": true, "stream_options": true,
}

type chatChoiceDTO struct {
	Message      chatMessageDTO `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

type chatCompletionResponse struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Created int64           `json:"created"`
	Choices []chatChoiceDTO `json:"choices"`
	Usage   usageDTO        `json:"usage"`
}

func toMessageDTO(m runtime.ChatMessage) chatMessageDTO {
	dto := chatMessageDTO{
		Role:       m.Role,
		Content:    m.Content,
		Parts:      toContentPartDTOs(m.ContentParts),
		Name:       m.Name,
		ToolCallID: m.ToolCallID,
	}
	for _, tc := range m.ToolCalls {
		dto.ToolCalls = append(dto.ToolCalls, toolCallDTO{
			ID:   tc.ID,
			Type: tc.Type,
			Function: functionCallDTO{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return dto
}

func fromMessageDTO(dto chatMessageDTO) runtime.ChatMessage {
	m := runtime.ChatMessage{
		Role:       dto.Role,
		Content:    dto.Content,
		Name:       dto.Name,
		ToolCallID: dto.ToolCallID,
	}
	for _, tc := range dto.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, runtime.ToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: runtime.FunctionCall{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	return m
}

func toContentPartDTOs(parts []runtime.ContentPart) []contentPartDTO {
	if len(parts) == 0 {
		return nil
	}
	out := make([]contentPartDTO, len(parts))
	for i, p := range parts {
		dto := contentPartDTO{Type: p.Type, Text: p.Text}
		if p.ImageURL != nil {
			dto.ImageURL = &imageURLPartDTO{URL: p.ImageURL.URL, Detail: p.ImageURL.Detail}
		}
		if p.Audio != nil {
			dto.InputAudio = &inputAudioDTO{Data: p.Audio.Data, Format: p.Audio.Format}
		}
		if p.File != nil {
			dto.File = &filePartDTO{FileData: p.File.URL, Filename: p.File.Filename}
		}
		out[i] = dto
	}
	return out
}

func toToolDTO(t runtime.Tool) toolDTO {
	return toolDTO{
		Type: t.Type,
		Function: functionDefDTO{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		},
	}
}

func toResponseFormatDTO(rf *runtime.ResponseFormat) *responseFormatDTO {
	if rf == nil {
		return nil
	}
	dto := &responseFormatDTO{Type: rf.Type}
	if rf.JSONSchema != nil {
		dto.JSONSchema = &jsonSchemaFormatDTO{
			Name:   rf.JSONSchema.Name,
			Strict: rf.JSONSchema.Strict,
			Schema: rf.JSONSchema.Schema,
		}
	}
	return dto
}

// buildChatCompletionRequest converts req's modeled fields into the wire
// DTO shared by Chat and ChatStream. It does not touch req.Extra — callers
// merge that separately via Client.mergeExtraFields, after any
// stream-specific fields (stream, stream_options) have been added.
func buildChatCompletionRequest(req runtime.ChatRequest) chatCompletionRequest {
	dto := chatCompletionRequest{
		Model:          req.Model,
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		MaxTokens:      req.MaxTokens,
		Stop:           req.Stop,
		Seed:           req.Seed,
		ToolChoice:     req.ToolChoice,
		ResponseFormat: toResponseFormatDTO(req.ResponseFormat),
	}
	for _, m := range req.Messages {
		dto.Messages = append(dto.Messages, toMessageDTO(m))
	}
	for _, tool := range req.Tools {
		dto.Tools = append(dto.Tools, toToolDTO(tool))
	}
	return dto
}

// Chat calls POST /v1/chat/completions and returns the first choice in the
// response. It returns a *runtime.RuntimeError with Code ErrorProtocol if
// the backend responds with no choices, and with Code ErrorInvalidConfig if
// req.Extra collides with a modeled field name.
func Chat(ctx context.Context, c *Client, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	dtoReq := buildChatCompletionRequest(req)
	body, err := c.mergeExtraFields("chat", dtoReq, req.Extra, modeledChatFields)
	if err != nil {
		return runtime.ChatResponse{}, err
	}

	var dtoResp chatCompletionResponse
	if err := c.Do(ctx, "chat", http.MethodPost, "/v1/chat/completions", body, &dtoResp); err != nil {
		return runtime.ChatResponse{}, err
	}
	if len(dtoResp.Choices) == 0 {
		return runtime.ChatResponse{}, &runtime.RuntimeError{
			Code:      runtime.ErrorProtocol,
			RuntimeID: c.runtimeID,
			Kind:      c.kind,
			Operation: "chat",
			Message:   "backend returned no choices",
		}
	}
	choice := dtoResp.Choices[0]
	return runtime.ChatResponse{
		ID:      dtoResp.ID,
		Model:   dtoResp.Model,
		Message: fromMessageDTO(choice.Message),
		Usage: runtime.Usage{
			PromptTokens:     dtoResp.Usage.PromptTokens,
			CompletionTokens: dtoResp.Usage.CompletionTokens,
			TotalTokens:      dtoResp.Usage.TotalTokens,
		},
		FinishReason: choice.FinishReason,
		CreatedAt:    time.Unix(dtoResp.Created, 0).UTC(),
	}, nil
}
