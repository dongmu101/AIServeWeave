package runtime

import (
	"encoding/json"
	"io"
	"time"
)

type Kind string

const (
	KindVLLM    Kind = "vllm"
	KindSGLang  Kind = "sglang"
	KindOllama  Kind = "ollama"
	KindComfyUI Kind = "comfyui"
)

type Config struct {
	ID                  string
	Kind                Kind
	BaseURL             string
	APIKey              string
	Headers             map[string]string
	ProbeTimeout        time.Duration
	RequestTimeout      time.Duration
	StreamIdleTimeout   time.Duration
	HealthInterval      time.Duration
	DiscoveryInterval   time.Duration
	MaxConcurrent       int
	TLS                 TLSConfig
	CapabilityOverrides map[Capability]SupportLevel
	Exclusive           bool
}
type TLSConfig struct {
	CAFile             string
	ServerName         string
	InsecureSkipVerify bool
}

type State string

const (
	StateRegistering State = "registering"
	StateUnknown     State = "unknown"
	StateHealthy     State = "healthy"
	StateUnhealthy   State = "unhealthy"
	StateClosed      State = "closed"
)

// Descriptor is the stable, non-secret identity and scheduling summary of a
// runtime instance. Dynamic state and discovered capabilities are reported by
// HealthReport and Discovery instead.
//
// Descriptor must never contain credentials or custom header values.
type Descriptor struct {
	ID            string
	Kind          Kind
	BaseURL       string
	MaxConcurrent int
	Exclusive     bool
}

// ProbeResult records the evidence collected while validating that an
// endpoint satisfies the minimum contract for the configured runtime kind.
// Evidence must be safe to expose in logs and status snapshots.
type ProbeResult struct {
	Kind             Kind
	Version          string
	IdentityVerified bool
	Evidence         string
	ProbedAt         time.Time
}

// HealthReport is a point-in-time health snapshot. ErrorSummary contains a
// sanitized diagnostic message and must not include credentials, headers, or
// request payloads.
type HealthReport struct {
	State        State
	Latency      time.Duration
	CheckedAt    time.Time
	ErrorSummary string
}

// Discovery is an immutable-by-convention snapshot of runtime metadata and
// capabilities. Callers must not mutate its slices or maps.
type Discovery struct {
	Version      string
	Models       []Model
	NodeTypes    []string
	Capabilities CapabilitySet
	Warnings     []string
	DiscoveredAt time.Time
}

// Model describes a backend model and the capabilities supported by that
// specific model. Unknown capabilities remain explicitly unknown.
type Model struct {
	ID           string
	Capabilities CapabilitySet
}

type ChatRequest struct {
	Model    string
	Messages []ChatMessage
}

type ChatMessage struct {
	Role       string
	Content    string
	Name       string
	ToolCallID string
	ToolCalls  []ToolCall
}

type ChatResponse struct {
	ID           string
	Model        string
	Message      ChatMessage
	FinishReason string
	Usage        Usage
	CreatedAt    time.Time
}

type ChatEvent struct {
	ID           string
	Model        string
	Delta        ChatMessageDelta
	FinishReason string
	Usage        *Usage
}

type ChatMessageDelta struct {
	Role      string
	Content   string
	ToolCalls []ToolCallDelta
}

type ToolCallDelta struct {
	Index    int
	ID       string
	Type     string
	Function FunctionCallDelta
}

type FunctionCallDelta struct {
	Name      string
	Arguments string
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type ToolCall struct {
	ID       string
	Type     string
	Function FunctionCall
}

type FunctionCall struct {
	Name      string
	Arguments string
}

type EmbeddingRequest struct {
	Model      string
	Input      []string
	Dimensions *int
}

type EmbeddingResponse struct {
	Model string
	Data  []Embedding
	Usage Usage
}

type Embedding struct {
	Index  int
	Vector []float32
}

// WorkflowRequest submits an API Format ComfyUI workflow. Template must
// already be validated and size-limited by the caller; the runtime does not
// inspect node contents beyond what it needs for cancellation and event
// routing.
type WorkflowRequest struct {
	Template       json.RawMessage
	ClientID       string
	IdempotencyKey string
}

// WorkflowRun is the local handle for a submitted workflow. ID wraps the
// backend's prompt_id; it is not the public job ID used by upper layers.
type WorkflowRun struct {
	ID          string
	RuntimeID   string
	SubmittedAt time.Time
}

// WorkflowEventType is the normalized event vocabulary produced by
// WorkflowRuntime.Subscribe, independent of the backend's native event
// names.
type WorkflowEventType string

const (
	WorkflowEventQueueChanged WorkflowEventType = "queue_changed"
	WorkflowEventStarted      WorkflowEventType = "started"
	WorkflowEventCached       WorkflowEventType = "cached"
	WorkflowEventNodeStarted  WorkflowEventType = "node_started"
	WorkflowEventCompleted    WorkflowEventType = "completed"
	WorkflowEventProgress     WorkflowEventType = "progress"
	WorkflowEventNodeOutput   WorkflowEventType = "node_output"
	WorkflowEventSucceeded    WorkflowEventType = "succeeded"
	WorkflowEventFailed       WorkflowEventType = "failed"
	WorkflowEventCancelled    WorkflowEventType = "cancelled"
	WorkflowEventUnknown      WorkflowEventType = "unknown"
)

// WorkflowEvent is one normalized event from a subscribed workflow run. Raw
// preserves the size-limited, unparsed backend payload for event types the
// adapter does not fully model.
type WorkflowEvent struct {
	Type       WorkflowEventType
	RunID      string
	NodeID     string
	Raw        json.RawMessage
	ReceivedAt time.Time
}

type WorkflowState string

const (
	WorkflowPending   WorkflowState = "pending"
	WorkflowRunning   WorkflowState = "running"
	WorkflowSucceeded WorkflowState = "succeeded"
	WorkflowFailed    WorkflowState = "failed"
	WorkflowCancelled WorkflowState = "cancelled"
)

// WorkflowStatus is a point-in-time status snapshot, sourced from the
// backend's queue and history so it remains accurate even if events were
// missed on the WebSocket connection.
type WorkflowStatus struct {
	State         WorkflowState
	QueuePosition int
	StartedAt     *time.Time
	FinishedAt    *time.Time
	ErrorSummary  string
}

// ArtifactRef identifies a single output artifact produced by a workflow
// run, as returned in history/executed events.
type ArtifactRef struct {
	RunID     string
	Filename  string
	Subfolder string
	Type      string
}

// Artifact is a streamed artifact body. Callers must call Body.Close(); the
// runtime does not buffer artifact contents in memory.
type Artifact struct {
	Ref         ArtifactRef
	ContentType string
	Size        int64
	Body        io.ReadCloser
}
