package alertengine_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/alertengine"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// fakeClock is a minimal runtime.Clock fixed at a known instant. RunOnce
// only ever calls Now(), but webhook_test.go (same package) also drives
// the Sender's retry loop, so NewTimer returns an already-fired channel
// instead of actually waiting — retries proceed immediately under test.
// Mirrors metricshistory/retention_test.go's local fakeClock (no shared
// fake-clock package exists in this repo).
//
// fakeClock 是一个固定在已知时刻的最小 runtime.Clock。RunOnce 只调用
// Now()，但 webhook_test.go(同一个包)还要驱动 Sender 的重试循环，因此
// NewTimer 返回一个已经触发的 channel 而不是真的等待——测试中重试立即
// 继续。与 metricshistory/retention_test.go 的本地 fakeClock 一致(本仓库
// 没有共享的 fake-clock 包)。
type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

// NewTimer returns a channel that has already fired and a no-op stop
// func, so callers waiting on it under test proceed without a real delay.
//
// NewTimer 返回一个已经触发的 channel 和一个空操作的 stop 函数，测试中
// 等待它的调用方无需真正延迟即可继续。
func (c fakeClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch, func() bool { return false }
}

const bucketWidth = 5 * time.Minute

// fakeStore is an in-memory alertengine.Store for tests.
//
// fakeStore 是测试用的内存版 alertengine.Store。
type fakeStore struct {
	rules     []model.AlertRule
	points    []model.MetricsHistoryPoint
	instances map[string]*model.AlertInstance // keyed by instance ID

	createCalls  int
	touchCalls   int
	resolveCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{instances: map[string]*model.AlertInstance{}}
}

func (f *fakeStore) ListEnabledAlertRules(ctx context.Context) ([]model.AlertRule, error) {
	return f.rules, nil
}

func (f *fakeStore) ListRollup(ctx context.Context, metrics []string, since, until time.Time) ([]model.MetricsHistoryPoint, error) {
	var out []model.MetricsHistoryPoint
	for _, p := range f.points {
		if p.BucketAt.Before(since) || !p.BucketAt.Before(until) {
			continue
		}
		for _, m := range metrics {
			if p.Metric == m {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}

func (f *fakeStore) GetOpenAlertInstance(ctx context.Context, ruleID string) (model.AlertInstance, bool, error) {
	for _, inst := range f.instances {
		if inst.RuleID == ruleID && inst.Status != model.AlertStatusResolved {
			return *inst, true, nil
		}
	}
	return model.AlertInstance{}, false, nil
}

func (f *fakeStore) CreateAlertInstance(ctx context.Context, instance *model.AlertInstance) error {
	f.createCalls++
	cp := *instance
	f.instances[instance.ID] = &cp
	return nil
}

func (f *fakeStore) TouchAlertInstance(ctx context.Context, id string, lastEvaluatedAt time.Time) error {
	f.touchCalls++
	if inst, ok := f.instances[id]; ok {
		inst.LastEvaluatedAt = lastEvaluatedAt
	}
	return nil
}

func (f *fakeStore) ResolveAlertInstance(ctx context.Context, id string, resolvedAt time.Time) error {
	f.resolveCalls++
	if inst, ok := f.instances[id]; ok {
		inst.Status = model.AlertStatusResolved
		inst.ResolvedAt = &resolvedAt
	}
	return nil
}

func (f *fakeStore) SetAlertInstanceNotifyResult(ctx context.Context, id, status string, attempts int) error {
	if inst, ok := f.instances[id]; ok {
		inst.NotifyStatus = status
		inst.NotifyAttempts = attempts
	}
	return nil
}

// fakeNotifier is an in-memory alertengine.Notifier for tests.
//
// fakeNotifier 是测试用的内存版 alertengine.Notifier。
type fakeNotifier struct {
	calls []alertengine.WebhookPayload
	err   error
}

func (f *fakeNotifier) Notify(ctx context.Context, webhookURL string, payload alertengine.WebhookPayload) (int, error) {
	f.calls = append(f.calls, payload)
	if f.err != nil {
		return 1, f.err
	}
	return 1, nil
}

// seedRequestRatePoints writes gateway_http_requests_total rows at every
// 5-minute bucket from start to end (inclusive), split across "200" and
// "500" status labels so successRate/counterDelta have something to diff,
// with rate rows chosen so counterDelta(rate) == perBucketDelta at every
// bucket after the first.
//
// seedRequestRatePoints 在 start 到 end(含)之间的每个 5 分钟桶写入
// gateway_http_requests_total 行，按 "200"/"500" 状态标签拆分，使
// counterDelta(rate) 在首个桶之后的每个桶都等于 perBucketDelta。
func seedRequestRatePoints(start, end time.Time, perBucketDelta float64) []model.MetricsHistoryPoint {
	var out []model.MetricsHistoryPoint
	cumulative := 0.0
	for at := start; !at.After(end); at = at.Add(bucketWidth) {
		cumulative += perBucketDelta
		out = append(out, model.MetricsHistoryPoint{
			Metric: "gateway_http_requests_total", Labels: "status=200", BucketAt: at, Value: cumulative,
		})
	}
	return out
}

func testRule(threshold float64, consecutive int) model.AlertRule {
	return model.AlertRule{
		ID: "alr_test", Name: "test rule", Metric: model.MetricRequestRate,
		Operator: model.OperatorGreaterThan, Threshold: threshold,
		ConsecutiveBuckets: consecutive, WebhookURL: "https://example.invalid/hook", Enabled: true,
	}
}

func TestEvaluatorFiresNewInstanceAndNotifiesOnce(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC).Truncate(bucketWidth)
	clock := fakeClock{now: now}
	rule := testRule(10, 3)
	// Need ConsecutiveBuckets+1 = 4 raw boundary buckets plus one more
	// leading bucket for the very first diff: seed from now-4*bucketWidth
	// to now, with per-bucket delta of 20 (> threshold 10) throughout.
	store := newFakeStore()
	store.rules = []model.AlertRule{rule}
	store.points = seedRequestRatePoints(now.Add(-5*bucketWidth), now, 20)
	notifier := &fakeNotifier{}

	e := alertengine.New(store, notifier, clock, nil)
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if store.createCalls != 1 {
		t.Fatalf("CreateAlertInstance called %d times, want 1", store.createCalls)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("Notify called %d times, want 1", len(notifier.calls))
	}
	if notifier.calls[0].Status != model.AlertStatusFiring {
		t.Errorf("Notify payload status = %q, want %q", notifier.calls[0].Status, model.AlertStatusFiring)
	}
}

func TestEvaluatorDebouncesWhileStillBreachingAndOpen(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC).Truncate(bucketWidth)
	clock := fakeClock{now: now}
	rule := testRule(10, 3)
	store := newFakeStore()
	store.rules = []model.AlertRule{rule}
	store.points = seedRequestRatePoints(now.Add(-5*bucketWidth), now, 20)
	notifier := &fakeNotifier{}

	e := alertengine.New(store, notifier, clock, nil)
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	if store.createCalls != 1 || len(notifier.calls) != 1 {
		t.Fatalf("after first RunOnce: createCalls=%d notifyCalls=%d, want 1,1", store.createCalls, len(notifier.calls))
	}

	// Second evaluation, still breaching, instance still open.
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}

	if store.createCalls != 1 {
		t.Errorf("CreateAlertInstance called %d times across two RunOnce, want 1 (no second instance)", store.createCalls)
	}
	if len(notifier.calls) != 1 {
		t.Errorf("Notify called %d times across two RunOnce, want 1 (no second notify)", len(notifier.calls))
	}
	if store.touchCalls != 1 {
		t.Errorf("TouchAlertInstance called %d times, want 1", store.touchCalls)
	}
}

func TestEvaluatorResolvesWhenConditionClears(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC).Truncate(bucketWidth)
	clock := fakeClock{now: now}
	rule := testRule(10, 3)
	store := newFakeStore()
	store.rules = []model.AlertRule{rule}
	store.points = seedRequestRatePoints(now.Add(-5*bucketWidth), now, 20)
	notifier := &fakeNotifier{}

	e := alertengine.New(store, notifier, clock, nil)
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	if store.createCalls != 1 {
		t.Fatalf("expected instance to have fired, createCalls = %d", store.createCalls)
	}

	// Advance the clock one bucket and reseed with a delta below threshold
	// throughout the new window, so the condition clears.
	now2 := now.Add(bucketWidth)
	clock2 := fakeClock{now: now2}
	store.points = seedRequestRatePoints(now2.Add(-5*bucketWidth), now2, 1)
	e2 := alertengine.New(store, notifier, clock2, nil)
	if err := e2.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}

	if store.resolveCalls != 1 {
		t.Fatalf("ResolveAlertInstance called %d times, want 1", store.resolveCalls)
	}
	if len(notifier.calls) != 2 {
		t.Fatalf("Notify called %d times, want 2 (fire + resolve)", len(notifier.calls))
	}
	if notifier.calls[1].Status != model.AlertStatusResolved {
		t.Errorf("second Notify payload status = %q, want %q", notifier.calls[1].Status, model.AlertStatusResolved)
	}
}

// TestEvaluatorSparseDataDoesNotSpuriouslyBreachAGreaterThanRule covers a
// rule whose window has some, but not enough, raw data: with an
// OperatorGreaterThan rule, the missing buckets zero-fill and 0 is not
// greater than the threshold, so it correctly never breaches. This does
// NOT exercise the "no data at all" guard (see
// TestEvaluatorSkipsWhenNoRawDataYet for that) — it only shows a
// `gt`-operator rule happens not to false-positive on sparse data, which is
// why the original version of this test (misleadingly named
// "...InsufficientHistory") did not catch the `lt`-operator false-firing
// bug a review later found.
//
// TestEvaluatorSparseDataDoesNotSpuriouslyBreachAGreaterThanRule 覆盖一条
// 窗口内数据不完整(但不是完全没有)的规则：对于 OperatorGreaterThan 规则，
// 缺失的桶会被填成 0，而 0 不大于阈值，所以正确地不会触发。这个测试
// 并不覆盖"完全没有数据"的场景(那个场景见
// TestEvaluatorSkipsWhenNoRawDataYet)——它只是说明 `gt` 运算符的规则碰巧
// 不会在稀疏数据上误报，这正是这个测试原来的名字
// ("...InsufficientHistory")具有误导性、且没能抓住后来 review 发现的
// `lt` 运算符误触发缺陷的原因。
func TestEvaluatorSparseDataDoesNotSpuriouslyBreachAGreaterThanRule(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC).Truncate(bucketWidth)
	clock := fakeClock{now: now}
	rule := testRule(10, 3)
	store := newFakeStore()
	store.rules = []model.AlertRule{rule}
	// Only seed the last bucket's worth of data — sparse, but not empty:
	// len(points) == 1, so the len(points) == 0 guard does not apply here.
	store.points = seedRequestRatePoints(now, now, 20)
	notifier := &fakeNotifier{}

	e := alertengine.New(store, notifier, clock, nil)
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if store.createCalls != 0 {
		t.Errorf("CreateAlertInstance called %d times, want 0 (zero-filled buckets don't breach a gt rule)", store.createCalls)
	}
	if len(notifier.calls) != 0 {
		t.Errorf("Notify called %d times, want 0", len(notifier.calls))
	}
}

// TestEvaluatorSkipsWhenNoRawDataYet is the regression test for the review
// finding: a fresh rule using OperatorLessThan (a completely normal
// combination — "alert if capacity < 3 nodes", "alert if request_rate < 1
// to catch an outage") must NOT fire just because metrics_history has no
// rows yet for its window, PROVIDED the rule is still within its grace
// period (CreatedAt recent relative to now). Without the
// len(points) == 0 guard in evaluateRule, every missing bucket zero-fills,
// 0 < threshold is true for every bucket, allBreach returns true, and the
// rule fires immediately — exactly the "alerting cries wolf" failure this
// guard exists to prevent. (The grace period's expiry — an old rule with a
// persistently empty raw series DOES fire — is covered separately by
// TestEvaluatorFiresPersistentZeroAfterGracePeriodExpires.)
//
// TestEvaluatorSkipsWhenNoRawDataYet 是这次 review 发现问题的回归测试：一条
// 使用 OperatorLessThan 的新规则(完全正常的组合——"capacity < 3 就告警"、
// "request_rate < 1 用于捕捉故障")，不能仅仅因为 metrics_history 这个窗口
// 内还没有任何行就触发——前提是该规则仍处于宽限期内(CreatedAt 相对 now
// 还很新)。如果 evaluateRule 里没有 len(points) == 0 这道防线，缺失的每个
// 桶都会被填成 0，0 < threshold 对每个桶都成立，allBreach 返回 true，规则
// 立刻触发——这正是这道防线要防止的"告警狼来了"失败模式。(宽限期过期后——
// 一条持续为空的旧规则应当照常触发——由另一个测试
// TestEvaluatorFiresPersistentZeroAfterGracePeriodExpires 单独覆盖。)
func TestEvaluatorSkipsWhenNoRawDataYet(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC).Truncate(bucketWidth)
	clock := fakeClock{now: now}
	rule := testRule(3, 3)
	rule.Operator = model.OperatorLessThan
	rule.Metric = model.MetricCapacity
	rule.CreatedAt = now // just created: well within the grace period
	store := newFakeStore()
	store.rules = []model.AlertRule{rule}
	// Zero points at all for the queried window — a fresh rule / fresh
	// deployment with nothing collected yet.
	store.points = nil
	notifier := &fakeNotifier{}

	e := alertengine.New(store, notifier, clock, nil)
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if store.createCalls != 0 {
		t.Errorf("CreateAlertInstance called %d times, want 0 (no raw data yet, still within grace period, must not fire an lt rule on zero-filled buckets)", store.createCalls)
	}
	if len(notifier.calls) != 0 {
		t.Errorf("Notify called %d times, want 0", len(notifier.calls))
	}
}

// TestEvaluatorFiresPersistentZeroAfterGracePeriodExpires is the regression
// test for the re-review finding: some raw series (tunnel_server_slots_
// total, gateway_http_requests_total) are only written when something
// happens, so zero connected nodes or zero traffic since boot leaves
// metrics_history empty not just for one bucketWidth but indefinitely. If
// the "no data yet" skip applied forever, a genuinely alarming "capacity
// has been zero this whole time" condition could never fire no matter how
// long it persisted. This test uses a rule whose CreatedAt is well past the
// grace period (ConsecutiveBuckets*bucketWidth since creation) with a
// still-empty raw series, and confirms the rule now fires — the grace
// period must expire, not suppress forever.
//
// TestEvaluatorFiresPersistentZeroAfterGracePeriodExpires 是这次二次 review
// 发现问题的回归测试：有些原始序列(tunnel_server_slots_total、
// gateway_http_requests_total)只在发生了什么事情时才写入，零个已连接节点
// 或自启动以来零流量会让 metrics_history 不只是一个 bucketWidth、而是
// 无限期地保持为空。如果"还没有数据"这道防线永远生效，一个真正值得告警的
// "capacity 一直是零"的情况就永远不会触发，无论它持续多久。这个测试用一条
// CreatedAt 远早于宽限期(创建以来已经过了 ConsecutiveBuckets*bucketWidth)
// 的规则，原始序列仍然为空，验证规则现在会触发——宽限期必须会过期，而不是
// 永远压制告警。
func TestEvaluatorFiresPersistentZeroAfterGracePeriodExpires(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC).Truncate(bucketWidth)
	clock := fakeClock{now: now}
	rule := testRule(3, 3)
	rule.Operator = model.OperatorLessThan
	rule.Metric = model.MetricCapacity
	rule.CreatedAt = now.Add(-1 * time.Hour) // far past the 3*bucketWidth (15m) grace period
	store := newFakeStore()
	store.rules = []model.AlertRule{rule}
	// Still zero raw points — e.g. no node has ever connected — but the
	// rule is old enough that this must now be evaluated as a genuine
	// sustained-zero condition, not benefit-of-the-doubt "too new".
	store.points = nil
	notifier := &fakeNotifier{}

	e := alertengine.New(store, notifier, clock, nil)
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if store.createCalls != 1 {
		t.Errorf("CreateAlertInstance called %d times, want 1 (persistent zero on an old rule must fire, grace period must expire)", store.createCalls)
	}
	if len(notifier.calls) != 1 {
		t.Errorf("Notify called %d times, want 1", len(notifier.calls))
	}
}

func TestEvaluatorSkipsNotifyWhenWebhookURLEmpty(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC).Truncate(bucketWidth)
	clock := fakeClock{now: now}
	rule := testRule(10, 3)
	rule.WebhookURL = ""
	store := newFakeStore()
	store.rules = []model.AlertRule{rule}
	store.points = seedRequestRatePoints(now.Add(-5*bucketWidth), now, 20)
	notifier := &fakeNotifier{}

	e := alertengine.New(store, notifier, clock, nil)
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if store.createCalls != 1 {
		t.Fatalf("CreateAlertInstance called %d times, want 1", store.createCalls)
	}
	if len(notifier.calls) != 0 {
		t.Errorf("Notify called %d times, want 0 (no WebhookURL)", len(notifier.calls))
	}
	var got *model.AlertInstance
	for _, inst := range store.instances {
		got = inst
	}
	if got == nil {
		t.Fatalf("no instance recorded")
	}
	if got.NotifyStatus != model.NotifyStatusSkipped {
		t.Errorf("instance NotifyStatus = %q, want %q", got.NotifyStatus, model.NotifyStatusSkipped)
	}
}

// clockSpy records whether Now was called, so the "RunOnce uses the
// injected clock" test can prove RunOnce never reaches for the real wall
// clock.
//
// clockSpy 记录 Now 是否被调用过，用于证明 RunOnce 从不去读真实系统时钟。
type clockSpy struct {
	now      time.Time
	nowCalls int
}

func (c *clockSpy) Now() time.Time {
	c.nowCalls++
	return c.now
}
func (c *clockSpy) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	panic("not used by RunOnce")
}

func TestEvaluatorUsesInjectedClock(t *testing.T) {
	// A now far from the real wall clock (year 2099) — if RunOnce ever
	// used the real clock instead, ListRollup's since/until window would
	// not line up with the seeded points and the rule would be (wrongly)
	// skipped for lack of history.
	//
	// 一个远离真实系统时钟的 now(2099 年)——RunOnce 如果误用了真实时钟，
	// ListRollup 的 since/until 窗口就不会跟种下的数据点对齐，规则会被
	// (错误地)当作历史不足而跳过。
	now := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC).Truncate(bucketWidth)
	clock := &clockSpy{now: now}
	rule := testRule(10, 3)
	store := newFakeStore()
	store.rules = []model.AlertRule{rule}
	store.points = seedRequestRatePoints(now.Add(-5*bucketWidth), now, 20)
	notifier := &fakeNotifier{}

	e := alertengine.New(store, notifier, clock, nil)
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if clock.nowCalls == 0 {
		t.Fatalf("injected clock's Now() was never called")
	}
	if store.createCalls != 1 {
		t.Fatalf("CreateAlertInstance called %d times, want 1 (RunOnce must use the injected 2099 now, not the real clock)", store.createCalls)
	}
}
