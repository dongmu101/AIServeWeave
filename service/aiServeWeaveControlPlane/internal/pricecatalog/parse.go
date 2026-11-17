// Package pricecatalog turns the LiteLLM price list
// (model_prices_and_context_window.json) into official unit prices, and
// decides which changes between two copies of it take effect on their own
// and which wait for an operator. It has no storage dependency; the store
// applies its decisions in one transaction. See
// .scratch/token-pricing/spec.md「价目表」.
//
// pricecatalog 包把 LiteLLM 价目表（model_prices_and_context_window.json）转换成
// 官方单价，并判断两份价目表之间的哪些变动可以自动生效、哪些要等运维审核。它不依赖
// 任何存储；store 在一个事务里执行它的判断。见 .scratch/token-pricing/spec.md「价目表」。
package pricecatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"AIServeWeave/common/pricing"
)

// Limits on what one entry may carry, matching the catalog tables' columns.
//
// 一个条目可以携带的内容上限，与价目表各列的宽度一致。
const (
	MaxModelLen    = 255
	MaxProviderLen = 64
	MaxModeLen     = 32
)

// usdToMicroPerM converts LiteLLM's US dollars per token into micro-credits
// per million tokens: one credit is one dollar, so the factor is 1e6 × 1e6.
//
// usdToMicroPerM 把 LiteLLM 的「美元 / token」换算成「micro 点 / 百万 token」：1 点 =
// 1 美元，因此换算系数是 1e6 × 1e6。
const usdToMicroPerM = 1e12

// keptModes are the modes whose entries are imported: the token-metered
// front doors. Image, audio and video modes are left out this release.
//
// keptModes 是导入其条目的模式，即按 token 计量的前门。本版本不导入图片、音频与视频模式。
var keptModes = map[string]bool{"chat": true, "completion": true, "responses": true, "embedding": true}

// sampleSpec is the upstream file's documentation entry, not a model.
//
// sampleSpec 是上游文件中的说明条目，不是模型。
const sampleSpec = "sample_spec"

// baseFields maps a LiteLLM cost field, without its suffixes, to a meter.
//
// baseFields 把不带后缀的 LiteLLM 计价字段映射到计量项。
var baseFields = []struct{ field, meter string }{
	{"input_cost_per_token", pricing.MeterInput},
	{"output_cost_per_token", pricing.MeterOutput},
	{"cache_read_input_token_cost", pricing.MeterCacheRead},
	{"cache_creation_input_token_cost", pricing.MeterCacheWrite},
	{"output_cost_per_reasoning_token", pricing.MeterReasoning},
}

// Price is one official unit price. Derived marks a tier price this package
// filled in from the base tier because upstream did not list it.
//
// Price 是一条官方单价。Derived 标记本包因上游未列出、而按基础档推算补齐的档位单价。
type Price struct {
	pricing.PriceItem
	Derived bool `json:"derived,omitempty"`
}

// Entry is one imported catalog entry. Prices are in canonical order; Raw is
// the upstream entry, compacted, kept so a later release can map more of its
// fields without fetching it again.
//
// Entry 是一个导入的价目表条目。Prices 按规范顺序排列；Raw 是压缩后的上游原始条目，保留
// 下来，使以后的版本无需重新抓取就能映射它的更多字段。
type Entry struct {
	Model    string          `json:"model"`
	Provider string          `json:"provider"`
	Mode     string          `json:"mode"`
	Prices   []Price         `json:"prices"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

// PriceList returns the entry's prices without the derived marks.
//
// PriceList 返回该条目的单价，不带推算标记。
func (e Entry) PriceList() pricing.PriceList {
	out := make(pricing.PriceList, len(e.Prices))
	for i, p := range e.Prices {
		out[i] = p.PriceItem
	}
	return out
}

// PricesHash identifies the entry's prices, derived marks included, so two
// entries with the same prices hash alike whatever else upstream changed.
//
// PricesHash 标识该条目的单价（包括推算标记），使单价相同的两个条目无论上游改了别的
// 什么，哈希都相同。
func (e Entry) PricesHash() string {
	return HashPrices(e.Prices)
}

// HashPrices hashes prices in canonical order.
//
// HashPrices 按规范顺序对单价求哈希。
func HashPrices(prices []Price) string {
	sorted := sortPrices(prices)
	encoded, _ := json.Marshal(sorted)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Snapshot is one parsed copy of the price list. Entries are sorted by
// model; ContentHash identifies the bytes it was parsed from.
//
// Snapshot 是解析后的一份价目表。Entries 按模型名排序；ContentHash 标识解析所用的
// 原始字节。
type Snapshot struct {
	Entries     []Entry
	ContentHash string
}

// ErrNoEntries is returned for a document that parses but yields nothing
// usable, such as an error page served as JSON or a truncated download.
//
// ErrNoEntries 用于能够解析、却没有任何可用条目的文档，例如以 JSON 形式返回的错误页
// 或被截断的下载。
var ErrNoEntries = errors.New("pricecatalog: the price list has no usable entries")

// Parse reads a LiteLLM price list. Entries outside the kept modes, without
// a positive base input or output price, or with an unusable name are
// skipped; fields this release does not map are ignored. See
// .scratch/token-pricing/spec.md「字段映射」.
//
// Parse 读取一份 LiteLLM 价目表。不在保留模式内、没有大于 0 的基础输入价或输出价、或
// 名称不可用的条目被跳过；本版本不映射的字段被忽略。见 .scratch/token-pricing/spec.md
// 「字段映射」。
func Parse(data []byte) (Snapshot, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return Snapshot{}, fmt.Errorf("pricecatalog: the price list is not a JSON object: %w", err)
	}
	entries := make([]Entry, 0, len(document))
	for name, raw := range document {
		if entry, ok := parseEntry(name, raw); ok {
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return Snapshot{}, ErrNoEntries
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Model < entries[j].Model })
	sum := sha256.Sum256(data)
	return Snapshot{Entries: entries, ContentHash: hex.EncodeToString(sum[:])}, nil
}

// parseEntry maps one upstream entry, reporting false when it is skipped.
//
// parseEntry 映射一个上游条目；被跳过时报告 false。
func parseEntry(name string, raw json.RawMessage) (Entry, bool) {
	if name == sampleSpec || !validModelName(name) {
		return Entry{}, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return Entry{}, false
	}
	mode := jsonString(fields["mode"])
	if !keptModes[mode] {
		return Entry{}, false
	}
	prices := map[pricing.PriceKey]int64{}
	for field, value := range fields {
		key, ok := mapField(field)
		if !ok {
			continue
		}
		micro, ok := toMicroPerM(value)
		if !ok {
			continue
		}
		prices[key] = micro
	}
	baseIn := prices[pricing.PriceKey{Meter: pricing.MeterInput}]
	baseOut := prices[pricing.PriceKey{Meter: pricing.MeterOutput}]
	if baseIn <= 0 && baseOut <= 0 {
		return Entry{}, false
	}
	// A zero price below a root or above the base tier is more likely a gap
	// in upstream data than a free service; dropping it lets the lookup fall
	// back to the parent or base price, which never charges less.
	// 根之下或基础档之上的 0 单价更可能是上游数据缺漏而不是免费服务；去掉它，查找就会
	// 回落到父项或基础档的单价，收费绝不会更低。
	for key, micro := range prices {
		if micro == 0 && (key.AboveInputTokens > 0 || strings.Contains(key.Meter, ".")) {
			delete(prices, key)
		}
	}
	derived := deriveTiers(prices)
	out := make([]Price, 0, len(prices))
	for key, micro := range prices {
		out = append(out, Price{
			PriceItem: pricing.PriceItem{Meter: key.Meter, Variant: key.Variant, AboveInputTokens: key.AboveInputTokens, PriceMicroPerM: micro},
			Derived:   derived[key],
		})
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return Entry{}, false
	}
	return Entry{
		Model:    name,
		Provider: truncate(jsonString(fields["litellm_provider"]), MaxProviderLen),
		Mode:     mode,
		Prices:   sortPrices(out),
		Raw:      json.RawMessage(compact.Bytes()),
	}, true
}

// mapField returns the price key a LiteLLM field maps to: a base field, an
// optional cache TTL (_above_1hr, _above_12hr, only for cache writes), an
// optional tier (_above_<N>k_tokens) and nothing else. A field carrying a
// service-tier suffix (_priority, _flex, _batches, ...) or anything unknown
// is not mapped this release.
//
// mapField 返回一个 LiteLLM 字段映射到的单价键：一个基础字段、可选的缓存 TTL
// （_above_1hr、_above_12hr，只用于缓存写入）、可选的档位（_above_<N>k_tokens），
// 此外别无其他。带服务等级后缀（_priority、_flex、_batches 等）或其他未知后缀的字段
// 本版本不映射。
func mapField(field string) (pricing.PriceKey, bool) {
	for _, base := range baseFields {
		rest, ok := strings.CutPrefix(field, base.field)
		if !ok {
			continue
		}
		meter := base.meter
		if meter == pricing.MeterCacheWrite {
			if r, ok := cutWord(rest, "_above_1hr"); ok {
				meter, rest = pricing.MeterCacheWrite1h, r
			} else if r, ok := cutWord(rest, "_above_12hr"); ok {
				meter, rest = pricing.MeterCacheWrite12h, r
			}
		}
		if rest == "" {
			return pricing.PriceKey{Meter: meter}, true
		}
		tier, ok := parseTier(rest)
		if !ok {
			return pricing.PriceKey{}, false
		}
		return pricing.PriceKey{Meter: meter, AboveInputTokens: tier}, true
	}
	return pricing.PriceKey{}, false
}

// maxTierTokens bounds a parsed tier threshold.
//
// maxTierTokens 限制解析出的档位阈值。
const maxTierTokens = 1_000_000_000

// parseTier reads exactly "_above_<N>k_tokens" as N × 1000 tokens.
//
// parseTier 只接受 "_above_<N>k_tokens"，读作 N × 1000 个 token。
func parseTier(rest string) (int64, bool) {
	digits, ok := strings.CutPrefix(rest, "_above_")
	if !ok {
		return 0, false
	}
	digits, ok = strings.CutSuffix(digits, "k_tokens")
	if !ok || digits == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || n <= 0 || n > maxTierTokens/1000 {
		return 0, false
	}
	return n * 1000, true
}

// cutWord removes word from the front of s when it is the whole of s or is
// followed by another "_" suffix.
//
// cutWord 在 word 恰好是 s 的全部、或其后紧跟另一个 "_" 后缀时，把它从 s 的开头去掉。
func cutWord(s, word string) (string, bool) {
	rest, ok := strings.CutPrefix(s, word)
	if !ok || (rest != "" && rest[0] != '_') {
		return s, false
	}
	return rest, true
}

// toMicroPerM converts one field value; anything but a finite, non-negative
// number within pricing.MaxPriceMicroPerM is rejected.
//
// toMicroPerM 换算一个字段值；只接受有限、非负且不超过 pricing.MaxPriceMicroPerM 的数字。
func toMicroPerM(value json.RawMessage) (int64, bool) {
	var usd float64
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &usd) != nil {
		return 0, false
	}
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return 0, false
	}
	micro := math.Round(usd * usdToMicroPerM)
	if micro > pricing.MaxPriceMicroPerM {
		return 0, false
	}
	return int64(micro), true
}

// deriveTiers fills in, for each root that lists tier prices, the tier
// prices of its sub-meters upstream left out: base sub-meter price × tier
// root price / base root price, rounded half up. It returns the keys it
// added. Without it a long-context request's cached tokens would be charged
// at the base tier.
//
// deriveTiers 对每个列出了档位单价的根，补齐上游漏掉的子项档位单价：基础档子项单价 ×
// 该档根单价 / 基础档根单价，四舍五入。它返回补齐的键。没有这一步，长上下文请求的缓存
// token 会按基础档收费。
func deriveTiers(prices map[pricing.PriceKey]int64) map[pricing.PriceKey]bool {
	derived := map[pricing.PriceKey]bool{}
	for _, root := range []string{pricing.MeterInput, pricing.MeterOutput} {
		base := prices[pricing.PriceKey{Meter: root}]
		if base <= 0 {
			continue
		}
		var tiers []int64
		for key := range prices {
			if key.Meter == root && key.Variant == "" && key.AboveInputTokens > 0 {
				tiers = append(tiers, key.AboveInputTokens)
			}
		}
		if len(tiers) == 0 {
			continue
		}
		var subs []pricing.PriceKey
		for key := range prices {
			if key.Meter != root && pricing.Root(key.Meter) == root && key.Variant == "" && key.AboveInputTokens == 0 {
				subs = append(subs, key)
			}
		}
		for _, sub := range subs {
			for _, tier := range tiers {
				key := pricing.PriceKey{Meter: sub.Meter, AboveInputTokens: tier}
				if _, listed := prices[key]; listed {
					continue
				}
				tierRoot := prices[pricing.PriceKey{Meter: root, AboveInputTokens: tier}]
				value, ok := scale(prices[sub], tierRoot, base)
				if !ok {
					continue
				}
				prices[key] = value
				derived[key] = true
			}
		}
	}
	return derived
}

// scale returns round(value × numerator / denominator), or false when the
// result exceeds pricing.MaxPriceMicroPerM.
//
// scale 返回 round(value × numerator / denominator)；结果超过
// pricing.MaxPriceMicroPerM 时返回 false。
func scale(value, numerator, denominator int64) (int64, bool) {
	product := new(big.Int).Mul(big.NewInt(value), big.NewInt(numerator))
	product.Add(product, big.NewInt(denominator/2))
	product.Quo(product, big.NewInt(denominator))
	if !product.IsInt64() || product.Int64() > pricing.MaxPriceMicroPerM {
		return 0, false
	}
	return product.Int64(), true
}

// sortPrices returns a copy of prices in pricing.PriceList.Sorted order.
//
// sortPrices 返回按 pricing.PriceList.Sorted 顺序排列的 prices 副本。
func sortPrices(prices []Price) []Price {
	out := append([]Price(nil), prices...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Meter != b.Meter {
			return a.Meter < b.Meter
		}
		if a.Variant != b.Variant {
			return a.Variant < b.Variant
		}
		return a.AboveInputTokens < b.AboveInputTokens
	})
	return out
}

// validModelName reports whether name can be stored and shown: non-empty
// valid UTF-8 within MaxModelLen bytes, without control characters.
//
// validModelName 报告 name 能否被保存与展示：非空、合法 UTF-8、不超过 MaxModelLen 字节、
// 不含控制字符。
func validModelName(name string) bool {
	if name == "" || len(name) > MaxModelLen || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// jsonString decodes a JSON string, or returns "" for anything else.
//
// jsonString 解码一个 JSON 字符串；其他类型返回 ""。
func jsonString(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// truncate shortens s to at most n bytes on a rune boundary.
//
// truncate 在字符边界上把 s 截短到不超过 n 字节。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
