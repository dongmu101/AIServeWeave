package alertengine_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/alertengine"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// fakeClock is a minimal runtime.Clock fixed at a known instant — RunOnce
// only ever calls Now(), so NewTimer is never exercised and left unused.
// Mirrors metricshistory/retention_test.go's local fakeClock (no shared
// fake-clock package exists in this repo).
//
// fakeClock 是一个固定在已知时刻的最小 runtime.Clock——RunOnce 只调用
// Now()，NewTimer 从未被用到，故留空未实现。与
// metricshistory/retention_test.go 的本地 fakeClock 一致(本仓库没有共享的
// fake-clock 包)。
type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }
func (c fakeClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	panic("not used by RunOnce")
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

func TestEvaluatorSkipsRuleWithInsufficientHistory(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC).Truncate(bucketWidth)
	clock := fakeClock{now: now}
	rule := testRule(10, 3)
	store := newFakeStore()
	store.rules = []model.AlertRule{rule}
	// Only seed the last bucket's worth of data — nowhere near enough for
	// a 3-bucket window plus its leading diff bucket.
	store.points = seedRequestRatePoints(now, now, 20)
	notifier := &fakeNotifier{}

	e := alertengine.New(store, notifier, clock, nil)
	if err := e.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	if store.createCalls != 0 {
		t.Errorf("CreateAlertInstance called %d times, want 0 (insufficient history)", store.createCalls)
	}
	if len(notifier.calls) != 0 {
		t.Errorf("Notify called %d times, want 0", len(notifier.calls))
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
