package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

type chatMessageDTO struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	Name       string        `json:"name,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCallDTO `json:"tool_calls,omitempty"`
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
