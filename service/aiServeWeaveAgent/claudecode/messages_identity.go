package claudecode

import (
	"encoding/json"
	"strings"
)

// Prefixes of the system blocks the Claude CLI puts ahead of every prompt:
// the billing header it computes, and the identity line that subscription
// credentials require. A caller that is not Claude Code (DeepSeek Harness,
// an SDK) sends neither, and its system would otherwise replace the CLI's.
//
// Claude CLI 放在每个提示最前的 system 块前缀：它计算出的计费头，以及订阅凭据要求的身份行。
// 不是 Claude Code 的调用方（DeepSeek Harness、SDK）两者都不发送，否则它的 system 会整体
// 替换掉 CLI 自己的。
const billingHeaderPrefix = "x-anthropic-billing-header:"

var identityPrefixes = []string{
	"You are Claude Code, Anthropic's official CLI for Claude",
	"You are a Claude agent, built on Anthropic's Claude Agent SDK",
}

type systemBlock = map[string]json.RawMessage

// mergeNativeRequest overlays the caller's document on the CLI's own request,
// as before, while keeping what the CLI's credentials need: the CLI's
// identity blocks go ahead of a caller system that lacks them, and the CLI's
// metadata.user_id fills a caller metadata that names none. A caller that is
// Claude Code already carries both and is overlaid unchanged.
//
// mergeNativeRequest 与之前一样把调用方文档覆盖到 CLI 自己的请求上，同时保留 CLI 凭据所需的
// 部分：调用方 system 缺少身份块时，把 CLI 的身份块放在前面；调用方 metadata 未给出 user_id
// 时，用 CLI 的 metadata.user_id 补上。调用方本身是 Claude Code 时两者都已具备，原样覆盖。
func mergeNativeRequest(seed, override map[string]json.RawMessage) {
	if system, ok := override["system"]; ok {
		if merged, changed := withCLIIdentity(seed["system"], system); changed {
			override["system"] = merged
		}
	}
	if metadata, ok := override["metadata"]; ok {
		if merged, changed := withCLIUserID(seed["metadata"], metadata); changed {
			override["metadata"] = merged
		}
	}
	// The CLI computes context edits for its own thinking mode before applying
	// the caller's settings. Only the caller's context edits belong to this turn.
	// CLI 在应用调用方设置之前按自己的思考模式计算上下文策略，本轮只使用调用方指定的策略。
	delete(seed, "context_management")
	for key, value := range override {
		seed[key] = value
	}
}

// withCLIIdentity returns caller with the CLI's billing and identity blocks
// put first, each without cache_control so the caller's breakpoint budget is
// unchanged. It leaves caller alone when caller already names an identity,
// when the CLI sent none, or when either system cannot be read.
//
// withCLIIdentity 返回把 CLI 计费块与身份块放在最前面的调用方 system，这些块去掉
// cache_control，调用方的缓存断点数量因此不变。调用方已带身份、CLI 未发送身份、或任一
// system 无法解析时，保持调用方不变。
func withCLIIdentity(cli, caller json.RawMessage) (json.RawMessage, bool) {
	callerBlocks, ok := systemBlocks(caller)
	if !ok || hasPrefixBlock(callerBlocks, identityPrefixes...) {
		return nil, false
	}
	cliBlocks, ok := systemBlocks(cli)
	if !ok || !hasPrefixBlock(cliBlocks, identityPrefixes...) {
		return nil, false
	}
	callerBilled := hasPrefixBlock(callerBlocks, billingHeaderPrefix)
	var merged []systemBlock
	for _, block := range cliBlocks {
		text := blockText(block)
		identity := hasAnyPrefix(text, identityPrefixes...)
		if !identity && (callerBilled || !strings.HasPrefix(text, billingHeaderPrefix)) {
			continue
		}
		copied := make(systemBlock, len(block))
		for key, value := range block {
			if key != "cache_control" {
				copied[key] = value
			}
		}
		merged = append(merged, copied)
	}
	merged = append(merged, callerBlocks...)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// withCLIUserID adds the CLI's metadata.user_id to a caller metadata object
// that has none. Anything that is not an object is left for the upstream.
//
// withCLIUserID 给没有 user_id 的调用方 metadata 对象补上 CLI 的 metadata.user_id。
// 不是对象的值留给上游判断。
func withCLIUserID(cli, caller json.RawMessage) (json.RawMessage, bool) {
	var callerFields, cliFields map[string]json.RawMessage
	if json.Unmarshal(caller, &callerFields) != nil || callerFields == nil {
		return nil, false
	}
	if id, ok := callerFields["user_id"]; ok && string(id) != "null" {
		return nil, false
	}
	if json.Unmarshal(cli, &cliFields) != nil || cliFields == nil {
		return nil, false
	}
	id, ok := cliFields["user_id"]
	if !ok || string(id) == "null" {
		return nil, false
	}
	callerFields["user_id"] = id
	encoded, err := json.Marshal(callerFields)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// systemBlocks reads a Messages system: absent, a string, or an array of
// blocks. An empty string has no blocks.
//
// systemBlocks 读取 Messages 的 system：缺省、字符串或内容块数组。空字符串没有块。
func systemBlocks(raw json.RawMessage) ([]systemBlock, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil, true
		}
		encoded, _ := json.Marshal(text)
		return []systemBlock{{"type": json.RawMessage(`"text"`), "text": encoded}}, true
	}
	var blocks []systemBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil, false
	}
	return blocks, true
}

func blockText(block systemBlock) string {
	var kind, text string
	_ = json.Unmarshal(block["type"], &kind)
	if kind != "text" {
		return ""
	}
	_ = json.Unmarshal(block["text"], &text)
	return text
}

func hasPrefixBlock(blocks []systemBlock, prefixes ...string) bool {
	for _, block := range blocks {
		if hasAnyPrefix(blockText(block), prefixes...) {
			return true
		}
	}
	return false
}

func hasAnyPrefix(text string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}
