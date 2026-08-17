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
	OpenArtifact(ctx context.Context, ref ArtifactRef) (Artifact, error)
}
