package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"AIServeWeave/common/runtime"
)

// inferenceUnsupported names the first field the inference image path cannot
// honour. Its backend takes a prompt only, so every size, quality and output
// control is refused by name instead of being silently dropped.
//
// inferenceUnsupported 指出推理生图路径无法兑现的第一个字段。它的后端只接受
// 提示词，因此尺寸、质量和输出控制一律按名字拒绝，而不是静默丢弃。
func (req imagesRequest) inferenceUnsupported() string {
	switch {
	case req.N != nil && *req.N != 1:
		return "n"
	case req.ResponseFormat != "" && req.ResponseFormat != "b64_json":
		return "response_format"
	case req.Size != "" && req.Size != "auto":
		return "size"
	case req.Quality != "" && req.Quality != "auto":
		return "quality"
	case req.Style != "":
		return "style"
	case req.Background != "" && req.Background != "auto":
		return "background"
	case req.OutputFormat != "":
		return "output_format"
	case req.OutputCompression != nil:
		return "output_compression"
	case req.Moderation != "":
		return "moderation"
	case req.PartialImages != nil:
		return "partial_images"
	case req.Stream:
		return "stream"
	}
	return ""
}

// inferenceImages serves POST /v1/images/generations for a model a node
// published as an image model (for example the Codex CLI's codex-image),
// instead of the ComfyUI workflow template. The image is read through a
// bounded buffer and answered as b64_json; the prompt is never logged.
//
// inferenceImages 为某个节点作为图像模型发布的 model（例如 Codex CLI 的
// codex-image）提供 POST /v1/images/generations，而不是走 ComfyUI 工作流模板。
// 图像经有界缓冲读取后以 b64_json 作答；提示词从不写入日志。
func (h *handlers) inferenceImages(w http.ResponseWriter, r *http.Request, req imagesRequest) {
	if strings.TrimSpace(req.Prompt) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "prompt is required")
		return
	}
	if len(req.Prompt) > runtime.MaxImagePromptBytes || !utf8.ValidString(req.Prompt) {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "prompt is too long or not valid UTF-8")
		return
	}
	if field := req.inferenceUnsupported(); field != "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "unsupported_parameter",
			field+" is not supported for model "+req.Model)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.imagesGenerationTimeout)
	defer cancel()
	identity, _ := IdentityFrom(r.Context())
	meta, stream, candidate, err := h.sched.GenerateImage(ctx, runtime.ImageRequest{
		Model: req.Model, Prompt: req.Prompt, TenantID: identity.TenantID, KeyID: identity.KeyID,
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeOpenAIError(w, http.StatusGatewayTimeout, "timeout_error", "timeout",
				"image generation did not finish before the configured timeout")
			return
		}
		handleDispatchError(w, h.logger, err)
		return
	}
	defer stream.Close()
	if meta.Size <= 0 || meta.Size > MaxImageResponseBytes {
		h.logger.Error("image metadata is out of bounds", slog.String("model", req.Model), slog.Int64("size", meta.Size))
		writeOpenAIError(w, http.StatusBadGateway, "api_error", "response_too_large", "the generated image exceeds this gateway's size limit")
		return
	}
	data := make([]byte, 0, meta.Size)
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			handleDispatchError(w, h.logger, err)
			return
		}
		if event.Meta != nil || int64(len(data)+len(event.Data)) > meta.Size {
			h.logger.Error("image stream does not match its metadata", slog.String("model", req.Model))
			writeOpenAIError(w, http.StatusBadGateway, "api_error", "upstream_protocol", "the node returned a malformed image")
			return
		}
		data = append(data, event.Data...)
	}
	if int64(len(data)) != meta.Size {
		h.logger.Error("image stream ended early", slog.String("model", req.Model))
		writeOpenAIError(w, http.StatusBadGateway, "api_error", "upstream_protocol", "the node returned a truncated image")
		return
	}
	h.logger.Info("image generated",
		slog.String("model", req.Model), slog.String("node_id", candidate.NodeID), slog.String("runtime_id", candidate.RuntimeID),
		slog.String("content_type", meta.ContentType), slog.Int("bytes", len(data)), slog.String("request_id", requestIDFrom(r.Context())))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(imagesResponse{Created: h.clock.Now().Unix(), Data: []imagesDataJSON{{
		B64JSON: base64.StdEncoding.EncodeToString(data), RevisedPrompt: meta.RevisedPrompt,
	}}})
}
