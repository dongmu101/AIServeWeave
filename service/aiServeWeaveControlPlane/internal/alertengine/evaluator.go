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
	values, err := DerivedSeries(rule.Metric, points, bucketAts)
	if err != nil {
		return err
	}
	// The leading bucket was only for diffing; the rule's own window is
	// the remaining ConsecutiveBuckets values.
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
