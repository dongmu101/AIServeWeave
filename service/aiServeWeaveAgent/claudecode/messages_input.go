package claudecode

import (
	"bytes"
	"encoding/json"

	"AIServeWeave/service/aiServeWeaveAgent/attachments"
)

// Claude Code 2.1.319 refuses settings files larger than 2 MiB before emitting any result.
// Claude Code 2.1.319 在输出结果之前便拒绝大于 2 MiB 的设置文件。
const maxCLISettingsBytes = 2 << 20

func encodeMessagesJSON(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, errMessages
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

// nativeInputSettings keeps settings bounded. Large documents enter the same
// streamed API call through the per-process proxy, after CLI authentication.
// nativeInputSettings 限制设置文件大小；大文档在 CLI 完成认证后，经每进程代理进入
// 同一次流式 API 调用，完整历史不放入进程环境或受限的设置文件。
func nativeInputSettings(extra map[string]any) (settings, request []byte, err error) {
	body, err := encodeMessagesJSON(extra)
	if err != nil {
		return nil, nil, err
	}
	encodeSettings := func(body []byte) ([]byte, error) {
		return encodeMessagesJSON(map[string]any{"disableAllHooks": true, "autoMemoryEnabled": false, "env": map[string]string{"CLAUDE_CODE_EXTRA_BODY": string(body)}})
	}
	settings, err = encodeSettings(body)
	if err != nil || len(settings) <= maxCLISettingsBytes {
		return settings, nil, err
	}
	// betas belongs to the SDK header selector, not the HTTP JSON document.
	// betas 属于 SDK 的请求头选择参数，不是 HTTP JSON 文档字段。
	payload := make(map[string]any, len(extra))
	for key, value := range extra {
		if key != "betas" {
			payload[key] = value
		}
	}
	request, err = encodeMessagesJSON(payload)
	if err != nil {
		return nil, nil, err
	}
	if len(request) > maxRequestBytes {
		return nil, nil, &attachmentFailure{attachments.ErrLimit}
	}
	selectors := map[string]any{}
	if betas, ok := extra["betas"]; ok {
		selectors["betas"] = betas
	}
	body, err = encodeMessagesJSON(selectors)
	if err != nil {
		return nil, nil, err
	}
	settings, err = encodeSettings(body)
	return settings, request, err
}
