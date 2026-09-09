package runtime

import (
	"context"
	"io"
)

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
	// UploadInput writes body to the backend's input area (STATUS.md's P04),
	// so a later Submit's Template can reference the result through
	// InputUploadResult.InputRef. body is read to EOF or meta.Size bytes,
	// whichever the adapter enforces first; an implementation must never
	// buffer the whole body in memory, the same streaming discipline
	// OpenArtifact already holds in the opposite direction.
	//
	// UploadInput 把 body 写入后端的输入区（STATUS.md 的 P04），这样之后某个
	// Submit 的 Template 就能通过 InputUploadResult.InputRef 引用这次上传的
	// 结果。body 被读到 EOF 或 meta.Size 字节为止，以适配器先执行到的那个
	// 为准；实现绝不能把整个 body 缓冲进内存，与 OpenArtifact 在相反方向上
	// 已经坚持的流式纪律相同。
	UploadInput(ctx context.Context, meta InputUploadMeta, body io.Reader) (InputUploadResult, error)
}
