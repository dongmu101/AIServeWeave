package claudecode

import (
	"context"
	"encoding/json"
	"errors"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/attachments"
)

type attachmentFailure struct{ cause error }

func (e *attachmentFailure) Error() string { return e.cause.Error() }
func (e *attachmentFailure) Unwrap() error { return e.cause }

func attachmentPart(b messageBlock) attachments.Part {
	return attachments.Part{Type: b.Type, Text: b.Text, Title: b.Title, Source: b.Source}
}

func (s *messagesSession) prepareBlocks(ctx context.Context, list []messageBlock, tool bool) ([]messageBlock, error) {
	var output []messageBlock
	for _, block := range list {
		if block.Type == "tool_result" {
			inner, _ := inputBlocks(block.Content)
			prepared, err := s.prepareBlocks(ctx, inner, tool)
			if err != nil {
				return nil, err
			}
			block.Content, _ = json.Marshal(prepared)
		} else if block.Type == "document" || block.Type == "video" {
			key := canonical(struct {
				Part attachments.Part
				Tool bool
			}{attachmentPart(block), tool})
			s.cacheMu.Lock()
			cached, ok := s.attachmentCache[key]
			s.cacheMu.Unlock()
			if !ok {
				parts, err := s.processor.Convert(ctx, attachmentPart(block), tool)
				if err != nil {
					return nil, &attachmentFailure{err}
				}
				for _, part := range parts {
					cached = append(cached, messageBlock{Type: part.Type, Text: part.Text, Title: part.Title, Source: part.Source})
				}
				s.cacheMu.Lock()
				encoded, _ := json.Marshal(cached)
				if len(encoded) > maxRequestBytes {
					s.cacheMu.Unlock()
					return nil, &attachmentFailure{attachments.ErrLimit}
				}
				if s.attachmentCacheBytes+len(encoded) > maxRequestBytes {
					s.attachmentCache = nil
					s.attachmentCacheBytes = 0
				}
				if s.attachmentCache == nil {
					s.attachmentCache = make(map[[32]byte][]messageBlock)
				}
				s.attachmentCache[key] = cached
				s.attachmentCacheBytes += len(encoded)
				s.cacheMu.Unlock()
			}
			output = append(output, cached...)
			continue
		}
		output = append(output, block)
	}
	data, _ := json.Marshal(output)
	if len(data) > maxRequestBytes {
		return nil, &attachmentFailure{attachments.ErrLimit}
	}
	return output, nil
}

func (s *messagesSession) prepareMessages(ctx context.Context) ([]bridgeMessage, error) {
	result := make([]bridgeMessage, len(s.request.Messages))
	for i, message := range s.request.Messages {
		list, _ := blocks(message.Content)
		prepared, err := s.prepareBlocks(ctx, list, false)
		if err != nil {
			return nil, err
		}
		message.Content, _ = json.Marshal(prepared)
		result[i] = message
	}
	return result, nil
}

// attachmentError logs only a fixed category; it never logs file names or process output.
// attachmentError 只记录固定分类，不记录文件名、附件内容或转换器原始输出。
func (r *Runtime) attachmentError(err error) error {
	var failure *attachmentFailure
	if !errors.As(err, &failure) {
		return nil
	}
	code, reason := runtime.ErrorUpstream, "conversion_failed"
	switch {
	case errors.Is(err, attachments.ErrDependency):
		code, reason = runtime.ErrorCapability, "converter_unavailable"
	case errors.Is(err, attachments.ErrInvalid):
		code, reason = runtime.ErrorInvalidConfig, "invalid_attachment"
	case errors.Is(err, attachments.ErrLimit):
		code, reason = runtime.ErrorResponseTooLarge, "attachment_limit"
	case errors.Is(err, attachments.ErrBusy):
		code, reason = runtime.ErrorBackpressure, "converter_busy"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code, reason = runtime.ErrorTimeout, "conversion_timeout"
	}
	r.logger.Warn("Claude attachment processing failed", "runtime_id", r.cfg.ID, "reason", reason)
	return &runtime.RuntimeError{Code: code, RuntimeID: r.cfg.ID, Kind: runtime.KindClaude, Operation: "attachments", Message: reason}
}
