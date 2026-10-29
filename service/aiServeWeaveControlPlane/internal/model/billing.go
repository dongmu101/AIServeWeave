// billing.go holds the prepaid-credit billing entities: a tenant's balance,
// the credit packages a tenant may buy through Dodo Payments, the per-model
// prices usage is charged at, the checkout orders a purchase creates, and the
// append-only ledger every balance change is written to.
//
// Balances and prices are integers in micro-credits (MicroPerCredit to one
// credit), so a charge never rounds through a float.
//
// billing.go 保存预充值点数计费的实体：租户余额、租户可经 Dodo Payments 购买的充值
// 包、用量扣费所依据的模型单价、一次购买创建的结账订单，以及记录每一次余额变动的只追加
// 账目。
//
// 余额与单价都以 micro 点（MicroPerCredit 个为一点）为单位的整数存储，扣费过程从不经过
// 浮点数取整。
package model

import (
	"math"
	"math/big"
	"math/bits"
	"time"
)

// MicroPerCredit is how many stored micro-credits make one credit.
//
// MicroPerCredit 是一个点数对应的 micro 点数量。
const MicroPerCredit = 1_000_000

// MaxPriceMicroPerMTok bounds a model price at one million credits per million
// tokens. The bound keeps tokens × price inside 64 bits for any token count a
// single request can report, and no real price comes near it.
//
// MaxPriceMicroPerMTok 把模型单价限制在每百万 token 一百万点。这个上限让单次请求可能
// 上报的任何 token 数乘以单价都落在 64 位以内，且没有任何真实单价会接近它。
const MaxPriceMicroPerMTok = 1_000_000 * MicroPerCredit

// Billing modes a tenant may be in.
//
// 租户可处的计费模式。
const (
	// BillingModePrepaid charges usage against the balance and refuses
	// traffic once the balance is not positive.
	//
	// BillingModePrepaid 从余额中扣除用量，余额不为正时拒绝流量。
	BillingModePrepaid = "prepaid"
	// BillingModeExempt records what usage would cost but never charges or
	// refuses. A tenant without a tenant_billing row is exempt, so tenants
	// that predate billing keep serving after an upgrade.
	//
	// BillingModeExempt 记录用量本应花费多少，但从不扣费也从不拒绝。没有
	// tenant_billing 行的租户就是 exempt，因此计费上线之前的租户在升级之后照常服务。
	BillingModeExempt = "exempt"
)

// Credit package kinds.
//
// 充值包类型。
const (
	// PackageKindFixed sells a fixed number of credits at a fixed product price.
	//
	// PackageKindFixed 以固定的产品价格出售固定数量的点数。
	PackageKindFixed = "fixed"
	// PackageKindCustom sells credits for an amount the tenant chooses, through
	// a Dodo "pay what you want" product.
	//
	// PackageKindCustom 经 Dodo 的「按意愿付费」产品，按租户自选的金额出售点数。
	PackageKindCustom = "custom"
)

// Checkout order statuses.
//
// 结账订单状态。
const (
	OrderStatusPending  = "pending"
	OrderStatusPaid     = "paid"
	OrderStatusFailed   = "failed"
	OrderStatusRefunded = "refunded"
	OrderStatusDisputed = "disputed"
)

// Ledger entry kinds. The set is closed for the same reason the audit actions
// are: a reconciliation query is written against a known vocabulary.
//
// 账目类型。集合是封闭的，理由与审计动作相同：对账查询要针对一套已知词汇来写。
const (
	LedgerKindGrant           = "grant"
	LedgerKindTopup           = "topup"
	LedgerKindUsage           = "usage"
	LedgerKindRefund          = "refund"
	LedgerKindDispute         = "dispute"
	LedgerKindDisputeReversal = "dispute_reversal"
	LedgerKindAdjust          = "adjust"
)

// TenantBilling is one tenant's billing state. A tenant with no row is
// exempt with a zero balance.
//
// TenantBilling 是一个租户的计费状态。没有这一行的租户是余额为零的 exempt 租户。
type TenantBilling struct {
	TenantID       string `gorm:"primaryKey;size:32"`
	Mode           string `gorm:"size:16;not null"`
	BalanceMicro   int64  `gorm:"not null"`
	DodoCustomerID string `gorm:"size:64;not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (TenantBilling) TableName() string { return "tenant_billing" }

// Blocked reports whether the Gateway must refuse this tenant's traffic: a
// prepaid tenant whose balance is not positive.
//
// Blocked 报告 Gateway 是否必须拒绝该租户的流量：余额不为正的 prepaid 租户。
func (b TenantBilling) Blocked() bool {
	return b.Mode == BillingModePrepaid && b.BalanceMicro <= 0
}

// CreditPackage is one purchasable credit offer, backed by one Dodo product.
// A fixed package grants CreditsMicro; a custom one grants
// CreditsPerMinorUnitMicro for every minor currency unit paid, within
// [MinAmountMinor, MaxAmountMinor]. Packages are archived (Active false)
// rather than deleted, because orders keep pointing at them.
//
// CreditPackage 是一份可购买的点数方案，对应一个 Dodo 产品。固定包发放
// CreditsMicro；自定义包对支付的每个货币最小单位发放 CreditsPerMinorUnitMicro，金额须在
// [MinAmountMinor, MaxAmountMinor] 之内。充值包只下架（Active 为 false）而不删除，因为
// 订单仍然指向它们。
type CreditPackage struct {
	ID                       string `gorm:"primaryKey;size:32"`
	Name                     string `gorm:"size:128;not null"`
	Kind                     string `gorm:"size:16;not null"`
	DodoProductID            string `gorm:"size:64;not null"`
	CreditsMicro             int64  `gorm:"not null"`
	PriceMinor               int64  `gorm:"not null"`
	Currency                 string `gorm:"size:8;not null"`
	CreditsPerMinorUnitMicro int64  `gorm:"not null"`
	MinAmountMinor           int64  `gorm:"not null"`
	MaxAmountMinor           int64  `gorm:"not null"`
	Active                   bool   `gorm:"not null"`
	SortOrder                int64  `gorm:"not null"`
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (CreditPackage) TableName() string { return "credit_packages" }

// Credits returns how many micro-credits buying this package for amountMinor
// grants, and false when the amount is outside what the package sells or the
// product overflows.
//
// Credits 返回以 amountMinor 购买该充值包可得到多少 micro 点；金额超出该充值包的售卖
// 范围或乘积溢出时返回 false。
func (p CreditPackage) Credits(amountMinor int64) (int64, bool) {
	switch p.Kind {
	case PackageKindFixed:
		return p.CreditsMicro, p.CreditsMicro > 0
	case PackageKindCustom:
		if amountMinor < p.MinAmountMinor || amountMinor > p.MaxAmountMinor || amountMinor <= 0 {
			return 0, false
		}
		return mulNonNegative(amountMinor, p.CreditsPerMinorUnitMicro)
	default:
		return 0, false
	}
}

// ModelPrice is the per-million-token price for one modelroute alias, in
// micro-credits. The alias DefaultPriceModel prices every alias without a row
// of its own.
//
// ModelPrice 是某个 modelroute 别名每百万 token 的单价，单位为 micro 点。别名
// DefaultPriceModel 为所有没有自己那一行的别名定价。
type ModelPrice struct {
	Model              string `gorm:"primaryKey;size:128"`
	InputMicroPerMTok  int64  `gorm:"column:input_micro_per_m_tok;not null"`
	OutputMicroPerMTok int64  `gorm:"column:output_micro_per_m_tok;not null"`
	UpdatedBy          string `gorm:"size:32;not null"`
	UpdatedAt          time.Time
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (ModelPrice) TableName() string { return "model_prices" }

// DefaultPriceModel is the ModelPrice row that applies to any alias without
// a price of its own.
//
// DefaultPriceModel 是适用于任何没有自己单价的别名的那一行 ModelPrice。
const DefaultPriceModel = "*"

// Cost returns the charge for one request in micro-credits, rounded up so a
// non-zero usage at a non-zero price never costs nothing. It reports false
// when the product would overflow, which only a token count no request can
// carry reaches given MaxPriceMicroPerMTok.
//
// Cost 返回一次请求的费用（micro 点），向上取整，使得非零单价下的非零用量绝不会
// 免费。乘积会溢出时返回 false——在 MaxPriceMicroPerMTok 的约束下，只有任何请求都不可能
// 携带的 token 数才会走到这一步。
func (p ModelPrice) Cost(promptTokens, completionTokens int64) (int64, bool) {
	in, ok := mulNonNegative(promptTokens, p.InputMicroPerMTok)
	if !ok {
		return 0, false
	}
	out, ok := mulNonNegative(completionTokens, p.OutputMicroPerMTok)
	if !ok || in > math.MaxInt64-out {
		return 0, false
	}
	sum := in + out
	return sum/1_000_000 + boolToInt64(sum%1_000_000 != 0), true
}

// CheckoutOrder is one purchase attempt. Its credits are fixed when the order
// is created, so a webhook only confirms what was sold rather than deciding
// it. DodoPaymentID is unique when set: it is the second guard against
// crediting one payment twice, behind the webhook-id dedup.
//
// CheckoutOrder 是一次购买尝试。点数在创建订单时就已确定，因此 webhook 只负责确认
// 卖出了什么，而不负责决定卖什么。DodoPaymentID 一旦设置即唯一：它是 webhook-id 去重
// 之后，防止同一笔付款入账两次的第二道防线。
type CheckoutOrder struct {
	ID            string  `gorm:"primaryKey;size:32"`
	TenantID      string  `gorm:"size:32;not null"`
	PackageID     string  `gorm:"size:32;not null"`
	DodoSessionID string  `gorm:"size:128;not null"`
	DodoPaymentID *string `gorm:"size:64"`
	AmountMinor   int64   `gorm:"not null"`
	Currency      string  `gorm:"size:8;not null"`
	CreditsMicro  int64   `gorm:"not null"`
	RefundedMinor int64   `gorm:"not null"`
	Status        string  `gorm:"size:16;not null"`
	CreatedBy     string  `gorm:"size:32;not null"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (CheckoutOrder) TableName() string { return "checkout_orders" }

// RefundCredits returns the micro-credits a refund of refundMinor takes back:
// the order's credits in proportion to the share of its amount refunded,
// rounded up so a partial refund never leaves a fraction of credit unpaid for.
//
// RefundCredits 返回退款 refundMinor 应扣回多少 micro 点：按退款占订单金额的比例
// 折算订单点数，向上取整，使部分退款绝不会留下一点未付费的零头。
func (o CheckoutOrder) RefundCredits(refundMinor int64) int64 {
	if o.AmountMinor <= 0 || refundMinor <= 0 {
		return 0
	}
	if refundMinor >= o.AmountMinor {
		return o.CreditsMicro
	}
	num := new(big.Int).Mul(big.NewInt(o.CreditsMicro), big.NewInt(refundMinor))
	den := big.NewInt(o.AmountMinor)
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

// CreditLedgerEntry is one balance change. The ledger is append-only and
// BalanceAfterMicro is the balance this entry left behind, so a tenant's
// history reads without replaying it. IdempotencyKey, when set, is unique:
// it is what makes a replayed webhook a no-op.
//
// CreditLedgerEntry 是一次余额变动。账目只追加，BalanceAfterMicro 是本条账目留下的
// 余额，因此读取租户历史无需重放。IdempotencyKey 一旦设置即唯一：重放的 webhook 正是靠
// 它变成空操作。
type CreditLedgerEntry struct {
	ID                string  `gorm:"primaryKey;size:32"`
	TenantID          string  `gorm:"size:32;not null"`
	Kind              string  `gorm:"size:24;not null"`
	AmountMicro       int64   `gorm:"not null"`
	BalanceAfterMicro int64   `gorm:"not null"`
	Ref               string  `gorm:"size:128;not null"`
	IdempotencyKey    *string `gorm:"size:160"`
	ActorID           string  `gorm:"size:32;not null"`
	CreatedAt         time.Time
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (CreditLedgerEntry) TableName() string { return "credit_ledger" }

// UsageCharge is what one usage record cost. Its key is the usage record's
// request id, so a usage record is charged at most once however often the
// Gateway re-pushes it.
//
// UsageCharge 是一条用量记录的费用。它的主键就是用量记录的请求 id，因此无论 Gateway
// 重推多少次，一条用量记录最多扣费一次。
type UsageCharge struct {
	RequestID string `gorm:"primaryKey;size:64"`
	TenantID  string `gorm:"size:32;not null"`
	Model     string `gorm:"size:128;not null"`
	CostMicro int64  `gorm:"not null"`
	// Charged reports whether CostMicro was taken from a balance, false for
	// an exempt tenant whose cost is recorded for reporting only.
	//
	// Charged 报告 CostMicro 是否真的从余额中扣除；exempt 租户的费用只为报表而记录，
	// 此时为 false。
	Charged   bool `gorm:"not null"`
	CreatedAt time.Time
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (UsageCharge) TableName() string { return "usage_charges" }

// BillingWebhookEvent records a processed webhook-id, so a redelivered event
// is acknowledged without being applied again.
//
// BillingWebhookEvent 记录一个已处理的 webhook-id，使重新投递的事件被确认而不被再次
// 应用。
type BillingWebhookEvent struct {
	WebhookID  string `gorm:"primaryKey;size:128"`
	Type       string `gorm:"size:64;not null"`
	ReceivedAt time.Time
}

// TableName pins the table name.
//
// TableName 钉死表名。
func (BillingWebhookEvent) TableName() string { return "billing_webhook_events" }

// mulNonNegative multiplies two non-negative values, reporting false on a
// negative operand or an overflow.
//
// mulNonNegative 将两个非负数相乘；操作数为负或溢出时返回 false。
func mulNonNegative(a, b int64) (int64, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, false
	}
	return int64(lo), true
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
