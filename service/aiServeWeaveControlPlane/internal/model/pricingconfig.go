package model

import "time"

// ID prefixes of the pricing configuration's entities.
//
// 定价配置各实体的 ID 前缀。
const (
	// PrefixPricePlan prefixes a price plan id. Plans live inside the
	// pricing document, not in a table of their own.
	// PrefixPricePlan 是价格方案 id 的前缀。方案保存在定价文档里，没有自己的表。
	PrefixPricePlan = "plan_"
	// PrefixTenantPricePlan prefixes a tenant_price_plans row id.
	// PrefixTenantPricePlan 是 tenant_price_plans 行 id 的前缀。
	PrefixTenantPricePlan = "tpp_"
)

// Audit actions of the pricing configuration, all platform actions recorded
// under PlatformScope. A tenant's plan change targets the tenant id.
//
// 定价配置的审计动作，都是平台动作，记在 PlatformScope 下。租户方案的变更以租户 id
// 为目标。
const (
	ActionBillingPricingModelSet    = "billing.pricing.model.set"
	ActionBillingPricingModelDelete = "billing.pricing.model.delete"
	ActionBillingPricingDefaultSet  = "billing.pricing.default.set"
	ActionBillingPlanCreate         = "billing.plan.create"
	ActionBillingPlanUpdate         = "billing.plan.update"
	ActionBillingPlanDelete         = "billing.plan.delete"
	ActionBillingTenantPlan         = "billing.tenant.plan"
)

// PricingRevisionSystemActor is the CreatedBy of a revision the control
// plane wrote itself: the first one, converted from model_prices.
//
// PricingRevisionSystemActor 是控制面自己写入的版本的 CreatedBy：即由 model_prices
// 转换而来的第一个版本。
const PricingRevisionSystemActor = "system"

// PricingRevision is one version of the pricing configuration: a whole
// pricingconfig document, in effect from EffectiveAt until the next
// revision's EffectiveAt. Revisions are only ever appended.
//
// PricingRevision 是定价配置的一个版本：一份完整的 pricingconfig 文档，从 EffectiveAt
// 起生效，直到下一个版本的 EffectiveAt。版本只追加，不修改。
type PricingRevision struct {
	ID          int64     `gorm:"primaryKey;autoIncrement"`
	EffectiveAt time.Time `gorm:"not null"`
	// Document is the encoded pricingconfig.Document. A revision list leaves
	// it empty; read one revision to get it.
	// Document 是编码后的 pricingconfig.Document。版本列表中它为空；读取单个版本才能
	// 拿到它。
	Document  string    `gorm:"not null"`
	CreatedBy string    `gorm:"size:40;not null"`
	Summary   string    `gorm:"size:512;not null"`
	CreatedAt time.Time `gorm:"not null"`
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (PricingRevision) TableName() string { return "pricing_revisions" }

// TenantPricePlan is one stretch of time a tenant belonged to a plan other
// than the default: from ValidFrom until ValidTo (exclusive), open-ended
// while ValidTo is nil. A tenant with no row covering a moment belonged to
// the default plan then.
//
// TenantPricePlan 是租户属于某个非默认方案的一段时间：从 ValidFrom 起，到 ValidTo
// （不含）为止，ValidTo 为空表示仍在持续。某个时刻没有任何一行覆盖的租户，在那个时刻
// 属于默认方案。
type TenantPricePlan struct {
	ID        string     `gorm:"primaryKey;size:40"`
	TenantID  string     `gorm:"size:40;not null"`
	PlanID    string     `gorm:"size:40;not null"`
	ValidFrom time.Time  `gorm:"not null"`
	ValidTo   *time.Time ``
	CreatedBy string     `gorm:"size:40;not null"`
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (TenantPricePlan) TableName() string { return "tenant_price_plans" }

// PricingConfigState is the single row holding the pricing-configuration
// version, which goes up by one in the same transaction as every new
// revision and every tenant plan change. Charging compares it with its cache
// to know when to reload. Its row lock also serializes those writes.
//
// PricingConfigState 是保存定价配置版本号的唯一一行；每个新版本、每次租户方案变更都在
// 同一个事务里让它加一。扣费用它与缓存比较，以判断何时重新加载。它的行锁同时让这些写入
// 串行化。
type PricingConfigState struct {
	ID        int       `gorm:"primaryKey;autoIncrement:false"`
	Version   int64     `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (PricingConfigState) TableName() string { return "pricing_config_state" }

// PricingConfigStateID is the id of PricingConfigState's only row.
//
// PricingConfigStateID 是 PricingConfigState 唯一那一行的 id。
const PricingConfigStateID = 1
