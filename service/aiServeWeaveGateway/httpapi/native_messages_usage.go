package httpapi

import (
	"encoding/json"
	"errors"

	"AIServeWeave/common/runtime"
)

// nativeMessagesBilledUsage includes every sampling iteration without rewriting public usage.
// nativeMessagesBilledUsage 计入每次采样的用量，不改写公开的 usage。
func nativeMessagesBilledUsage(value map[string]any) (runtime.Usage, error) {
	type tokens struct {
		Input  int `json:"input_tokens"`
		Output int `json:"output_tokens"`
	}
	var document struct {
		tokens
		Iterations []tokens `json:"iterations"`
	}
	failure := errors.New("invalid native Messages usage")
	data, err := json.Marshal(value)
	if err != nil || json.Unmarshal(data, &document) != nil {
		return runtime.Usage{}, failure
	}
	counts := document.Iterations
	if len(counts) == 0 {
		counts = []tokens{document.tokens}
	}
	var usage runtime.Usage
	maxInt := int(^uint(0) >> 1)
	for _, count := range counts {
		if count.Input < 0 || count.Output < 0 || count.Input > maxInt-usage.PromptTokens || count.Output > maxInt-usage.CompletionTokens {
			return runtime.Usage{}, failure
		}
		usage.PromptTokens += count.Input
		usage.CompletionTokens += count.Output
	}
	if usage.PromptTokens > maxInt-usage.CompletionTokens {
		return runtime.Usage{}, failure
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return usage, nil
}
