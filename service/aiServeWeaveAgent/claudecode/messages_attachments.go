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

// nativeExtension reports whether a native block is one of this platform's
// attachment extensions, which the upstream API would refuse: a video, or a
// base64 document other than a PDF (Office and text files).
//
// nativeExtension 判断原生内容块是否属于本平台的附件扩展（上游 API 会拒绝）：视频，
// 或 PDF 之外的 base64 文档（Office 与文本文件）。
func nativeExtension(block map[string]json.RawMessage) bool {
	var kind string
	_ = json.Unmarshal(block["type"], &kind)
	if kind == "video" {
		return true
	}
	var source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
	}
	return kind == "document" && json.Unmarshal(block["source"], &source) == nil && source.Type == "base64" && source.MediaType != "application/pdf"
}

// nativeBlocks converts the extension blocks of one content array in place,
// recursing into tool results; it reports whether anything changed. A
// converted block's cache_control moves to the last block it became, so the
// caller's cache breakpoint stays where it was.
//
// nativeBlocks 就地转换一个内容数组中的扩展块，并递归进入工具结果；返回是否有改动。被转换
// 块的 cache_control 移到其转换结果的最后一块上，使调用方的缓存断点位置不变。
func (s *messagesSession) nativeBlocks(ctx context.Context, raw json.RawMessage) (json.RawMessage, bool, error) {
	var list []map[string]json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return raw, false, nil // a string content has no attachments / 字符串内容没有附件
	}
	var output []map[string]json.RawMessage
	changed := false
	for _, block := range list {
		var kind string
		_ = json.Unmarshal(block["type"], &kind)
		if kind == "tool_result" && len(block["content"]) > 0 {
			inner, innerChanged, err := s.nativeBlocks(ctx, block["content"])
			if err != nil {
				return nil, false, err
			}
			if innerChanged {
				block["content"], changed = inner, true
			}
		}
		if !nativeExtension(block) {
			output = append(output, block)
			continue
		}
		var parsed messageBlock
		if json.Unmarshal(mustMarshal(block), &parsed) != nil {
			return nil, false, &attachmentFailure{attachments.ErrInvalid}
		}
		// Upstream accepts documents in tool results, so these convert as top-level content.
		// 上游接受工具结果中的文档，因此这里按顶层内容转换。
		prepared, err := s.prepareBlocks(ctx, []messageBlock{parsed}, false)
		if err != nil {
			return nil, false, err
		}
		for i, part := range prepared {
			var converted map[string]json.RawMessage
			_ = json.Unmarshal(mustMarshal(part), &converted)
			if cache, ok := block["cache_control"]; ok && i == len(prepared)-1 {
				converted["cache_control"] = cache
			}
			output = append(output, converted)
		}
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	encoded, err := json.Marshal(output)
	return encoded, true, err
}

// nativeMessages returns the native messages with platform attachments
// converted into content the upstream accepts, or nil when there are none.
// Every other block keeps its original fields.
//
// nativeMessages 返回把本平台附件转换为上游可接受内容后的原生消息；没有附件时返回 nil。
// 其他内容块保留全部原始字段。
func (s *messagesSession) nativeMessages(ctx context.Context) (json.RawMessage, error) {
	var messages []map[string]json.RawMessage
	if json.Unmarshal(s.request.nativeDocument["messages"], &messages) != nil {
		return nil, errMessages
	}
	changed := false
	for _, message := range messages {
		content, messageChanged, err := s.nativeBlocks(ctx, message["content"])
		if err != nil {
			return nil, err
		}
		if messageChanged {
			message["content"], changed = content, true
		}
	}
	if !changed {
		return nil, nil
	}
	encoded, err := json.Marshal(messages)
	if err != nil || len(encoded) > maxRequestBytes {
		return nil, &attachmentFailure{attachments.ErrLimit}
	}
	return encoded, nil
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
