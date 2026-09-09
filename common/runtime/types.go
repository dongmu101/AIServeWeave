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

// ChatRequest is the protocol-neutral request Chat and ChatStream accept.
// Stream is deliberately not a field: streaming or not is chosen by calling
// Chat vs ChatStream, and the openai conversion layer sets the wire-level
// "stream"/"stream_options" fields accordingly.
type ChatRequest struct {
	Model    string
	Messages []ChatMessage

	// Sampling parameters use pointers so "caller did not set this" can be
	// distinguished from "caller explicitly set this to the zero value";
	// the latter must reach the backend unchanged rather than being
	// silently dropped to a default. A nil field is omitted from the wire
	// request entirely.
	Temperature *float64
	TopP        *float64
	MaxTokens   *int
	Stop        []string
	Seed        *int64

	// Tools requires CapabilityTools support; ResponseFormat requires
	// CapabilityStructuredOutput. Callers must check CapabilitySet.Require
	// before setting either — Runtime implementations reject the request
	// rather than silently drop the field.
	Tools          []Tool
	ToolChoice     string
	ResponseFormat *ResponseFormat

	// Extra forwards backend-private parameters verbatim. A key that
	// collides with an already-modeled field (e.g. "model", "temperature")
	// must be rejected by the conversion layer with ErrorInvalidConfig
	// rather than silently overwriting the modeled value.
	Extra map[string]json.RawMessage
}

// Tool describes a callable function the model may invoke, mirroring the
// OpenAI-compatible "function" tool shape vLLM, SGLang and Ollama all
// implement.
type Tool struct {
	// Type is currently always "function", kept as a string so a future
	// tool type can round-trip without a Runtime code change.
	Type     string
	Function FunctionDefinition
}

// FunctionDefinition describes one callable function's name, human-readable
// purpose, and JSON Schema parameters.
type FunctionDefinition struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object, passed through verbatim; Runtime
	// does not validate or interpret schema contents.
	Parameters json.RawMessage
}

// ResponseFormat constrains the shape of a model's output, mirroring the
// OpenAI-compatible response_format parameter.
type ResponseFormat struct {
	// Type is one of "text", "json_object", or "json_schema".
	Type string
	// JSONSchema is set when Type == "json_schema" and nil otherwise.
	JSONSchema *JSONSchemaFormat
}

// JSONSchemaFormat is the json_schema variant of ResponseFormat.
type JSONSchemaFormat struct {
	Name   string
	Strict bool
	Schema json.RawMessage
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

// InputUploadMeta describes an input file about to be uploaded to a
// workflow-capable backend (STATUS.md's P04), before any of its bytes.
// Filename and Subfolder are caller-supplied labels, never a filesystem
// path — where the bytes actually land on disk is decided entirely by the
// adapter implementing WorkflowRuntime.UploadInput, the same separation
// ArtifactRef already draws for an output's locator.
//
// InputUploadMeta 描述一个即将上传给具备工作流能力的后端的输入文件
// （STATUS.md 的 P04），发生在它的任何字节之前。Filename 与 Subfolder 是
// 调用方提供的标签，绝不是文件系统路径——字节最终落在磁盘的什么位置，完全
// 由实现 WorkflowRuntime.UploadInput 的适配器决定，与 ArtifactRef 已经为
// 一个输出的定位信息划出的界线相同。
type InputUploadMeta struct {
	Filename  string
	Subfolder string
	// Size is the exact byte count the caller will supply, or -1 if unknown.
	//
	// Size 是调用方将提供的确切字节数，未知则为 -1。
	Size int64
	// SHA256 is an optional hex-encoded integrity check the caller computed
	// before sending; empty if not computed.
	//
	// SHA256 是调用方发送前算好的、可选的十六进制整体性校验；未计算则为空。
	SHA256 string
}

// InputUploadResult is what UploadInput returns once an input's bytes have
// been fully received.
//
// InputUploadResult 是一个输入的字节被完整接收之后，UploadInput 返回的东西。
type InputUploadResult struct {
	// InputRef is an opaque handle a later WorkflowRequest's Template can
	// reference — e.g. the filename ComfyUI's own /upload/image assigned,
	// which may differ from the requested filename on a collision.
	// Meaningful only to the adapter that produced it; callers must not
	// parse or construct one.
	//
	// InputRef 是一个不透明句柄，供之后某个 WorkflowRequest 的 Template
	// 引用——例如 ComfyUI 自己的 /upload/image 所分配的文件名，遇到重名时
	// 可能与请求的文件名不同。它只对产生它的适配器有意义；调用方不得解析
	// 或自行构造它。
	InputRef string
}
