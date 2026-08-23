package tunnelserver

import (
	"context"
	"io"
	"sync"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
)

// NodeRuntime presents one runtime instance on one node as an ordinary
// runtime.InferenceRuntime and runtime.WorkflowRuntime, with the tunnel in the
// middle.
//
// This is why the runtime contract lives in common/: the Gateway's scheduler
// and API layer program against the same interfaces the Agent's adapters
// implement, so a request travelling over a tunnel and one served in-process
// are the same shape of call. Anything that reaches through this type reaches
// the node, and nothing else.
//
// The read-only halves of the contract — Descriptor, Probe, Health, Discover —
// are answered from the inventory the Agent reports on its Control stream, not
// by a round trip. There is no probe operation in the tunnel protocol on
// purpose: probing is the Agent's job, done once against a local backend, and
// asking every Gateway replica to re-probe every node would multiply that
// traffic by the size of the fleet.
type NodeRuntime struct {
	srv       *Server
	nodeID    string
	runtimeID string
}

var (
	_ runtime.InferenceRuntime = (*NodeRuntime)(nil)
	_ runtime.WorkflowRuntime  = (*NodeRuntime)(nil)
)

// Runtime returns a handle to one runtime instance on one node. The handle is
// cheap and stateless: it resolves the node on each call, so it stays valid
// across a reconnect and fails cleanly when the node is gone.
func (s *Server) Runtime(nodeID, runtimeID string) *NodeRuntime {
	return &NodeRuntime{srv: s, nodeID: nodeID, runtimeID: runtimeID}
}

// NodeID reports which node this handle dispatches to.
func (r *NodeRuntime) NodeID() string { return r.nodeID }

// snapshot returns the instance's last reported state.
func (r *NodeRuntime) snapshot() (runtime.Snapshot, error) {
	n, ok := r.srv.lookup(r.nodeID)
	if !ok {
		return runtime.Snapshot{}, r.err(runtime.ErrorConnection, "node is not connected to this replica", true, ErrNodeNotConnected)
	}
	n.mu.Lock()
	snap, ok := n.snapshots[r.runtimeID]
	n.mu.Unlock()
	if !ok {
		return runtime.Snapshot{}, r.err(runtime.ErrorInvalidConfig, "node reports no such runtime", false, runtime.ErrRuntimeNotFound)
	}
	return snap, nil
}

func (r *NodeRuntime) err(code runtime.ErrorCode, msg string, retryable bool, cause error) error {
	return &runtime.RuntimeError{
		Code:      code,
		RuntimeID: r.runtimeID,
		Operation: "tunnel_dispatch",
		Message:   msg,
		Retryable: retryable,
		Cause:     cause,
	}
}

// Descriptor returns the instance's scheduling summary as last reported. An
// unknown instance yields the zero Descriptor rather than an error, because
// Descriptor has no error return in the contract; callers that need to know
// whether the instance exists use Health or Discover.
func (r *NodeRuntime) Descriptor() runtime.Descriptor {
	snap, err := r.snapshot()
	if err != nil {
		return runtime.Descriptor{}
	}
	return snap.Descriptor
}

// Probe returns the probe evidence the Agent collected locally.
func (r *NodeRuntime) Probe(context.Context) (runtime.ProbeResult, error) {
	snap, err := r.snapshot()
	if err != nil {
		return runtime.ProbeResult{}, err
	}
	return snap.Probe, nil
}

// Health returns the Agent's latest health report for the instance.
func (r *NodeRuntime) Health(context.Context) (runtime.HealthReport, error) {
	snap, err := r.snapshot()
	if err != nil {
		return runtime.HealthReport{}, err
	}
	return snap.Health, nil
}

// Discover returns the Agent's latest capability and model discovery.
func (r *NodeRuntime) Discover(context.Context) (runtime.Discovery, error) {
	snap, err := r.snapshot()
	if err != nil {
		return runtime.Discovery{}, err
	}
	return snap.Discovery, nil
}

// Close releases the handle. It closes nothing on the node: the tunnel and its
// slots belong to the connection, not to a caller's view of one instance.
func (r *NodeRuntime) Close() error { return nil }

// -----------------------------------------------------------------------
// Inference
// -----------------------------------------------------------------------

// ListModels returns the models the instance currently serves.
func (r *NodeRuntime) ListModels(ctx context.Context) ([]runtime.Model, error) {
	payload, err := r.single(ctx, tunnelv1.Operation_OPERATION_LIST_MODELS, nil, nil)
	if err != nil {
		return nil, err
	}
	return tunnelwire.UnmarshalModels(payload)
}

// Chat runs a non-streaming chat completion on the node.
func (r *NodeRuntime) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	payload, err := tunnelwire.MarshalChatRequest(req)
	if err != nil {
		return runtime.ChatResponse{}, err
	}
	body, err := r.single(ctx, tunnelv1.Operation_OPERATION_CHAT, payload, nil)
	if err != nil {
		return runtime.ChatResponse{}, err
	}
	return tunnelwire.UnmarshalChatResponse(body)
}

// ChatStream runs a streaming chat completion. Each DataChunk carries exactly
// one event and is handed to the caller the moment it arrives, so the tunnel
// adds a hop but not a buffer.
func (r *NodeRuntime) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	payload, err := tunnelwire.MarshalChatRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := r.srv.Dispatch(ctx, r.request(tunnelv1.Operation_OPERATION_CHAT_STREAM, payload, nil))
	if err != nil {
		return nil, err
	}
	return newResponseStream(resp, tunnelwire.UnmarshalChatEvent), nil
}

// Embed computes embeddings on the node.
func (r *NodeRuntime) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	payload, err := tunnelwire.MarshalEmbeddingRequest(req)
	if err != nil {
		return runtime.EmbeddingResponse{}, err
	}
	body, err := r.single(ctx, tunnelv1.Operation_OPERATION_EMBED, payload, nil)
	if err != nil {
		return runtime.EmbeddingResponse{}, err
	}
	return tunnelwire.UnmarshalEmbeddingResponse(body)
}

// -----------------------------------------------------------------------
// Workflows
// -----------------------------------------------------------------------

// Submit queues a workflow. The template travels in DataChunks rather than in
// the request headers: an API-format ComfyUI workflow is large enough to
// threaten the frame limit on its own.
func (r *NodeRuntime) Submit(ctx context.Context, req runtime.WorkflowRequest) (runtime.WorkflowRun, error) {
	template := req.Template
	stripped := req
	stripped.Template = nil
	payload, err := tunnelwire.MarshalWorkflowRequest(stripped)
	if err != nil {
		return runtime.WorkflowRun{}, err
	}
	body := func(send func([]byte) error) error {
		limit := r.srv.cfg.MaxFrameBytes
		for len(template) > 0 {
			chunk := template
			if len(chunk) > limit {
				chunk = chunk[:limit]
			}
			if err := send(chunk); err != nil {
				return err
			}
			template = template[len(chunk):]
		}
		return nil
	}
	out, err := r.single(ctx, tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT, payload, body)
	if err != nil {
		return runtime.WorkflowRun{}, err
	}
	return tunnelwire.UnmarshalWorkflowRun(out)
}

// Subscribe streams a run's normalized events.
func (r *NodeRuntime) Subscribe(ctx context.Context, runID string) (runtime.Stream[runtime.WorkflowEvent], error) {
	payload, err := tunnelwire.MarshalRunRef(runID)
	if err != nil {
		return nil, err
	}
	resp, err := r.srv.Dispatch(ctx, r.request(tunnelv1.Operation_OPERATION_WORKFLOW_SUBSCRIBE, payload, nil))
	if err != nil {
		return nil, err
	}
	return newResponseStream(resp, tunnelwire.UnmarshalWorkflowEvent), nil
}

// Status returns a run's status, read from the backend's queue and history so
// it stays accurate even when events were missed.
func (r *NodeRuntime) Status(ctx context.Context, runID string) (runtime.WorkflowStatus, error) {
	payload, err := tunnelwire.MarshalRunRef(runID)
	if err != nil {
		return runtime.WorkflowStatus{}, err
	}
	body, err := r.single(ctx, tunnelv1.Operation_OPERATION_WORKFLOW_STATUS, payload, nil)
	if err != nil {
		return runtime.WorkflowStatus{}, err
	}
	return tunnelwire.UnmarshalWorkflowStatus(body)
}

// Cancel stops a run.
func (r *NodeRuntime) Cancel(ctx context.Context, runID string) error {
	payload, err := tunnelwire.MarshalRunRef(runID)
	if err != nil {
		return err
	}
	_, err = r.single(ctx, tunnelv1.Operation_OPERATION_WORKFLOW_CANCEL, payload, nil)
	return err
}

// OpenArtifact streams one output artifact. The body is read from the tunnel
// as the caller reads it: a large artifact is never held whole in this
// process, and it travels on a bulk slot so it cannot displace inference.
func (r *NodeRuntime) OpenArtifact(ctx context.Context, ref runtime.ArtifactRef) (runtime.Artifact, error) {
	payload, err := tunnelwire.MarshalArtifactRef(ref)
	if err != nil {
		return runtime.Artifact{}, err
	}
	resp, err := r.srv.Dispatch(ctx, r.request(tunnelv1.Operation_OPERATION_ARTIFACT_OPEN, payload, nil))
	if err != nil {
		return runtime.Artifact{}, err
	}

	// One read pulls the ResponseHeaders through, since they always precede
	// the body. The chunk it yields is held as the first piece to hand back,
	// not buffered further.
	first, err := resp.Recv()
	if err != nil && err != io.EOF {
		_ = resp.Close()
		return runtime.Artifact{}, err
	}
	headers := resp.Headers()
	size := int64(-1)
	contentType := ""
	if headers != nil {
		size = headers.GetSize()
		contentType = headers.GetContentType()
	}
	return runtime.Artifact{
		Ref:         ref,
		ContentType: contentType,
		Size:        size,
		Body:        &artifactBody{resp: resp, pending: first, done: err == io.EOF},
	}, nil
}

// -----------------------------------------------------------------------
// Plumbing
// -----------------------------------------------------------------------

func (r *NodeRuntime) request(op tunnelv1.Operation, payload []byte, body func(func([]byte) error) error) Request {
	return Request{
		NodeID:    r.nodeID,
		RuntimeID: r.runtimeID,
		Operation: op,
		Payload:   payload,
		Body:      body,
	}
}

// single runs an operation whose response is one payload or none, and returns
// that payload. It always closes the response, so a caller cannot strand a
// slot by forgetting to.
func (r *NodeRuntime) single(ctx context.Context, op tunnelv1.Operation, payload []byte, body func(func([]byte) error) error) ([]byte, error) {
	resp, err := r.srv.Dispatch(ctx, r.request(op, payload, body))
	if err != nil {
		return nil, err
	}
	defer resp.Close()

	out, err := resp.Recv()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Drain to ResponseEnd so the error a failing operation reports after
	// its payload is not lost, and so the slot is released for reuse.
	if _, err := resp.Recv(); err != nil && err != io.EOF {
		return nil, err
	}
	return out, nil
}

// responseStream adapts a Response to runtime.Stream. It decodes on the
// caller's goroutine rather than in a pump: an extra goroutine per stream
// would buy nothing but a buffer, and a buffer is exactly what a token stream
// must not have.
type responseStream[T any] struct {
	resp   *Response
	decode func([]byte) (T, error)

	mu        sync.Mutex
	committed bool
	closed    bool
}

func newResponseStream[T any](resp *Response, decode func([]byte) (T, error)) *responseStream[T] {
	return &responseStream[T]{resp: resp, decode: decode}
}

func (s *responseStream[T]) Recv() (T, error) {
	var zero T
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return zero, runtime.ErrStreamClosed
	}

	payload, err := s.resp.Recv()
	if err != nil {
		return zero, err
	}
	item, err := s.decode(payload)
	if err != nil {
		return zero, err
	}
	s.mu.Lock()
	s.committed = true
	s.mu.Unlock()
	return item, nil
}

// Committed reports whether an event has already been handed to the consumer.
// Once true the request must not be transparently retried: part of the answer
// is already downstream.
func (s *responseStream[T]) Committed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.committed
}

func (s *responseStream[T]) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.resp.Close()
}

// artifactBody turns a Response into the io.ReadCloser runtime.Artifact
// promises, carrying at most one chunk at a time.
type artifactBody struct {
	resp    *Response
	pending []byte
	done    bool
}

func (b *artifactBody) Read(p []byte) (int, error) {
	for len(b.pending) == 0 {
		if b.done {
			return 0, io.EOF
		}
		chunk, err := b.resp.Recv()
		if err != nil {
			if err == io.EOF {
				b.done = true
			}
			return 0, err
		}
		b.pending = chunk
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *artifactBody) Close() error { return b.resp.Close() }
