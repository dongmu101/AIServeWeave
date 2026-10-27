package codexcli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"AIServeWeave/common/runtime"
)

// ImageModelID is the one image model this adapter publishes. Codex's
// built-in image tool does not let a caller choose the upstream image model,
// so the ID deliberately names no model version.
//
// ImageModelID 是本适配器发布的唯一图像模型。Codex 内置生图工具不允许调用方
// 选择上游图像模型，因此这个 ID 刻意不标注任何模型版本。
const ImageModelID = "codex-image"

// maxImageFrameBytes bounds the one frame that carries a finished image as
// base64, sized for runtime.MaxImageBytes after base64 inflation.
// maxImageFrameBytes 限制携带 base64 成品图像的那一帧，按 runtime.MaxImageBytes
// 经 base64 膨胀后的大小设定。
const maxImageFrameBytes = 48 << 20

var errRateLimited = errors.New("codexcli: account image generation limit reached")
var errNoImage = errors.New("codexcli: turn finished without an image")

const imageBaseInstructions = "You generate images for a remote client. Use the image generation tool to create exactly one image matching the user's description. Do not ask questions. Do not call any other tool. Never access files or execute commands."

// imageContentTypes is the closed set of formats this adapter returns.
// imageContentTypes 是本适配器返回的封闭格式集合。
var imageContentTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true}

type generatedImage struct {
	data          []byte
	contentType   string
	revisedPrompt string
	threadID      string
}

// GenerateImage runs one isolated image generation turn and returns the
// finished image as a bounded event stream. The CLI process is reaped and
// its saved copy deleted before the first event is available.
//
// GenerateImage 执行一次隔离的生图回合，并以有界事件流返回成品图像。第一个事件
// 可读之前，CLI 进程已被回收，其保存的副本也已删除。
func (r *Runtime) GenerateImage(parent context.Context, req runtime.ImageRequest) (runtime.Stream[runtime.ImageEvent], error) {
	if req.Model != ImageModelID {
		return nil, r.wrap("image_generate", errUnsupported)
	}
	if strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > runtime.MaxImagePromptBytes || !utf8.ValidString(req.Prompt) {
		return nil, r.wrap("image_generate", ErrProtocol)
	}
	release, err := r.limiter.Acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	// No idle deadline: the image tool is silent for tens of seconds by design.
	// 不设空闲期限：生图工具按设计会静默数十秒。
	ctx, finish, touch, err := r.operation(parent, r.cfg.RequestTimeout, 0)
	if err != nil {
		return nil, err
	}
	defer finish()
	var image generatedImage
	err = withServerMode(ctx, r.executable, r.env, true, func(p *protocol, dir string) error {
		p.activity = touch
		var runErr error
		image, runErr = runImage(p, dir, req.Prompt, accountInstructions(r.env))
		return runErr
	})
	removeSavedImages(r.env, image.threadID)
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, errFinished) {
		err = cause
	}
	if err != nil {
		return nil, r.wrap("image_generate", err)
	}
	meta := runtime.ImageMeta{ContentType: image.contentType, Size: int64(len(image.data)), RevisedPrompt: image.revisedPrompt}
	return &imageStream{meta: &meta, data: image.data}, nil
}

// runImage drives one image turn and stops at the first finished image.
// runImage 驱动一次生图回合，在第一张完成的图像处停止。
func runImage(p *protocol, dir, prompt string, instructions []string) (generatedImage, error) {
	var out generatedImage
	if err := initializeServer(p); err != nil {
		return out, err
	}
	var caps struct {
		ImageGeneration bool `json:"imageGeneration"`
	}
	if err := p.request(3, "modelProvider/capabilities/read", map[string]any{}, &caps); err != nil {
		return out, err
	}
	if !caps.ImageGeneration {
		return out, errUnsupported
	}
	params := map[string]any{
		"cwd": dir, "ephemeral": true, "sandbox": "read-only", "approvalPolicy": "never", "environments": []any{},
		"baseInstructions": imageBaseInstructions, "developerInstructions": "Generate exactly one image, then stop.", "dynamicTools": []any{},
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		InstructionSources []string `json:"instructionSources"`
	}
	if err := p.request(4, "thread/start", params, &thread); err != nil {
		return out, err
	}
	if !validThreadID(thread.Thread.ID) {
		return out, ErrProtocol
	}
	out.threadID = thread.Thread.ID
	for _, path := range thread.InstructionSources {
		allowed := false
		for _, expected := range instructions {
			if filepath.Clean(path) == expected {
				allowed = true
				break
			}
		}
		if !allowed {
			return out, ErrIsolation
		}
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	input := []any{map[string]string{"type": "text", "text": prompt}}
	if err := p.request(5, "turn/start", map[string]any{"threadId": out.threadID, "input": input}, &turn); err != nil {
		return out, err
	}
	if turn.Turn.ID == "" {
		return out, ErrProtocol
	}
	for {
		msg, err := p.read()
		if err != nil {
			return out, err
		}
		if len(msg.ID) > 0 {
			// The image turn registers no dynamic tools and approves nothing.
			// 生图回合不注册动态工具，也不批准任何请求。
			return out, ErrUnexpectedTool
		}
		var n struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Item     struct {
				Type          string          `json:"type"`
				Status        string          `json:"status"`
				Result        string          `json:"result"`
				RevisedPrompt *string         `json:"revisedPrompt"`
				Failure       json.RawMessage `json:"failure"`
			} `json:"item"`
			Turn struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if json.Unmarshal(msg.Params, &n) != nil {
			return out, ErrProtocol
		}
		if (n.ThreadID != "" && n.ThreadID != out.threadID) || (n.TurnID != "" && n.TurnID != turn.Turn.ID) {
			return out, ErrProtocol
		}
		switch msg.Method {
		case "item/completed":
			if n.Item.Type != "imageGeneration" {
				continue
			}
			if failure := imageFailure(n.Item.Failure); failure != nil {
				return out, failure
			}
			data, contentType, err := decodeImage(n.Item.Result)
			if err != nil {
				return out, err
			}
			out.data, out.contentType = data, contentType
			if n.Item.RevisedPrompt != nil && len(*n.Item.RevisedPrompt) <= runtime.MaxImagePromptBytes && utf8.ValidString(*n.Item.RevisedPrompt) {
				out.revisedPrompt = *n.Item.RevisedPrompt
			}
			// Stop at the first image so one request can never yield two.
			// 在第一张图处停止，一次请求永远不会产出两张。
			return out, nil
		case "turn/completed":
			if n.Turn.ID != turn.Turn.ID {
				return out, ErrProtocol
			}
			return out, errNoImage
		case "error":
			return out, ErrTurn
		}
	}
}

// imageFailure maps a reported image failure to a fixed error category.
// imageFailure 把上报的生图失败映射为固定错误类别。
func imageFailure(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var failure struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &failure) == nil && failure.Type == "usageLimitExceeded" {
		return errRateLimited
	}
	return ErrTurn
}

// decodeImage decodes a bounded base64 image and detects its type from the bytes.
// decodeImage 解码有界的 base64 图像，并依据字节判定其类型。
func decodeImage(encoded string) ([]byte, string, error) {
	if encoded == "" {
		return nil, "", errNoImage
	}
	if base64.StdEncoding.DecodedLen(len(encoded)) > runtime.MaxImageBytes+2 {
		return nil, "", ErrProtocol
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > runtime.MaxImageBytes {
		return nil, "", ErrProtocol
	}
	contentType := http.DetectContentType(data)
	if !imageContentTypes[contentType] {
		return nil, "", ErrProtocol
	}
	return data, contentType, nil
}

// validThreadID accepts only identifiers that are safe as one path element.
// validThreadID 只接受可安全作为单个路径元素的标识符。
func validThreadID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// removeSavedImages deletes the copy Codex saves under CODEX_HOME for this
// thread. The directory is derived from the validated thread ID, never from
// a path the app-server reported, so this cannot delete anything else.
//
// removeSavedImages 删除 Codex 为本线程保存在 CODEX_HOME 下的副本。目录由校验
// 过的线程 ID 推导，从不取自 app-server 报告的路径，因此不可能删到别的东西。
func removeSavedImages(env []string, threadID string) {
	home := codexHomeDir(env)
	if home == "" || !filepath.IsAbs(home) || !validThreadID(threadID) {
		return
	}
	_ = os.RemoveAll(filepath.Join(home, "generated_images", threadID))
}

// imageStream hands out one meta event and then bounded slices of the image.
// imageStream 先交出一个元数据事件，再交出图像的有界切片。
type imageStream struct {
	mu        sync.Mutex
	meta      *runtime.ImageMeta
	data      []byte
	sentMeta  bool
	offset    int
	committed bool
	closed    bool
}

func (s *imageStream) Recv() (runtime.ImageEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return runtime.ImageEvent{}, runtime.ErrStreamClosed
	}
	s.committed = true
	if !s.sentMeta {
		s.sentMeta = true
		return runtime.ImageEvent{Meta: s.meta}, nil
	}
	if s.offset >= len(s.data) {
		return runtime.ImageEvent{}, io.EOF
	}
	end := min(s.offset+runtime.MaxImageChunkBytes, len(s.data))
	chunk := s.data[s.offset:end]
	s.offset = end
	return runtime.ImageEvent{Data: chunk}, nil
}

func (s *imageStream) Committed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.committed }

func (s *imageStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed, s.data = true, nil
	return nil
}

var _ runtime.ImageRuntime = (*Runtime)(nil)
