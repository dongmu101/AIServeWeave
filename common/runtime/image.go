package runtime

import "context"

// Image generation bounds shared by every hop. They are contract values, not
// tuning knobs: the Agent refuses to produce more and the Gateway refuses to
// accept more, so both sides must agree on the same numbers.
//
// 图像生成在每一跳共用的上限。它们是契约值而非调优旋钮：Agent 拒绝产出更多，
// Gateway 拒绝接收更多，因此两边必须认同同一组数字。
const (
	// MaxImagePromptBytes bounds ImageRequest.Prompt.
	// MaxImagePromptBytes 限制 ImageRequest.Prompt 的字节数。
	MaxImagePromptBytes = 32 << 10
	// MaxImageBytes bounds one generated image, matching the Gateway's
	// synchronous b64_json response limit.
	// MaxImageBytes 限制单张生成图像的字节数，与 Gateway 同步 b64_json 响应上限一致。
	MaxImageBytes = 32 << 20
	// MaxImageChunkBytes bounds the Data of one ImageEvent, keeping each
	// tunnel frame far below the frame limit.
	// MaxImageChunkBytes 限制单个 ImageEvent 的 Data，使每个隧道帧远低于帧上限。
	MaxImageChunkBytes = 256 << 10
)

// ImageRequest asks for exactly one generated image. It carries a model ID
// and a prompt only: no URL, path, header or credential.
//
// ImageRequest 请求生成恰好一张图像。它只携带模型 ID 与提示词，不含 URL、路径、
// 请求头或凭据。
type ImageRequest struct {
	// Model is an image model ID the runtime itself published.
	// Model 是运行时自己发布的图像模型 ID。
	Model string
	// Prompt describes the image; it must never be logged.
	// Prompt 描述要生成的图像，绝不能写入日志。
	Prompt string
	// TenantID and KeyID identify the authenticated caller; they are not credentials.
	// TenantID 与 KeyID 标识已认证的调用方，它们不是凭据。
	TenantID string
	KeyID    string
}

// ImageMeta describes the image whose bytes follow it.
// ImageMeta 描述随后到达字节的那张图像。
type ImageMeta struct {
	// ContentType is detected from the bytes, never from a file name.
	// ContentType 依据字节内容判定，从不依据文件名。
	ContentType string
	// Size is the exact total of every following ImageEvent.Data.
	// Size 是随后所有 ImageEvent.Data 的精确总字节数。
	Size int64
	// RevisedPrompt is the prompt the backend actually used, when it reports one.
	// RevisedPrompt 是后端实际使用的提示词，后端报告时才有值。
	RevisedPrompt string
	Usage         *Usage
}

// ImageEvent is one element of an image generation stream. The first event
// carries Meta and no Data; every later event carries Data only, in order.
//
// ImageEvent 是图像生成流中的一个元素。第一个事件只携带 Meta、不含 Data；
// 之后每个事件只按顺序携带 Data。
type ImageEvent struct {
	Meta *ImageMeta
	Data []byte
}

// ImageRuntime is the optional interface of a runtime that can generate
// images. It is optional, like MessagesRuntime, so backends without the
// feature need no stub method.
//
// ImageRuntime 是可生成图像的运行时所实现的可选接口。与 MessagesRuntime 一样
// 是可选的，不具备该能力的后端无需实现空方法。
type ImageRuntime interface {
	Runtime
	// GenerateImage streams one image as a Meta event followed by bounded Data
	// events. The stream never holds more than one chunk beyond what the
	// caller has read.
	//
	// GenerateImage 以一个 Meta 事件加若干有界 Data 事件流式返回一张图像。
	// 流在调用方已读取的部分之外，最多只持有一个分块。
	GenerateImage(ctx context.Context, req ImageRequest) (Stream[ImageEvent], error)
}
