// alipay.go implements credits bought through Alipay with 一人付 (opcpay).
// A tenant buys a count or point package in Alipay; the quota stays in
// Alipay until the tenant redeems part of it, which formally consumes it
// there and, once the consumption is confirmed, books credits here.
//
// Nothing here treats a purchase entry, a return to the Console or a QR scan
// as a payment: 一人付 reports no per-payment status, so only the quota it
// reports and a confirmed consumption count. The 一人付 user id is the
// tenant id from the verified session, never from the request.
//
// alipay.go 实现经一人付（opcpay）以支付宝购买点数。租户在支付宝购买次数或积分套餐；
// 额度一直保留在支付宝，直到租户兑换其中一部分——这会在支付宝正式核销，并在核销确认
// 之后在本服务入账点数。
//
// 这里从不把购买入口、回到 Console 或扫码当作付款：一人付不报告逐笔付款状态，只有它
// 报告的额度与已确认的核销才算数。一人付的 user id 是来自已验证会话的租户 id，从不取自请求。
package logic

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"regexp"
	"sync"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// AlipayConfig is what Alipay billing needs: the 一人付 client, how many
// micro-credits one unit redeems into, and the page Alipay returns to.
//
// AlipayConfig 是支付宝计费所需的一切：一人付客户端、每单位兑换的 micro 点数，以及
// 支付宝回跳的页面。
type AlipayConfig struct {
	Client              billing.Alipay
	CreditsPerUnitMicro int64
	ReturnURL           string
}

// alipayCatalogTTL is how long the capabilities and plans are reused. They
// change only when the merchant edits the project in Alipay, and 一人付
// allows 120 calls a minute per key for the whole deployment.
//
// alipayCatalogTTL 是能力与套餐的复用时长。它们只在商户于支付宝修改项目时变化，而一人付
// 对整个部署的每把 Key 每分钟只允许 120 次调用。
const alipayCatalogTTL = time.Minute

// alipayRetryGap is the least time between two resumptions of the same
// redemption, so a tenant pressing refresh cannot spend the shared rate.
//
// alipayRetryGap 是同一兑换两次恢复之间的最短间隔，使租户反复刷新不会耗尽共享的调用额度。
const alipayRetryGap = 5 * time.Second

// alipayCallTimeout bounds every 一人付 call one operation makes, together.
// It stays below the route timeout, itself below the Console's 10 seconds,
// so a slow answer is recorded as unknown rather than cut off unseen.
// Writes run detached from the request, so a client that goes away mid-call
// does not cut off a consumption whose answer must be recorded.
//
// alipayCallTimeout 合计限制一次操作发起的全部一人付调用。它低于路由超时，而路由超时
// 又低于 Console 的 10 秒，使缓慢的答复被记为未知，而不是在无人察觉时被截断。写操作与
// 请求解绑，使中途离开的客户端不会截断一次必须记录其答复的核销。
const alipayCallTimeout = 8 * time.Second

// alipayContext is the detached, bounded context of one write operation.
func alipayContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), alipayCallTimeout)
}

// alipayRecoveryAge is how old a pending redemption must be before the
// background recovery resumes it, leaving the tenant's own call to finish.
//
// alipayRecoveryAge 是后台恢复处理一个待确认兑换前它必须达到的时长，让租户自己的调用先完成。
const alipayRecoveryAge = 30 * time.Second

// alipayReason is the consumption reason 一人付 records: short, fixed, and
// free of anything personal.
//
// alipayReason 是一人付记录的核销说明：简短、固定、不含任何个人信息。
const alipayReason = "兑换 AIServeWeave 点数"

// actionIDPattern is a browser-generated action id: a UUID or similar.
var actionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// alipayCache holds the plans (mu, held across their fetch so a cold cache
// is fetched once) and the recent resumption attempts (attemptsMu).
type alipayCache struct {
	mu      sync.Mutex
	at      time.Time
	caps    billing.AlipayCapabilities
	product billing.AlipayProduct

	attemptsMu sync.Mutex
	attempts   map[string]time.Time
}

func (s *Service) alipayEnabled() bool { return s.billing != nil && s.billing.Alipay != nil }

// AlipayOverview is what a tenant's Alipay section shows. Plans are the
// merchant's plans as Alipay reports them, prices included; the access
// fields are only meaningful when AccessKnown.
//
// AlipayOverview 是租户支付宝分区展示的内容。Plans 是支付宝报告的商户套餐（含价格）；
// 额度字段只在 AccessKnown 时有意义。
type AlipayOverview struct {
	CapabilityStatus    string
	ProductName         string
	BillingMode         string
	QuotaUnit           string
	Supported           bool
	CanPurchase         bool
	CanRedeem           bool
	Plans               []billing.AlipayOption
	AccessKnown         bool
	Total               int64
	Used                int64
	Remaining           int64
	CreditsPerUnitMicro int64
	MaxAmount           int64
	Pending             *model.AlipayRedemption
}

// AlipayPurchaseResult is an opened purchase entry.
//
// AlipayPurchaseResult 是一个已创建的购买入口。
type AlipayPurchaseResult struct {
	ID          string
	PurchaseURL string
	ExpireTime  string
}

// alipayCatalog returns the capabilities and plans, cached for
// alipayCatalogTTL.
func (s *Service) alipayCatalog(ctx context.Context) (billing.AlipayCapabilities, billing.AlipayProduct, error) {
	c := s.alipayCache
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.clock.Now()
	if !c.at.IsZero() && now.Sub(c.at) < alipayCatalogTTL {
		return c.caps, c.product, nil
	}
	caps, err := s.billing.Alipay.Client.Capabilities(ctx)
	if err != nil {
		return billing.AlipayCapabilities{}, billing.AlipayProduct{}, err
	}
	product, err := s.billing.Alipay.Client.Product(ctx)
	if err != nil {
		return billing.AlipayCapabilities{}, billing.AlipayProduct{}, err
	}
	c.caps, c.product, c.at = caps, product, now
	return caps, product, nil
}

// forgetAlipayCatalog drops the cached plans after 一人付 refused an
// operation for its configuration, so the next read sees the change.
func (s *Service) forgetAlipayCatalog() {
	s.alipayCache.mu.Lock()
	s.alipayCache.at = time.Time{}
	s.alipayCache.mu.Unlock()
}

// alipayTerms is what the current configuration allows.
type alipayTerms struct {
	supported, canPurchase, canRedeem bool
	maxAmount                         int64
	plans                             []billing.AlipayOption
}

func (s *Service) alipayTermsOf(caps billing.AlipayCapabilities, product billing.AlipayProduct) alipayTerms {
	t := alipayTerms{supported: product.Quantity(), plans: []billing.AlipayOption{}}
	enabled := caps.ProviderConfigured && product.CapabilityStatus == billing.AlipayCapabilityEnabled
	if !t.supported {
		return t
	}
	for _, o := range product.Options {
		if o.BillingMode == "" || o.BillingMode == billing.AlipayModeQuantity {
			t.plans = append(t.plans, o)
		}
	}
	t.canPurchase = enabled && caps.PurchaseEntry && len(t.plans) > 0
	t.canRedeem = enabled && caps.Consumption
	t.maxAmount = 1
	if product.QuotaUnit == billing.AlipayUnitPoint {
		t.maxAmount = min(int64(billing.MaxAlipayPointAmount), math.MaxInt64/s.billing.Alipay.CreditsPerUnitMicro)
	}
	return t
}

func (s *Service) requireAlipayTenant(actor Actor, write bool) error {
	if !s.alipayEnabled() {
		return ErrNotFound
	}
	if actor.TenantID == "" || actor.TenantID == model.PlatformScope {
		return ErrForbidden
	}
	if write && !actor.canPurchase() {
		return ErrForbidden
	}
	return nil
}

// alipayUnavailable logs a failed 一人付 call by its code and request id
// only, and answers ErrUnavailable.
func alipayUnavailable(op string, err error) error {
	code, status := billing.AlipayErrorCode(err)
	slog.Warn("calling 一人付 failed", slog.String("op", op), slog.String("code", code), slog.Int("status", status))
	return ErrUnavailable
}

// AlipayOverview returns the plans, the tenant's remaining quota, what may be
// done now and the tenant's pending redemption, which RefreshAlipayRedemption
// resumes. Every role may read it.
//
// AlipayOverview 返回套餐、租户剩余额度、当前可做的操作，以及租户待确认的兑换（由
// RefreshAlipayRedemption 恢复）。任何角色都可读取。
func (s *Service) AlipayOverview(ctx context.Context, actor Actor) (AlipayOverview, error) {
	if err := s.requireAlipayTenant(actor, false); err != nil {
		return AlipayOverview{}, err
	}
	pending, err := s.tenantPendingRedemption(ctx, actor.TenantID)
	if err != nil {
		return AlipayOverview{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, alipayCallTimeout)
	defer cancel()
	caps, product, err := s.alipayCatalog(ctx)
	if err != nil {
		return AlipayOverview{}, alipayUnavailable("product", err)
	}
	terms := s.alipayTermsOf(caps, product)
	out := AlipayOverview{
		CapabilityStatus: product.CapabilityStatus, ProductName: product.ProductName, BillingMode: product.BillingMode,
		QuotaUnit: product.QuotaUnit, Supported: terms.supported, CanPurchase: terms.canPurchase, CanRedeem: terms.canRedeem,
		Plans: terms.plans, CreditsPerUnitMicro: s.billing.Alipay.CreditsPerUnitMicro, MaxAmount: terms.maxAmount, Pending: pending,
	}
	if terms.supported && caps.EntitlementQuery {
		access, err := s.billing.Alipay.Client.Access(ctx, actor.TenantID, 1)
		if err != nil {
			// The plans are still worth showing; the quota is simply unknown.
			_ = alipayUnavailable("access", err)
		} else if access.QuotaUnit == "" || access.QuotaUnit == product.QuotaUnit {
			out.AccessKnown, out.Total, out.Used, out.Remaining = true, access.Total, access.Used, access.Remaining
		}
	}
	return out, nil
}

func (s *Service) tenantPendingRedemption(ctx context.Context, tenantID string) (*model.AlipayRedemption, error) {
	page, err := s.store.ListAlipayRedemptions(ctx, store.ListQuery{Limit: 1}, store.AlipayRedemptionFilter{
		TenantID: tenantID, Status: model.AlipayRedemptionPending,
	})
	if err != nil {
		return nil, translate(err)
	}
	if len(page.Items) == 0 {
		return nil, nil
	}
	return &page.Items[0], nil
}

// CreateAlipayPurchase opens the Alipay purchase entry for skuID (empty lets
// the buyer choose in Alipay), saved under the browser's actionID first so a
// retry of the same click repeats the same request. Only an owner or admin
// may buy. A saved entry that failed or expired is a conflict: a new click
// needs a new action id.
//
// CreateAlipayPurchase 为 skuID（为空时由购买者在支付宝选择）创建支付宝购买入口，先以
// 浏览器的 actionID 保存，使同一次点击的重试重复同一个请求。只有 owner 或 admin 可以购买。
// 已失败或已过期的入口视为冲突：新的点击需要新的 action id。
func (s *Service) CreateAlipayPurchase(ctx context.Context, actor Actor, actionID, skuID string) (AlipayPurchaseResult, error) {
	if err := s.requireAlipayTenant(actor, true); err != nil {
		return AlipayPurchaseResult{}, err
	}
	if !actionIDPattern.MatchString(actionID) || len(skuID) > 128 {
		return AlipayPurchaseResult{}, ErrInvalidInput
	}
	ctx, cancel := alipayContext(ctx)
	defer cancel()
	caps, product, err := s.alipayCatalog(ctx)
	if err != nil {
		return AlipayPurchaseResult{}, alipayUnavailable("product", err)
	}
	terms := s.alipayTermsOf(caps, product)
	if !terms.canPurchase {
		return AlipayPurchaseResult{}, ErrConflict
	}
	if skuID != "" {
		found := false
		for _, o := range terms.plans {
			found = found || o.SkuID == skuID
		}
		if !found {
			return AlipayPurchaseResult{}, ErrNotFound
		}
	}
	now := s.clock.Now().UTC()
	row, err := s.store.CreateAlipayPurchase(ctx, model.AlipayPurchase{
		ID: model.NewID(model.PrefixAlipayPurchase), TenantID: actor.TenantID, ActionID: actionID, SkuID: skuID,
		CallbackURL: s.billing.Alipay.ReturnURL, Status: model.AlipayPurchaseUnknown, CreatedBy: actor.UserID,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return AlipayPurchaseResult{}, translate(err)
	}
	if row.Status == model.AlipayPurchaseFailed || row.Status == model.AlipayPurchaseExpired {
		return AlipayPurchaseResult{}, ErrConflict
	}
	// The saved parameters, not this request's, so a retry is identical.
	got, callErr := s.billing.Alipay.Client.Purchase(ctx, billing.AlipayPurchaseRequest{
		UserID: row.TenantID, IdempotencyKey: row.ID, SkuID: row.SkuID, CallbackURL: row.CallbackURL,
	})
	row.UpdatedAt = s.clock.Now().UTC()
	result := ErrUnavailable
	switch code, _ := billing.AlipayErrorCode(callErr); {
	case callErr == nil && got.Status == billing.AlipayPurchaseReady && httpsURL(got.URL):
		row.Status, row.PurchaseURL, row.ExpireTime, row.ReasonCode = model.AlipayPurchaseReady, got.URL, truncate(got.ExpireTime, 64), ""
		result = nil
	case callErr == nil && got.Status == billing.AlipayPurchaseFailed:
		row.Status, row.ReasonCode = model.AlipayPurchaseFailed, truncate(got.ReasonCode, 64)
		result = ErrConflict
	case code == "PURCHASE_ENTRY_EXPIRED":
		row.Status, row.ReasonCode = model.AlipayPurchaseExpired, code
		result = ErrConflict
	case code == "CONFLICT":
		s.forgetAlipayCatalog()
		result = ErrConflict
	case code == "IDEMPOTENCY_CONFLICT":
		slog.Error("一人付 refused a purchase retry as changed", slog.String("purchase_id", row.ID))
		result = ErrConflict
	default:
		// UNKNOWN, a timeout or an outage: the entry may exist. The same
		// click retried resends the same request.
		if callErr != nil {
			_ = alipayUnavailable("purchase", callErr)
		}
	}
	if err := s.store.SaveAlipayPurchase(context.WithoutCancel(ctx), row); err != nil {
		return AlipayPurchaseResult{}, translate(err)
	}
	if result != nil {
		return AlipayPurchaseResult{}, result
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionAlipayPurchase, row.ID, "sku "+row.SkuID, actor.IP)
	return AlipayPurchaseResult{ID: row.ID, PurchaseURL: row.PurchaseURL, ExpireTime: row.ExpireTime}, nil
}

func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && len(raw) <= 2048
}

// RedeemAlipay redeems amount units of the tenant's Alipay quota into
// credits at the configured rate, both fixed now. The redemption is saved
// under the browser's actionID before 一人付 is asked, so a retry of the same
// click resumes it rather than consuming twice; while one is pending the
// tenant cannot start another. A count package redeems one count at a time.
// The redemption comes back pending when the outcome is not yet known.
//
// RedeemAlipay 按配置比例把租户支付宝额度中的 amount 个单位兑换为点数，两者此刻确定。
// 兑换在请求一人付之前以浏览器的 actionID 保存，使同一次点击的重试恢复它而不是重复扣减；
// 有待确认兑换时租户不能发起新的兑换。次数包每次兑换一次。结果尚未知晓时，兑换以待确认状态返回。
func (s *Service) RedeemAlipay(ctx context.Context, actor Actor, actionID string, amount int64) (model.AlipayRedemption, error) {
	if err := s.requireAlipayTenant(actor, true); err != nil {
		return model.AlipayRedemption{}, err
	}
	if !actionIDPattern.MatchString(actionID) || amount < 1 {
		return model.AlipayRedemption{}, ErrInvalidInput
	}
	ctx, cancel := alipayContext(ctx)
	defer cancel()
	caps, product, err := s.alipayCatalog(ctx)
	if err != nil {
		return model.AlipayRedemption{}, alipayUnavailable("product", err)
	}
	terms := s.alipayTermsOf(caps, product)
	if !terms.canRedeem {
		return model.AlipayRedemption{}, ErrConflict
	}
	if amount > terms.maxAmount {
		return model.AlipayRedemption{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	fresh := model.AlipayRedemption{
		ID: model.NewID(model.PrefixAlipayRedemption), TenantID: actor.TenantID, ActionID: actionID, Amount: amount,
		QuotaUnit: product.QuotaUnit, CreditsMicro: amount * s.billing.Alipay.CreditsPerUnitMicro, Reason: alipayReason,
		CreatedBy: actor.UserID, CreatedAt: now, UpdatedAt: now,
	}
	row, err := s.store.CreateAlipayRedemption(ctx, fresh)
	if err != nil {
		return model.AlipayRedemption{}, translate(err)
	}
	if row.Status != model.AlipayRedemptionPending {
		return row, nil
	}
	if row.ID != fresh.ID {
		// The same click again: resume what it started.
		return s.resumeRedemption(ctx, row)
	}
	s.noteAttempt(row.ID)
	return s.advanceRedemption(ctx, row, true)
}

// RefreshAlipayRedemption resumes one of the tenant's pending redemptions
// and returns it as it now stands.
//
// RefreshAlipayRedemption 恢复租户的一个待确认兑换，并返回其当前状态。
func (s *Service) RefreshAlipayRedemption(ctx context.Context, actor Actor, id string) (model.AlipayRedemption, error) {
	if err := s.requireAlipayTenant(actor, false); err != nil {
		return model.AlipayRedemption{}, err
	}
	row, err := s.store.GetAlipayRedemption(ctx, id)
	if err != nil {
		return model.AlipayRedemption{}, translate(err)
	}
	if row.TenantID != actor.TenantID {
		return model.AlipayRedemption{}, ErrNotFound
	}
	ctx, cancel := alipayContext(ctx)
	defer cancel()
	return s.resumeRedemption(ctx, row)
}

// ListAlipayRedemptions lists the actor's tenant's redemptions.
//
// ListAlipayRedemptions 列出 actor 所属租户的兑换。
func (s *Service) ListAlipayRedemptions(ctx context.Context, actor Actor, query store.ListQuery) (store.Page[model.AlipayRedemption], error) {
	if err := s.requireAlipayTenant(actor, false); err != nil {
		return store.Page[model.AlipayRedemption]{}, err
	}
	page, err := s.store.ListAlipayRedemptions(ctx, query, store.AlipayRedemptionFilter{TenantID: actor.TenantID})
	return page, translate(err)
}

// RunAlipayRecovery resumes pending redemptions every interval, covering a
// call cut off by a restart or a tenant who never came back.
//
// RunAlipayRecovery 每隔 interval 恢复一次待确认兑换，覆盖被重启截断的调用，以及一去
// 不回的租户。
func (s *Service) RunAlipayRecovery(ctx context.Context, interval time.Duration) {
	if !s.alipayEnabled() || interval <= 0 {
		return
	}
	for {
		fired, stop := s.clock.NewTimer(interval)
		select {
		case <-ctx.Done():
			stop()
			return
		case <-fired:
		}
		s.RecoverAlipayRedemptions(ctx)
	}
}

// RecoverAlipayRedemptions resumes a bounded batch of pending redemptions
// older than alipayRecoveryAge.
//
// RecoverAlipayRedemptions 恢复一批有上限、早于 alipayRecoveryAge 的待确认兑换。
func (s *Service) RecoverAlipayRedemptions(ctx context.Context) {
	if !s.alipayEnabled() {
		return
	}
	page, err := s.store.ListAlipayRedemptions(ctx, store.ListQuery{Limit: 10}, store.AlipayRedemptionFilter{
		Status: model.AlipayRedemptionPending, CreatedBefore: s.clock.Now().Add(-alipayRecoveryAge),
	})
	if err != nil {
		slog.Error("listing pending Alipay redemptions failed")
		return
	}
	for _, row := range page.Items {
		if ctx.Err() != nil {
			return
		}
		opCtx, cancel := context.WithTimeout(ctx, alipayCallTimeout)
		_, err := s.resumeRedemption(opCtx, row)
		cancel()
		if err != nil {
			slog.Error("resuming an Alipay redemption failed", slog.String("redemption_id", row.ID))
		}
	}
}

// noteAttempt records a call for row id and reports whether the previous
// one was at least alipayRetryGap ago.
func (s *Service) noteAttempt(id string) bool {
	c := s.alipayCache
	c.attemptsMu.Lock()
	defer c.attemptsMu.Unlock()
	now := s.clock.Now()
	if c.attempts == nil {
		c.attempts = map[string]time.Time{}
	}
	for key, at := range c.attempts {
		if now.Sub(at) >= alipayRetryGap {
			delete(c.attempts, key)
		}
	}
	if _, recent := c.attempts[id]; recent {
		return false
	}
	c.attempts[id] = now
	return true
}

// resumeRedemption asks 一人付 about a pending redemption, unless it was
// asked within alipayRetryGap. A redemption no longer pending is returned
// as it is.
func (s *Service) resumeRedemption(ctx context.Context, row model.AlipayRedemption) (model.AlipayRedemption, error) {
	if row.Status != model.AlipayRedemptionPending || !s.noteAttempt(row.ID) {
		return row, nil
	}
	return s.advanceRedemption(ctx, row, false)
}

// advanceRedemption consumes (consume) or looks up the consumption of a
// pending redemption and settles it once the answer is definite. A lookup
// that finds no consumption sends the original consumption again, with the
// same key and parameters. An answer that is not definite leaves the
// redemption pending, which is not an error. ctx bounds the calls; the
// settlement itself is written even if ctx has ended.
func (s *Service) advanceRedemption(ctx context.Context, row model.AlipayRedemption, consume bool) (model.AlipayRedemption, error) {
	callCtx := ctx
	client := s.billing.Alipay.Client
	req := billing.AlipayConsumeRequest{UserID: row.TenantID, IdempotencyKey: row.ID, Amount: row.Amount, Reason: row.Reason}
	var got billing.AlipayConsumption
	var err error
	if consume {
		got, err = client.Consume(callCtx, req)
	} else {
		got, err = client.Consumption(callCtx, row.TenantID, row.ID)
		if code, status := billing.AlipayErrorCode(err); status == 404 && code == "NOT_FOUND" {
			consume = true
			got, err = client.Consume(callCtx, req)
		}
	}
	outcome, definite := judgeConsumption(row, got, err, consume)
	if !definite {
		return row, nil
	}
	settled, changed, err := s.store.SettleAlipayRedemption(context.WithoutCancel(ctx), outcome, s.clock.Now().UTC())
	if err != nil {
		slog.Error("settling an Alipay redemption failed", slog.String("redemption_id", row.ID))
		return model.AlipayRedemption{}, translate(err)
	}
	s.invalidateIf(ctx, changed)
	if outcome.Succeeded && row.Status != settled.Status {
		detail := fmt.Sprintf("amount %d %s credits_micro %d", settled.Amount, settled.QuotaUnit, settled.CreditsMicro)
		if settled.PresaleOrderID != "" {
			detail = fmt.Sprintf("amount %d %s presale_order %s", settled.Amount, settled.QuotaUnit, settled.PresaleOrderID)
		}
		s.audit(ctx, settled.TenantID, settled.CreatedBy, model.ActionAlipayRedeem, settled.ID, detail, "")
	}
	return settled, nil
}

// judgeConsumption decides whether an answer settles row. Only SUCCEEDED
// with confirmed=true and the same key, amount and unit is a success; only
// FAILED, or a refusal of the consume request itself, is a failure. A
// success that does not match is logged and left pending for an operator.
func judgeConsumption(row model.AlipayRedemption, got billing.AlipayConsumption, err error, consumed bool) (store.AlipayOutcome, bool) {
	outcome := store.AlipayOutcome{ID: row.ID}
	if err != nil {
		code, status := billing.AlipayErrorCode(err)
		// A refused /consume took nothing: the arguments or the mode were
		// rejected before any consumption.
		if consumed && (status == 400 || (status == 409 && code == "CONFLICT")) {
			outcome.ReasonCode = truncate(code, 64)
			return outcome, true
		}
		if code == "IDEMPOTENCY_CONFLICT" || (status == 409 && !consumed) {
			slog.Error("一人付 reports a conflicting consumption", slog.String("redemption_id", row.ID), slog.String("code", code))
		} else {
			_ = alipayUnavailable("consume", err)
		}
		return outcome, false
	}
	outcome.ReasonCode, outcome.ConsumeOrderID = truncate(got.ReasonCode, 64), truncate(got.ConsumeOrderID, 128)
	switch got.Status {
	case billing.AlipayConsumeSucceeded:
		if !got.Confirmed {
			return outcome, false
		}
		if got.IdempotencyKey != row.ID || got.Consumed != row.Amount || (got.QuotaUnit != "" && got.QuotaUnit != row.QuotaUnit) {
			slog.Error("一人付 confirmed a consumption that does not match its redemption; left pending for an operator",
				slog.String("redemption_id", row.ID))
			return outcome, false
		}
		outcome.Succeeded = true
		return outcome, true
	case billing.AlipayConsumeFailed:
		return outcome, true
	default:
		return outcome, false
	}
}
