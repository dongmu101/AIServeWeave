// billing.go implements prepaid-credit billing: what a tenant may buy and
// how a purchase opens a provider checkout, how a verified provider event
// moves a balance, how usage is priced, and the operator's packages, prices,
// tenant modes and balance adjustments.
//
// billing.go 实现预充值点数计费：租户可以买什么、一次购买如何发起服务商结账、一个已
// 验签的服务商事件如何变动余额、用量如何定价，以及运维的充值包、单价、租户计费模式与
// 余额调整。
package logic

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ErrInvalidSignature is a webhook whose signature does not verify.
//
// ErrInvalidSignature 表示签名校验不通过的 webhook。
var ErrInvalidSignature = errors.New("logic: invalid webhook signature")

// BillingConfig is what billing needs beyond the store: the provider, where
// a tenant returns to after checkout, what a self-registered tenant starts
// with, and how many days after a purchase its tenant may ask for a refund
// (zero turns tenant refund requests off).
//
// BillingConfig 是计费在 store 之外所需的一切：服务商、租户结账后返回的地址、自助注册
// 的租户起步时拥有的点数，以及购买后多少天内租户可以申请退款（为零时关闭租户退款申请）。
type BillingConfig struct {
	// Provider is Dodo. Nil turns credit packages, checkouts, the customer
	// portal, the webhook and refunds off.
	//
	// Provider 是 Dodo。为 nil 时关闭充值包、结账、客户门户、webhook 与退款。
	Provider           billing.Provider
	ReturnURL          string
	SignupGrantCredits int64
	RefundWindowDays   int
	// Alipay is 一人付. Nil turns Alipay purchases and redemptions off.
	//
	// Alipay 是一人付。为 nil 时关闭支付宝购买与兑换。
	Alipay *AlipayConfig
	// Ezfp is 易支付, a second fiat provider for CNY-priced items. Nil turns
	// it off.
	//
	// Ezfp 是易支付，为以 CNY 标价的商品提供的第二个法币服务商。为 nil 时关闭。
	Ezfp *EzfpConfig
}

// WithBilling turns billing on. Without it the Service charges nothing,
// never refuses a key for its balance. Unless WithAccountSales is set,
// billing methods answer ErrNotFound, matching an unconfigured deployment.
//
// WithBilling 开启计费。没有它时 Service 不扣任何费用，从不因余额拒绝 key，每个计费
// 方法在未设置 WithAccountSales 时都回答 ErrNotFound。
func WithBilling(cfg BillingConfig) Option {
	return func(s *Service) {
		if cfg.Provider != nil || cfg.Alipay != nil || cfg.Ezfp != nil {
			s.billing = &cfg
			if cfg.Alipay != nil {
				s.alipayCache = &alipayCache{}
			}
			if cfg.Ezfp != nil {
				s.ezfpAttempts = &attemptGate{gap: ezfpQueryGap}
			}
		}
	}
}

// BillingOverview is what a tenant's billing page shows.
//
// BillingOverview 是租户计费页面展示的内容。
type BillingOverview struct {
	Mode         string
	BalanceMicro int64
	Blocked      bool
	HasCustomer  bool
	Packages     []model.CreditPackage
	// RefundWindowDays is how long after a purchase the tenant may ask for
	// a refund; zero means it may not.
	//
	// RefundWindowDays 是购买后租户可申请退款的天数；为零表示不可申请。
	RefundWindowDays int
	// Dodo, Alipay and Ezfp report which payment methods the deployment
	// offers; EzfpTypes are the 易支付 payment types a tenant may choose.
	//
	// Dodo、Alipay 与 Ezfp 报告部署提供了哪些付款方式；EzfpTypes 是租户可选的易支付
	// 付款方式。
	Dodo      bool
	Alipay    bool
	Ezfp      bool
	EzfpTypes []string
}

// CheckoutResult is an opened checkout: the order it created and where to
// send the tenant.
//
// CheckoutResult 是一次已发起的结账：它创建的订单与要把租户送往的地址。
type CheckoutResult struct {
	OrderID     string
	CheckoutURL string
}

// CreditPackageInput is an operator's package definition. Fields that do not
// apply to the kind must be zero.
//
// CreditPackageInput 是运维定义的充值包。与类型无关的字段必须为零。
type CreditPackageInput struct {
	Name                     string
	Kind                     string
	DodoProductID            string
	CreditsMicro             int64
	PriceMinor               int64
	Currency                 string
	CreditsPerMinorUnitMicro int64
	MinAmountMinor           int64
	MaxAmountMinor           int64
	Active                   bool
	SortOrder                int64
}

// TenantBillingView is one tenant with its billing state, for the operator.
//
// TenantBillingView 是一个租户及其计费状态，供运维查看。
type TenantBillingView struct {
	Tenant  model.Tenant
	Billing model.TenantBilling
}

func (s *Service) billingEnabled() bool { return s.billing != nil }

// dodoEnabled reports whether Dodo checkouts, the portal, the webhook and
// refunds are available.
//
// dodoEnabled 报告 Dodo 结账、门户、webhook 与退款是否可用。
func (s *Service) dodoEnabled() bool { return s.billing != nil && s.billing.Provider != nil }

// ezfpEnabled reports whether 易支付 checkouts, notifications and refunds are
// available.
//
// ezfpEnabled 报告易支付结账、通知与退款是否可用。
func (s *Service) ezfpEnabled() bool { return s.billing != nil && s.billing.Ezfp != nil }

// fiatEnabled reports whether any fiat provider is: credit packages, the
// three checkouts and refunds need one.
//
// fiatEnabled 报告是否有任一法币服务商可用：充值包、三种结账与退款都需要一个。
func (s *Service) fiatEnabled() bool { return s.dodoEnabled() || s.ezfpEnabled() }

// FiatOffers reports through which enabled fiat providers an item priced in
// currency, with Dodo product productID, can be checked out.
//
// FiatOffers 报告一个以 currency 标价、Dodo 产品为 productID 的商品可以经哪些已开启的
// 法币服务商结账。
func (s *Service) FiatOffers(currency, productID string) (dodo, ezfp bool) {
	return s.dodoEnabled() && model.FiatPayable(model.PaymentProviderDodo, currency, productID),
		s.ezfpEnabled() && model.FiatPayable(model.PaymentProviderEzfp, currency, productID)
}

// fiatPayable reports whether an item can be checked out through any
// enabled fiat provider.
//
// fiatPayable 报告一个商品能否经任一已开启的法币服务商结账。
func (s *Service) fiatPayable(currency, productID string) bool {
	dodo, ezfp := s.FiatOffers(currency, productID)
	return dodo || ezfp
}

func (a Actor) canPurchase() bool { return a.Role == model.RoleOwner || a.Role == model.RoleAdmin }

// tenantBilling reads a tenant's billing row, treating a missing one as the
// exempt zero-balance row it stands for.
//
// tenantBilling 读取租户的计费行，缺失时视为它所代表的那一行零余额 exempt 记录。
func (s *Service) tenantBilling(ctx context.Context, tenantID string) (model.TenantBilling, error) {
	row, err := s.store.GetTenantBilling(ctx, tenantID)
	if errors.Is(err, store.ErrNotFound) {
		return model.TenantBilling{TenantID: tenantID, Mode: model.BillingModeExempt}, nil
	}
	return row, err
}

// BillingOverview returns the actor's tenant's mode, balance and the active
// packages. Every role may read it, for the reason CurrentTenant gives.
//
// BillingOverview 返回 actor 所属租户的计费模式、余额与上架的充值包。任何角色都可读，
// 理由同 CurrentTenant。
func (s *Service) BillingOverview(ctx context.Context, actor Actor) (BillingOverview, error) {
	if !s.accountsEnabled() {
		return BillingOverview{}, ErrNotFound
	}
	row, err := s.tenantBilling(ctx, actor.TenantID)
	if err != nil {
		return BillingOverview{}, err
	}
	if !s.billingEnabled() {
		return BillingOverview{Mode: row.Mode, BalanceMicro: row.BalanceMicro, Packages: []model.CreditPackage{}}, nil
	}
	out := BillingOverview{
		Mode: row.Mode, BalanceMicro: row.BalanceMicro, Blocked: row.Blocked(), Packages: []model.CreditPackage{},
		Dodo: s.dodoEnabled(), Alipay: s.alipayEnabled(), Ezfp: s.ezfpEnabled(), EzfpTypes: s.ezfpTypes(),
	}
	if !s.fiatEnabled() {
		return out, nil
	}
	packages, err := s.store.ListCreditPackages(ctx, true)
	if err != nil {
		return BillingOverview{}, err
	}
	for _, pkg := range packages {
		if s.fiatPayable(pkg.Currency, pkg.DodoProductID) {
			out.Packages = append(out.Packages, pkg)
		}
	}
	out.HasCustomer, out.RefundWindowDays = out.Dodo && row.DodoCustomerID != "", s.billing.RefundWindowDays
	return out, nil
}

// CreateCheckout opens a provider checkout for one package through pay's
// provider (the zero Payment is Dodo). Only an owner or admin may buy, as
// only they may manage keys. The credits are fixed on the order now, from
// the package as it stands, so a later price change cannot alter what this
// purchase grants. amountMinor is the chosen amount for a custom package and
// must be zero for a fixed one.
//
// CreateCheckout 经 pay 的服务商（零值 Payment 即 Dodo）为一个充值包发起结账。只有
// owner 或 admin 可以购买，与只有他们能管理 key 一致。点数此刻就按充值包的当前定义固定
// 在订单上，之后的价格变更无法改变这次购买所发放的内容。amountMinor 是自定义充值包所选的
// 金额，固定充值包必须为零。
func (s *Service) CreateCheckout(ctx context.Context, actor Actor, packageID string, amountMinor int64, pay Payment) (CheckoutResult, error) {
	provider, err := s.resolvePayment(pay)
	if err != nil {
		return CheckoutResult{}, err
	}
	if !actor.canPurchase() {
		return CheckoutResult{}, ErrForbidden
	}
	pkg, err := s.store.GetCreditPackage(ctx, packageID)
	if err != nil || !pkg.Active || !model.FiatPayable(provider, pkg.Currency, pkg.DodoProductID) {
		if err == nil || errors.Is(err, store.ErrNotFound) {
			return CheckoutResult{}, ErrNotFound
		}
		return CheckoutResult{}, err
	}
	switch pkg.Kind {
	case model.PackageKindFixed:
		if amountMinor != 0 {
			return CheckoutResult{}, ErrInvalidInput
		}
		amountMinor = pkg.PriceMinor
	case model.PackageKindCustom:
	default:
		return CheckoutResult{}, ErrInvalidInput
	}
	credits, ok := pkg.Credits(amountMinor)
	if !ok {
		return CheckoutResult{}, ErrInvalidInput
	}
	if provider == model.PaymentProviderEzfp {
		now := s.clock.Now()
		order := model.CheckoutOrder{
			ID: model.NewID(model.PrefixCheckoutOrder), TenantID: actor.TenantID, PackageID: pkg.ID,
			AmountMinor: amountMinor, Currency: pkg.Currency, CreditsMicro: credits, Provider: provider, PayType: pay.PayType,
			Status: model.OrderStatusPending, CreatedBy: actor.UserID, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.store.CreateCheckoutOrder(ctx, &order); err != nil {
			return CheckoutResult{}, translate(err)
		}
		result, err := s.openEzfpCheckout(ctx, actor, order, pkg.Name)
		if err != nil {
			return CheckoutResult{}, err
		}
		s.audit(ctx, actor.TenantID, actor.UserID, model.ActionBillingCheckout, order.ID,
			fmt.Sprintf("package %s amount %d %s via ezfp %s", pkg.ID, amountMinor, pkg.Currency, pay.PayType), actor.IP)
		return result, nil
	}
	customerID, err := s.ensureCustomer(ctx, actor)
	if err != nil {
		return CheckoutResult{}, err
	}

	now := s.clock.Now()
	order := model.CheckoutOrder{
		ID: model.NewID(model.PrefixCheckoutOrder), TenantID: actor.TenantID, PackageID: pkg.ID, DodoProductID: pkg.DodoProductID,
		AmountMinor: amountMinor, Currency: pkg.Currency, CreditsMicro: credits, Provider: model.PaymentProviderDodo,
		Status: model.OrderStatusPending, CreatedBy: actor.UserID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.CreateCheckoutOrder(ctx, &order); err != nil {
		return CheckoutResult{}, translate(err)
	}
	req := billing.CheckoutRequest{
		CustomerID: customerID, ProductID: pkg.DodoProductID, Currency: pkg.Currency, ReturnURL: s.billing.ReturnURL,
		OrderID: order.ID, TenantID: actor.TenantID,
	}
	if pkg.Kind == model.PackageKindCustom {
		req.AmountMinor = amountMinor
	}
	checkout, err := s.billing.Provider.CreateCheckout(ctx, req)
	if err != nil {
		slog.Error("opening a billing checkout failed", slog.String("order_id", order.ID))
		return CheckoutResult{}, ErrUnavailable
	}
	if err := s.store.SetCheckoutOrderSession(ctx, order.ID, checkout.SessionID, s.clock.Now()); err != nil {
		return CheckoutResult{}, translate(err)
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionBillingCheckout, order.ID,
		fmt.Sprintf("package %s amount %d %s", pkg.ID, amountMinor, pkg.Currency), actor.IP)
	return CheckoutResult{OrderID: order.ID, CheckoutURL: checkout.URL}, nil
}

// ensureCustomer returns the tenant's provider customer, registering one under
// the purchasing user's email and the tenant's name on its first purchase.
//
// ensureCustomer 返回租户的服务商客户；首次购买时以购买者的邮箱与租户名称登记一个。
func (s *Service) ensureCustomer(ctx context.Context, actor Actor) (string, error) {
	row, err := s.tenantBilling(ctx, actor.TenantID)
	if err != nil {
		return "", err
	}
	if row.DodoCustomerID != "" {
		return row.DodoCustomerID, nil
	}
	user, err := s.store.GetUser(ctx, actor.TenantID, actor.UserID)
	if err != nil {
		return "", translate(err)
	}
	tenant, err := s.store.GetTenant(ctx, actor.TenantID)
	if err != nil {
		return "", translate(err)
	}
	customerID, err := s.billing.Provider.CreateCustomer(ctx, user.Email, tenant.Name, tenant.ID)
	if err != nil {
		slog.Error("registering a billing customer failed", slog.String("tenant_id", tenant.ID))
		return "", ErrUnavailable
	}
	// A concurrent first purchase may have recorded its customer meanwhile;
	// the store keeps that one and this registration goes unused.
	// 并发的首次购买可能已先记录了它的客户；store 保留那一个，本次登记的客户不再使用。
	recorded, err := s.store.SetDodoCustomerID(ctx, tenant.ID, customerID, s.clock.Now())
	return recorded, translate(err)
}

// BillingPortalLink returns a link to the provider's customer portal, where
// receipts and payment methods live. ErrNotFound before the first purchase.
//
// BillingPortalLink 返回服务商客户门户的链接，收据与支付方式都在那里。首次购买之前
// 返回 ErrNotFound。
func (s *Service) BillingPortalLink(ctx context.Context, actor Actor) (string, error) {
	if !s.dodoEnabled() {
		return "", ErrNotFound
	}
	if !actor.canPurchase() {
		return "", ErrForbidden
	}
	row, err := s.tenantBilling(ctx, actor.TenantID)
	if err != nil {
		return "", err
	}
	if row.DodoCustomerID == "" {
		return "", ErrNotFound
	}
	link, err := s.billing.Provider.PortalLink(ctx, row.DodoCustomerID, s.billing.ReturnURL)
	if err != nil {
		slog.Error("opening the billing portal failed", slog.String("tenant_id", actor.TenantID))
		return "", ErrUnavailable
	}
	return link, nil
}

// ListBillingLedger reads a ledger. A tenant actor sees its own tenant only;
// a platform actor sees every tenant, narrowed by filter.
//
// ListBillingLedger 读取账目。租户 actor 只能看到自己的租户；平台 actor 可看到所有
// 租户，由 filter 收窄。
func (s *Service) ListBillingLedger(ctx context.Context, actor Actor, query store.ListQuery, filter store.CreditLedgerFilter) (store.Page[model.CreditLedgerEntry], error) {
	if !s.accountsEnabled() {
		return store.Page[model.CreditLedgerEntry]{}, ErrNotFound
	}
	if !isPlatformActor(actor) {
		filter.TenantID = actor.TenantID
	}
	page, err := s.store.ListCreditLedger(ctx, query, filter)
	return page, translate(err)
}

// ListBillingOrders reads checkout orders, scoped as ListBillingLedger is.
//
// ListBillingOrders 读取结账订单，范围限定同 ListBillingLedger。
func (s *Service) ListBillingOrders(ctx context.Context, actor Actor, query store.ListQuery, filter store.CheckoutOrderFilter) (store.Page[model.CheckoutOrder], error) {
	if !s.accountsEnabled() {
		return store.Page[model.CheckoutOrder]{}, ErrNotFound
	}
	if !isPlatformActor(actor) {
		filter.TenantID = actor.TenantID
	}
	page, err := s.store.ListCheckoutOrders(ctx, query, filter)
	return page, translate(err)
}

// HandleBillingWebhook verifies and applies one provider webhook. It returns
// an error only when the webhook must not be acknowledged: a bad signature,
// store.ErrBillingEventEarly for a refund or dispute that outran the event it
// follows, or a store failure. Every other result — including a payment for
// an order this service does not know — is final, and the webhook is
// acknowledged. Logs carry ids and outcomes, never the payload.
//
// HandleBillingWebhook 校验并应用一个服务商 webhook。只有在 webhook 不应被确认时才
// 返回错误：签名错误、退款或争议早于其前置事件到达时的 store.ErrBillingEventEarly，或
// store 故障。其他所有结果——包括本服务不认识的订单的付款——都是最终结果，webhook 会被
// 确认。日志只带 id 与结果，从不带载荷。
func (s *Service) HandleBillingWebhook(ctx context.Context, payload []byte, headers http.Header) (store.BillingEventResult, error) {
	if !s.dodoEnabled() {
		return store.BillingEventResult{}, ErrNotFound
	}
	event, err := s.billing.Provider.ParseWebhook(payload, headers)
	if err != nil {
		if errors.Is(err, billing.ErrInvalidSignature) {
			slog.Warn("a billing webhook failed signature verification", slog.String("webhook_id", headers.Get("webhook-id")))
			return store.BillingEventResult{}, ErrInvalidSignature
		}
		return store.BillingEventResult{}, err
	}
	if event.Kind == "" {
		return store.BillingEventResult{Outcome: store.BillingOutcomeIgnored}, nil
	}
	result, err := s.store.ApplyBillingEvent(ctx, event, s.clock.Now())
	if errors.Is(err, store.ErrBillingEventEarly) {
		slog.Warn("a billing webhook arrived before the event it follows; asking for redelivery",
			slog.String("type", event.Type), slog.String("webhook_id", event.WebhookID), slog.String("payment_id", event.PaymentID))
		return store.BillingEventResult{}, err
	}
	if err != nil {
		return store.BillingEventResult{}, err
	}
	if (result.BlockedChanged || result.AccessChanged) && s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	level := slog.LevelInfo
	switch result.Outcome {
	case store.BillingOutcomeNoOrder:
		level = slog.LevelWarn
	case store.BillingOutcomeMismatch:
		// A paid order left uncredited, or a refund not taken back, needs an
		// operator. 已付款却未入账的订单，或未扣回的退款，需要运维处理。
		level = slog.LevelError
	}
	slog.Log(ctx, level, "billing webhook processed",
		slog.String("type", event.Type), slog.String("outcome", result.Outcome),
		slog.String("order_id", result.OrderID), slog.String("tenant_id", result.TenantID))
	return result, nil
}

// chargeUsage prices records and charges them in one store transaction,
// dropping cached verifications when a tenant's balance ran out. A request
// served by the tenant's own node costs nothing.
//
// chargeUsage 为 records 定价并在一次 store 事务中扣费；租户余额耗尽时丢弃已缓存的
// 校验结果。由商户自己的节点服务的请求不收费。
func (s *Service) chargeUsage(ctx context.Context, records []model.UsageRecord) error {
	prices, err := s.store.ListModelPrices(ctx)
	if err != nil {
		return err
	}
	byModel := make(map[string]model.ModelPrice, len(prices))
	for _, p := range prices {
		byModel[p.Model] = p
	}
	now := s.clock.Now()
	charges := make([]model.UsageCharge, 0, len(records))
	for _, r := range records {
		price, ok := byModel[r.Model]
		if !ok {
			price = byModel[model.DefaultPriceModel]
		}
		cost := price.Cost(r.PromptTokens, r.CompletionTokens)
		// A tenant's own hardware is not billed; the charge row is still
		// written, at zero, so reports count the request.
		// 商户自己的硬件不计费；费用行仍以零写入，使报表能计入这次请求。
		if owner, owned := nodeaccess.OwnerOf(r.NodeID); owned && owner == r.TenantID {
			cost = 0
		}
		charges = append(charges, model.UsageCharge{
			RequestID: r.ID, TenantID: r.TenantID, Model: r.Model, CostMicro: cost, CreatedAt: now,
		})
	}
	changed, err := s.store.ChargeUsage(ctx, records, charges)
	if err != nil {
		return err
	}
	if changed && s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	return nil
}

// billingBlocked reports whether key verification must refuse the tenant for
// its balance. A store failure is returned rather than read as "not
// blocked", so the Gateway sees an unavailable dependency, not a free pass.
//
// billingBlocked 报告 key 校验是否必须因余额拒绝该租户。store 故障会被返回，而不是被
// 当作「未拦截」，使 Gateway 看到的是一个不可用的依赖，而不是一张免费通行证。
func (s *Service) billingBlocked(ctx context.Context, tenantID string) (bool, error) {
	if !s.billingEnabled() {
		return false, nil
	}
	row, err := s.tenantBilling(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return row.Blocked(), nil
}

// initialBilling is the billing row and opening grant a self-registered tenant
// starts with, or nil when billing is off.
//
// initialBilling 是自助注册的租户起步时的计费行与期初赠送；计费关闭时为 nil。
func (s *Service) initialBilling(tenantID string) (*model.TenantBilling, *model.CreditLedgerEntry) {
	if !s.billingEnabled() {
		return nil, nil
	}
	now := s.clock.Now()
	row := &model.TenantBilling{TenantID: tenantID, Mode: model.BillingModePrepaid, CreatedAt: now, UpdatedAt: now}
	if s.billing.SignupGrantCredits <= 0 {
		return row, nil
	}
	return row, &model.CreditLedgerEntry{
		ID: model.NewID(model.PrefixCreditLedger), TenantID: tenantID, Kind: model.LedgerKindGrant,
		AmountMicro: s.billing.SignupGrantCredits * model.MicroPerCredit, Ref: "signup", CreatedAt: now,
	}
}

// ListCreditPackagesAll returns every package, archived ones included, for
// the operator.
//
// ListCreditPackagesAll 返回全部充值包（包括已下架的），供运维使用。
func (s *Service) ListCreditPackagesAll(ctx context.Context, actor Actor) ([]model.CreditPackage, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return nil, err
	}
	return s.store.ListCreditPackages(ctx, false)
}

// CreateCreditPackage adds a package.
//
// CreateCreditPackage 新增一个充值包。
func (s *Service) CreateCreditPackage(ctx context.Context, actor Actor, in CreditPackageInput) (model.CreditPackage, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return model.CreditPackage{}, err
	}
	pkg, err := packageFromInput(in)
	if err != nil {
		return model.CreditPackage{}, err
	}
	now := s.clock.Now()
	pkg.ID, pkg.CreatedAt, pkg.UpdatedAt = model.NewID(model.PrefixCreditPackage), now, now
	if err := s.store.CreateCreditPackage(ctx, &pkg); err != nil {
		return model.CreditPackage{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingPackageCreate, pkg.ID, pkg.Kind+" "+pkg.DodoProductID, actor.IP)
	return pkg, nil
}

// UpdateCreditPackage replaces a package's definition. Orders already created
// keep the credits they were sold with.
//
// UpdateCreditPackage 替换一个充值包的定义。已创建的订单保留其售出时的点数。
func (s *Service) UpdateCreditPackage(ctx context.Context, actor Actor, id string, in CreditPackageInput) (model.CreditPackage, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return model.CreditPackage{}, err
	}
	old, err := s.store.GetCreditPackage(ctx, id)
	if err != nil {
		return model.CreditPackage{}, translate(err)
	}
	pkg, err := packageFromInput(in)
	if err != nil {
		return model.CreditPackage{}, err
	}
	pkg.ID, pkg.CreatedAt, pkg.UpdatedAt = id, old.CreatedAt, s.clock.Now()
	if err := s.store.UpdateCreditPackage(ctx, pkg); err != nil {
		return model.CreditPackage{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingPackageUpdate, pkg.ID, fmt.Sprintf("active=%t", pkg.Active), actor.IP)
	return pkg, nil
}

// ListModelPrices returns every price, for the operator.
//
// ListModelPrices 返回全部单价，供运维使用。
func (s *Service) ListModelPrices(ctx context.Context, actor Actor) ([]model.ModelPrice, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return nil, err
	}
	return s.store.ListModelPrices(ctx)
}

// PutModelPrice sets one alias's price; model.DefaultPriceModel sets the
// default. A new price applies to usage the Gateway reports from now on.
//
// PutModelPrice 设置一个别名的单价；model.DefaultPriceModel 设置默认单价。新单价
// 适用于此后 Gateway 上报的用量。
func (s *Service) PutModelPrice(ctx context.Context, actor Actor, alias string, inputMicroPerMTok, outputMicroPerMTok int64) (model.ModelPrice, error) {
	if err := s.requireBillingOperator(actor); err != nil {
		return model.ModelPrice{}, err
	}
	alias = strings.TrimSpace(alias)
	if alias == "" || len(alias) > 128 ||
		inputMicroPerMTok < 0 || inputMicroPerMTok > model.MaxPriceMicroPerMTok ||
		outputMicroPerMTok < 0 || outputMicroPerMTok > model.MaxPriceMicroPerMTok {
		return model.ModelPrice{}, ErrInvalidInput
	}
	price := model.ModelPrice{
		Model: alias, InputMicroPerMTok: inputMicroPerMTok, OutputMicroPerMTok: outputMicroPerMTok,
		UpdatedBy: actor.UserID, UpdatedAt: s.clock.Now(),
	}
	if err := s.store.PutModelPrice(ctx, price); err != nil {
		return model.ModelPrice{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingPriceUpdate, truncate(alias, 64),
		fmt.Sprintf("input=%d output=%d", inputMicroPerMTok, outputMicroPerMTok), actor.IP)
	return price, nil
}

// DeleteModelPrice removes one alias's price, so it falls back to the
// default.
//
// DeleteModelPrice 删除一个别名的单价，使其回落到默认单价。
func (s *Service) DeleteModelPrice(ctx context.Context, actor Actor, alias string) error {
	if err := s.requireBillingOperator(actor); err != nil {
		return err
	}
	if err := s.store.DeleteModelPrice(ctx, alias); err != nil {
		return translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingPriceDelete, truncate(alias, 64), "", actor.IP)
	return nil
}

// ListBillingTenants pages through tenants with their billing state.
//
// ListBillingTenants 分页读取租户及其计费状态。
func (s *Service) ListBillingTenants(ctx context.Context, actor Actor, query store.ListQuery, filter store.TenantFilter) (store.Page[TenantBillingView], error) {
	if err := s.requireAccountOperator(actor); err != nil {
		return store.Page[TenantBillingView]{}, err
	}
	tenants, err := s.ListTenants(ctx, actor, query, filter)
	if err != nil {
		return store.Page[TenantBillingView]{}, err
	}
	ids := make([]string, len(tenants.Items))
	for i, t := range tenants.Items {
		ids[i] = t.ID
	}
	rows, err := s.store.GetTenantBillings(ctx, ids)
	if err != nil {
		return store.Page[TenantBillingView]{}, err
	}
	out := store.Page[TenantBillingView]{NextCursor: tenants.NextCursor, Items: make([]TenantBillingView, len(tenants.Items))}
	for i, t := range tenants.Items {
		row, ok := rows[t.ID]
		if !ok {
			row = model.TenantBilling{TenantID: t.ID, Mode: model.BillingModeExempt}
		}
		out.Items[i] = TenantBillingView{Tenant: t, Billing: row}
	}
	return out, nil
}

// SetTenantBillingMode selects prepaid, exempt or account_monthly. Switching a
// tenant with no balance to prepaid starts refusing its traffic at once.
//
// SetTenantBillingMode 设置 prepaid、exempt 或 account_monthly。把一个没有余额的租户切到
// prepaid，会立即开始拒绝它的流量。
func (s *Service) SetTenantBillingMode(ctx context.Context, actor Actor, tenantID, mode string) error {
	if err := s.requireAccountOperator(actor); err != nil {
		return err
	}
	if mode != model.BillingModePrepaid && mode != model.BillingModeExempt && mode != model.BillingModeAccountMonthly {
		return ErrInvalidInput
	}
	if _, err := s.store.GetTenant(ctx, tenantID); err != nil {
		return translate(err)
	}
	changed, err := s.store.SetTenantBillingMode(ctx, tenantID, mode, s.clock.Now())
	if err != nil {
		return translate(err)
	}
	if changed && s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingTenantMode, tenantID, "mode="+mode, actor.IP)
	return nil
}

// AdjustTenantCredit adds amountMicro (negative to deduct) to a tenant's
// balance, with reason as the ledger reference.
//
// AdjustTenantCredit 把 amountMicro（为负则扣减）记入租户余额，以 reason 作为账目
// 引用。
func (s *Service) AdjustTenantCredit(ctx context.Context, actor Actor, tenantID string, amountMicro int64, reason string) (model.CreditLedgerEntry, error) {
	if err := s.requireAccountOperator(actor); err != nil {
		return model.CreditLedgerEntry{}, err
	}
	reason = strings.TrimSpace(reason)
	if amountMicro == 0 || reason == "" || utf8.RuneCountInString(reason) > 128 || len(reason) > 128 {
		return model.CreditLedgerEntry{}, ErrInvalidInput
	}
	if _, err := s.store.GetTenant(ctx, tenantID); err != nil {
		return model.CreditLedgerEntry{}, translate(err)
	}
	entry, changed, err := s.store.AdjustCredit(ctx, model.CreditLedgerEntry{
		ID: model.NewID(model.PrefixCreditLedger), TenantID: tenantID, Kind: model.LedgerKindAdjust,
		AmountMicro: amountMicro, Ref: reason, ActorID: actor.UserID, CreatedAt: s.clock.Now(),
	})
	if err != nil {
		return model.CreditLedgerEntry{}, translate(err)
	}
	if changed && s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionBillingTenantAdjust, tenantID,
		fmt.Sprintf("amount_micro=%d balance_after_micro=%d", amountMicro, entry.BalanceAfterMicro), actor.IP)
	return entry, nil
}

func (s *Service) requireBillingOperator(actor Actor) error {
	if !s.billingEnabled() {
		return ErrNotFound
	}
	return s.requirePlatformActor(actor)
}

// packageFromInput validates an operator's package definition.
//
// packageFromInput 校验运维定义的充值包。
func packageFromInput(in CreditPackageInput) (model.CreditPackage, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.DodoProductID = strings.TrimSpace(in.DodoProductID)
	// A CNY package may leave the Dodo product out: it is then bought through
	// 易支付 only. 以 CNY 标价的充值包可以不填 Dodo 产品：此时只能经易支付购买。
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 128 || len(in.Name) > 512 ||
		(in.DodoProductID == "" && in.Currency != model.EzfpCurrency) || len(in.DodoProductID) > 64 || !model.ValidCurrency(in.Currency) {
		return model.CreditPackage{}, ErrInvalidInput
	}
	pkg := model.CreditPackage{
		Name: in.Name, Kind: in.Kind, DodoProductID: in.DodoProductID, Currency: in.Currency,
		Active: in.Active, SortOrder: in.SortOrder,
	}
	switch in.Kind {
	case model.PackageKindFixed:
		if in.CreditsMicro <= 0 || in.PriceMinor <= 0 ||
			in.CreditsPerMinorUnitMicro != 0 || in.MinAmountMinor != 0 || in.MaxAmountMinor != 0 {
			return model.CreditPackage{}, ErrInvalidInput
		}
		pkg.CreditsMicro, pkg.PriceMinor = in.CreditsMicro, in.PriceMinor
	case model.PackageKindCustom:
		if in.CreditsMicro != 0 || in.PriceMinor != 0 || in.CreditsPerMinorUnitMicro <= 0 ||
			in.MinAmountMinor <= 0 || in.MaxAmountMinor < in.MinAmountMinor {
			return model.CreditPackage{}, ErrInvalidInput
		}
		pkg.CreditsPerMinorUnitMicro, pkg.MinAmountMinor, pkg.MaxAmountMinor = in.CreditsPerMinorUnitMicro, in.MinAmountMinor, in.MaxAmountMinor
		if _, ok := pkg.Credits(pkg.MaxAmountMinor); !ok {
			return model.CreditPackage{}, ErrInvalidInput
		}
	default:
		return model.CreditPackage{}, ErrInvalidInput
	}
	return pkg, nil
}

// truncate shortens s to at most n bytes on a rune boundary, for audit
// targets whose column is narrower than the value.
//
// truncate 在字符边界上把 s 截短到至多 n 字节，用于列宽小于取值的审计目标。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
