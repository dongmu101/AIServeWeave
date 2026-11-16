package httpapi

import (
	"encoding/json"
	"errors"

	"AIServeWeave/common/pricing"
	"AIServeWeave/common/runtime"
)

// nativeTokens is the token counts of one Messages usage object or one of
// its iterations. The Messages API leaves cached tokens out of input_tokens
// and reports them beside it, so every input count here is added up into
// the prompt, the way runtime.Usage counts it.
//
// nativeTokens 是一个 Messages 用量对象或其中一次采样的 token 数。Messages API 的
// input_tokens 不含缓存 token，而是把它们放在旁边单独报告，因此这里的各项输入数都会
// 加总进 prompt，与 runtime.Usage 的计法一致。
type nativeTokens struct {
	Input         int `json:"input_tokens"`
	Output        int `json:"output_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
	CacheTTL      *struct {
		Hour int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

var errNativeUsage = errors.New("invalid native Messages usage")

// nativeMessagesBilledUsage includes every sampling iteration without rewriting public usage.
// nativeMessagesBilledUsage 计入每次采样的用量，不改写公开的 usage。
func nativeMessagesBilledUsage(value map[string]any) (runtime.Usage, error) {
	return nativeMessagesUsage(value, true)
}

// nativeMessagesUsage counts a Messages usage object: its iterations when
// withIterations is set and it has any, otherwise its own counts. Cache
// reads and writes count as input and are kept as details. A negative,
// fractional or overflowing count is an error.
//
// nativeMessagesUsage 统计一个 Messages 用量对象：withIterations 为真且存在采样明细时
// 统计各次采样，否则统计它自身的计数。缓存读取与写入计为输入，并保留为明细。计数为负、
// 为小数或溢出时返回错误。
func nativeMessagesUsage(value map[string]any, withIterations bool) (runtime.Usage, error) {
	var document struct {
		nativeTokens
		Iterations []nativeTokens `json:"iterations"`
	}
	data, err := json.Marshal(value)
	if err != nil || json.Unmarshal(data, &document) != nil {
		return runtime.Usage{}, errNativeUsage
	}
	counts := []nativeTokens{document.nativeTokens}
	if withIterations && len(document.Iterations) > 0 {
		counts = document.Iterations
	}
	var usage runtime.Usage
	var cacheRead, cacheWrite, cacheHour int
	for _, count := range counts {
		hour := 0
		if count.CacheTTL != nil {
			hour = count.CacheTTL.Hour
		}
		if count.Input < 0 || count.Output < 0 || count.CacheRead < 0 || count.CacheCreation < 0 || hour < 0 {
			return runtime.Usage{}, errNativeUsage
		}
		prompt, ok := addCounts(usage.PromptTokens, count.Input, count.CacheRead, count.CacheCreation)
		if !ok {
			return runtime.Usage{}, errNativeUsage
		}
		completion, ok := addCounts(usage.CompletionTokens, count.Output)
		if !ok {
			return runtime.Usage{}, errNativeUsage
		}
		usage.PromptTokens, usage.CompletionTokens = prompt, completion
		cacheRead += count.CacheRead
		cacheWrite += count.CacheCreation
		cacheHour += min(hour, count.CacheCreation)
	}
	total, ok := addCounts(usage.PromptTokens, usage.CompletionTokens)
	if !ok {
		return runtime.Usage{}, errNativeUsage
	}
	usage.TotalTokens = total
	usage.AddDetail(pricing.MeterCacheRead, int64(cacheRead))
	usage.AddDetail(pricing.MeterCacheWrite, int64(cacheWrite))
	usage.AddDetail(pricing.MeterCacheWrite1h, int64(cacheHour))
	return usage, nil
}

// addCounts adds non-negative counts to base, reporting false on overflow.
// The cache sums are bounded by the prompt total, so checking it is enough.
//
// addCounts 把非负计数加到 base 上，溢出时报告 false。缓存部分的总和不超过 prompt
// 总数，因此只检查它就够了。
func addCounts(base int, counts ...int) (int, bool) {
	maxInt := int(^uint(0) >> 1)
	for _, n := range counts {
		if n > maxInt-base {
			return 0, false
		}
		base += n
	}
	return base, true
}
