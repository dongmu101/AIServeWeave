package geminicli

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"AIServeWeave/common/runtime"
)

// GenerateImage calls generate_image once and returns bounded image chunks.
// GenerateImage 只调用一次 generate_image，并返回有大小限制的图像分块。
func (r *Runtime) GenerateImage(parent context.Context, req runtime.ImageRequest) (runtime.Stream[runtime.ImageEvent], error) {
	if req.Model != ImageModelID {
		return nil, r.wrap("image_generate", runtime.ErrCapabilityUnsupported)
	}
	if strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > runtime.MaxImagePromptBytes || !utf8.ValidString(req.Prompt) {
		return nil, &runtime.RuntimeError{Code: runtime.ErrorInvalidConfig, RuntimeID: r.cfg.ID, Kind: runtime.KindGeminiCLI, Operation: "image_generate", Message: "image prompt must be non-empty UTF-8 within the byte limit"}
	}
	release, err := r.limiter.Acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, finish, err := r.operation(parent, r.cfg.RequestTimeout)
	if err != nil {
		return nil, r.wrap("image_generate", err)
	}
	defer finish()
	var data []byte
	var contentType string
	err = r.withServer(ctx, func(p *protocol, dir string) error {
		if err := p.initialize(); err != nil {
			return err
		}
		var result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		args := map[string]any{"prompt": req.Prompt, "outputCount": 1, "format": "separate", "preview": false, "noPreview": true}
		if err := p.request(3, "tools/call", map[string]any{"name": "generate_image", "arguments": args}, &result); err != nil {
			return err
		}
		if result.IsError {
			var text strings.Builder
			for _, c := range result.Content {
				text.WriteString(c.Text)
			}
			return upstreamError(text.String())
		}
		// No file path from the MCP response is ever opened. Reap before inspecting output.
		// 从不打开 MCP 响应提供的文件路径；检查输出前先回收进程。
		p.reap()
		var err error
		data, contentType, err = readImage(dir)
		return err
	})
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	if err != nil {
		return nil, r.wrap("image_generate", err)
	}
	return &imageStream{meta: runtime.ImageMeta{ContentType: contentType, Size: int64(len(data))}, data: data}, nil
}

func readImage(dir string) ([]byte, string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", errProtocol
	}
	defer root.Close()
	info, err := root.Lstat("nanobanana-output")
	if err != nil || !info.IsDir() {
		return nil, "", errProtocol
	}
	out, err := root.Open("nanobanana-output")
	if err != nil {
		return nil, "", errProtocol
	}
	defer out.Close()
	entries, err := out.ReadDir(2)
	if (err != nil && err != io.EOF) || len(entries) != 1 || !entries[0].Type().IsRegular() {
		return nil, "", errProtocol
	}
	f, err := root.Open("nanobanana-output/" + entries[0].Name())
	if err != nil {
		return nil, "", errProtocol
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > runtime.MaxImageBytes {
		return nil, "", errProtocol
	}
	data, err := io.ReadAll(io.LimitReader(f, runtime.MaxImageBytes+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, "", errProtocol
	}
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return data, contentType, nil
	default:
		return nil, "", errProtocol
	}
}

type imageStream struct {
	mu                          sync.Mutex
	meta                        runtime.ImageMeta
	data                        []byte
	offset                      int
	sentMeta, committed, closed bool
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
		meta := s.meta
		return runtime.ImageEvent{Meta: &meta}, nil
	}
	if s.offset >= len(s.data) {
		s.data = nil
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
	s.closed = true
	s.data = nil
	return nil
}
