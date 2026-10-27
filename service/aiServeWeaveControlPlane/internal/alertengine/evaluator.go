// evaluator.go is STATUS.md's P09/C29 alert evaluation loop: on every tick
// it reads every enabled alert_rules row, computes its derived metric
// series (metrics.go) over the rule's ConsecutiveBuckets window, and
// transitions alert_instances between firing/acknowledged/resolved —
// notifying at most once per transition, never while an instance is
// already open (the deliberate de-bounce STATUS.md's design doc calls
// for).
//
// evaluator.go 是 STATUS.md P09/C29 的告警评估循环：每一轮读取全部已启用的
// alert_rules 行，在规则的 ConsecutiveBuckets 窗口内计算其派生指标序列
// (metrics.go)，并在 firing/acknowledged/resolved 之间迁移
// alert_instances——每次状态转换至多通知一次，实例已经处于打开状态时绝不
// 重复通知(STATUS.md 设计文档要求的刻意去抖动)。
package alertengine

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// bucketWidth is metrics_history's rollup grain (P08's Interval default).
// It is a package constant, not configurable here, because it must match
// the grain metrics_history was actually written at — a mismatch would
// silently misalign every bucket lookup in DerivedSeries.
//
// bucketWidth 是 metrics_history 的汇总粒度(P08 Interval 的默认值)。它是
// 包常量而不是这里的可配置项，因为它必须与 metrics_history 实际写入时的
// 粒度一致——不一致会让 DerivedSeries 里每一次分桶查找都悄悄错位。
const bucketWidth = 5 * time.Minute

// Store is the persistence surface the evaluator needs: reading enabled
// rules and raw rollup rows, and moving alert_instances through its
// lifecycle.
//
// Store 是评估循环所需的持久化接口：读取已启用规则与原始汇总行，并推进
// alert_instances 的生命周期。
type Store interface {
	ListEnabledAlertRules(ctx context.Context) ([]model.AlertRule, error)
	ListRollup(ctx context.Context, metrics []string, since, until time.Time) ([]model.MetricsHistoryPoint, error)
	GetOpenAlertInstance(ctx context.Context, ruleID string) (model.AlertInstance, bool, error)
	CreateAlertInstance(ctx context.Context, instance *model.AlertInstance) error
	TouchAlertInstance(ctx context.Context, id string, lastEvaluatedAt time.Time) error
	ResolveAlertInstance(ctx context.Context, id string, resolvedAt time.Time) error
	SetAlertInstanceNotifyResult(ctx context.Context, id, status string, attempts int) error
}

// Notifier delivers one webhook event for an alert instance's state
// transition. Implemented by webhook.go's Sender.
//
// Notifier 为一个告警实例的状态转换投递一次 Webhook 事件。由 webhook.go 的
// Sender 实现。
type Notifier interface {
	Notify(ctx context.Context, webhookURL string, payload WebhookPayload) (attempts int, err error)
}

// WebhookPayload is the JSON body a webhook delivery carries, per the
// design doc's fixed schema.
//
// WebhookPayload 是一次 Webhook 投递携带的 JSON 请求体，格式见设计文档。
type WebhookPayload struct {
	AlertID    string     `json:"alert_id"`
	RuleID     string     `json:"rule_id"`
	RuleName   string     `json:"rule_name"`
	Metric     string     `json:"metric"`
	Status     string     `json:"status"`
	Value      float64    `json:"value"`
	Threshold  float64    `json:"threshold"`
	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// rawMetricsFor names the raw metrics_history metric(s) DerivedSeries needs
// to compute one semantic metric — request_rate/success_rate both derive
// from the same raw counter, latency_p95 from the histogram, etc.
//
// rawMetricsFor 列出 DerivedSeries 计算某个语义指标所需的原始
// metrics_history 指标名——request_rate 与 success_rate 都从同一个原始
// 计数器派生，latency_p95 来自直方图，等等。
func rawMetricsFor(metric string) []string {
	switch metric {
	case model.MetricRequestRate, model.MetricSuccessRate:
		return []string{rawRequestsTotal}
	case model.MetricLatencyP95:
		return []string{rawDurationBucket}
	case model.MetricTokenUsage:
		return []string{rawTokensTotal}
	case model.MetricCapacity:
		return []string{rawCapacityGauge}
	default:
		return nil
	}
}

// Evaluator runs the alert evaluation loop.
//
// Evaluator 运行告警评估循环。
type Evaluator struct {
	store    Store
	notifier Notifier
	clock    runtime.Clock
	logger   *slog.Logger
}

// New builds an Evaluator. A nil clock defaults to the system clock; a nil
// logger discards.
//
// New 构造一个 Evaluator。clock 为 nil 时使用系统时钟；logger 为 nil 时
// 丢弃日志。
func New(store Store, notifier Notifier, clock runtime.Clock, logger *slog.Logger) *Evaluator {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Evaluator{store: store, notifier: notifier, clock: clock, logger: logger}
}

// RunOnce evaluates every enabled rule once.
//
// RunOnce 对每一条已启用的规则评估一次。
func (e *Evaluator) RunOnce(ctx context.Context) error {
	rules, err := e.store.ListEnabledAlertRules(ctx)
	if err != nil {
		return err
	}
	now := e.clock.Now().Truncate(bucketWidth)
	for _, rule := range rules {
		if err := e.evaluateRule(ctx, rule, now); err != nil {
			e.logger.Error("evaluating alert rule failed", slog.String("rule_id", rule.ID), slog.Any("error", err))
		}
	}
	return nil
}

func (e *Evaluator) evaluateRule(ctx context.Context, rule model.AlertRule, now time.Time) error {
	// ConsecutiveBuckets+1 timestamps: one extra leading bucket so
	// counter-based metrics have a predecessor to diff against for the
	// first requested value.
	bucketAts := make([]time.Time, rule.ConsecutiveBuckets+1)
	for i := range bucketAts {
		bucketAts[i] = now.Add(-time.Duration(rule.ConsecutiveBuckets-i) * bucketWidth)
	}
	since := bucketAts[0].Add(-bucketWidth)
	points, err := e.store.ListRollup(ctx, rawMetricsFor(rule.Metric), since, now.Add(bucketWidth))
	if err != nil {
		return err
	}
	// No raw metrics_history rows at all covering this window, AND the
	// rule is still within its grace period (one ConsecutiveBuckets-sized
	// window since it was created) — give a brand-new rule, or the first
	// bucketWidth after a fresh deployment, a chance to actually collect
	// something before judging it. This must be checked against the raw
	// points, not against DerivedSeries's output: every derivation helper
	// in metrics.go always returns exactly len(bucketAts) values (missing
	// buckets zero-fill via map lookups, they never shrink the slice), so
	// a zero-data window would otherwise silently read as
	// counterDelta/gaugeValue == 0 for every bucket — which breaches a
	// `lt` rule (e.g. "capacity < 3") on the very first evaluation, before
	// a single real metric has ever been collected.
	//
	// The grace period must expire, though: some raw series
	// (tunnel_server_slots_total, gateway_http_requests_total) are only
	// written when something happens — zero connected nodes or zero
	// traffic since boot means metrics_history stays empty not just for
	// one bucketWidth but indefinitely. If this guard applied forever, a
	// genuinely alarming "capacity has been zero this whole time" or
	// "request_rate has been zero this whole time" condition could never
	// fire, no matter how long it persisted — silently-never-fires, which
	// is worse than the original false-positive-on-creation bug this guard
	// exists to fix. So past the grace period, an empty raw series is no
	// longer given the benefit of the doubt: fall through to
	// DerivedSeries/allBreach as normal, where zero-filled buckets
	// legitimately breaching a `lt` rule is then the correct outcome.
	//
	// 窗口内完全没有原始 metrics_history 行，且该规则仍处于宽限期内(自
	// CreatedAt 起，一个 ConsecutiveBuckets 大小的窗口时长)——给刚创建的
	// 新规则，或部署后的第一个 bucketWidth，一个真正采集到数据的机会，再
	// 去评估它。这个判断必须基于原始 points，不能基于 DerivedSeries 的
	// 输出长度：metrics.go 里每一个派生函数都始终返回恰好 len(bucketAts)
	// 个值(缺失的桶通过 map 查找零值填充，从不会让切片变短)，否则一个
	// 完全没有数据的窗口会悄悄读成 counterDelta/gaugeValue 处处为 0——这会
	// 让一条 `lt` 规则(比如"capacity < 3")在还没采集到任何真实指标之前的
	// 第一次评估就被判定为触发。
	//
	// 但这个宽限期必须会过期：有些原始序列(tunnel_server_slots_total、
	// gateway_http_requests_total)只在发生了什么事情时才会写入——零个已
	// 连接节点或自启动以来零流量，会让 metrics_history 不只是一个
	// bucketWidth、而是无限期地保持为空。如果这道防线永远生效，一个真正
	// 值得告警的"capacity 一直是零"或"request_rate 一直是零"的情况就永远
	// 不会触发，不管它持续多久——这种"静默永不触发"比这道防线本来要修的
	// "创建时误触发"更糟。所以过了宽限期之后，空的原始序列不再享有这种
	// 豁免：照常走到 DerivedSeries/allBreach，零值填充的桶合理地触发一条
	// `lt` 规则，此时就是正确的结果。
	if len(points) == 0 && now.Sub(rule.CreatedAt) < time.Duration(rule.ConsecutiveBuckets)*bucketWidth {
		return nil
	}
	values, err := DerivedSeries(rule.Metric, points, bucketAts)
	if err != nil {
		return err
	}
	// The leading bucket was only for diffing; the rule's own window is
	// the remaining ConsecutiveBuckets values.
	//
	// This length check can never actually trigger: DerivedSeries always
	// returns len(bucketAts) values regardless of how sparse points is
	// (see the len(points) == 0 guard above, which is what actually
	// protects against missing data). Left in place as a harmless
	// defensive belt-and-suspenders check in case that invariant ever
	// changes.
	//
	// 这个长度检查实际上永远不会触发：不论 points 有多稀疏，
	// DerivedSeries 始终返回 len(bucketAts) 个值(真正防住数据缺失的是上面
	// 的 len(points) == 0 判断)。这里保留只是防御性的双重保险，以防这条
	// 不变式将来被打破。
	window := values[1:]
	if len(window) < rule.ConsecutiveBuckets {
		return nil // not enough history yet
	}

	breach := allBreach(window, rule.Operator, rule.Threshold)
	existing, hasOpen, err := e.store.GetOpenAlertInstance(ctx, rule.ID)
	if err != nil {
		return err
	}

	switch {
	case breach && !hasOpen:
		return e.fire(ctx, rule, window[len(window)-1], now)
	case breach && hasOpen:
		return e.store.TouchAlertInstance(ctx, existing.ID, now)
	case !breach && hasOpen:
		return e.resolve(ctx, rule, existing, now)
	default:
		return nil
	}
}

func allBreach(window []float64, operator string, threshold float64) bool {
	for _, v := range window {
		if !compare(v, operator, threshold) {
			return false
		}
	}
	return true
}

func compare(v float64, operator string, threshold float64) bool {
	switch operator {
	case model.OperatorLessThan:
		return v < threshold
	case model.OperatorLessThanOrEqual:
		return v <= threshold
	case model.OperatorGreaterThan:
		return v > threshold
	case model.OperatorGreaterThanOrEqual:
		return v >= threshold
	default:
		return false
	}
}

func (e *Evaluator) fire(ctx context.Context, rule model.AlertRule, valueAtFire float64, now time.Time) error {
	instance := model.AlertInstance{
		ID: model.NewID(model.PrefixAlertInstance), RuleID: rule.ID, Status: model.AlertStatusFiring,
		ValueAtFire: valueAtFire, CreatedAt: now, LastEvaluatedAt: now, NotifyStatus: model.NotifyStatusPending,
	}
	if err := e.store.CreateAlertInstance(ctx, &instance); err != nil {
		return err
	}
	e.deliver(ctx, rule, instance, model.AlertStatusFiring, valueAtFire, now, nil)
	return nil
}

func (e *Evaluator) resolve(ctx context.Context, rule model.AlertRule, instance model.AlertInstance, now time.Time) error {
	if err := e.store.ResolveAlertInstance(ctx, instance.ID, now); err != nil {
		return err
	}
	e.deliver(ctx, rule, instance, model.AlertStatusResolved, instance.ValueAtFire, instance.CreatedAt, &now)
	return nil
}

// deliver sends (or skips) the webhook for one state transition and
// records the outcome. It never returns an error — a notify failure must
// not undo the state transition that already committed; see webhook.go's
// own doc comment for the bounded-retry, no-persistent-queue contract this
// call sits on top of.
//
// deliver 为一次状态转换发送(或跳过)Webhook 并记录结果。它从不返回错误——
// 一次通知失败不能撤销已经提交的状态转换；这次调用所依赖的"有界重试、不做
// 持久队列"契约见 webhook.go 自己的文档注释。
func (e *Evaluator) deliver(ctx context.Context, rule model.AlertRule, instance model.AlertInstance, status string, value float64, firedAt time.Time, resolvedAt *time.Time) {
	if rule.WebhookURL == "" {
		_ = e.store.SetAlertInstanceNotifyResult(ctx, instance.ID, model.NotifyStatusSkipped, 0)
		return
	}
	payload := WebhookPayload{
		AlertID: instance.ID, RuleID: rule.ID, RuleName: rule.Name, Metric: rule.Metric,
		Status: status, Value: value, Threshold: rule.Threshold, FiredAt: firedAt, ResolvedAt: resolvedAt,
	}
	attempts, err := e.notifier.Notify(ctx, rule.WebhookURL, payload)
	notifyStatus := model.NotifyStatusSent
	if err != nil {
		notifyStatus = model.NotifyStatusFailed
		e.logger.Warn("alert webhook delivery failed", slog.String("alert_id", instance.ID), slog.Any("error", err))
	}
	_ = e.store.SetAlertInstanceNotifyResult(ctx, instance.ID, notifyStatus, attempts)
}

// Run calls RunOnce once per interval until ctx is done, matching
// metricshistory.Retention.Run's real-ticker shape — only RunOnce is
// clock-injected for testing, per this package's established convention.
//
// Run 每隔 interval 调用一次 RunOnce，直到 ctx 结束，形状与
// metricshistory.Retention.Run 的真实定时器一致——按本仓库既有约定，只有
// RunOnce 注入时钟以供测试。
func (e *Evaluator) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.RunOnce(ctx); err != nil {
				e.logger.Error("alert evaluation run failed", slog.Any("error", err))
			}
		}
	}
}
