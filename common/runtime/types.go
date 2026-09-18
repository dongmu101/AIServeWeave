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
// request payloads. QueueRunning and QueuePending are a ComfyUI-only
// occupancy signal (STATUS.md's P2 realtime-utilization subtask): every
// other runtime kind leaves both 0, and the scheduler treats that the same
// as "not reported", so it never penalizes a runtime kind that has no queue
// concept.
//
// HealthReport 是某一时刻的健康快照。ErrorSummary 是脱敏后的诊断信息，不得包含
// 凭据、Header 或请求体。QueueRunning 与 QueuePending 是仅 ComfyUI 才有的占用信号
// （STATUS.md P2 的实时利用率子任务）：其余运行时种类两者恒为 0，调度器把这与
// "未上报"同等对待，因此不会惩罚本就没有队列概念的运行时种类。
type HealthReport struct {
	State        State
	Latency      time.Duration
	CheckedAt    time.Time
	ErrorSummary string
	QueueRunning int
	QueuePending int
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
	Role string
	// Content is the message's text. For a multi-part message that mixes
	// text with images, ContentParts is set instead and Content stays
	// empty — see ContentParts. This is the overwhelmingly common case, so
	// every existing caller that only ever sets Content is unaffected by
	// ContentParts existing.
	//
	// Content 是消息的文本。混合文本与图片的多部分消息改用 ContentParts 承载，
	// 此时 Content 留空——见 ContentParts。纯文本是绝大多数情况，因此只设置
	// Content 的既有调用方不受 ContentParts 存在的影响。
	Content string
	// ContentParts carries a multi-part message body (STATUS.md's P2
	// ChatMessage.Content structured rework): ordered text and image
	// parts, mirroring the content-array shape OpenAI and the
	// OpenAI-compatible vLLM/SGLang/Ollama endpoints for vision models all
	// accept. Nil for a text-only message — see Content.
	//
	// ContentParts 承载多部分消息体（STATUS.md 的 P2 ChatMessage.Content
	// 结构化改造）：有序的文本与图片片段，对应 OpenAI 以及面向视觉模型的
	// vLLM/SGLang/Ollama OpenAI 兼容端点共同接受的 content 数组形状。纯文本
	// 消息留空——见 Content。
	ContentParts []ContentPart
	Name         string
	ToolCallID   string
	ToolCalls    []ToolCall
}

// ContentPart is one piece of a multi-part ChatMessage.ContentParts: either
// a text run or an image reference, the two part types this codebase's
// front doors and adapters support.
//
// ContentPart 是 ChatMessage.ContentParts 的一个片段：文本或图片引用，是本
// 代码库前门与适配器所支持的两种片段类型。
type ContentPart struct {
	// Type is "text" or "image_url".
	Type string
	// Text is set when Type == "text".
	Text string
	// ImageURL is set when Type == "image_url".
	ImageURL *ContentImageURL
}

// ContentImageURL is an image content part's source, mirroring the
// OpenAI-compatible image_url object. URL may be an http(s) URL or a data:
// URI carrying inline base64 image bytes — this package does not
// distinguish them; the backend does.
//
// ContentImageURL 是图片内容片段的来源，对应 OpenAI 兼容的 image_url 对象。
// URL 既可以是 http(s) 地址，也可以是内联 base64 图片字节的 data: URI——本包
// 不区分两者，交给后端处理。
type ContentImageURL struct {
	URL string
	// Detail is optional and mirrors OpenAI's image_url.detail ("auto",
	// "low", "high"); empty means the backend's own default.
	//
	// Detail 可选，对应 OpenAI 的 image_url.detail（"auto"、"low"、
	// "high"）；留空表示交给后端自己的默认值。
	Detail string
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

// RerankRequest asks a backend to score Documents against Query and return
// them ordered by relevance. TopN, when set, asks the backend to return only
// the best-scoring TopN results rather than the whole list — a nil TopN
// means "all of them."
//
// RerankRequest 请求后端把 Documents 相对 Query 打分并按相关性排序。TopN
// 设置时，要求后端只返回打分最高的 TopN 条而非全部——TopN 为 nil 表示「全部」。
type RerankRequest struct {
	Model     string
	Query     string
	Documents []string
	TopN      *int
}

// RerankResponse is what Rerank returns: Results ordered best-first,
// each naming which original Documents entry it scored via Index.
//
// RerankResponse 是 Rerank 的返回值：Results 按最优先排序，每一项通过
// Index 指明它为 Documents 中的哪一条打分。
type RerankResponse struct {
	Model   string
	Results []RerankResult
}

// RerankResult scores one document from the original RerankRequest.Documents
// slice. Index is the document's position in that slice, not a rank.
//
// RerankResult 为原始 RerankRequest.Documents 切片中的一条文档打分。Index 是
// 该文档在切片中的位置，不是名次。
type RerankResult struct {
	Index int
	Score float64
}

// WorkflowRequest submits an API Format ComfyUI workflow. Template must
// already be validated and size-limited by the caller; the runtime does not
// inspect node contents beyond what it needs for cancellation and event
// routing.
type WorkflowRequest struct {
	Template       json.RawMessage
	ClientID       string
	IdempotencyKey string
	// MinGPUMemoryBytes is the smallest GPU memory total a candidate node must
	// declare to run this workflow, mirroring modelroute.Target's field of the
	// same name and the same default-open semantics: zero applies no filter,
	// and a node that has not reported hardware is never excluded by it. It
	// is the caller's to set — the scheduler has no notion of what a workflow
	// graph requires — closing the gap where workflow submissions previously
	// could never reach STATUS.md's P2 admission-threshold filtering at all,
	// since it only ever flowed through modelroute.Target for the Chat/Embed/
	// Rerank paths.
	//
	// MinGPUMemoryBytes 是候选节点必须声明的最小 GPU 显存总量才能运行本工作流，
	// 与 modelroute.Target 同名字段及其默认放行语义一致：零值不做任何过滤，一个
	// 尚未上报硬件的节点同样不会被它排除。它由调用方设置——调度器本身不理解某张
	// 工作流图需要什么——补上此前的缺口：工作流提交此前完全无法触达 STATUS.md P2
	// 的准入门槛过滤，因为该过滤只经由 modelroute.Target 流向 Chat/Embed/Rerank
	// 路径。
	MinGPUMemoryBytes int64
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
	// OutOfMemory is the adapter's best-effort classification of a Failed run
	// as a backend out-of-memory error, from whatever structured evidence the
	// backend's own failure report carries (STATUS.md's A06). It is never set
	// for a State other than WorkflowFailed, and a false value means either
	// success or a failure the adapter could not attribute to memory
	// exhaustion — not proof memory was not the cause.
	//
	// OutOfMemory 是适配器对一次 Failed 运行的尽力而为分类：是否为后端显存/内存
	// 不足，依据后端失败报告自身携带的结构化证据判断（STATUS.md 的 A06）。它
	// 从不在 State 非 WorkflowFailed 时被置位；取值为 false 既可能是成功，也
	// 可能是适配器无法归因到内存耗尽的失败——不是「确认不是内存问题」的证明。
	OutOfMemory bool
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

// AudioTask names which of the two OpenAI-compatible audio endpoints a
// AudioTranscriptionRequest targets: transcription keeps the source language,
// translation always answers in English. They share one Capability and one
// request/response shape because the wire and backend contracts differ only
// in this one field, not in how bytes flow.
//
// AudioTask 指明一次 AudioTranscriptionRequest 面向 OpenAI 兼容的两个音频端点中的
// 哪一个：转录保留原语言，翻译始终以英文作答。两者共用一个 Capability 与一套
// 请求/响应形状，因为 wire 层与后端契约的差异只在这一个字段上，不在字节如何流动上。
type AudioTask string

const (
	AudioTaskTranscribe AudioTask = "transcribe"
	AudioTaskTranslate  AudioTask = "translate"
)

// AudioTranscriptionRequest describes an audio transcription or translation
// call, before any of the audio bytes — which travel separately as the
// audio io.Reader InferenceRuntime.Transcribe takes, the same split
// InputUploadMeta makes for an upload's bytes. Filename carries the source
// extension a backend may need to pick a decoder; it is a label, never a
// filesystem path.
//
// AudioTranscriptionRequest 描述一次音频转录或翻译调用，发生在任何音频字节之前
// ——字节作为 InferenceRuntime.Transcribe 单独接受的 audio io.Reader 传输，与
// InputUploadMeta 对一次上传字节所做的拆分相同。Filename 携带源文件扩展名，供
// 后端据此选择解码器；它是一个标签，绝不是文件系统路径。
type AudioTranscriptionRequest struct {
	Model    string
	Filename string
	Task     AudioTask
	// Language is a BCP-47/ISO-639-1 hint for AudioTaskTranscribe; a backend
	// may ignore it. Meaningless for AudioTaskTranslate, whose output
	// language is always English.
	//
	// Language 是 AudioTaskTranscribe 的 BCP-47/ISO-639-1 提示；后端可以忽略
	// 它。对 AudioTaskTranslate 无意义——它的输出语言恒为英文。
	Language *string
	// Prompt steers the backend's decoding the way OpenAI's own transcription
	// prompt parameter does; it is not a chat prompt.
	//
	// Prompt 按 OpenAI 自家转录接口 prompt 参数的方式引导后端解码；它不是
	// 一个 chat 提示词。
	Prompt      *string
	Temperature *float64
	// ResponseFormat is "text" or "json" (the default when empty). Other
	// OpenAI values (srt, vtt, verbose_json) are out of v1's scope and are
	// rejected by the caller before a request reaches this type.
	//
	// ResponseFormat 取 "text" 或 "json"（留空时默认为 "json"）。OpenAI 的其余
	// 取值（srt、vtt、verbose_json）不在 v1 范围内，调用方在请求到达这个类型
	// 之前就已拒绝。
	ResponseFormat string
}

// AudioTranscriptionResponse is what Transcribe returns once the backend has
// fully processed the audio. Language and Duration are best-effort: a
// backend that does not report them leaves both zero.
//
// AudioTranscriptionResponse 是后端完整处理完音频之后 Transcribe 返回的结果。
// Language 与 Duration 是尽力而为：不报告它们的后端会让两者都保持零值。
type AudioTranscriptionResponse struct {
	Text     string
	Language string
	Duration *float64
}
