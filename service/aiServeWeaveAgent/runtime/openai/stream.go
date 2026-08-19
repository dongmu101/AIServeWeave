package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

const (
	defaultSSEMaxLineBytes  = 1 << 20 // 1 MiB
	defaultSSEMaxEventBytes = 4 << 20 // 4 MiB
)

type chatStreamRequestDTO struct {
	chatCompletionRequest
	Stream        bool              `json:"stream"`
	StreamOptions *streamOptionsDTO `json:"stream_options,omitempty"`
}

// streamOptionsDTO requests that the final SSE chunk carry usage, matching
// OpenAI's stream_options.include_usage. Without it, some backends omit
// usage entirely from a streamed response.
type streamOptionsDTO struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatStreamChunkDTO struct {
	ID      string                `json:"id"`
	Model   string                `json:"model"`
	Choices []chatStreamChoiceDTO `json:"choices"`
	Usage   *usageDTO             `json:"usage"`
}

type chatStreamChoiceDTO struct {
	Delta        chatMessageDeltaDTO `json:"delta"`
	FinishReason *string             `json:"finish_reason"`
}

type chatMessageDeltaDTO struct {
	Role      string             `json:"role"`
	Content   string             `json:"content"`
	ToolCalls []toolCallDeltaDTO `json:"tool_calls"`
}

type toolCallDeltaDTO struct {
	Index    int                  `json:"index"`
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function functionCallDeltaDTO `json:"function"`
}

type functionCallDeltaDTO struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatEventStream wraps runtime.ChanStream so that Close also cancels the
// underlying HTTP request context, unblocking a producer goroutine that is
// stuck waiting on a network read rather than waiting to Send.
type chatEventStream struct {
	*runtime.ChanStream[runtime.ChatEvent]
	cancel    context.CancelFunc
	idleTimer *time.Timer
}

func (s *chatEventStream) Close() error {
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	s.cancel()
	return s.ChanStream.Close()
}

// ChatStream calls POST /v1/chat/completions with stream:true and decodes
// the SSE response into runtime.ChatEvent values.
//
// idleTimeout, if positive, aborts the request and closes the stream with
// an ErrorTimeout RuntimeError if no SSE event arrives within that window;
// 0 disables the idle watchdog. The returned Stream must be closed by the
// caller; closing it cancels the underlying HTTP request.
func ChatStream(ctx context.Context, c *Client, req runtime.ChatRequest, idleTimeout time.Duration) (runtime.Stream[runtime.ChatEvent], error) {
	dtoReq := chatStreamRequestDTO{
		chatCompletionRequest: buildChatCompletionRequest(req),
		Stream:                true,
		StreamOptions:         &streamOptionsDTO{IncludeUsage: true},
	}
	body, err := c.mergeExtraFields("chat_stream", dtoReq, req.Extra, modeledChatFields)
	if err != nil {
		return nil, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	resp, err := c.doRaw(streamCtx, "chat_stream", http.MethodPost, "/v1/chat/completions", body)
	if err != nil {
		cancel()
		return nil, err
	}

	out := runtime.NewChanStream[runtime.ChatEvent](0)
	var idleTimer *time.Timer
	if idleTimeout > 0 {
		idleTimer = time.AfterFunc(idleTimeout, cancel)
	}

	go decodeChatStream(c, resp, out, idleTimer, idleTimeout)

	return &chatEventStream{ChanStream: out, cancel: cancel, idleTimer: idleTimer}, nil
}

func decodeChatStream(c *Client, resp *http.Response, out *runtime.ChanStream[runtime.ChatEvent], idleTimer *time.Timer, idleTimeout time.Duration) {
	defer func() {
		if idleTimer != nil {
			idleTimer.Stop()
		}
	}()
	defer resp.Body.Close()

	reader := newSSEReader(resp.Body, defaultSSEMaxLineBytes, defaultSSEMaxEventBytes)
	for {
		frame, err := reader.readFrame()
		if idleTimer != nil {
			idleTimer.Reset(idleTimeout)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, ErrSSEDone) {
				out.CloseWithError(nil)
				return
			}
			out.CloseWithError(sseDecodeError(c, err))
			return
		}
		if frame.Data == "" {
			continue
		}

		var chunk chatStreamChunkDTO
		if err := json.Unmarshal([]byte(frame.Data), &chunk); err != nil {
			out.CloseWithError(sseDecodeError(c, err))
			return
		}

		if !out.Send(chatEventFromChunk(chunk)) {
			return // consumer closed the stream
		}
	}
}

func chatEventFromChunk(chunk chatStreamChunkDTO) runtime.ChatEvent {
	event := runtime.ChatEvent{ID: chunk.ID, Model: chunk.Model}
	if chunk.Usage != nil {
		event.Usage = &runtime.Usage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
		}
	}
	if len(chunk.Choices) == 0 {
		return event
	}
	choice := chunk.Choices[0]
	event.Delta = runtime.ChatMessageDelta{Role: choice.Delta.Role, Content: choice.Delta.Content}
	for _, tc := range choice.Delta.ToolCalls {
		event.Delta.ToolCalls = append(event.Delta.ToolCalls, runtime.ToolCallDelta{
			Index: tc.Index,
			ID:    tc.ID,
			Type:  tc.Type,
			Function: runtime.FunctionCallDelta{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}
	if choice.FinishReason != nil {
		event.FinishReason = *choice.FinishReason
	}
	return event
}

// sseDecodeError classifies a stream-decode failure as a timeout when it
// stems from the request context ending (idle timeout or caller
// cancellation reads back as a body-read error), and as a protocol error
// otherwise (malformed SSE framing or malformed JSON payload).
func sseDecodeError(c *Client, err error) *runtime.RuntimeError {
	code := runtime.ErrorProtocol
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code = runtime.ErrorTimeout
	}
	return &runtime.RuntimeError{
		Code:      code,
		RuntimeID: c.runtimeID,
		Kind:      c.kind,
		Operation: "chat_stream",
		Message:   "sse stream ended with an error",
		Cause:     err,
	}
}
