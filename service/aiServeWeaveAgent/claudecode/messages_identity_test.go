package claudecode

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	fixtureBilling  = `{"type":"text","text":"x-anthropic-billing-header: cc_version=fixture; cc_entrypoint=cli;"}`
	fixtureIdentity = `{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral"}}`
	fixtureCLI      = `{"system":[` + fixtureBilling + `,` + fixtureIdentity + `,{"type":"text","text":"cli custom prompt"}],"metadata":{"user_id":"cli-user"},"stream":true}`
)

func mergeFixture(t *testing.T, override string) map[string]json.RawMessage {
	t.Helper()
	var seed, doc map[string]json.RawMessage
	if json.Unmarshal([]byte(fixtureCLI), &seed) != nil || json.Unmarshal([]byte(override), &doc) != nil {
		t.Fatal("invalid fixture")
	}
	mergeNativeRequest(seed, doc)
	return seed
}

func systemTexts(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	blocks, ok := systemBlocks(raw)
	if !ok {
		t.Fatalf("unreadable system %s", raw)
	}
	var texts []string
	for _, block := range blocks {
		if _, cached := block["cache_control"]; cached && strings.HasPrefix(blockText(block), identityPrefixes[0]) {
			t.Fatal("prepended identity kept cache_control")
		}
		texts = append(texts, blockText(block))
	}
	return texts
}

// TestNativeMergeAddsCLIIdentity covers a caller that is not Claude Code:
// its system follows the CLI's billing and identity blocks, never the CLI's
// own custom prompt.
// TestNativeMergeAddsCLIIdentity 覆盖不是 Claude Code 的调用方：其 system 接在 CLI 的计费块与
// 身份块之后，CLI 自己的自定义提示不保留。
func TestNativeMergeAddsCLIIdentity(t *testing.T) {
	for name, system := range map[string]string{
		"string": `"harness prompt"`,
		"blocks": `[{"type":"text","text":"harness prompt","cache_control":{"type":"ephemeral"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			merged := mergeFixture(t, `{"system":`+system+`}`)
			texts := systemTexts(t, merged["system"])
			if len(texts) != 3 || !strings.HasPrefix(texts[0], billingHeaderPrefix) || !strings.HasPrefix(texts[1], identityPrefixes[0]) || texts[2] != "harness prompt" {
				t.Fatalf("system = %q", texts)
			}
			blocks, _ := systemBlocks(merged["system"])
			if _, cached := blocks[2]["cache_control"]; name == "blocks" && !cached {
				t.Fatal("caller cache_control lost")
			}
		})
	}
	merged := mergeFixture(t, `{"system":[]}`)
	if texts := systemTexts(t, merged["system"]); len(texts) != 2 {
		t.Fatalf("empty caller system = %q", texts)
	}
}

// TestNativeMergeKeepsClaudeCodeCaller leaves a Claude Code document, which
// carries its own identity and billing, exactly as sent.
// TestNativeMergeKeepsClaudeCodeCaller 让自带身份与计费块的 Claude Code 文档保持原样。
func TestNativeMergeKeepsClaudeCodeCaller(t *testing.T) {
	system := `[{"type":"text","text":"x-anthropic-billing-header: cc_version=caller;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"caller prompt"}]`
	merged := mergeFixture(t, `{"system":`+system+`,"metadata":{"user_id":"caller-user"}}`)
	if canonical(merged["system"]) != canonical(json.RawMessage(system)) || canonical(merged["metadata"]) != canonical(json.RawMessage(`{"user_id":"caller-user"}`)) {
		t.Fatalf("Claude Code caller changed: %s %s", merged["system"], merged["metadata"])
	}
	// A caller billing header without identity is not duplicated.
	// 调用方只带计费头而无身份时，计费头不重复。
	merged = mergeFixture(t, `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=caller;"}]}`)
	if texts := systemTexts(t, merged["system"]); len(texts) != 2 || !strings.HasPrefix(texts[0], identityPrefixes[0]) || !strings.Contains(texts[1], "caller") {
		t.Fatalf("system = %q", texts)
	}
}

// TestNativeMergeUserID fills only a missing user_id and keeps other metadata.
// TestNativeMergeUserID 只补缺失的 user_id，并保留其他 metadata 字段。
func TestNativeMergeUserID(t *testing.T) {
	for override, want := range map[string]string{
		`{}`:                                  `{"user_id":"cli-user"}`,
		`{"metadata":{}}`:                     `{"user_id":"cli-user"}`,
		`{"metadata":{"user_id":null,"x":1}}`: `{"user_id":"cli-user","x":1}`,
		`{"metadata":{"user_id":"caller","x":1}}`: `{"user_id":"caller","x":1}`,
	} {
		merged := mergeFixture(t, override)
		if canonical(merged["metadata"]) != canonical(json.RawMessage(want)) {
			t.Fatalf("%s: metadata = %s", override, merged["metadata"])
		}
	}
}

// TestNativeMergeWithoutCLIIdentity changes nothing when the CLI sent no
// identity, so a future CLI that drops it is overlaid as before.
// TestNativeMergeWithoutCLIIdentity 在 CLI 未发送身份时不做改动，未来去掉身份块的 CLI 仍按原方式覆盖。
func TestNativeMergeWithoutCLIIdentity(t *testing.T) {
	seed := map[string]json.RawMessage{"system": json.RawMessage(`[{"type":"text","text":"plain"}]`)}
	mergeNativeRequest(seed, map[string]json.RawMessage{"system": json.RawMessage(`"caller"`)})
	if string(seed["system"]) != `"caller"` {
		t.Fatalf("system = %s", seed["system"])
	}
}

func TestNativeSettingsRelayFields(t *testing.T) {
	contextManagement := json.RawMessage(`{"edits":[{"type":"clear_tool_uses_20250919"}]}`)
	extra := map[string]any{
		"model": "fixture", "stream": true, "system": "caller",
		"metadata": map[string]string{"user_id": "u"}, "context_management": contextManagement,
		"messages": []any{map[string]string{"role": "user", "content": "hi"}}, "betas": []string{"feature-a"},
	}
	settings, request, err := nativeInputSettings(extra)
	if err != nil || request == nil {
		t.Fatalf("request/error = %t/%v", request != nil, err)
	}
	var parsed struct{ Env map[string]string }
	var body, relay map[string]json.RawMessage
	if json.Unmarshal(settings, &parsed) != nil || json.Unmarshal([]byte(parsed.Env["CLAUDE_CODE_EXTRA_BODY"]), &body) != nil || json.Unmarshal(request, &relay) != nil {
		t.Fatal("invalid settings or relay JSON")
	}
	if _, ok := body["system"]; ok {
		t.Fatal("system entered settings")
	}
	if _, ok := body["metadata"]; ok {
		t.Fatal("metadata entered settings")
	}
	if canonical(body["messages"]) != canonical(extra["messages"]) || canonical(body["context_management"]) != canonical(contextManagement) ||
		len(relay) != 3 || canonical(relay["system"]) != canonical(extra["system"]) || canonical(relay["metadata"]) != canonical(extra["metadata"]) {
		t.Fatalf("settings %s relay %s", parsed.Env["CLAUDE_CODE_EXTRA_BODY"], request)
	}
	seed := map[string]json.RawMessage{"context_management": json.RawMessage(`{"edits":[{"type":"clear_thinking_20251015"}]}`)}
	mergeNativeRequest(seed, relay)
	if canonical(seed["context_management"]) != canonical(contextManagement) {
		t.Fatalf("caller context edits changed: %s", seed["context_management"])
	}
}
