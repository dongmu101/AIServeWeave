package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
)

// responses implements POST /v1/responses.
//
// responses 实现 POST /v1/responses。
func (h *handlers) responses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req responsesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "the request body is not valid JSON")
		return
	}
	if req.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "model is required")
		return
	}
	if field := req.unsupported(); field != "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "unsupported_parameter",
			field+" is not supported by this gateway: responses are not stored and each request is scheduled independently")
		return
	}
	canonical, err := req.toRuntime()
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		return
	}

	if req.Stream {
		h.responsesStream(w, r, req, canonical, start)
		return
	}
	h.responsesOnce(w, r, req, canonical, start)
}

// responsesOnce serves a non-streaming response.
//
// responsesOnce 服务一次非流式响应。
func (h *handlers) responsesOnce(w http.ResponseWriter, r *http.Request, req responsesRequest, canonical runtime.ChatRequest, start time.Time) {
	resp, _, err := h.sched.Chat(r.Context(), canonical)
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}

	body := req.render(newResponseID(), resp.Model, h.clock.Now())
	body.Status, body.IncompleteDetails = statusFor(resp.FinishReason)
	body.Output = outputItemsFor(resp.Message)
	body.Usage = usageFor(resp.Usage)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
	h.recordUsage(r.Context(), resp.Usage, time.Since(start))
}

// render builds the response envelope, echoing back the request parameters a
// Responses client expects to find on it.
//
// render 构造响应信封，把 Responses 客户端期望在上面找到的请求参数回显回去。
func (req responsesRequest) render(id, model string, now time.Time) responseObject {
	if model == "" {
		model = req.Model
	}
	obj := responseObject{
		ID:                id,
		Object:            "response",
		CreatedAt:         now.Unix(),
		Model:             model,
		Output:            []responseOutputRaw{},
		MaxOutputTokens:   req.MaxOutputTokens,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		ParallelToolCalls: true,
		Tools:             req.Tools,
		ToolChoice:        "auto",
	}
	if obj.Tools == nil {
		obj.Tools = []responsesTool{}
	}
	if req.Instructions != "" {
		instructions := req.Instructions
		obj.Instructions = &instructions
	}
	return obj
}

// statusFor maps a backend finish reason onto the Responses status vocabulary.
// "length" is the one that is not a completion: the answer stopped because it
// hit a bound, and a client that treated it as complete would silently lose
// the tail.
//
// statusFor 把后端的结束原因映射到 Responses 的状态词汇上。"length" 是其中唯一不算
// 完成的那个：答案因为撞到上限而停止，把它当作完成的客户端会悄无声息地丢掉尾巴。
func statusFor(finishReason string) (string, *incompleteDetails) {
	if finishReason == "length" {
		return "incomplete", &incompleteDetails{Reason: "max_output_tokens"}
	}
	return "completed", nil
}

// usageFor renders a backend's token counts, or nothing when it reported none.
// Sending zeros would tell a client this request cost nothing, which is a
// different claim from "the backend did not say" — and the first one is the
// kind of number that ends up in somebody's cost dashboard.
//
// usageFor 渲染后端的 token 计数；后端什么都没报时返回 nothing。发送零值等于告诉
// 客户端这次请求不花钱，而那与「后端没说」是两个不同的断言——前者正是那种会出现在
// 某人成本看板上的数字。
func usageFor(usage runtime.Usage) *responsesUsage {
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	return &responsesUsage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		TotalTokens:  usage.TotalTokens,
	}
}

// outputItemsFor renders an assistant message as Responses output items. A
// message with tool calls produces one function_call item each and no message
// item: the model chose to call rather than to answer.
//
// outputItemsFor 把一条 assistant 消息渲染成 Responses 的输出项。带工具调用的消息
// 会产出每个调用一个 function_call 项、且不产出 message 项：模型选择的是调用而不是
// 作答。
func outputItemsFor(msg runtime.ChatMessage) []responseOutputRaw {
	if len(msg.ToolCalls) > 0 {
		items := make([]responseOutputRaw, len(msg.ToolCalls))
		for i, call := range msg.ToolCalls {
			items[i] = responseOutputRaw{
				Type:      "function_call",
				ID:        newItemID("fc"),
				Status:    "completed",
				CallID:    call.ID,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			}
		}
		return items
	}
	return []responseOutputRaw{{
		Type:   "message",
		ID:     newItemID("msg"),
		Status: "completed",
		Role:   "assistant",
		Content: []responseContentPart{{
			Type:        "output_text",
			Text:        msg.Content,
			Annotations: []any{},
		}},
	}}
}

// -----------------------------------------------------------------------
// Streaming
// -----------------------------------------------------------------------

// responsesStream serves the SSE form. Unlike Chat Completions, whose stream
// is a flat run of chunks, Responses wraps the text in a nested lifecycle —
// response, then item, then content part — and a client's state machine is
// built on those boundaries. So the events are emitted in that order even
// though the backend below only ever hands up a flat run of deltas.
//
// responsesStream 服务 SSE 形式。与 Chat Completions 那种扁平 chunk 流不同，Responses
// 把文本包在一层嵌套的生命周期里——先 response、再 item、再 content part——而客户端的
// 状态机正是建立在这些边界上的。因此即便下面的后端始终只递上来一串扁平的 delta，事件
// 也要按那个顺序发出。
func (h *handlers) responsesStream(w http.ResponseWriter, r *http.Request, req responsesRequest, canonical runtime.ChatRequest, start time.Time) {
	stream, candidate, err := h.sched.ChatStream(r.Context(), canonical)
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}
	defer stream.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "api_error", "streaming_unsupported", "this connection does not support streaming")
		return
	}

	writeSSEHeader(w)
	w.WriteHeader(http.StatusOK)

	em := &responsesEmitter{w: w, flusher: flusher}
	obj := req.render(newResponseID(), req.Model, h.clock.Now())
	obj.Status = "in_progress"
	em.event("response.created", map[string]any{"response": obj})
	em.event("response.in_progress", map[string]any{"response": obj})

	itemID := newItemID("msg")
	var text strings.Builder
	var usage runtime.Usage
	var finishReason string
	opened := false
	loggedTTFT := false

	for {
		ev, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			h.logger.Error("responses stream failed",
				slog.Any("error", recvErr), slog.String("node_id", candidate.NodeID))
			obj.Status = "failed"
			obj.Error = &responsesError{Code: "server_error", Message: "the stream ended before the response was complete"}
			em.event("response.failed", map[string]any{"response": obj})
			return
		}
		if ev.Usage != nil {
			usage = *ev.Usage
		}
		if ev.FinishReason != "" {
			finishReason = ev.FinishReason
		}
		if ev.Delta.Content == "" {
			continue
		}
		if !opened {
			opened = true
			// The item and its content part are announced before the first
			// delta, because a delta names the item it belongs to and a client
			// that has not seen that item yet has nowhere to put it.
			//
			// 项目与它的内容部件在第一个 delta 之前宣告，因为 delta 会点名它所属的
			// 项目，而尚未见过该项目的客户端无处安放它。
			em.event("response.output_item.added", map[string]any{
				"output_index": 0,
				"item": responseOutputRaw{
					Type: "message", ID: itemID, Status: "in_progress", Role: "assistant",
					Content: []responseContentPart{},
				},
			})
			em.event("response.content_part.added", map[string]any{
				"item_id": itemID, "output_index": 0, "content_index": 0,
				"part": responseContentPart{Type: "output_text", Text: "", Annotations: []any{}},
			})
		}
		text.WriteString(ev.Delta.Content)
		em.event("response.output_text.delta", map[string]any{
			"item_id": itemID, "output_index": 0, "content_index": 0, "delta": ev.Delta.Content,
		})
		if !loggedTTFT {
			loggedTTFT = true
			h.metrics.TTFT(EndpointResponses, time.Since(start))
		}
	}

	if opened {
		final := responseContentPart{Type: "output_text", Text: text.String(), Annotations: []any{}}
		em.event("response.output_text.done", map[string]any{
			"item_id": itemID, "output_index": 0, "content_index": 0, "text": text.String(),
		})
		em.event("response.content_part.done", map[string]any{
			"item_id": itemID, "output_index": 0, "content_index": 0, "part": final,
		})
		em.event("response.output_item.done", map[string]any{
			"output_index": 0,
			"item": responseOutputRaw{
				Type: "message", ID: itemID, Status: "completed", Role: "assistant",
				Content: []responseContentPart{final},
			},
		})
		obj.Output = []responseOutputRaw{{
			Type: "message", ID: itemID, Status: "completed", Role: "assistant",
			Content: []responseContentPart{final},
		}}
	}
	obj.Status, obj.IncompleteDetails = statusFor(finishReason)
	obj.Usage = usageFor(usage)
	em.event("response.completed", map[string]any{"response": obj})
	h.recordUsage(r.Context(), usage, time.Since(start))
}

// responsesEmitter writes SSE frames, numbering them as it goes. The sequence
// number is what lets a client detect a dropped frame, so it is owned here
// rather than left to each call site to remember to increment.
//
// responsesEmitter 写出 SSE 帧并顺带编号。序号正是客户端用来发现丢帧的东西，因此它
// 归本处所有，而不是留给每个调用点自己记得加一。
type responsesEmitter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	sequence int
}

// event writes one named SSE frame carrying type and sequence_number plus the
// event's own fields.
//
// event 写出一个具名 SSE 帧，携带 type、sequence_number 以及该事件自己的字段。
func (e *responsesEmitter) event(name string, fields map[string]any) {
	payload := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		payload[k] = v
	}
	payload["type"] = name
	payload["sequence_number"] = e.sequence
	e.sequence++
	if err := writeNamedSSE(e.w, name, payload); err != nil {
		return
	}
	e.flusher.Flush()
}

func newResponseID() string { return "resp_" + newRequestID() }

func newItemID(prefix string) string { return prefix + "_" + newRequestID() }
