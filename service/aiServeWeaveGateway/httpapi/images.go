package httpapi

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// This file is the OpenAI-compatible image generation endpoint, translated at
// the boundary into a workflow Job against one admin-configured ComfyUI
// template (STATUS.md's P2; see README's note on mapping a simple
// text-to-image request onto an admin-specified template). Unlike
// chat/responses/embeddings, which forward to whichever model the caller
// names, this endpoint always targets the single template -images-workflow-id
// names at startup — the caller supplies a prompt, never a graph, and never
// chooses which template runs.
//
// 本文件是 OpenAI 兼容的图像生成端点，在边界处被转换成对一个管理员在启动时配置的
// ComfyUI 模板（STATUS.md 的 P2；见 README 关于把简单文生图请求映射到管理员指定
// 模板的说明）发起的工作流 Job。与转发给调用方指名的任意模型的
// chat/responses/embeddings 不同，本端点永远只对准启动时由 -images-workflow-id
// 指名的单一模板——调用方提供的是提示词，从不是图，也从不选择运行哪个模板。

// Input names this endpoint binds onto the configured template, by
// convention rather than per-deployment configuration. A control-plane-synced
// template (P03) can be hot-swapped independently of a Gateway restart; a
// static name-mapping flag would silently desync from a republished
// template's declared Inputs, while a fixed, documented convention keeps the
// two in lockstep by construction. See main.go's startup validation and the
// Gateway README's 图像生成 section.
//
// Only prompt is required at startup (validateImagesWorkflow). width/
// height/quality/style are all optional by the same convention: a caller's
// size/quality/style is honoured only when the configured template declares
// the matching input name (see templateDeclares), and rejected by name
// otherwise — this endpoint never invents a template-specific meaning for a
// value the template itself did not ask for.
//
// 本端点按约定（而非逐部署配置）绑定到已配置模板上的这些输入名。一个从控制面
// 同步来的模板（P03）可以独立于 Gateway 重启被热替换；一张静态的名字映射 flag
// 会与重新发布后模板声明的 Inputs 悄悄失步，而一个固定、写进文档的约定能从结构上
// 让两者保持一致。见 main.go 的启动期校验与 Gateway README「图像生成」一节。
//
// 启动期只有 prompt 是必需的（validateImagesWorkflow）。width/height/
// quality/style 都按同一个约定可选：调用方的 size/quality/style 只有在已配置
// 模板声明了对应输入名时才会被兑现（见 templateDeclares），否则按名字拒绝——
// 本端点从不替一个模板本身没有要求的值臆造出模板特定的含义。
const (
	imagesInputPrompt  = "prompt"
	imagesInputWidth   = "width"
	imagesInputHeight  = "height"
	imagesInputQuality = "quality"
	imagesInputStyle   = "style"
)

// DefaultImagesGenerationTimeout bounds imagesGenerations' whole synchronous
// Submit-to-terminal loop when -images-generation-timeout is zero.
//
// DefaultImagesGenerationTimeout 在 -images-generation-timeout 为零时，限定
// imagesGenerations 整个同步的「提交到终态」循环。
const DefaultImagesGenerationTimeout = 120 * time.Second

// imagesPollInterval is the fixed cadence the execution loop re-asks
// WorkflowStatus at, the same order of magnitude as scheduler's own
// QueueRetryInterval default. It is an implementation detail, not an
// operator knob: unlike -images-generation-timeout, no deployment needs to
// tune how often this endpoint polls, only how long it is willing to wait
// overall.
//
// imagesPollInterval 是执行循环重新询问 WorkflowStatus 的固定节奏，与调度器
// 自身 QueueRetryInterval 默认值同一量级。它是实现细节，不是运维旋钮：与
// -images-generation-timeout 不同，没有哪种部署需要调这里的轮询频率，只需要
// 调整总共愿意等多久。
const imagesPollInterval = 500 * time.Millisecond

// MaxImageResponseBytes bounds one generated image's bytes as this endpoint
// reads them into memory to build a synchronous b64_json response. It is
// deliberately far smaller than the ComfyUI adapter's own MaxArtifactBytes,
// which bounds a live pull a caller streams through downloadArtifact:
// base64-inflating a full-sized artifact into one JSON response body is not
// something a synchronous handler should ever attempt, so this is the
// deliberate, bounded exception AGENTS.md's "任何一跳都不得无界缓冲" allows —
// sized for a real generated image, not a video or a multi-hundred-megabyte
// batch.
//
// MaxImageResponseBytes 限制本端点为构造同步 b64_json 响应，把一张生成图像
// 读入内存时的字节数上限。它刻意远小于 ComfyUI 适配器自身的 MaxArtifactBytes——
// 后者限定的是调用方经 downloadArtifact 流式拉取的实时下载：把一个足尺寸产物
// base64 膨胀进一个 JSON 响应体，不是一个同步处理器该做的事，因此这是
// AGENTS.md「任何一跳都不得无界缓冲」允许的、刻意且有界的例外——按真实生成
// 图像的量级设定，不是视频或几百兆的一批。
const MaxImageResponseBytes = 32 << 20

// imageArtifactExtensions is the closed set of file extensions this endpoint
// treats as a generated image among a run's "output"-bucket artifacts. See
// selectImageArtifacts's doc comment for why this extension convention
// stands in for a runtime lookup this Gateway cannot perform.
//
// imageArtifactExtensions 是本端点在一次运行的 "output" 分区产物里，用来判定
// 「这是一张生成图像」的封闭扩展名集合。为何这个扩展名约定要替代一个本
// Gateway 做不到的运行期查找，见 selectImageArtifacts 的文档注释。
var imageArtifactExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".bmp": true,
}

// imagesRequest is the subset of POST /v1/images/generations this Gateway
// serves. Fields it cannot honour are present so they can be refused by name
// rather than silently dropped — the same discipline responsesRequest
// follows.
//
// imagesRequest 是本 Gateway 所服务的 POST /v1/images/generations 子集。
// 那些它无法兑现的字段也列在这里，是为了能指名拒绝而不是默默丢弃——与
// responsesRequest 相同的纪律。
type imagesRequest struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model,omitempty"`
	Size   string `json:"size,omitempty"`
	// ResponseFormat is "b64_json" (the default) or "url". Anything else is
	// refused by unsupported.
	//
	// ResponseFormat 是 "b64_json"（默认值）或 "url"。其他任何取值都会被
	// unsupported 拒绝。
	ResponseFormat string `json:"response_format,omitempty"`

	// Quality and Style, like Size, are honoured only when the configured
	// template declares a matching input name (checked by the handler via
	// templateDeclares, not here — unlike Size this endpoint does not parse
	// or validate their values, since OpenAI's own "standard"/"hd" and
	// "vivid"/"natural" enums are DALL-E-3-specific and this endpoint's
	// template is an arbitrary ComfyUI graph, not DALL-E-3; the raw string
	// is passed straight to the template, same as prompt).
	//
	// Quality 与 Style，和 Size 一样，只有在已配置模板声明了对应输入名时才会
	// 被兑现（由处理器经 templateDeclares 检查，不在这里）——与 Size 不同，
	// 本端点不解析或校验它们的取值，因为 OpenAI 自己的 "standard"/"hd" 与
	// "vivid"/"natural" 枚举是 DALL-E-3 专属的，而本端点的模板是任意一张
	// ComfyUI 图，不是 DALL-E-3；原始字符串直接传给模板，与 prompt 相同。
	Quality string `json:"quality,omitempty"`
	Style   string `json:"style,omitempty"`

	// Refused below. It needs something this endpoint does not have — see
	// unsupported.
	//
	// 以下字段会被拒绝。它需要本端点不具备的东西——见 unsupported。
	N *int `json:"n,omitempty"`
}

// unsupported names the field this endpoint cannot honour, or empty when the
// request only asks for things it can do. size/quality/style are checked
// separately by the caller, since whether each can be honoured depends on
// the configured template's declared inputs, not on this request alone.
//
// unsupported 指出本端点无法兑现的那个字段；请求只要求它做得到的事情时返回
// 空。size/quality/style 由调用方另行检查，因为它们能否被兑现取决于已配置
// 模板声明的输入，而不只取决于这一个请求本身。
func (req imagesRequest) unsupported() string {
	switch {
	case req.N != nil && *req.N != 1:
		// Batch generation inside one already-bounded synchronous timeout
		// would multiply worst-case latency and node contention by n, or
		// else require a template-specific batching convention this
		// endpoint does not assume exists. Rejecting by name keeps the
		// first cut simple; STATUS.md records this as a scoped-down first
		// cut, not a permanent ceiling.
		//
		// 在一个已经有界的同步超时内做批量生成，会把最坏情况延迟与节点争用
		// 放大 n 倍，否则就要求一个本端点并不假定存在的、模板特定的批量约定。
		// 按名字拒绝让首版保持简单；STATUS.md 把这记作收窄范围的首版，不是
		// 永久上限。
		return "n"
	}
	switch req.ResponseFormat {
	case "", "b64_json", "url":
	default:
		return "response_format"
	}
	return ""
}

// imagesResponse is this endpoint's OpenAI-shaped result.
type imagesResponse struct {
	Created int64            `json:"created"`
	Data    []imagesDataJSON `json:"data"`
}

type imagesDataJSON struct {
	B64JSON string `json:"b64_json,omitempty"`
	URL     string `json:"url,omitempty"`
}

// templateDeclares reports whether tpl declares an input named name, for
// deciding whether a caller-supplied size can be honoured against this
// particular template.
//
// templateDeclares 报告 tpl 是否声明了名为 name 的输入，用于判定调用方给出
// 的 size 能否被这一个具体模板兑现。
func templateDeclares(tpl *workflow.Template, name string) bool {
	for _, in := range tpl.Inputs {
		if in.Name == name {
			return true
		}
	}
	return false
}

// parseImageSize parses OpenAI's "WIDTHxHEIGHT" size string. ok is false and
// err is nil for an empty size, which is not a request error — it just means
// no size was given.
//
// parseImageSize 解析 OpenAI 的 "WIDTHxHEIGHT" size 字符串。size 为空时 ok
// 为 false 且 err 为 nil，这不是请求错误——只是没有给出尺寸。
func parseImageSize(size string) (width, height int, ok bool, err error) {
	if size == "" {
		return 0, 0, false, nil
	}
	parts := strings.SplitN(size, "x", 2)
	if len(parts) != 2 {
		return 0, 0, false, fmt.Errorf(`size must be in "WIDTHxHEIGHT" form`)
	}
	w, wErr := strconv.Atoi(parts[0])
	h, hErr := strconv.Atoi(parts[1])
	if wErr != nil || hErr != nil || w <= 0 || h <= 0 {
		return 0, 0, false, fmt.Errorf(`size must be in "WIDTHxHEIGHT" form with positive integers`)
	}
	return w, h, true, nil
}

// selectImageArtifacts filters a run's artifacts down to the ones this
// endpoint treats as generated images: the "output" bucket, with a known
// image extension.
//
// runtime.ArtifactRef carries no node identity, so there is no way to
// correlate a listed artifact back to the template's declared Output.Node —
// that correlation is enforced only once, structurally, at startup (see
// main.go: the configured template must declare at least one Output typed
// "image"), and at request time this extension convention stands in for it.
// A non-image "output"-bucket artifact (e.g. a debug dump saved by a
// template author's own Save node) is silently skipped rather than treated
// as an error, which is the more useful default but is a known limitation —
// see the Gateway README's 图像生成 section.
//
// selectImageArtifacts 把一次运行的产物过滤到本端点视为「生成图像」的那些：
// "output" 分区、且扩展名已知。
//
// runtime.ArtifactRef 不携带节点身份，因此无法把一个已列举的产物与模板声明的
// Output.Node 做运行期关联——这层关联只在启动期被结构性地强制过一次（见
// main.go：已配置模板必须声明至少一个 Type 为 "image" 的 Output），运行期
// 则由这个扩展名约定代为判断。一个非图像的 "output" 分区产物（例如模板作者
// 自己的 Save 节点存下的调试文件）会被静默跳过而不是当作错误，这是更有用的
// 默认行为，但也是一个已知限制——见 Gateway README「图像生成」一节。
func selectImageArtifacts(refs []runtime.ArtifactRef) []runtime.ArtifactRef {
	var out []runtime.ArtifactRef
	for _, ref := range refs {
		if ref.Type != "output" {
			continue
		}
		if imageArtifactExtensions[strings.ToLower(path.Ext(ref.Filename))] {
			out = append(out, ref)
		}
	}
	return out
}

// promptInputs builds the scalar input map tpl.Bind accepts, from a validated
// request. It is a small helper only so imagesGenerations itself stays
// readable.
//
// promptInputs 从一个已校验的请求构造 tpl.Bind 接受的标量输入映射。把它拆成
// 一个小函数，只是为了让 imagesGenerations 本身保持可读。
func promptInputs(prompt string, width, height int, hasSize bool, quality, style string) (map[string]json.RawMessage, error) {
	promptJSON, err := json.Marshal(prompt)
	if err != nil {
		return nil, err
	}
	inputs := map[string]json.RawMessage{imagesInputPrompt: promptJSON}
	if hasSize {
		widthJSON, err := json.Marshal(width)
		if err != nil {
			return nil, err
		}
		heightJSON, err := json.Marshal(height)
		if err != nil {
			return nil, err
		}
		inputs[imagesInputWidth] = widthJSON
		inputs[imagesInputHeight] = heightJSON
	}
	if quality != "" {
		qualityJSON, err := json.Marshal(quality)
		if err != nil {
			return nil, err
		}
		inputs[imagesInputQuality] = qualityJSON
	}
	if style != "" {
		styleJSON, err := json.Marshal(style)
		if err != nil {
			return nil, err
		}
		inputs[imagesInputStyle] = styleJSON
	}
	return inputs, nil
}
