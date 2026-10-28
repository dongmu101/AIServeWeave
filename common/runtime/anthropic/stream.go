package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/oaibase"
	"AIServeWeave/common/runtime/openai"
)

const (
	maxSSELineBytes  = 1 << 20
	maxSSEEventBytes = 4 << 20
)

// eventStream releases the request's context and concurrency slot on Close.
// eventStream 在 Close 时释放请求的 context 与并发槽位。
type eventStream struct {
	*runtime.ChanStream[runtime.ChatEvent]
	cancel  context.CancelFunc
	release func()
	idle    *time.Timer
	done    chan struct{}
	once    sync.Once
}

// Close is idempotent; the slot is released exactly once.
// Close 可重复调用；槽位只释放一次。
func (s *eventStream) Close() error {
	s.once.Do(func() {
		if s.idle != nil {
			s.idle.Stop()
		}
		s.cancel()
		_ = s.ChanStream.Close()
		<-s.done
		s.release()
	})
	return nil
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage usage  `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
		Data     string `json:"data"`
		ID       string `json:"id"`
		Name     string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *usage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// startStream decodes Messages SSE into ChatEvents on a goroutine, one event
// at a time with synchronous backpressure. Thinking deltas become
// ReasoningContent deltas; a signature delta and a redacted_thinking block
// each become a ReasoningBlock delta, so a client that holds signatures can
// rebuild the signed blocks. A stream that ends before message_stop is
// reported as a protocol error, not as a normal end.
//
// startStream 在协程中把 Messages SSE 逐条解码为 ChatEvent，同步背压。thinking
// 增量转为 ReasoningContent 增量；签名增量与 redacted_thinking 块各自转为一个
// ReasoningBlock 增量，使能够保存签名的客户端可以重建签过名的块。在 message_stop
// 之前结束的流报告为协议错误，而不是正常结束。
func startStream(base *oaibase.Base, resp *http.Response, cancel context.CancelFunc, release func(), idleTimeout time.Duration, apiKey string) runtime.Stream[runtime.ChatEvent] {
	s := &eventStream{ChanStream: runtime.NewChanStream[runtime.ChatEvent](0), cancel: cancel, release: release, done: make(chan struct{})}
	if idleTimeout > 0 {
		s.idle = time.AfterFunc(idleTimeout, cancel)
	}
	fail := func(code runtime.ErrorCode, message string, cause error) {
		s.CloseWithError(&runtime.RuntimeError{Code: code, RuntimeID: base.Config().ID, Kind: base.Descriptor().Kind, Operation: "chat_stream",
			Message: runtime.Redact(message, apiKey), Cause: cause})
	}
	go func() {
		defer close(s.done)
		defer resp.Body.Close()
		decoder := openai.NewSSEDecoder(resp.Body, maxSSELineBytes, maxSSEEventBytes)
		var id, model string
		var input usage
		tools := map[int]int{}    // content block index -> tool call ordinal / 内容块下标 -> 工具调用序号
		hasArgs := map[int]bool{} // tool blocks that received input / 已收到参数的工具块
		for {
			_, data, err := decoder.Next()
			if s.idle != nil {
				s.idle.Reset(idleTimeout)
			}
			if err != nil {
				switch {
				case errors.Is(err, openai.ErrSSEDone), errors.Is(err, io.EOF):
					// Not wrapped: consumers test errors.Is(err, io.EOF) for a normal end.
					// 不包装原错误：消费方用 errors.Is(err, io.EOF) 判断正常结束。
					fail(runtime.ErrorProtocol, "stream ended before message_stop", io.ErrUnexpectedEOF)
				case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
					fail(runtime.ErrorTimeout, "stream interrupted", err)
				default:
					fail(runtime.ErrorProtocol, "sse stream ended with an error", err)
				}
				return
			}
			if data == "" {
				continue
			}
			var ev streamEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				fail(runtime.ErrorProtocol, "invalid stream event", err)
				return
			}
			out := runtime.ChatEvent{ID: id, Model: model}
			switch ev.Type {
			case "message_start":
				id, model, input = ev.Message.ID, ev.Message.Model, ev.Message.Usage
				out = runtime.ChatEvent{ID: id, Model: model, Delta: runtime.ChatMessageDelta{Role: "assistant"}}
			case "content_block_start":
				switch ev.ContentBlock.Type {
				case "text":
					if ev.ContentBlock.Text == "" {
						continue
					}
					out.Delta.Content = ev.ContentBlock.Text
				case "thinking":
					if ev.ContentBlock.Thinking == "" {
						continue
					}
					out.Delta.ReasoningContent = ev.ContentBlock.Thinking
				case "redacted_thinking":
					if ev.ContentBlock.Data == "" {
						continue
					}
					out.Delta.ReasoningBlock = &runtime.ReasoningBlock{Type: runtime.ReasoningBlockRedacted, Data: ev.ContentBlock.Data}
				case "tool_use":
					n := len(tools)
					tools[ev.Index] = n
					out.Delta.ToolCalls = []runtime.ToolCallDelta{{Index: n, ID: ev.ContentBlock.ID, Type: "function", Function: runtime.FunctionCallDelta{Name: ev.ContentBlock.Name}}}
				default:
					continue
				}
			case "content_block_delta":
				switch ev.Delta.Type {
				case "text_delta":
					out.Delta.Content = ev.Delta.Text
				case "thinking_delta":
					if ev.Delta.Thinking == "" {
						continue
					}
					out.Delta.ReasoningContent = ev.Delta.Thinking
				case "signature_delta":
					if ev.Delta.Signature == "" {
						continue
					}
					out.Delta.ReasoningBlock = &runtime.ReasoningBlock{Type: runtime.ReasoningBlockThinking, Signature: ev.Delta.Signature}
				case "input_json_delta":
					n, ok := tools[ev.Index]
					if !ok || ev.Delta.PartialJSON == "" {
						continue // server-side tool blocks are not client tool calls / 服务端工具块不是客户端工具调用
					}
					hasArgs[ev.Index] = true
					out.Delta.ToolCalls = []runtime.ToolCallDelta{{Index: n, Function: runtime.FunctionCallDelta{Arguments: ev.Delta.PartialJSON}}}
				default:
					continue
				}
			case "content_block_stop":
				n, ok := tools[ev.Index]
				if !ok || hasArgs[ev.Index] {
					continue
				}
				// A tool called without input streams no delta; send "{}" so the
				// arguments match the non-streaming response.
				// 无参工具调用不会流式发送任何增量；补发 "{}"，使参数与非流式响应一致。
				hasArgs[ev.Index] = true
				out.Delta.ToolCalls = []runtime.ToolCallDelta{{Index: n, Function: runtime.FunctionCallDelta{Arguments: "{}"}}}
			case "message_delta":
				out.FinishReason = finishReason(ev.Delta.StopReason)
				output := 0
				if ev.Usage != nil {
					output = ev.Usage.OutputTokens
					if ev.Usage.prompt() > 0 {
						input = *ev.Usage // cumulative counts supersede message_start / 累计值取代 message_start 中的值
					}
				}
				out.Usage = &runtime.Usage{PromptTokens: input.prompt(), CompletionTokens: output, TotalTokens: input.prompt() + output}
			case "message_stop":
				s.CloseWithError(nil)
				return
			case "error":
				message := "upstream stream error"
				if ev.Error != nil && ev.Error.Message != "" {
					message = ev.Error.Type + ": " + ev.Error.Message
				}
				fail(runtime.ErrorUpstream, message, nil)
				return
			default:
				continue // ping and future types / ping 及未来新增类型
			}
			if !s.Send(out) {
				return
			}
		}
	}()
	return s
}
