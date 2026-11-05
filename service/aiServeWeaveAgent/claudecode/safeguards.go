package claudecode

import (
	"bytes"
	"encoding/json"

	"AIServeWeave/common/runtime"
)

// decodeMessagesRequest keeps guarded inference structured so opaque policy provenance still matches it.
// decodeMessagesRequest 保留受检查推理的原始结构，使不透明策略来源证明仍与其对应。
func decodeMessagesRequest(raw json.RawMessage, out *messagesRequest) error {
	var probe struct {
		Safeguards []json.RawMessage `json:"safeguards"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return errMessages
	}
	if len(probe.Safeguards) == 0 {
		return decodeStrict(raw, out)
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return errMessages
	}
	for key := range doc {
		switch key {
		case "model", "max_tokens", "stream", "system", "messages", "tools", "tool_choice", "metadata", "thinking", "output_config", "temperature", "stop_sequences", "context_management", "compaction", "cache_control", "top_p", "top_k", "service_tier", "safeguards":
		default:
			return errMessages
		}
	}
	if json.Unmarshal(raw, out) != nil {
		return errMessages
	}
	out.nativeDocument = doc
	return nil
}

// preserveMessagesContent overlays accumulated standard fields on the original block objects.
// preserveMessagesContent 将已累积的标准字段叠加到原始内容块，保留未识别的原生字段。
func preserveMessagesContent(original []map[string]json.RawMessage, accumulated any) any {
	data, err := json.Marshal(accumulated)
	var blocks []map[string]json.RawMessage
	if err != nil || json.Unmarshal(data, &blocks) != nil || len(blocks) != len(original) {
		return accumulated
	}
	for i, block := range blocks {
		var kind string
		_ = json.Unmarshal(original[i]["type"], &kind)
		for key, value := range block {
			if kind == "compaction" && key == "content" {
				continue // accumulated by the native compaction delta / 已由原生压缩增量累积
			}
			original[i][key] = value
		}
	}
	return original
}

func nativeThinkingEnabled(raw json.RawMessage) bool {
	var hint struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(bytes.TrimSpace(raw), &hint) == nil && (hint.Type == "adaptive" || hint.Type == "enabled")
}

func validateSafeguards(guards []json.RawMessage) error {
	if len(guards) == 0 {
		return nil
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	err := encoder.Encode(guards)
	data := bytes.TrimSpace(encoded.Bytes())
	if err != nil || runtime.ValidateMessagesSafeguards(data) != nil {
		return errMessages
	}
	return nil
}

// remapSafeguardResults changes only the IDs of actual upstream verdicts, preserving outcomes and unknown fields.
// remapSafeguardResults 仅替换真实上游判定关联的 ID，保留判定结果及未知字段。
func remapSafeguardResults(raw json.RawMessage, ids map[string]string) (json.RawMessage, error) {
	var results []map[string]json.RawMessage
	if json.Unmarshal(raw, &results) != nil {
		return nil, errCLIProtocol
	}
	for _, result := range results {
		var kind string
		_ = json.Unmarshal(result["type"], &kind)
		if kind != "dangerous_tool_use" {
			continue
		}
		var status map[string]json.RawMessage
		if json.Unmarshal(result["status"], &status) != nil {
			return nil, errCLIProtocol
		}
		var state string
		_ = json.Unmarshal(status["type"], &state)
		if state != "available" {
			continue
		}
		var calls map[string]json.RawMessage
		if json.Unmarshal(status["tool_uses"], &calls) != nil || calls == nil {
			return nil, errCLIProtocol
		}
		mapped := make(map[string]json.RawMessage, len(calls))
		for id, verdict := range calls {
			public, ok := ids[id]
			if !ok {
				return nil, errCLIProtocol
			}
			if public != "" {
				mapped[public] = verdict
			}
		}
		status["tool_uses"], _ = json.Marshal(mapped)
		result["status"], _ = json.Marshal(status)
	}
	return json.Marshal(results)
}

func remapSafeguardDelta(event json.RawMessage, ids map[string]string) (json.RawMessage, json.RawMessage, error) {
	var document map[string]json.RawMessage
	if json.Unmarshal(event, &document) != nil {
		return nil, nil, errCLIProtocol
	}
	var delta map[string]json.RawMessage
	if json.Unmarshal(document["delta"], &delta) != nil {
		return nil, nil, errCLIProtocol
	}
	if results, ok := delta["safeguard_results"]; ok {
		mapped, err := remapSafeguardResults(results, ids)
		if err != nil {
			return nil, nil, err
		}
		delta["safeguard_results"] = mapped
		document["delta"], _ = json.Marshal(delta)
		encoded, err := json.Marshal(document)
		return encoded, mapped, err
	}
	return event, nil, nil
}
