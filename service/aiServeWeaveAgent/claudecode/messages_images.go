package claudecode

import (
	"AIServeWeave/service/aiServeWeaveAgent/attachments"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// imageSource carries inline image bytes, never a URL for the Agent to fetch.
// imageSource 只承载内联图片字节，不接受需要 Agent 抓取的 URL。
type imageSource = attachments.Source

func validateImage(block messageBlock) error {
	source := block.Source
	if source == nil || source.Type != "base64" || source.Data == "" {
		return errMessages
	}
	switch source.MediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
	default:
		return errMessages
	}
	// Decode without another image-sized allocation. The enclosing Messages
	// body already bounds the encoded bytes to maxRequestBytes.
	// 流式校验编码，避免再分配整张图片；编码字节已受 Messages 请求大小约束。
	n, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(source.Data)))
	if err != nil || n == 0 {
		return errMessages
	}
	return nil
}

// inputBlocks validates text and inline media before CLI/MCP preparation.
// inputBlocks 在 CLI/MCP 内容转换前校验文本、图片、文档和视频。
func inputBlocks(raw json.RawMessage) ([]messageBlock, error) {
	if len(raw) == 0 {
		return []messageBlock{{Type: "text"}}, nil
	}
	list, err := blocks(raw)
	if err != nil {
		return nil, err
	}
	for _, block := range list {
		switch block.Type {
		case "text":
			if block.Source != nil {
				return nil, errMessages
			}
		case "image":
			if err := validateImage(block); err != nil {
				return nil, err
			}
		case "document", "video":
			if err := attachments.ValidateShape(attachmentPart(block)); err != nil {
				return nil, errMessages
			}
		default:
			return nil, errMessages
		}
	}
	return list, nil
}

// sessionInput keeps the text-only wire unchanged. Images and PDFs use the CLI's
// stream-json user envelope. For folded history, numbered placeholders keep
// each image's location while the actual bytes travel as image blocks once,
// including images nested in historical tool results.
// sessionInput 保持纯文本输入不变；有图片或 PDF 时使用 CLI 的 stream-json 用户信封。
// 历史记录用编号标明每张图的位置，图片字节作为独立图片块只传一次，包含历史工具结果里的图。
func sessionInput(messages []bridgeMessage) (string, bool) {
	var images []messageBlock
	var detach func([]messageBlock) []messageBlock
	detach = func(list []messageBlock) []messageBlock {
		list = append([]messageBlock(nil), list...)
		for i, block := range list {
			switch block.Type {
			case "image", "document":
				images = append(images, block)
				list[i] = messageBlock{Type: "text", Text: fmt.Sprintf("[%s %d attached below]", block.Type, len(images))}
			case "tool_result":
				content, _ := inputBlocks(block.Content)
				list[i].Content, _ = json.Marshal(detach(content))
			}
		}
		return list
	}
	history := make([]bridgeMessage, len(messages))
	for i, message := range messages {
		list, _ := blocks(message.Content)
		content, _ := json.Marshal(detach(list))
		history[i] = bridgeMessage{Role: message.Role, Content: content}
	}
	if len(images) == 0 {
		return sessionPrompt(messages), false
	}
	content := []messageBlock{{Type: "text", Text: sessionPrompt(history)}}
	for i, image := range images {
		content = append(content, messageBlock{Type: "text", Text: fmt.Sprintf("%s %d:", image.Type, i+1)}, image)
	}
	if len(messages) == 1 {
		list, _ := inputBlocks(messages[0].Content)
		if list != nil {
			content = list // preserve the caller's text/image order for a single prompt
		}
	}
	envelope := struct {
		Type            string        `json:"type"`
		Message         bridgeMessage `json:"message"`
		ParentToolUseID *string       `json:"parent_tool_use_id"`
	}{Type: "user", Message: bridgeMessage{Role: "user"}}
	envelope.Message.Content, _ = json.Marshal(content)
	data, _ := json.Marshal(envelope)
	return string(data) + "\n", true
}
