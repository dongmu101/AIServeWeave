// Package pricing holds the vocabulary token pricing shares across the
// Agent, the Gateway and the control plane: meter names, the limits a usage
// record's details and attributes must respect, and price lists. It has no
// storage dependency, so the control plane's charging and, later, a
// Gateway-side estimate can use the same rules. See
// .scratch/token-pricing/spec.md.
//
// pricing 包保存 Agent、Gateway 与控制面在 token 计价上共用的词汇：计量项名称、
// 用量记录的明细与属性必须遵守的上限，以及单价表。它不依赖任何存储，因此控制面的
// 扣费与以后 Gateway 侧的估价可以使用同一套规则。见 .scratch/token-pricing/spec.md。
package pricing

import "strings"

// Meter names this release reports and prices. A meter's name is a dotted
// path; every meter below a root is part of its parent's quantity, so a
// meter the control plane does not know falls back to its parent's price
// and is never charged less than the parent would be.
//
// 本版本上报并计价的计量项名称。计量项名称是以点分隔的路径；根之下的每个计量项都是
// 其父项数量的一部分，因此控制面不认识的计量项会回落到父项的单价，收费绝不会低于
// 父项。
const (
	// MeterInput is every input token, cached or not: prompt_tokens.
	// MeterInput 是全部输入 token，无论是否命中缓存：即 prompt_tokens。
	MeterInput = "input"
	// MeterOutput is every output token, reasoning included: completion_tokens.
	// MeterOutput 是全部输出 token，包括推理：即 completion_tokens。
	MeterOutput = "output"
	// MeterCacheRead is the input tokens served from a prompt cache.
	// MeterCacheRead 是由提示缓存提供的输入 token。
	MeterCacheRead = "input.cache_read"
	// MeterCacheWrite is the input tokens written to a prompt cache, every
	// TTL together; its own price is the 5-minute one.
	// MeterCacheWrite 是写入提示缓存的输入 token（各种 TTL 合计）；它自己的单价是
	// 5 分钟缓存的单价。
	MeterCacheWrite = "input.cache_write"
	// MeterCacheWrite1h is the part of MeterCacheWrite cached for an hour.
	// MeterCacheWrite1h 是 MeterCacheWrite 中缓存 1 小时的那部分。
	MeterCacheWrite1h = "input.cache_write.1h"
	// MeterCacheWrite12h is the part of MeterCacheWrite cached for twelve
	// hours. No backend reports it yet; the catalog already prices it.
	// MeterCacheWrite12h 是 MeterCacheWrite 中缓存 12 小时的那部分。目前还没有后端
	// 上报它；价目表已经为它定价。
	MeterCacheWrite12h = "input.cache_write.12h"
	// MeterReasoning is the part of MeterOutput the model spent reasoning.
	// MeterReasoning 是 MeterOutput 中模型用于推理的那部分。
	MeterReasoning = "output.reasoning"
)

// MaxMeterNameLen bounds a meter name, in bytes.
//
// MaxMeterNameLen 限制计量项名称的长度（字节）。
const MaxMeterNameLen = 64

// ValidMeterName reports whether name is a well-formed meter name: dot
// separated segments of lower-case ASCII letters, digits and underscores,
// the first segment starting with a letter, at most MaxMeterNameLen bytes.
// Later segments may start with a digit, so "input.cache_write.1h" is valid.
//
// ValidMeterName 报告 name 是否是格式正确的计量项名称：以点分隔的若干段，每段由小写
// ASCII 字母、数字和下划线组成，第一段以字母开头，总长不超过 MaxMeterNameLen 字节。
// 之后的段可以以数字开头，因此 "input.cache_write.1h" 是合法的。
func ValidMeterName(name string) bool {
	if name == "" || len(name) > MaxMeterNameLen {
		return false
	}
	for i, segment := range strings.Split(name, ".") {
		if !validSegment(segment, i == 0) {
			return false
		}
	}
	return true
}

// validSegment reports whether s is one non-empty name segment; a leading
// segment must start with a letter.
//
// validSegment 报告 s 是否是一个非空的名称段；首段必须以字母开头。
func validSegment(s string, leading bool) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9', c == '_':
			if leading && i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Parent returns the meter one level up and true, or "" and false for a
// root. It does not validate name.
//
// Parent 返回上一层的计量项与 true；name 是根时返回 "" 与 false。它不校验 name。
func Parent(name string) (string, bool) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return "", false
	}
	return name[:i], true
}

// Root returns the first segment of name.
//
// Root 返回 name 的第一段。
func Root(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return name
}

// IsTokenMeter reports whether name belongs under MeterInput or MeterOutput.
// Any other root is a count meter (requests, images, seconds, ...).
//
// IsTokenMeter 报告 name 是否属于 MeterInput 或 MeterOutput 之下。其他根都是数量
// 计量项（请求数、图片数、秒数等）。
func IsTokenMeter(name string) bool {
	root := Root(name)
	return root == MeterInput || root == MeterOutput
}
