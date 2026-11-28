// Package flow runs a Studio flow (编排): given a definition, a run's input
// values and a Dispatcher for model calls, it executes the nodes and reports
// progress as a stream of events and a final Result.
//
// The engine owns scheduling, parallel branches, condition skipping, variable
// substitution, the per-node timeout, the whole-run deadline and cancellation.
// It knows nothing about HTTP, tenants, storage or how a model call reaches a
// node: the Gateway supplies those through Dispatcher and by consuming events,
// and tests drive the engine with a fake Dispatcher.
//
// Execution rules:
//
//   - A node is decided once every edge into it has been settled, that is its
//     source finished or was skipped. It runs if at least one of those edges
//     is active — the source succeeded and, for an edge leaving a condition
//     node, the branch matches the condition's result — and is skipped
//     otherwise. A skipped node settles its own outgoing edges as inactive,
//     so skipping flows down a branch until it meets a join that another
//     branch keeps alive.
//   - A reference to a node that was skipped renders as "".
//   - Independent nodes run in parallel, at most Config.MaxParallel calls —
//     model calls, image generations and workflow template runs — at a time.
//   - An image_generation or comfyui_template node's fields, and image and
//     file inputs, hold artifact ids, which flowdef only lets an output
//     reference, or a comfyui_template input when they name one file; such
//     an input reaches the Dispatcher in TemplateCall.Files.
//   - The first node failure fails the run: model calls still in flight are
//     cancelled and nothing further starts.
//   - The output node renders Definition.Outputs into Result.Outputs. When the
//     output node itself is skipped the run still succeeds, with no outputs.
//
// Flow content — prompts, input values, model replies — travels in events and
// results because the caller must show and persist it, but never in an error
// string: node failures carry only a stable code.
//
// flow 包运行 Studio 的编排：给定定义、一次运行的输入值和负责模型调用的 Dispatcher，
// 执行各节点，以事件流和最终的 Result 报告进度。
//
// 引擎负责调度、并行分支、条件跳过、变量替换、单节点超时、整体期限与取消。它不了解
// HTTP、租户、存储，也不了解模型调用如何到达节点：这些由 Gateway 通过 Dispatcher 和
// 消费事件来提供；测试用假的 Dispatcher 驱动引擎。
//
// 执行规则：
//
//   - 指向某节点的每条边都已落定（其起点已结束或被跳过）时，该节点得到判定。只要其中
//     至少一条边是激活的——起点成功，且对从条件节点出发的边，branch 与条件结果一致——
//     就运行，否则跳过。被跳过的节点把自己的出边都落定为未激活，因此跳过会沿分支向下
//     传播，直到遇到另一条分支仍然激活的汇合点。
//   - 引用被跳过的节点时渲染为空字符串。
//   - 互不依赖的节点并行运行，同时进行的调用（模型调用、生图与工作流模板运行）最多
//     Config.MaxParallel 个。
//   - image_generation 与 comfyui_template 节点的字段，以及 image 与 file 输入，保存
//     产物 id。flowdef 只允许输出引用它们；指向单个文件时，也允许 comfyui_template 的
//     输入引用，这样的输入经 TemplateCall.Files 交给 Dispatcher。
//   - 第一个节点失败即令运行失败：仍在进行的模型调用被取消，之后不再启动任何节点。
//   - 输出节点把 Definition.Outputs 渲染为 Result.Outputs。输出节点本身被跳过时运行
//     仍然成功，只是没有输出。
//
// 编排内容——提示词、输入值、模型回复——会出现在事件与结果中，因为调用方必须展示和
// 持久化它们；但从不出现在错误字符串里：节点失败只携带稳定的错误码。
package flow

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"fmt"
	"time"

	"AIServeWeave/common/flowdef"
	"AIServeWeave/common/runtime"
)

// Defaults for Config's zero values. The run deadline and the node timeout
// are the Studio spec's defaults.
//
// Config 零值对应的默认值。整体期限与单节点超时取自 Studio 规格的默认值。
const (
	DefaultRunTimeout    = 30 * time.Minute
	DefaultNodeTimeout   = 5 * time.Minute
	// A video generation task routinely queues and renders for longer than
	// any other call, so a video_generation node has its own timeout, and a
	// run that contains one its own deadline.
	// 视频生成任务的排队与渲染通常比其他调用都久，因此 video_generation 节点有自己的
	// 超时，含该节点的运行也有自己的整体期限。
	DefaultVideoNodeTimeout = time.Hour
	DefaultVideoRunTimeout  = 2 * time.Hour
	DefaultMaxParallel   = 4
	DefaultMaxValueBytes = 1 << 20
)

// Stable failure codes the engine itself assigns. A Dispatcher adds its own
// through *Error, e.g. "model_unavailable" or "rate_limited".
//
// 引擎自己分配的稳定失败码。Dispatcher 经 *Error 加入自己的错误码，如
// 「model_unavailable」或「rate_limited」。
const (
	CodeUpstream          = "upstream_error"
	CodeNodeTimeout       = "node_timeout"
	CodeRunTimeout        = "run_timeout"
	CodeInvalidJSONOutput = "invalid_json_output"
	CodeNotANumber        = "not_a_number"
	CodeValueTooLarge     = "value_too_large"
	// CodeNoImage fails an image_generation node whose call produced no image.
	// CodeNoImage 表示 image_generation 节点的调用没有产出图片。
	CodeNoImage = "no_image_produced"
	// CodeNoVideo fails a video_generation node whose call produced no video.
	// CodeNoVideo 表示 video_generation 节点的调用没有产出视频。
	CodeNoVideo = "no_video_produced"
)

// Error is a failure with a stable code and no content. A Dispatcher returns
// one to say why a model call failed; any other error counts as
// CodeUpstream.
//
// Error 是只带稳定错误码、不带内容的失败。Dispatcher 返回它来说明模型调用为何失败；
// 其他任何错误都按 CodeUpstream 处理。
type Error struct {
	Code string
}

func (e *Error) Error() string { return "flow: " + e.Code }

// ChatCall is one llm node's model call, its templates already rendered.
// / ChatCall 是一个 llm 节点的模型调用，模板均已渲染。
type ChatCall struct {
	Model       string
	System      string
	Prompt      string
	Temperature *float64
	MaxTokens   *int
}

// Usage counts tokens. / Usage 统计 token。
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
}

func (u *Usage) add(o Usage) {
	u.PromptTokens += o.PromptTokens
	u.CompletionTokens += o.CompletionTokens
}

// ChatReply is a finished model call. / ChatReply 是一次已完成的模型调用。
type ChatReply struct {
	Text  string
	Usage Usage
}

// ImageCall is one image_generation node's call, its prompt rendered. Model
// is empty for the deployment's own image generation, Size for its default.
//
// ImageCall 是一个 image_generation 节点的调用，提示词已渲染。Model 为空表示使用
// 部署自己的生图，Size 为空表示默认尺寸。
type ImageCall struct {
	Model  string
	Prompt string
	Size   string
}

// TemplateCall is one comfyui_template node's call: the workflow template id
// and the rendered value of each input the node sets. An input that rendered
// empty is still present; the Dispatcher decides how the template takes it.
//
// An input set to exactly one file — an image or file input of the run, or
// the image of a node upstream (flowdef.Definition.RefersToFile) — is in
// Files instead, as that file's artifact id. The id always comes from the
// run itself, never from text a member typed. A file that is absent (an
// optional input not given, a skipped node) leaves the input out.
//
// TemplateCall 是一个 comfyui_template 节点的调用：工作流模板 id，以及该节点设置的
// 每个输入渲染后的取值。渲染为空的输入仍然存在；模板如何接收它由 Dispatcher 决定。
//
// 恰好设为一个文件的输入——运行的 image 或 file 输入，或上游节点的 image（见
// flowdef.Definition.RefersToFile）——改为放在 Files 中，取值为该文件的产物 id。这个
// id 总是来自运行本身，从不来自成员输入的文本。文件不存在时（可选输入未提供、节点被
// 跳过）不传该输入。
type TemplateCall struct {
	Template string
	Inputs   map[string]string
	Files    map[string]string
}

// VideoCall is one video_generation node's call, its prompt rendered. Each
// of FirstFrame, LastFrame and ReferenceImages is the artifact id of one
// image of the run, never text a member typed; an image that is absent (an
// optional input not given, a node that produced none) is left out.
// Resolution, Ratio and Duration are empty or zero for the provider's
// default.
//
// VideoCall 是一个 video_generation 节点的调用，提示词已渲染。FirstFrame、LastFrame
// 与 ReferenceImages 的每一项都是本次运行中某张图片的产物 id，从不来自成员输入的文本；
// 图片不存在时（可选输入未提供、节点没有产出）不传该项。Resolution、Ratio 与 Duration
// 为空或零时取供应商的默认值。
type VideoCall struct {
	Model           string
	Prompt          string
	FirstFrame      string
	LastFrame       string
	ReferenceImages []string
	Resolution      string
	Ratio           string
	Duration        int
	GenerateAudio   *bool
	Watermark       *bool
}

// Artifact kinds. / 产物种类。
const (
	ArtifactImage = "image"
	ArtifactVideo = "video"
	ArtifactFile  = "file"
)

// Artifact is one file a call produced, by its public artifact id. Kind is
// ArtifactImage, ArtifactVideo or ArtifactFile.
//
// Artifact 是一次调用产出的一个文件，以公开的产物 id 标识。Kind 为 ArtifactImage、
// ArtifactVideo 或 ArtifactFile。
type Artifact struct {
	ID       string `json:"artifact_id"`
	Filename string `json:"filename"`
	Kind     string `json:"kind"`
}

// MediaReply is a finished image generation, workflow template run or video
// generation. Usage is the tokens a provider billed for it, when it reports
// them (video generation does).
//
// MediaReply 是一次已完成的生图、工作流模板运行或视频生成。Usage 是供应商为此计费的
// token 数（供应商报告时才有，视频生成会报告）。
type MediaReply struct {
	Artifacts []Artifact
	Usage     Usage
}

// Dispatcher makes model calls on the engine's behalf.
//
// Chat runs one call and returns the whole reply. It calls delta with each
// piece of reply text as it arrives, from the goroutine that called Chat and
// never after Chat returns. It must return promptly once ctx ends. On failure
// it returns a *Error naming why, and may still return the usage spent.
//
// GenerateImage and RunTemplate run an image_generation and a
// comfyui_template node's call to the end and return the artifacts produced.
// Like Chat they must return promptly once ctx ends, and fail with a *Error.
// A RunTemplate whose run is still going when ctx ends should cancel it, and
// so should a GenerateVideo whose task is.
//
// Dispatcher 代表引擎进行模型调用。
//
// Chat 执行一次调用并返回完整回复。回复文本每到达一段，它就用该段调用 delta；调用发生
// 在调用 Chat 的 goroutine 上，且绝不在 Chat 返回之后。ctx 结束后它必须尽快返回。失败时
// 返回说明原因的 *Error，并且仍可返回已消耗的用量。
//
// GenerateImage 与 RunTemplate 把 image_generation 与 comfyui_template 节点的调用执行到
// 结束，并返回产出的产物。与 Chat 一样，ctx 结束后它们必须尽快返回，失败时返回 *Error。
// ctx 结束时运行仍在进行的 RunTemplate 应当取消该运行，任务仍在进行的 GenerateVideo
// 也应当取消该任务。
type Dispatcher interface {
	Chat(ctx context.Context, call ChatCall, delta func(text string)) (ChatReply, error)
	GenerateImage(ctx context.Context, call ImageCall) (MediaReply, error)
	RunTemplate(ctx context.Context, call TemplateCall) (MediaReply, error)
	GenerateVideo(ctx context.Context, call VideoCall) (MediaReply, error)
}

// Config configures an Engine. Only Dispatcher is required.
// / Config 配置 Engine。只有 Dispatcher 必填。
type Config struct {
	Dispatcher Dispatcher
	// Clock times steps and drives both timeouts; nil uses the system clock.
	// Clock 为步骤计时并驱动两种超时；nil 使用系统时钟。
	Clock runtime.Clock
	// RunTimeout bounds a whole run. / RunTimeout 限制整次运行。
	RunTimeout time.Duration
	// NodeTimeout bounds one call. / NodeTimeout 限制一次调用。
	NodeTimeout time.Duration
	// VideoNodeTimeout bounds one video_generation call instead of
	// NodeTimeout, and VideoRunTimeout a run that contains such a node
	// instead of RunTimeout when it is longer.
	// VideoNodeTimeout 代替 NodeTimeout 限制一次 video_generation 调用；含该节点的运行
	// 改用 VideoRunTimeout 作为整体期限（当它比 RunTimeout 长时）。
	VideoNodeTimeout time.Duration
	VideoRunTimeout  time.Duration
	// MaxParallel bounds a run's concurrent calls.
	// MaxParallel 限制一次运行中并发的调用数。
	MaxParallel int
	// MaxValueBytes bounds a node's output value and each rendered template:
	// a longer reply is cut, a longer rendering fails the node.
	// MaxValueBytes 限制节点输出值与每个渲染后的模板：更长的回复被截断，更长的渲染
	// 结果令节点失败。
	MaxValueBytes int
}

// Engine runs flows. It is safe for concurrent use.
// / Engine 运行编排，可并发使用。
type Engine struct {
	cfg Config
}

// New validates cfg, fills defaults and returns an Engine.
// / New 校验 cfg、填充默认值并返回 Engine。
func New(cfg Config) (*Engine, error) {
	if cfg.Dispatcher == nil {
		return nil, errors.New("flow: a Dispatcher is required")
	}
	if cfg.RunTimeout < 0 || cfg.NodeTimeout < 0 || cfg.VideoNodeTimeout < 0 || cfg.VideoRunTimeout < 0 || cfg.MaxParallel < 0 || cfg.MaxValueBytes < 0 {
		return nil, errors.New("flow: timeouts and limits must not be negative")
	}
	if cfg.Clock == nil {
		cfg.Clock = runtime.NewSystemClock()
	}
	if cfg.RunTimeout == 0 {
		cfg.RunTimeout = DefaultRunTimeout
	}
	if cfg.NodeTimeout == 0 {
		cfg.NodeTimeout = DefaultNodeTimeout
	}
	if cfg.VideoNodeTimeout == 0 {
		cfg.VideoNodeTimeout = DefaultVideoNodeTimeout
	}
	if cfg.VideoRunTimeout == 0 {
		cfg.VideoRunTimeout = DefaultVideoRunTimeout
	}
	if cfg.MaxParallel == 0 {
		cfg.MaxParallel = DefaultMaxParallel
	}
	if cfg.MaxValueBytes == 0 {
		cfg.MaxValueBytes = DefaultMaxValueBytes
	}
	return &Engine{cfg: cfg}, nil
}

// NodeStatus is how a node ended. / NodeStatus 是节点的结束方式。
type NodeStatus string

// Node statuses. A node that never started has no status and no event.
// / 节点状态。从未启动的节点没有状态，也没有事件。
const (
	NodeSucceeded NodeStatus = "succeeded"
	NodeFailed    NodeStatus = "failed"
	NodeCanceled  NodeStatus = "canceled"
	NodeSkipped   NodeStatus = "skipped"
)

// RunStatus is how a run ended. A run is never "interrupted" here: that is
// assigned by whoever finds a run's lease expired, not by the engine.
//
// RunStatus 是运行的结束方式。这里从不出现「interrupted」：那由发现运行租约过期的
// 一方标记，而不是引擎。
type RunStatus string

// Run statuses. / 运行状态。
const (
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

// EventType names an event. The values are the Studio SSE event names.
// / EventType 是事件名，取值即 Studio SSE 的事件名。
type EventType string

// Event types. / 事件类型。
const (
	EventRunStarted   EventType = "run_started"
	EventNodeStarted  EventType = "node_started"
	EventNodeDelta    EventType = "node_delta"
	EventNodeFinished EventType = "node_finished"
	EventNodeSkipped  EventType = "node_skipped"
	EventRunFinished  EventType = "run_finished"
)

// Event is one step of a run's progress. Node is set on node events, Delta on
// node_delta, Step on node_finished and node_skipped, Result on run_finished.
//
// Event 是运行进度中的一步。节点事件设置 Node，node_delta 设置 Delta，node_finished
// 与 node_skipped 设置 Step，run_finished 设置 Result。
type Event struct {
	Type   EventType
	Time   time.Time
	Node   string
	Delta  string
	Step   *Step
	Result *Result
}

// Step is one node's outcome. Inputs holds what the node actually evaluated —
// for an llm node the rendered system prompt and prompt, for a condition the
// rendered operands, for an image_generation node the prompt and size, for a
// comfyui_template node each input it set. Error is a code, set when Status
// is NodeFailed. Model is the model of an llm or image_generation node and
// the workflow template of a comfyui_template node.
//
// Step 是一个节点的结果。Inputs 是节点实际求值的内容——llm 节点为渲染后的系统
// 提示词与提示词，条件节点为渲染后的操作数，image_generation 节点为提示词与尺寸，
// comfyui_template 节点为它设置的每个输入。Error 是错误码，在 Status 为 NodeFailed
// 时设置。Model 对 llm 与 image_generation 节点是模型，对 comfyui_template 节点是
// 工作流模板。
type Step struct {
	Node       string
	Type       flowdef.NodeType
	Status     NodeStatus
	Inputs     map[string]string
	Outputs    map[string]string
	Error      string
	Model      string
	Usage      Usage
	StartedAt  time.Time
	FinishedAt time.Time
}

// Result is a finished run. Error and FailedNode are set when Status is
// RunFailed; FailedNode is empty when the run deadline, not a node, failed
// it.
//
// Result 是一次已结束的运行。Status 为 RunFailed 时设置 Error 与 FailedNode；因整体
// 期限而不是某个节点失败时，FailedNode 为空。
type Result struct {
	Status     RunStatus
	Error      string
	FailedNode string
	Outputs    map[string]string
	Usage      Usage
	StartedAt  time.Time
	FinishedAt time.Time
}

// Plan is a definition checked and prepared for running with one set of
// input values. It is immutable, and Run may be called on it more than once.
//
// Plan 是一份经过检查、并与一组输入值一起准备好运行的定义。它不可变，可以多次调用
// Run。
type Plan struct {
	cfg Config
	// runTimeout is cfg.RunTimeout, or cfg.VideoRunTimeout when the plan has
	// a video_generation node and that is longer.
	// runTimeout 为 cfg.RunTimeout；计划含 video_generation 节点且 cfg.VideoRunTimeout
	// 更长时为后者。
	runTimeout time.Duration
	inputs  map[string]string
	nodes   []*planNode
	byID    map[string]*planNode
	start   *planNode
	outputs []planOutput
}

type planNode struct {
	flowdef.Node
	index int
	in    []flowdef.Edge
	out   []flowdef.Edge
	tmpl  map[string]flowdef.Template // config field → template
	llm   flowdef.LLMConfig
	cond  flowdef.ConditionConfig
	image flowdef.ImageConfig
	comfy flowdef.ComfyUIConfig
	video flowdef.VideoConfig
	// files names the comfyui_template inputs set to exactly one file.
	// files 是恰好设为一个文件的 comfyui_template 输入。
	files map[string]bool
}

type planOutput struct {
	name string
	tmpl flowdef.Template
}

// Plan validates def, binds the run's input values and prepares the run. A
// problem with either is a *flowdef.ValidationError, so the caller can reject
// the request before anything runs.
//
// Plan 校验 def、绑定运行的输入值并准备运行。两者任一有问题都返回
// *flowdef.ValidationError，调用方可以在任何东西运行之前拒绝请求。
func (e *Engine) Plan(def flowdef.Definition, inputs map[string]json.RawMessage) (*Plan, error) {
	if err := flowdef.Validate(def); err != nil {
		return nil, err
	}
	values, err := flowdef.BindInputs(def.Inputs, inputs)
	if err != nil {
		return nil, err
	}
	p := &Plan{cfg: e.cfg, runTimeout: e.cfg.RunTimeout, inputs: values, byID: make(map[string]*planNode, len(def.Nodes))}
	for i, n := range def.Nodes {
		pn := &planNode{Node: n, index: i, tmpl: map[string]flowdef.Template{}}
		if err := pn.compile(); err != nil {
			// Validate accepted the definition, so this is a bug, not input.
			// Validate 已接受该定义，因此这是缺陷，而不是输入问题。
			return nil, fmt.Errorf("flow: node %q: %w", n.ID, err)
		}
		if n.Type == flowdef.NodeInput {
			p.start = pn
		}
		if n.Type == flowdef.NodeVideo && e.cfg.VideoRunTimeout > p.runTimeout {
			p.runTimeout = e.cfg.VideoRunTimeout
		}
		if n.Type == flowdef.NodeComfyUI {
			for name := range pn.comfy.Inputs {
				if ref, ok := pn.tmpl["inputs."+name].SoleRef(); ok && def.RefersToFile(ref) {
					if pn.files == nil {
						pn.files = map[string]bool{}
					}
					pn.files[name] = true
				}
			}
		}
		p.nodes = append(p.nodes, pn)
		p.byID[n.ID] = pn
	}
	for _, edge := range def.Edges {
		p.byID[edge.From].out = append(p.byID[edge.From].out, edge)
		p.byID[edge.To].in = append(p.byID[edge.To].in, edge)
	}
	for _, o := range def.Outputs {
		t, err := flowdef.ParseTemplate(o.Ref)
		if err != nil {
			return nil, fmt.Errorf("flow: output %q: %w", o.Name, err)
		}
		p.outputs = append(p.outputs, planOutput{name: o.Name, tmpl: t})
	}
	return p, nil
}

func (n *planNode) compile() error {
	parse := func(field, src string) error {
		t, err := flowdef.ParseTemplate(src)
		n.tmpl[field] = t
		return err
	}
	switch n.Type {
	case flowdef.NodeLLM:
		c, err := n.LLM()
		if err != nil {
			return err
		}
		n.llm = c
		return errors.Join(parse("system", c.System), parse("prompt", c.Prompt))
	case flowdef.NodeTemplate:
		c, err := n.Template()
		if err != nil {
			return err
		}
		return parse("template", c.Template)
	case flowdef.NodeCondition:
		c, err := n.Condition()
		if err != nil {
			return err
		}
		n.cond = c
		return errors.Join(parse("left", c.Left), parse("right", c.Right))
	case flowdef.NodeImage:
		c, err := n.Image()
		if err != nil {
			return err
		}
		n.image = c
		return parse("prompt", c.Prompt)
	case flowdef.NodeComfyUI:
		c, err := n.ComfyUI()
		if err != nil {
			return err
		}
		n.comfy = c
		var errs []error
		for name, src := range c.Inputs {
			errs = append(errs, parse("inputs."+name, src))
		}
		return errors.Join(errs...)
	case flowdef.NodeVideo:
		c, err := n.Video()
		if err != nil {
			return err
		}
		n.video = c
		errs := []error{parse("prompt", c.Prompt), parse("first_frame", c.FirstFrame), parse("last_frame", c.LastFrame)}
		for i, src := range c.ReferenceImages {
			errs = append(errs, parse(videoReferenceField(i), src))
		}
		return errors.Join(errs...)
	}
	return nil
}

// videoReferenceField is the template key of a video_generation node's i-th
// reference image, and the key it is recorded under in Step.Inputs.
func videoReferenceField(i int) string {
	return "reference_images." + strconv.Itoa(i)
}

// Run executes the plan and returns how it ended. emit receives every event in
// order, from one goroutine at a time, ending with run_finished; it is on the
// run's critical path and should hand events off rather than block. Cancelling
// ctx cancels the run.
//
// Run 执行计划并返回其结束方式。emit 按顺序收到每个事件，同一时刻只从一个 goroutine
// 调用，最后一个是 run_finished；它位于运行的关键路径上，应当转交事件而不是阻塞。取消
// ctx 即取消运行。
func (p *Plan) Run(ctx context.Context, emit func(Event)) Result {
	if emit == nil {
		emit = func(Event) {}
	}
	n := len(p.nodes)
	r := &run{
		p:         p,
		clock:     p.cfg.Clock,
		emit:      emit,
		msgs:      make(chan message),
		done:      make(chan struct{}),
		remaining: make([]int, n),
		status:    make([]NodeStatus, n),
		values:    make([]map[string]string, n),
	}
	return r.execute(ctx)
}
