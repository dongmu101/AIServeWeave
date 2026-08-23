package tunneltest

import (
	"context"

	"AIServeWeave/common/runtime"
)

// This file holds the scriptable runtime backends the dispatcher tests need.
// runtime/internal/runtimetest already has a fake runtime.Runtime, but Go's
// internal rule keeps it inside the runtime tree, and it only covers the base
// interface anyway — dispatch needs the InferenceRuntime and WorkflowRuntime
// methods. These fakes cover exactly those, and nothing here reaches a
// backend, a socket or a clock.

// BaseRuntime is the runtime.Runtime half both fakes share: a fixed
// descriptor and harmless defaults for the lifecycle methods, which the
// dispatcher never calls.
type BaseRuntime struct {
	// Desc is returned by Descriptor, and its MaxConcurrent is what the
	// dispatcher builds this instance's concurrency limiter from.
	Desc runtime.Descriptor
}

// Descriptor implements runtime.Runtime.
func (r *BaseRuntime) Descriptor() runtime.Descriptor { return r.Desc }

// Probe implements runtime.Runtime.
func (r *BaseRuntime) Probe(context.Context) (runtime.ProbeResult, error) {
	return runtime.ProbeResult{}, nil
}

// Health implements runtime.Runtime.
func (r *BaseRuntime) Health(context.Context) (runtime.HealthReport, error) {
	return runtime.HealthReport{State: runtime.StateHealthy}, nil
}

// Discover implements runtime.Runtime.
func (r *BaseRuntime) Discover(context.Context) (runtime.Discovery, error) {
	return runtime.Discovery{}, nil
}

// Close implements runtime.Runtime.
func (r *BaseRuntime) Close() error { return nil }

// InferenceRuntime is a scriptable runtime.InferenceRuntime: each method
// calls the corresponding *Func field when set, and otherwise returns a zero
// value and a nil error.
type InferenceRuntime struct {
	BaseRuntime

	ListModelsFunc func(ctx context.Context) ([]runtime.Model, error)
	ChatFunc       func(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error)
	ChatStreamFunc func(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error)
	EmbedFunc      func(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error)
}

// ListModels implements runtime.InferenceRuntime.
func (r *InferenceRuntime) ListModels(ctx context.Context) ([]runtime.Model, error) {
	if r.ListModelsFunc != nil {
		return r.ListModelsFunc(ctx)
	}
	return nil, nil
}

// Chat implements runtime.InferenceRuntime.
func (r *InferenceRuntime) Chat(ctx context.Context, req runtime.ChatRequest) (runtime.ChatResponse, error) {
	if r.ChatFunc != nil {
		return r.ChatFunc(ctx, req)
	}
	return runtime.ChatResponse{}, nil
}

// ChatStream implements runtime.InferenceRuntime.
func (r *InferenceRuntime) ChatStream(ctx context.Context, req runtime.ChatRequest) (runtime.Stream[runtime.ChatEvent], error) {
	if r.ChatStreamFunc != nil {
		return r.ChatStreamFunc(ctx, req)
	}
	return EmptyStream[runtime.ChatEvent](), nil
}

// Embed implements runtime.InferenceRuntime.
func (r *InferenceRuntime) Embed(ctx context.Context, req runtime.EmbeddingRequest) (runtime.EmbeddingResponse, error) {
	if r.EmbedFunc != nil {
		return r.EmbedFunc(ctx, req)
	}
	return runtime.EmbeddingResponse{}, nil
}

// WorkflowRuntime is a scriptable runtime.WorkflowRuntime.
type WorkflowRuntime struct {
	BaseRuntime

	SubmitFunc       func(ctx context.Context, req runtime.WorkflowRequest) (runtime.WorkflowRun, error)
	SubscribeFunc    func(ctx context.Context, runID string) (runtime.Stream[runtime.WorkflowEvent], error)
	StatusFunc       func(ctx context.Context, runID string) (runtime.WorkflowStatus, error)
	CancelFunc       func(ctx context.Context, runID string) error
	OpenArtifactFunc func(ctx context.Context, ref runtime.ArtifactRef) (runtime.Artifact, error)
}

// Submit implements runtime.WorkflowRuntime.
func (r *WorkflowRuntime) Submit(ctx context.Context, req runtime.WorkflowRequest) (runtime.WorkflowRun, error) {
	if r.SubmitFunc != nil {
		return r.SubmitFunc(ctx, req)
	}
	return runtime.WorkflowRun{}, nil
}

// Subscribe implements runtime.WorkflowRuntime.
func (r *WorkflowRuntime) Subscribe(ctx context.Context, runID string) (runtime.Stream[runtime.WorkflowEvent], error) {
	if r.SubscribeFunc != nil {
		return r.SubscribeFunc(ctx, runID)
	}
	return EmptyStream[runtime.WorkflowEvent](), nil
}

// Status implements runtime.WorkflowRuntime.
func (r *WorkflowRuntime) Status(ctx context.Context, runID string) (runtime.WorkflowStatus, error) {
	if r.StatusFunc != nil {
		return r.StatusFunc(ctx, runID)
	}
	return runtime.WorkflowStatus{}, nil
}

// Cancel implements runtime.WorkflowRuntime.
func (r *WorkflowRuntime) Cancel(ctx context.Context, runID string) error {
	if r.CancelFunc != nil {
		return r.CancelFunc(ctx, runID)
	}
	return nil
}

// OpenArtifact implements runtime.WorkflowRuntime.
func (r *WorkflowRuntime) OpenArtifact(ctx context.Context, ref runtime.ArtifactRef) (runtime.Artifact, error) {
	if r.OpenArtifactFunc != nil {
		return r.OpenArtifactFunc(ctx, ref)
	}
	return runtime.Artifact{Ref: ref, Size: -1}, nil
}

// EventStream returns a finished stream carrying events, which is what an
// adapter that produced its whole answer before the consumer arrived looks
// like. Passing err makes the stream end with that error instead of io.EOF.
func EventStream[T any](err error, events ...T) runtime.Stream[T] {
	stream := runtime.NewChanStream[T](len(events))
	for _, ev := range events {
		stream.Send(ev)
	}
	stream.CloseWithError(err)
	return stream
}

// EmptyStream returns a stream that ends immediately with no events.
func EmptyStream[T any]() runtime.Stream[T] { return EventStream[T](nil) }

var (
	_ runtime.InferenceRuntime = (*InferenceRuntime)(nil)
	_ runtime.WorkflowRuntime  = (*WorkflowRuntime)(nil)
)
