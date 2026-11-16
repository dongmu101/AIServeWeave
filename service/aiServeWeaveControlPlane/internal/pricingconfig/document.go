// Package pricingconfig is the pricing configuration the control plane
// versions as one JSON document per revision: the default price, each
// alias's catalog reference, override price and model rate, and the price
// plans tenants belong to. It has no storage dependency; the store keeps the
// encoded documents and logic edits them through the functions here. See
// .scratch/token-pricing/spec.md「定价与价格方案」.
//
// pricingconfig 包是控制面按版本保存的定价配置，每个版本一个 JSON 文档：默认价，各别名
// 的参照模型、覆盖价与模型倍率，以及租户所属的价格方案。它不依赖任何存储；store 保存
// 编码后的文档，logic 通过本包的函数修改它。见 .scratch/token-pricing/spec.md
// 「定价与价格方案」。
package pricingconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"AIServeWeave/common/pricing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
)

// FormatVersion is the document format this build reads and writes. A
// document with a higher version came from a newer build; it is refused
// rather than edited, so an older replica can never drop fields it does not
// know while writing a new revision.
//
// FormatVersion 是本版本读写的文档格式。版本更高的文档来自更新的程序；它会被拒绝而
// 不是被修改，使旧副本在写入新版本时绝不会丢掉它不认识的字段。
const FormatVersion = 1

// DefaultPlanID is the built-in plan every tenant without an assignment
// belongs to. Its default factor is the global rate. It cannot be deleted.
//
// DefaultPlanID 是没有指定方案的租户所属的内置方案。它的默认系数就是全局倍率。它不能
// 被删除。
const DefaultPlanID = "plan_default"

// DefaultPlanName is the default plan's name in a new document.
//
// DefaultPlanName 是新文档中默认方案的名称。
const DefaultPlanName = "Default"

// Limits on one document.
//
// 单个文档的上限。
const (
	MaxAliasLen = 128
	// MaxCatalogModelLen matches the catalog's own limit on entry names, so
	// every entry can be referenced.
	// MaxCatalogModelLen 与价目表自身对条目名称的上限一致，使每个条目都能被参照。
	MaxCatalogModelLen = pricecatalog.MaxModelLen
	MaxPlanNameRunes   = 128
	MaxPlanIDLen       = model.IDSize
	MaxModels          = 2000
	MaxPlans           = 100
	MaxPlanModels      = 2000
	// MaxDocumentBytes bounds one encoded document. MySQL keeps it in a
	// MEDIUMTEXT column (16 MiB); the bound keeps every revision cheap to
	// load on each charge-cache refresh.
	// MaxDocumentBytes 限制一个编码后的文档。MySQL 用 MEDIUMTEXT 列（16 MiB）保存它；
	// 这个上限使每次刷新扣费缓存时加载一个版本的代价都很低。
	MaxDocumentBytes = 4 << 20
)

// Errors the edit functions return. Logic maps ErrInvalid to a 400,
// ErrNotFound to a 404 and ErrDefaultPlan to a 409.
//
// 修改函数返回的错误。logic 把 ErrInvalid 映射为 400，ErrNotFound 映射为 404，
// ErrDefaultPlan 映射为 409。
var (
	ErrInvalid     = errors.New("pricingconfig: invalid configuration")
	ErrNotFound    = errors.New("pricingconfig: not found")
	ErrDefaultPlan = errors.New("pricingconfig: the default plan cannot be deleted")
)

// Document is one pricing-configuration revision.
//
// Document 是一个定价配置版本。
type Document struct {
	// Version is the format version, FormatVersion.
	// Version 是格式版本，即 FormatVersion。
	Version int `json:"v"`
	// DefaultPrices prices an alias with neither an override nor a catalog
	// entry. Empty means there is no default price.
	// DefaultPrices 为既没有覆盖价、也没有命中价目表的别名定价。为空表示没有默认价。
	DefaultPrices pricing.PriceList `json:"default_prices,omitempty"`
	// Models holds per-alias settings, keyed by modelroute alias.
	// Models 保存按别名的设置，键是 modelroute 别名。
	Models map[string]ModelConfig `json:"models,omitempty"`
	// Plans holds every price plan, keyed by plan id.
	// Plans 保存全部价格方案，键是方案 id。
	Plans map[string]Plan `json:"plans"`
	// DefaultPlan is the plan of a tenant without an assignment, always
	// DefaultPlanID.
	// DefaultPlan 是没有指定方案的租户所属的方案，始终是 DefaultPlanID。
	DefaultPlan string `json:"default_plan"`
}

// ModelConfig is one alias's settings, applying to every tenant.
//
// ModelConfig 是某个别名的设置，对所有租户生效。
type ModelConfig struct {
	// CatalogModel names the catalog entry that prices the alias. Empty
	// means the alias's own name, which must match an entry exactly.
	// CatalogModel 指定为该别名定价的价目表条目。为空表示用别名本身，必须与某个条目
	// 完全一致。
	CatalogModel string `json:"catalog_model,omitempty"`
	// Prices, when non-empty, replaces the catalog price wholesale.
	// Prices 非空时整张替换官方价。
	Prices pricing.PriceList `json:"prices,omitempty"`
	// RatePPM is the model rate in parts per million; pricing.RateOne is 1x.
	// RatePPM 是模型倍率，单位为百万分之一；pricing.RateOne 表示 1 倍。
	RatePPM int64 `json:"rate_ppm"`
}

// Plan is a price plan: a default factor and per-alias terms.
//
// Plan 是一个价格方案：一个默认系数，加上按别名的条款。
type Plan struct {
	Name           string               `json:"name"`
	DefaultRatePPM int64                `json:"default_rate_ppm"`
	Models         map[string]PlanModel `json:"models,omitempty"`
}

// PlanModel is a plan's terms for one alias: exactly one of a factor or a
// fixed sale price. A fixed price is charged as is, with neither the model
// rate nor any factor applied.
//
// PlanModel 是方案对某个别名的条款：系数与固定售价二者恰好取其一。固定售价按原样收费，
// 不再乘以模型倍率或任何系数。
type PlanModel struct {
	RatePPM *int64            `json:"rate_ppm,omitempty"`
	Prices  pricing.PriceList `json:"prices,omitempty"`
}

// New returns a document with no prices and only the default plan at 1x.
//
// New 返回一个没有任何单价、只有 1 倍默认方案的文档。
func New() Document {
	return Document{
		Version:     FormatVersion,
		Models:      map[string]ModelConfig{},
		Plans:       map[string]Plan{DefaultPlanID: {Name: DefaultPlanName, DefaultRatePPM: pricing.RateOne}},
		DefaultPlan: DefaultPlanID,
	}
}

// FromModelPrices converts the legacy model_prices rows into the first
// document: each alias row becomes that alias's override price (input and
// output) at a 1x model rate, and the model.DefaultPriceModel row becomes the
// default price. Rows that would not validate are skipped and counted, so a
// malformed legacy row cannot block the conversion.
//
// FromModelPrices 把旧的 model_prices 行转换为第一个文档：每个别名行变成该别名的覆盖价
// （输入与输出），模型倍率为 1；model.DefaultPriceModel 那一行变成默认价。无法通过校验的
// 行会被跳过并计数，使一行格式不对的旧数据不会挡住转换。
func FromModelPrices(rows []model.ModelPrice) (Document, int) {
	doc := New()
	skipped := 0
	for _, row := range rows {
		prices := pricing.PriceList{
			{Meter: pricing.MeterInput, PriceMicroPerM: row.InputMicroPerMTok},
			{Meter: pricing.MeterOutput, PriceMicroPerM: row.OutputMicroPerMTok},
		}
		if prices.Validate() != nil {
			skipped++
			continue
		}
		if row.Model == model.DefaultPriceModel {
			doc.DefaultPrices = prices.Sorted()
			continue
		}
		if !ValidAlias(row.Model) || len(doc.Models) >= MaxModels {
			skipped++
			continue
		}
		doc.Models[row.Model] = ModelConfig{Prices: prices.Sorted(), RatePPM: pricing.RateOne}
	}
	return doc, skipped
}

// Parse decodes a stored document. A document from a newer format, or one
// that does not validate, is an error.
//
// Parse 解码一个已保存的文档。来自更新格式的文档，或无法通过校验的文档，都会报错。
func Parse(raw string) (Document, error) {
	var doc Document
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return Document{}, fmt.Errorf("pricingconfig: decode: %w", err)
	}
	if doc.Version > FormatVersion {
		return Document{}, fmt.Errorf("pricingconfig: format version %d is newer than %d", doc.Version, FormatVersion)
	}
	doc.normalize()
	if err := doc.Validate(); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// Encode validates and serializes the document. Map keys serialize sorted
// and price lists are kept in canonical order, so equal documents encode to
// the same bytes.
//
// Encode 校验并序列化文档。map 的键按顺序序列化，单价表保持规范顺序，使相等的文档编码
// 出相同的字节。
func Encode(doc Document) (string, error) {
	doc = doc.Clone()
	doc.normalize()
	if err := doc.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	if len(raw) > MaxDocumentBytes {
		return "", fmt.Errorf("%w: the document is larger than %d bytes", ErrInvalid, MaxDocumentBytes)
	}
	return string(raw), nil
}

// Validate returns an error wrapping ErrInvalid unless the document is
// well formed. See the package limits.
//
// Validate 在文档格式不正确时返回包装了 ErrInvalid 的错误。上限见本包的常量。
func (d Document) Validate() error {
	if d.Version != FormatVersion {
		return invalid("format version %d, want %d", d.Version, FormatVersion)
	}
	if len(d.DefaultPrices) > 0 {
		if err := d.DefaultPrices.Validate(); err != nil {
			return invalid("default prices: %v", err)
		}
	}
	if len(d.Models) > MaxModels {
		return invalid("at most %d models", MaxModels)
	}
	for alias, cfg := range d.Models {
		if err := validateModel(alias, cfg); err != nil {
			return err
		}
	}
	if d.DefaultPlan != DefaultPlanID {
		return invalid("the default plan must be %q", DefaultPlanID)
	}
	if _, ok := d.Plans[DefaultPlanID]; !ok {
		return invalid("the default plan is missing")
	}
	if len(d.Plans) > MaxPlans {
		return invalid("at most %d plans", MaxPlans)
	}
	for id, plan := range d.Plans {
		if err := validatePlan(id, plan); err != nil {
			return err
		}
	}
	return nil
}

func validateModel(alias string, cfg ModelConfig) error {
	if !ValidAlias(alias) {
		return invalid("invalid alias %q", alias)
	}
	if cfg.CatalogModel != "" && !ValidCatalogModel(cfg.CatalogModel) {
		return invalid("model %q: invalid catalog model %q", alias, cfg.CatalogModel)
	}
	if len(cfg.Prices) > 0 {
		if err := cfg.Prices.Validate(); err != nil {
			return invalid("model %q: %v", alias, err)
		}
	}
	if !pricing.ValidRate(cfg.RatePPM) {
		return invalid("model %q: rate %d is outside [0, %d]", alias, cfg.RatePPM, int64(pricing.MaxRate))
	}
	return nil
}

func validatePlan(id string, plan Plan) error {
	if !ValidPlanID(id) {
		return invalid("invalid plan id %q", id)
	}
	if !validName(plan.Name, MaxPlanNameRunes) {
		return invalid("plan %q: invalid name", id)
	}
	if !pricing.ValidRate(plan.DefaultRatePPM) {
		return invalid("plan %q: default rate %d is outside [0, %d]", id, plan.DefaultRatePPM, int64(pricing.MaxRate))
	}
	if len(plan.Models) > MaxPlanModels {
		return invalid("plan %q: at most %d models", id, MaxPlanModels)
	}
	for alias, terms := range plan.Models {
		if !ValidAlias(alias) {
			return invalid("plan %q: invalid alias %q", id, alias)
		}
		hasRate, hasPrices := terms.RatePPM != nil, len(terms.Prices) > 0
		if hasRate == hasPrices {
			return invalid("plan %q, model %q: set exactly one of a rate or fixed prices", id, alias)
		}
		if hasRate && !pricing.ValidRate(*terms.RatePPM) {
			return invalid("plan %q, model %q: rate %d is outside [0, %d]", id, alias, *terms.RatePPM, int64(pricing.MaxRate))
		}
		if hasPrices {
			if err := terms.Prices.Validate(); err != nil {
				return invalid("plan %q, model %q: %v", id, alias, err)
			}
		}
	}
	return nil
}

// ValidAlias reports whether s can be a configured alias: non-empty, at most
// MaxAliasLen bytes, valid UTF-8 without surrounding space or control
// characters, and not model.DefaultPriceModel, which the legacy table used
// for the default price.
//
// ValidAlias 报告 s 能否作为已配置的别名：非空、不超过 MaxAliasLen 字节、是有效的
// UTF-8、首尾没有空白且不含控制字符，并且不是旧表用来表示默认价的
// model.DefaultPriceModel。
func ValidAlias(s string) bool {
	return s != model.DefaultPriceModel && validText(s, MaxAliasLen)
}

// ValidCatalogModel reports whether s is a well-formed catalog entry name.
// Whether the entry exists is checked against the catalog, not here.
//
// ValidCatalogModel 报告 s 是否是格式正确的价目表条目名称。条目是否存在要对照价目表
// 检查，不在这里检查。
func ValidCatalogModel(s string) bool {
	return validText(s, MaxCatalogModelLen)
}

// ValidPlanID reports whether s is DefaultPlanID or an id minted with
// model.PrefixPricePlan: the prefix followed by lower-case letters, digits
// or underscores, at most MaxPlanIDLen bytes.
//
// ValidPlanID 报告 s 是否是 DefaultPlanID，或以 model.PrefixPricePlan 铸造的 id：
// 前缀后面跟小写字母、数字或下划线，总长不超过 MaxPlanIDLen 字节。
func ValidPlanID(s string) bool {
	rest, ok := strings.CutPrefix(s, model.PrefixPricePlan)
	if !ok || rest == "" || len(s) > MaxPlanIDLen {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func validText(s string, maxBytes int) bool {
	if s == "" || len(s) > maxBytes || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

func validName(s string, maxRunes int) bool {
	if s == "" || utf8.RuneCountInString(s) > maxRunes || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// normalize replaces nil maps with empty ones and puts every price list in
// canonical order.
//
// normalize 用空 map 替换 nil map，并把每张单价表排成规范顺序。
func (d *Document) normalize() {
	if d.Models == nil {
		d.Models = map[string]ModelConfig{}
	}
	if d.Plans == nil {
		d.Plans = map[string]Plan{}
	}
	if len(d.DefaultPrices) == 0 {
		d.DefaultPrices = nil
	} else {
		d.DefaultPrices = d.DefaultPrices.Sorted()
	}
	for alias, cfg := range d.Models {
		if len(cfg.Prices) == 0 {
			cfg.Prices = nil
		} else {
			cfg.Prices = cfg.Prices.Sorted()
		}
		d.Models[alias] = cfg
	}
	for id, plan := range d.Plans {
		for alias, terms := range plan.Models {
			if len(terms.Prices) == 0 {
				terms.Prices = nil
			} else {
				terms.Prices = terms.Prices.Sorted()
			}
			plan.Models[alias] = terms
		}
		d.Plans[id] = plan
	}
}

// Clone returns a deep copy, so an edit never aliases the document it
// started from.
//
// Clone 返回深拷贝，使一次修改绝不会与其起点文档共享内存。
func (d Document) Clone() Document {
	out := d
	out.DefaultPrices = append(pricing.PriceList(nil), d.DefaultPrices...)
	out.Models = make(map[string]ModelConfig, len(d.Models))
	for alias, cfg := range d.Models {
		cfg.Prices = append(pricing.PriceList(nil), cfg.Prices...)
		out.Models[alias] = cfg
	}
	out.Plans = make(map[string]Plan, len(d.Plans))
	for id, plan := range d.Plans {
		out.Plans[id] = clonePlan(plan)
	}
	return out
}

func clonePlan(plan Plan) Plan {
	models := make(map[string]PlanModel, len(plan.Models))
	for alias, terms := range plan.Models {
		if terms.RatePPM != nil {
			rate := *terms.RatePPM
			terms.RatePPM = &rate
		}
		terms.Prices = append(pricing.PriceList(nil), terms.Prices...)
		models[alias] = terms
	}
	plan.Models = models
	return plan
}

// SetModel returns a copy with alias's settings replaced by cfg.
//
// SetModel 返回一个副本，其中 alias 的设置被 cfg 替换。
func (d Document) SetModel(alias string, cfg ModelConfig) (Document, error) {
	if err := validateModel(alias, cfg); err != nil {
		return Document{}, err
	}
	out := d.Clone()
	cfg.Prices = append(pricing.PriceList(nil), cfg.Prices...)
	out.Models[alias] = cfg
	out.normalize()
	return out, nil
}

// DeleteModel returns a copy without alias's settings, or ErrNotFound.
//
// DeleteModel 返回一个去掉 alias 设置的副本；没有该设置时返回 ErrNotFound。
func (d Document) DeleteModel(alias string) (Document, error) {
	if _, ok := d.Models[alias]; !ok {
		return Document{}, ErrNotFound
	}
	out := d.Clone()
	delete(out.Models, alias)
	return out, nil
}

// SetDefaultPrices returns a copy with the default price replaced; an empty
// list removes it.
//
// SetDefaultPrices 返回一个替换了默认价的副本；空表表示去掉默认价。
func (d Document) SetDefaultPrices(prices pricing.PriceList) (Document, error) {
	if len(prices) > 0 {
		if err := prices.Validate(); err != nil {
			return Document{}, invalid("default prices: %v", err)
		}
	}
	out := d.Clone()
	out.DefaultPrices = append(pricing.PriceList(nil), prices...)
	out.normalize()
	return out, nil
}

// PutPlan returns a copy with plan id created or replaced.
//
// PutPlan 返回一个新建或替换了方案 id 的副本。
func (d Document) PutPlan(id string, plan Plan) (Document, error) {
	if err := validatePlan(id, plan); err != nil {
		return Document{}, err
	}
	if _, exists := d.Plans[id]; !exists && len(d.Plans) >= MaxPlans {
		return Document{}, invalid("at most %d plans", MaxPlans)
	}
	out := d.Clone()
	out.Plans[id] = clonePlan(plan)
	out.normalize()
	return out, nil
}

// DeletePlan returns a copy without plan id. The default plan is
// ErrDefaultPlan and an unknown plan ErrNotFound. Whether tenants still use
// the plan is the store's check, made under its lock.
//
// DeletePlan 返回一个去掉方案 id 的副本。默认方案返回 ErrDefaultPlan，未知方案返回
// ErrNotFound。是否还有租户在用该方案由 store 在其锁内检查。
func (d Document) DeletePlan(id string) (Document, error) {
	if id == DefaultPlanID {
		return Document{}, ErrDefaultPlan
	}
	if _, ok := d.Plans[id]; !ok {
		return Document{}, ErrNotFound
	}
	out := d.Clone()
	delete(out.Plans, id)
	return out, nil
}

// PlanFor returns the plan with id, falling back to the default plan for an
// empty or unknown id. The store refuses to delete a plan in use, so the
// fallback only covers a document read at a moment before the plan existed.
//
// PlanFor 返回 id 对应的方案；id 为空或未知时回落到默认方案。store 拒绝删除仍在使用的
// 方案，因此这个回落只覆盖「读取的是方案存在之前的文档」这种情况。
func (d Document) PlanFor(id string) Plan {
	if plan, ok := d.Plans[id]; ok && id != "" {
		return plan
	}
	return d.Plans[d.DefaultPlan]
}

// ModelRate returns alias's model rate, pricing.RateOne when it has no
// settings.
//
// ModelRate 返回 alias 的模型倍率；没有设置时为 pricing.RateOne。
func (d Document) ModelRate(alias string) int64 {
	if cfg, ok := d.Models[alias]; ok {
		return cfg.RatePPM
	}
	return pricing.RateOne
}

// PlanTerms returns plan planID's terms for alias: its factor, and its fixed
// sale price when it sets one (then the factor is pricing.RateOne and
// neither the model rate nor any factor applies).
//
// PlanTerms 返回方案 planID 对 alias 的条款：系数，以及设置了固定售价时的那张单价表
// （此时系数为 pricing.RateOne，不再乘以模型倍率或任何系数）。
func (d Document) PlanTerms(planID, alias string) (ratePPM int64, fixed pricing.PriceList) {
	plan := d.PlanFor(planID)
	if terms, ok := plan.Models[alias]; ok {
		if len(terms.Prices) > 0 {
			return pricing.RateOne, terms.Prices
		}
		if terms.RatePPM != nil {
			return *terms.RatePPM, nil
		}
	}
	return plan.DefaultRatePPM, nil
}

// SortedAliases returns the configured aliases in order.
//
// SortedAliases 按顺序返回已配置的别名。
func (d Document) SortedAliases() []string {
	out := make([]string, 0, len(d.Models))
	for alias := range d.Models {
		out = append(out, alias)
	}
	sort.Strings(out)
	return out
}

// SortedPlanIDs returns the plan ids with the default plan first and the
// rest in order.
//
// SortedPlanIDs 返回方案 id：默认方案排第一，其余按顺序排列。
func (d Document) SortedPlanIDs() []string {
	out := make([]string, 0, len(d.Plans))
	for id := range d.Plans {
		if id != d.DefaultPlan {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	if _, ok := d.Plans[d.DefaultPlan]; ok {
		out = append([]string{d.DefaultPlan}, out...)
	}
	return out
}
