package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"AIServeWeave/common/runtime"
)

// jobEventJSON is one frame on a job's SSE stream. It names the job by this
// Gateway's own id, and carries the backend's own payload under Data:
// progress numbers and node outputs live only there, and a stream that
// dropped them would report that something is happening without ever saying
// how far along it is. The adapter has already size-limited that payload.
//
// jobEventJSON 是 job SSE 流上的一帧。它用本 Gateway 自己的 id 指称该 job，并把后端
// 自己的载荷放在 Data 下：进度数字与节点输出只存在于那里，丢掉它的流只会报告「有事
// 在发生」，却始终说不出进行到了哪一步。该载荷的大小已由适配器限制过。
type jobEventJSON struct {
	JobID string `json:"job_id"`
	Type  string `json:"type"`
	// Node is the graph node the event is about, when the event is about one.
	//
	// Node 是该事件所涉及的图节点（当它确实涉及某个节点时）。
	Node string `json:"node,omitempty"`
	// Status is the job's public state, sent on the frame that ends the
	// stream so a client has the verdict without a follow-up request.
	//
	// Status 是 job 的公开状态，随结束该流的那一帧发出，好让客户端无需再发一次请求
	// 就拿到结论。
	Status     string          `json:"status,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	ReceivedAt time.Time       `json:"received_at"`
}

// jobEvents implements GET /v1/jobs/{job_id}/events.
//
// A run that already finished is answered with a single frame carrying its
// final state rather than a subscription: the events are gone, and opening a
// backend stream that has nothing to say would leave the caller waiting on
// silence.
//
// jobEvents 实现 GET /v1/jobs/{job_id}/events。
//
// 已经结束的运行用单独一帧携带终态作答，而不是去订阅：那些事件已经过去了，开一条
// 无话可说的后端流只会让调用方对着沉默干等。
func (h *handlers) jobEvents(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	j, ok := h.jobs.get(r.PathValue("job_id"), identity.TenantID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "job_not_found", "no such job")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "api_error", "streaming_unsupported", "this connection does not support streaming")
		return
	}

	if j.terminal() {
		writeSSEHeader(w)
		_ = writeNamedSSE(w, publicStatus(j.State), jobEventJSON{
			JobID:      j.ID,
			Type:       publicStatus(j.State),
			Status:     publicStatus(j.State),
			ReceivedAt: h.clock.Now(),
		})
		flusher.Flush()
		return
	}

	// Subscribing can still fail — the node may be gone, or its runtime may
	// not advertise workflow_events — and that has to be an HTTP status
	// rather than an SSE frame, so it happens before a single header is
	// written.
	//
	// 订阅仍可能失败——节点可能已经不在，或它的 runtime 并不声明 workflow_events
	// ——那必须是一个 HTTP 状态码而不是一帧 SSE，因此它发生在写出任何响应头之前。
	stream, err := h.sched.WorkflowEvents(r.Context(), j.Candidate, j.RunID)
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}
	defer stream.Close()

	writeSSEHeader(w)
	w.WriteHeader(http.StatusOK)

	// A client that walks away unblocks the Recv below on its own:
	// tunnelserver.Response.Recv selects on the request's context (call.go),
	// so cancellation returns from it immediately and the deferred Close then
	// carries the cancel across the tunnel to the Agent. Nothing else is
	// needed to keep this goroutine from outliving its watcher — which matters
	// more here than on a chat stream, since a workflow can idle for the whole
	// length of a generation, per AGENTS.md's "任何一跳都不得无界缓冲".
	//
	// 调用方一走了之时，下面的 Recv 会自行解除阻塞：tunnelserver.Response.Recv 自己
	// select 请求的 context（call.go），因此取消会让它立即返回，随后被 defer 的 Close
	// 把这次取消经隧道送给 Agent。要让这个协程不比它的旁观者活得更久，不需要别的东西
	// ——而这一点在这里比在聊天流上更要紧，因为一个工作流可以空闲整个生成过程之久，
	// 对应 AGENTS.md 的「任何一跳都不得无界缓冲」。

	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			// The SSE header is already out, so an OpenAI-shaped JSON error
			// body would not parse as an event. The failure is reported as a
			// frame instead, the same way chat.go reports a broken stream.
			//
			// SSE 响应头已经发出，因此 OpenAI 形状的 JSON 错误体不会被解析成事件。
			// 这里改用一帧来报告失败，与 chat.go 报告断流的方式一致。
			h.logger.Error("workflow event stream failed",
				slog.String("job_id", j.ID),
				slog.String("node_id", j.Candidate.NodeID),
				slog.Any("error", err))
			_ = writeNamedSSE(w, "error", jobEventJSON{JobID: j.ID, Type: "error", ReceivedAt: h.clock.Now()})
			flusher.Flush()
			return
		}

		frame := jobEventJSON{
			JobID:      j.ID,
			Type:       string(ev.Type),
			Node:       ev.NodeID,
			Data:       ev.Raw,
			ReceivedAt: ev.ReceivedAt,
		}
		state, terminal := terminalState(ev.Type)
		if terminal {
			frame.Status = publicStatus(state)
			// The stream is the authority on how the run ended, so the store
			// learns it here. Without this, the status endpoint would go on
			// asking a node about a run that is already over.
			//
			// 这条流才是「运行如何结束」的权威，因此存储在这里知悉结果。没有这一步，
			// 状态端点会继续为一次早已结束的运行去打扰节点。
			h.jobs.update(j.ID, runtime.WorkflowStatus{State: state}, h.clock.Now())
		}
		if err := writeNamedSSE(w, string(ev.Type), frame); err != nil {
			return
		}
		flusher.Flush()
		if terminal {
			return
		}
	}
}

// terminalState maps the three event types that end a run onto the state they
// leave it in. Every other type — progress, node output, queue changes — is
// something happening on the way there.
//
// terminalState 把结束一次运行的三种事件类型映射到它们留下的状态。其余每一种——进度、
// 节点输出、队列变化——都是通往那里途中发生的事。
func terminalState(t runtime.WorkflowEventType) (runtime.WorkflowState, bool) {
	switch t {
	case runtime.WorkflowEventSucceeded:
		return runtime.WorkflowSucceeded, true
	case runtime.WorkflowEventFailed:
		return runtime.WorkflowFailed, true
	case runtime.WorkflowEventCancelled:
		return runtime.WorkflowCancelled, true
	default:
		return "", false
	}
}

func writeSSEHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

// writeNamedSSE writes one SSE frame with an event name, so a browser client
// can register a listener per event type instead of parsing every frame to
// find the ones it cares about.
//
// writeNamedSSE 写出一帧带事件名的 SSE，好让浏览器客户端能按事件类型各注册一个监听器，
// 而不必解析每一帧去找自己关心的那些。
func writeNamedSSE(w io.Writer, name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("event: " + name + "\ndata: ")); err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n\n"))
	return err
}
