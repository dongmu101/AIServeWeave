// ezfp.go takes CNY payments through 易支付 (EZFP) for the checkouts Dodo
// also serves: credit packages, monthly accounts and presales. An order is
// settled only by a verified notification or a verified query, both reduced
// to the same store.BillingEvent a Dodo webhook becomes, so crediting,
// granting, dedup and cache invalidation are the ones Dodo already uses. The
// browser's return from EZFP settles nothing.
//
// ezfp.go 经易支付（EZFP）为 Dodo 同样服务的结账收取人民币付款：充值包、包月账号与预售。
// 订单只由经验签的通知或经验签的查询结算，两者都归约为与 Dodo webhook 相同的
// store.BillingEvent，因此入账、开通、去重与缓存失效都沿用 Dodo 已有的那一套。浏览器从
// 易支付回跳不结算任何东西。
package logic

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// EzfpConfig is 易支付: the client, the payment types tenants may choose, and
// the two addresses it is given — NotifyURL, this control plane's public
// notification route, and ReturnURL, the Console billing page.
//
// EzfpConfig 是易支付：客户端、租户可选的付款方式，以及交给它的两个地址——NotifyURL
// 是本控制面公开的通知路由，ReturnURL 是 Console 计费页。
type EzfpConfig struct {
	Client    billing.Ezfp
	Types     []string
	NotifyURL string
	ReturnURL string
}

// Payment is how a tenant chose to pay a checkout. The zero value is Dodo,
// what every checkout was before a second provider.
//
// Payment 是租户为一次结账选择的付款方式。零值即 Dodo——第二个服务商出现之前所有结账
// 都是如此。
type Payment struct {
	Provider string
	PayType  string
}

const (
	// ezfpQueryGap is the least time between two queries of one order, so
	// repeated clicks cannot spend EZFP's patience.
	//
	// ezfpQueryGap 是同一订单两次查询之间的最短间隔，使反复点击不会耗尽易支付的耐心。
	ezfpQueryGap = 5 * time.Second
	// ezfpRecoveryMinAge and ezfpRecoveryMaxAge bound the pending orders
	// recovery queries: old enough that the notification had its chance,
	// young enough that a payment page is still open.
	//
	// ezfpRecoveryMinAge 与 ezfpRecoveryMaxAge 限定恢复所查询的待付款订单：足够旧，通知
	// 已有机会到达；足够新，付款页仍可能开着。
	ezfpRecoveryMinAge = 30 * time.Second
	ezfpRecoveryMaxAge = 2 * time.Hour
	// ezfpRecoveryBatch bounds one pass, per kind.
	//
	// ezfpRecoveryBatch 限制一轮恢复每类处理的数量。
	ezfpRecoveryBatch = 20
	// ezfpCallTimeout bounds one EZFP call made outside a request.
	//
	// ezfpCallTimeout 限制一次在请求之外发起的易支付调用。
	ezfpCallTimeout = 8 * time.Second
	// ezfpLocalIP stands in when the request's address is unknown; EZFP
	// requires one.
	//
	// ezfpLocalIP 在请求地址未知时代用；易支付要求必须提供。
	ezfpLocalIP = "127.0.0.1"
)

// attemptGate remembers recent calls per id so one is made at most once per
// gap.
//
// attemptGate 记住每个 id 最近的调用，使每个 gap 内至多调用一次。
type attemptGate struct {
	gap time.Duration
	mu  sync.Mutex
	at  map[string]time.Time
}

// allow records a call for id at now and reports whether the previous one
// was at least gap ago.
//
// allow 记录 id 在 now 的一次调用，并报告上一次调用是否至少在 gap 之前。
func (g *attemptGate) allow(id string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.at == nil {
		g.at = map[string]time.Time{}
	}
	for key, at := range g.at {
		if now.Sub(at) >= g.gap {
			delete(g.at, key)
		}
	}
	if _, recent := g.at[id]; recent {
		return false
	}
	g.at[id] = now
	return true
}

// ezfpTypes is the payment types tenants may choose, nil when 易支付 is off.
//
// ezfpTypes 是租户可选的付款方式；易支付关闭时为 nil。
func (s *Service) ezfpTypes() []string {
	if !s.ezfpEnabled() {
		return nil
	}
	return append([]string(nil), s.billing.Ezfp.Types...)
}

// resolvePayment checks a tenant's choice and returns its provider. A
// provider that is not configured is ErrNotFound, as its routes would be; a
// choice that names no known provider or type is ErrInvalidInput.
//
// resolvePayment 校验租户的选择并返回其服务商。未配置的服务商返回 ErrNotFound，与其
// 路由不存在一致；指明未知服务商或付款方式的选择返回 ErrInvalidInput。
func (s *Service) resolvePayment(pay Payment) (string, error) {
	switch pay.Provider {
	case "", model.PaymentProviderDodo:
		if pay.PayType != "" {
			return "", ErrInvalidInput
		}
		if !s.dodoEnabled() {
			return "", ErrNotFound
		}
		return model.PaymentProviderDodo, nil
	case model.PaymentProviderEzfp:
		if !slices.Contains(model.EzfpPayTypes, pay.PayType) {
			return "", ErrInvalidInput
		}
		if !s.ezfpEnabled() {
			return "", ErrNotFound
		}
		if !slices.Contains(s.billing.Ezfp.Types, pay.PayType) {
			return "", ErrInvalidInput
		}
		return model.PaymentProviderEzfp, nil
	default:
		return "", ErrInvalidInput
	}
}

// openEzfpCheckout opens the 易支付 payment for order, which the caller has
// already stored as pending, and records its trade on the order. A failed
// call marks the order failed, releasing any account hold or presale place:
// the tenant never got a page to pay on. The tenant id rides as param, a
// cross-check on the notification.
//
// openEzfpCheckout 为调用方已存为待付款的 order 发起易支付付款，并把交易记在订单上。
// 调用失败时把订单标为失败，释放其账号占用或预售名额：租户从未拿到付款页面。租户 id 作为
// param 随付款传出，用于核对通知。
func (s *Service) openEzfpCheckout(ctx context.Context, actor Actor, order model.CheckoutOrder, name string) (CheckoutResult, error) {
	clientIP := actor.IP
	if clientIP == "" {
		clientIP = ezfpLocalIP
	}
	opened, err := s.billing.Ezfp.Client.CreateOrder(ctx, billing.EzfpOrderRequest{
		OutTradeNo: order.ID, PayType: order.PayType, Name: name, MoneyMinor: order.AmountMinor,
		ClientIP: clientIP, Param: order.TenantID, NotifyURL: s.billing.Ezfp.NotifyURL, ReturnURL: s.billing.Ezfp.ReturnURL,
	})
	if err != nil {
		var refused *billing.EzfpError
		code, msg := 0, ""
		if errors.As(err, &refused) {
			code, msg = refused.Code, refused.Msg
		}
		// The client's errors carry no URL or key, only the cause: a timeout,
		// an HTTP status, an unverified answer. msg is EZFP's reason for a
		// refusal, written for the merchant, and is what says why code is
		// non-zero.
		slog.Error("opening an 易支付 payment failed", slog.String("order_id", order.ID), slog.Int("code", code),
			slog.String("msg", msg), slog.String("error", err.Error()))
		if failErr := s.store.FailCheckoutOrder(context.WithoutCancel(ctx), order.ID, s.clock.Now()); failErr != nil {
			slog.Error("releasing a failed 易支付 checkout failed", slog.String("order_id", order.ID))
		}
		return CheckoutResult{}, ErrUnavailable
	}
	if err := s.store.SetCheckoutOrderSession(ctx, order.ID, model.EzfpIDPrefix+opened.TradeNo, s.clock.Now()); err != nil {
		return CheckoutResult{}, translate(err)
	}
	return CheckoutResult{OrderID: order.ID, CheckoutURL: opened.PayURL}, nil
}

// HandleEzfpNotify verifies and applies an 易支付 notification. It returns an
// error only when the notification must not be acknowledged: a bad
// signature or a store failure. Every other result — a payment for an order
// this service does not know, or that does not match — is final. The
// notification's age is not checked, since settling is idempotent and a
// retry that kept its first timestamp must not be lost; an old one is only
// logged. Logs carry ids and outcomes, never the buyer.
//
// HandleEzfpNotify 校验并应用一个易支付通知。只有在通知不应被确认时才返回错误：签名
// 错误或 store 故障。其他所有结果——本服务不认识或不相符的订单的付款——都是最终结果。不检查
// 通知的新旧：结算是幂等的，保留了首次时间戳的重投不能丢失；过旧的通知只记日志。日志只带
// id 与结果，从不带付款人。
func (s *Service) HandleEzfpNotify(ctx context.Context, params url.Values) (store.BillingEventResult, error) {
	if !s.ezfpEnabled() {
		return store.BillingEventResult{}, ErrNotFound
	}
	payment, err := s.billing.Ezfp.Client.ParseNotify(params)
	if err != nil {
		slog.Warn("an 易支付 notification failed signature verification")
		return store.BillingEventResult{}, ErrInvalidSignature
	}
	if age := s.clock.Now().Sub(time.Unix(payment.Timestamp, 0)); age > 10*time.Minute || age < -10*time.Minute {
		slog.Warn("an 易支付 notification carries a timestamp far from this server's clock; check time sync",
			slog.String("order_id", payment.OutTradeNo), slog.Duration("age", age.Round(time.Second)))
	}
	if payment.Status != billing.EzfpStatusPaid {
		slog.Info("an 易支付 notification for a trade that has not succeeded was ignored", slog.String("order_id", payment.OutTradeNo))
		return store.BillingEventResult{Outcome: store.BillingOutcomeIgnored}, nil
	}
	return s.settleEzfpPayment(ctx, payment, "notify")
}

// settleEzfpPayment applies a verified paid trade to its order. The tenant
// is the order's own; param, when present, must agree with it.
//
// settleEzfpPayment 把一笔经验签的已付交易应用到其订单。租户取自订单本身；param 存在时
// 必须与之一致。
func (s *Service) settleEzfpPayment(ctx context.Context, payment billing.EzfpPayment, source string) (store.BillingEventResult, error) {
	order, err := s.store.GetCheckoutOrder(ctx, payment.OutTradeNo)
	if errors.Is(err, store.ErrNotFound) {
		slog.Warn("an 易支付 payment names no known order", slog.String("source", source))
		return store.BillingEventResult{Outcome: store.BillingOutcomeNoOrder}, nil
	}
	if err != nil {
		return store.BillingEventResult{}, err
	}
	if order.PaymentProvider() != model.PaymentProviderEzfp || (payment.Param != "" && payment.Param != order.TenantID) {
		slog.Error("an 易支付 payment does not match its order; an operator must check it",
			slog.String("source", source), slog.String("order_id", order.ID), slog.String("tenant_id", order.TenantID))
		return store.BillingEventResult{Outcome: store.BillingOutcomeMismatch, OrderID: order.ID, TenantID: order.TenantID}, nil
	}
	trade := model.EzfpIDPrefix + payment.TradeNo
	event := store.BillingEvent{
		WebhookID: "ezfp:pay:" + payment.TradeNo, Type: "ezfp.payment", Kind: store.BillingEventPaymentSucceeded,
		Provider: model.PaymentProviderEzfp, OrderID: order.ID, TenantID: order.TenantID,
		PaymentID: trade, CheckoutSessionID: trade, AmountMinor: payment.MoneyMinor, Currency: model.EzfpCurrency,
	}
	return s.applyEzfpEvent(ctx, event, source)
}

// applyEzfpEvent applies a synthesized event as a webhook's would be.
//
// applyEzfpEvent 像应用 webhook 事件一样应用一个合成的事件。
func (s *Service) applyEzfpEvent(ctx context.Context, event store.BillingEvent, source string) (store.BillingEventResult, error) {
	result, err := s.store.ApplyBillingEvent(ctx, event, s.clock.Now())
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
		level = slog.LevelError
	}
	slog.Log(ctx, level, "易支付 event processed", slog.String("type", event.Type), slog.String("source", source),
		slog.String("outcome", result.Outcome), slog.String("order_id", result.OrderID), slog.String("tenant_id", result.TenantID))
	return result, nil
}

// RefreshEzfpOrder asks 易支付 about one of the tenant's own pending 易支付
// orders and settles it if it was paid, for a tenant back from paying whose
// notification has not arrived. It answers with the order as it stands; a
// call too soon after the previous one for the same order, or a query that
// fails, simply leaves it as it was. Any role may ask, since it only reads
// what 易支付 already decided.
//
// RefreshEzfpOrder 就租户自己的一个待付款易支付订单询问易支付，已付款时结算它，供付款
// 归来而通知尚未到达的租户使用。答复订单的当前状态；同一订单距上一次调用太近，或查询失败
// 时，订单保持原样。任何角色都可调用，因为它只读取易支付已作出的结论。
func (s *Service) RefreshEzfpOrder(ctx context.Context, actor Actor, orderID string) (model.CheckoutOrder, error) {
	if !s.ezfpEnabled() {
		return model.CheckoutOrder{}, ErrNotFound
	}
	if actor.TenantID == "" || isPlatformActor(actor) {
		return model.CheckoutOrder{}, ErrForbidden
	}
	order, err := s.store.GetCheckoutOrder(ctx, orderID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (order.TenantID != actor.TenantID || order.PaymentProvider() != model.PaymentProviderEzfp)) {
		return model.CheckoutOrder{}, ErrNotFound
	}
	if err != nil {
		return model.CheckoutOrder{}, err
	}
	if order.Status != model.OrderStatusPending || !s.ezfpAttempts.allow(order.ID, s.clock.Now()) {
		return order, nil
	}
	if err := s.queryEzfpOrder(ctx, order, "refresh"); err != nil {
		return order, nil
	}
	fresh, err := s.store.GetCheckoutOrder(ctx, orderID)
	if err != nil {
		return order, nil
	}
	return fresh, nil
}

// queryEzfpOrder reads a pending order's trade from 易支付 and settles it if
// paid. A trade 易支付 reports refunded or frozen is left for an operator.
//
// queryEzfpOrder 从易支付读取一个待付款订单的交易，已付款时结算它。易支付报告已退款或
// 已冻结的交易留给运维处理。
func (s *Service) queryEzfpOrder(ctx context.Context, order model.CheckoutOrder, source string) error {
	payment, err := s.billing.Ezfp.Client.QueryOrder(ctx, order.ID)
	if err != nil {
		var refused *billing.EzfpError
		if !errors.As(err, &refused) {
			slog.Warn("querying an 易支付 order failed", slog.String("source", source), slog.String("order_id", order.ID))
		}
		return err
	}
	switch payment.Status {
	case billing.EzfpStatusPaid:
		_, err := s.settleEzfpPayment(ctx, payment, source)
		return err
	case billing.EzfpStatusUnpaid:
		return nil
	default:
		slog.Error("易支付 reports a pending order's trade neither paid nor unpaid; an operator must check it",
			slog.String("source", source), slog.String("order_id", order.ID), slog.Int("status", payment.Status))
		return nil
	}
}

// submitEzfpRefund asks 易支付 for an approved refund under the request's own
// id, and settles it at once when 易支付 answers that it went through. A
// refusal fails the request and returns its credits; a lost answer leaves it
// processing for recovery's refund query or an operator.
//
// submitEzfpRefund 以退款申请自己的 id 向易支付发起一笔已批准的退款，易支付答复退款完成
// 时立即结算。被拒绝时把申请标为失败并退回点数；答复丢失时保持 processing，交由恢复流程的
// 退款查询或运维处理。
func (s *Service) submitEzfpRefund(ctx context.Context, req model.RefundRequest, order model.CheckoutOrder) (model.RefundRequest, error) {
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), refundRecordTimeout)
	defer cancel()
	tradeNo, ok := ezfpTradeNo(order)
	if !s.ezfpEnabled() || !ok || !strings.EqualFold(req.Currency, model.EzfpCurrency) {
		return s.failSubmittedRefund(record, req, refundFailureNoTarget)
	}
	refund, err := s.billing.Ezfp.Client.Refund(ctx, billing.EzfpRefundRequest{
		OutRefundNo: req.ID, TradeNo: tradeNo, MoneyMinor: req.AmountMinor,
	})
	switch {
	case errors.Is(err, billing.ErrRefundRejected):
		slog.Error("易支付 refused a refund", slog.String("refund_request_id", req.ID), slog.String("order_id", order.ID),
			slog.String("error", err.Error()))
		return s.failSubmittedRefund(record, req, refundFailureRejected)
	case err != nil:
		slog.Error("an 易支付 refund's answer was lost; it stays processing until recovery's query or an operator settles it",
			slog.String("refund_request_id", req.ID), slog.String("order_id", order.ID))
		return req, nil
	}
	if err := s.settleEzfpRefund(record, req, order, refund, "refund"); err != nil {
		slog.Error("settling an 易支付 refund failed; recovery retries it", slog.String("refund_request_id", req.ID))
	}
	if fresh, err := s.store.GetRefundRequest(record, req.ID); err == nil {
		return fresh, nil
	}
	return req, nil
}

// ezfpTradeNo is the 易支付 trade a paid order's payment id names.
//
// ezfpTradeNo 是已付款订单的付款 id 所指的易支付交易号。
func ezfpTradeNo(order model.CheckoutOrder) (string, bool) {
	if order.DodoPaymentID == nil || !strings.HasPrefix(*order.DodoPaymentID, model.EzfpIDPrefix) {
		return "", false
	}
	trade := strings.TrimPrefix(*order.DodoPaymentID, model.EzfpIDPrefix)
	return trade, trade != ""
}

// settleEzfpRefund records the refund 易支付 made on the request and applies
// it as the refund event a webhook would carry, linked to the request so
// its held credits are what moves.
//
// settleEzfpRefund 把易支付完成的退款记在申请上，并像 webhook 携带的退款事件一样应用它，
// 关联到该申请，使变动的正是其冻结的点数。
func (s *Service) settleEzfpRefund(ctx context.Context, req model.RefundRequest, order model.CheckoutOrder, refund billing.EzfpRefund, source string) error {
	refundID := model.EzfpIDPrefix + refund.RefundNo
	if err := s.store.RecordRefundSubmitted(ctx, req.ID, refundID, s.clock.Now()); err != nil && !errors.Is(err, store.ErrConflict) {
		slog.Error("recording an 易支付 refund failed", slog.String("refund_request_id", req.ID))
	}
	amount := refund.MoneyMinor
	if amount <= 0 {
		amount = req.AmountMinor
	}
	_, err := s.applyEzfpEvent(ctx, store.BillingEvent{
		WebhookID: "ezfp:refund:" + refund.RefundNo, Type: "ezfp.refund", Kind: store.BillingEventRefundSucceeded,
		Provider: model.PaymentProviderEzfp, OrderID: order.ID, TenantID: order.TenantID, PaymentID: *order.DodoPaymentID,
		RefundID: refundID, RefundRequestID: req.ID, AmountMinor: amount, Currency: model.EzfpCurrency,
		IsPartial: amount < order.RemainingRefundMinor(),
	}, source)
	return err
}

// RunEzfpRecovery settles, every interval, the 易支付 payments and refunds
// whose answer never arrived: a notification that could not reach this
// control plane, a tenant who closed the page, a refund answer cut off.
//
// RunEzfpRecovery 每隔 interval 结算一次答复始终未到的易支付付款与退款：无法到达本控制面
// 的通知、关掉页面的租户、被截断的退款答复。
func (s *Service) RunEzfpRecovery(ctx context.Context, interval time.Duration) {
	if !s.ezfpEnabled() || interval <= 0 {
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
		s.RecoverEzfp(ctx)
	}
}

// RecoverEzfp runs one bounded pass: pending 易支付 orders between
// ezfpRecoveryMinAge and ezfpRecoveryMaxAge old are queried and settled if
// paid; processing refunds on 易支付 orders are queried and settled as
// 易支付 reports them. A refund 易支付 has no record of is left for an
// operator: resubmitting it could not tell a refund already made from one
// never received.
//
// RecoverEzfp 执行一轮有上限的恢复：查询创建时间在 ezfpRecoveryMinAge 到
// ezfpRecoveryMaxAge 之间的待付款易支付订单，已付款则结算；查询易支付订单上处理中的退款，
// 并按易支付报告的结果结算。易支付没有记录的退款留给运维：重新提交无法分辨退款是已经完成
// 还是从未收到。
func (s *Service) RecoverEzfp(ctx context.Context) {
	if !s.ezfpEnabled() {
		return
	}
	now := s.clock.Now()
	page, err := s.store.ListCheckoutOrders(ctx, store.ListQuery{Limit: ezfpRecoveryBatch}, store.CheckoutOrderFilter{
		Provider: model.PaymentProviderEzfp, Status: model.OrderStatusPending,
		CreatedAfter: now.Add(-ezfpRecoveryMaxAge), CreatedBefore: now.Add(-ezfpRecoveryMinAge),
	})
	if err != nil {
		slog.Error("listing pending 易支付 orders failed")
	} else {
		for _, order := range page.Items {
			if ctx.Err() != nil {
				return
			}
			if !s.ezfpAttempts.allow(order.ID, s.clock.Now()) {
				continue
			}
			opCtx, cancel := context.WithTimeout(ctx, ezfpCallTimeout)
			_ = s.queryEzfpOrder(opCtx, order, "recovery")
			cancel()
		}
	}
	s.recoverEzfpRefunds(ctx)
}

// recoverEzfpRefunds settles a bounded batch of processing refunds on
// 易支付 orders, as 易支付's refund query reports them.
//
// recoverEzfpRefunds 按易支付退款查询的报告，结算一批有上限的、易支付订单上处理中的退款。
func (s *Service) recoverEzfpRefunds(ctx context.Context) {
	query := store.ListQuery{Limit: 50}
	checked := 0
	for range 5 {
		page, err := s.store.ListRefundRequests(ctx, query, store.RefundRequestFilter{Status: model.RefundStatusProcessing})
		if err != nil {
			slog.Error("listing processing refunds failed")
			return
		}
		for _, req := range page.Items {
			if ctx.Err() != nil || checked >= ezfpRecoveryBatch {
				return
			}
			if s.clock.Now().Sub(req.UpdatedAt) < ezfpRecoveryMinAge {
				continue
			}
			order, err := s.store.GetCheckoutOrder(ctx, req.OrderID)
			if err != nil || order.PaymentProvider() != model.PaymentProviderEzfp {
				continue
			}
			checked++
			opCtx, cancel := context.WithTimeout(ctx, ezfpCallTimeout)
			s.recoverEzfpRefund(opCtx, req, order)
			cancel()
		}
		if page.NextCursor == "" {
			return
		}
		query.Cursor = page.NextCursor
	}
}

func (s *Service) recoverEzfpRefund(ctx context.Context, req model.RefundRequest, order model.CheckoutOrder) {
	refund, err := s.billing.Ezfp.Client.QueryRefund(ctx, req.ID)
	if err != nil {
		slog.Warn("querying an 易支付 refund failed; it stays processing for an operator if 易支付 has no record of it",
			slog.String("refund_request_id", req.ID))
		return
	}
	switch refund.Status {
	case billing.EzfpRefundSucceeded:
		if err := s.settleEzfpRefund(ctx, req, order, refund, "recovery"); err != nil {
			slog.Error("settling a recovered 易支付 refund failed", slog.String("refund_request_id", req.ID))
		}
	case billing.EzfpRefundFailed:
		if _, err := s.failSubmittedRefund(ctx, req, refundFailureReported); err != nil {
			slog.Error("failing a recovered 易支付 refund failed", slog.String("refund_request_id", req.ID))
		}
	}
}
