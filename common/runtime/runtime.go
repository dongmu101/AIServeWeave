package runtime

import "context"

type Runtime interface {
	Descriptor() Descriptor
	Probe(ctx context.Context) (ProbeResult, error)
	Health(ctx context.Context) (HealthReport, error)
	Discover(ctx context.Context) (Discovery, error)
	Close() error
}

type InferenceRuntime interface {
	Runtime
	ListModels(ctx context.Context) ([]Model, error)
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest) (Stream[ChatEvent], error)
	Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
}

type WorkflowRuntime interface {
	Runtime
	Submit(ctx context.Context, req WorkflowRequest) (WorkflowRun, error)
	Subscribe(ctx context.Context, runID string) (Stream[WorkflowEvent], error)
	Status(ctx context.Context, runID string) (WorkflowStatus, error)
	Cancel(ctx context.Context, runID string) error
	// Artifacts lists what a run produced, without any of their bytes.
	// OpenArtifact takes an ArtifactRef but cannot produce one, so without
	// this a caller that missed the node_output events — after a reconnect,
	// or after an Agent restart — has no way to find its own outputs.
	//
	// Artifacts 列举一次运行产出了什么，不含它们的任何字节。OpenArtifact 接受
	// ArtifactRef 却造不出一个来，因此没有这个方法，错过了 node_output 事件的调用方
	// ——重连之后，或 Agent 重启之后——就无从找到自己的产物。
	Artifacts(ctx context.Context, runID string) ([]ArtifactRef, error)
	OpenArtifact(ctx context.Context, ref ArtifactRef) (Artifact, error)
}
