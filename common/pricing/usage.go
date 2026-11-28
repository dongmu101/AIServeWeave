package pricing

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Limits on the details and attributes one usage record may carry. The
// control plane rejects a batch that breaks them; the Gateway filters a
// record with SanitizeDetails/SanitizeAttributes before pushing it, so one
// misbehaving backend cannot get a whole batch refused.
//
// 一条用量记录可以携带的明细与属性的上限。控制面拒绝违反上限的批次；Gateway 推送前
// 先用 SanitizeDetails/SanitizeAttributes 过滤，使一个行为异常的后端不会让整批被拒。
const (
	MaxDetails         = 32
	MaxAttributes      = 8
	MaxAttributeKeyLen = 32
	MaxAttributeValLen = 64
)

// AttrCredential names whose provider credential served a request that
// went to a third-party API rather than a node. CredentialTenant means the
// tenant's own key: the request is priced as usual but not charged, as for
// the tenant's own node. Any other value, or none, is charged.
//
// AttrCredential 说明一次发往第三方 API（而不是节点）的请求用的是谁的供应商凭据。
// CredentialTenant 表示租户自己的 Key：照常计价但不扣费，与租户自有节点相同。其他取值或
// 没有该属性时照常扣费。
const (
	AttrCredential     = "credential"
	CredentialTenant   = "tenant"
	CredentialPlatform = "platform"
)

// ValidAttributeKey reports whether key is a well-formed attribute key: one
// meter-name segment starting with a letter, at most MaxAttributeKeyLen bytes.
//
// ValidAttributeKey 报告 key 是否是格式正确的属性键：一个以字母开头的计量项名称段，
// 不超过 MaxAttributeKeyLen 字节。
func ValidAttributeKey(key string) bool {
	return len(key) <= MaxAttributeKeyLen && validSegment(key, true)
}

// ValidateDetails returns an error unless every entry of details has a valid
// meter name and a non-negative quantity, and there are at most MaxDetails
// of them. The roots MeterInput and MeterOutput themselves are not allowed:
// they are the record's prompt and completion totals, carried separately.
//
// ValidateDetails 在 details 的每一项都有合法的计量项名称与非负数量、且不超过
// MaxDetails 项时返回 nil，否则返回错误。根 MeterInput 与 MeterOutput 本身不允许出现：
// 它们是记录的 prompt 与 completion 总数，单独携带。
func ValidateDetails(details map[string]int64) error {
	if len(details) > MaxDetails {
		return fmt.Errorf("at most %d details", MaxDetails)
	}
	for name, quantity := range details {
		if err := checkDetail(name, quantity); err != nil {
			return err
		}
	}
	return nil
}

// ValidateAttributes returns an error unless every entry of attributes has a
// valid key and a value of at most MaxAttributeValLen bytes, and there are at
// most MaxAttributes of them.
//
// ValidateAttributes 在 attributes 的每一项都有合法的键、值不超过 MaxAttributeValLen
// 字节、且不超过 MaxAttributes 项时返回 nil，否则返回错误。
func ValidateAttributes(attributes map[string]string) error {
	if len(attributes) > MaxAttributes {
		return fmt.Errorf("at most %d attributes", MaxAttributes)
	}
	for key, value := range attributes {
		if !ValidAttributeKey(key) {
			return fmt.Errorf("invalid attribute key %q", key)
		}
		if len(value) > MaxAttributeValLen {
			return fmt.Errorf("attribute %q is longer than %d bytes", key, MaxAttributeValLen)
		}
	}
	return nil
}

// SanitizeDetails returns the entries of details ValidateDetails would
// accept, dropping zero quantities, and how many entries it dropped for
// being invalid. When more than MaxDetails valid entries remain, the
// lexically smallest names are kept so the result is deterministic. The
// result is nil when nothing is left.
//
// SanitizeDetails 返回 details 中 ValidateDetails 会接受的那些项（数量为 0 的项也去掉），
// 以及因不合法而丢弃的项数。合法项超过 MaxDetails 时，保留名称字典序最小的那些，使结果
// 是确定的。什么都不剩时返回 nil。
func SanitizeDetails(details map[string]int64) (map[string]int64, int) {
	var out map[string]int64
	dropped := 0
	for name, quantity := range details {
		if quantity == 0 && ValidMeterName(name) {
			continue
		}
		if checkDetail(name, quantity) != nil {
			dropped++
			continue
		}
		if out == nil {
			out = make(map[string]int64, len(details))
		}
		out[name] = quantity
	}
	return out, dropped + trimToSmallest(out, MaxDetails)
}

// SanitizeAttributes is SanitizeDetails for attributes; empty values are
// dropped as absent.
//
// SanitizeAttributes 是用于属性的 SanitizeDetails；空值视为不存在而去掉。
func SanitizeAttributes(attributes map[string]string) (map[string]string, int) {
	var out map[string]string
	dropped := 0
	for key, value := range attributes {
		if value == "" && ValidAttributeKey(key) {
			continue
		}
		if !ValidAttributeKey(key) || len(value) > MaxAttributeValLen {
			dropped++
			continue
		}
		if out == nil {
			out = make(map[string]string, len(attributes))
		}
		out[key] = value
	}
	return out, dropped + trimToSmallest(out, MaxAttributes)
}

var errRootDetail = errors.New("the input and output totals are not details")

// checkDetail validates one details entry.
//
// checkDetail 校验一个明细项。
func checkDetail(name string, quantity int64) error {
	if !ValidMeterName(name) {
		return fmt.Errorf("invalid meter name %q", name)
	}
	if name == MeterInput || name == MeterOutput {
		return errRootDetail
	}
	if quantity < 0 {
		return fmt.Errorf("meter %q has a negative quantity", name)
	}
	return nil
}

// trimToSmallest deletes the lexically largest keys of m until at most limit
// remain, and returns how many it deleted.
//
// trimToSmallest 删除 m 中字典序最大的键，直到剩下不超过 limit 个，返回删除的个数。
func trimToSmallest[V any](m map[string]V, limit int) int {
	removed := 0
	for len(m) > limit {
		largest := ""
		for key := range m {
			if key > largest {
				largest = key
			}
		}
		delete(m, largest)
		removed++
	}
	return removed
}

// Quantities returns the full meter tree of one usage record: MeterInput
// and MeterOutput set to the prompt and completion totals, every details
// entry, and every ancestor of a details entry. A parent smaller than the
// sum of its direct children is raised to that sum, so inconsistent counts
// are charged high rather than low; adjusted reports whether that happened.
// Non-positive totals and quantities count as zero and are left out.
//
// Quantities 返回一条用量记录完整的计量项树：MeterInput 与 MeterOutput 设为 prompt 与
// completion 总数，加上每个明细项及其所有祖先。父项小于其直接子项之和时，被提高到这个
// 和，使不一致的计数宁可多收、不会少收；adjusted 报告是否发生过这种提高。不为正的总数与
// 数量按零计，不出现在结果中。
func Quantities(prompt, completion int64, details map[string]int64) (quantities map[string]int64, adjusted bool) {
	quantities = make(map[string]int64, len(details)+2)
	add := func(name string, n int64) {
		if n > 0 {
			quantities[name] = n
		}
	}
	add(MeterInput, prompt)
	add(MeterOutput, completion)
	maxDepth := 0
	for name, n := range details {
		if n <= 0 {
			continue
		}
		quantities[name] = n
		depth := strings.Count(name, ".")
		maxDepth = max(maxDepth, depth)
		for parent, ok := Parent(name); ok; parent, ok = Parent(parent) {
			if _, present := quantities[parent]; !present {
				quantities[parent] = 0
			}
		}
	}
	for depth := maxDepth; depth > 0; depth-- {
		childSums := map[string]int64{}
		for name, n := range quantities {
			if strings.Count(name, ".") != depth {
				continue
			}
			parent, _ := Parent(name)
			childSums[parent] = saturatingAdd(childSums[parent], n)
		}
		for parent, sum := range childSums {
			if quantities[parent] < sum {
				quantities[parent] = sum
				adjusted = true
			}
		}
	}
	return quantities, adjusted
}

// saturatingAdd adds two non-negative quantities, stopping at math.MaxInt64.
//
// saturatingAdd 相加两个非负数量，到 math.MaxInt64 为止。
func saturatingAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
