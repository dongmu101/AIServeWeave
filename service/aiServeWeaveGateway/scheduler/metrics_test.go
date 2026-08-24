package scheduler_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// schedulerMetrics is every instrument the scheduler records, restated so the
// catalogue test iterates a list rather than whatever happened to be recorded.
//
// schedulerMetrics 是调度器记录的全部仪器，在此重述，好让目录测试遍历一份清单，而不是
// 遍历「碰巧被记录到的那些」。
var schedulerMetrics = []string{
	scheduler.MetricDispatchesTotal,
	scheduler.MetricNoCandidateTotal,
	scheduler.MetricRetriesTotal,
	scheduler.MetricCandidates,
	scheduler.MetricBreakerOpen,
	scheduler.MetricBreakerTripsTotal,
}

func TestSchedulerDescriptionsCoverEveryMetric(t *testing.T) {
	descs := scheduler.Descriptions()

	for _, name := range schedulerMetrics {
		if _, ok := descs[name]; !ok {
			t.Errorf("metric %s has no description", name)
		}
	}
	known := make(map[string]bool, len(schedulerMetrics))
	for _, name := range schedulerMetrics {
		known[name] = true
	}
	for name, desc := range descs {
		if !known[name] {
			t.Errorf("description %s names no metric this package records", name)
		}
		if desc.Help == "" {
			t.Errorf("metric %s has an empty help string", name)
		}
		if desc.Kind == metrics.KindHistogram && len(desc.Buckets) == 0 {
			t.Errorf("histogram %s has no buckets", name)
		}
	}
}

// TestSchedulerMetricsCarryNoModelLabel is the executable form of metrics.go's
// one rule. The model name is caller-supplied free text, and a label carrying
// it lets one client mint unbounded series; this test fails the moment a
// recording site starts passing it.
//
// TestSchedulerMetricsCarryNoModelLabel 是 metrics.go 那唯一一条规则的可执行版本。
// 模型名是调用方提供的自由文本，携带它的标签会让单个客户端就能造出无界序列；一旦某个
// 记录点开始传它，这个测试立刻失败。
func TestSchedulerMetricsCarryNoModelLabel(t *testing.T) {
	mx := metricstest.New()
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler("node-a", nil))
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, Metrics: mx})

	// One served request and one for a model nothing deploys: between them
	// every recording site that could carry a model name has run.
	//
	// 一次被服务的请求，加一次请求一个没人部署的模型：两者合起来，所有可能携带模型名
	// 的记录点都已经跑过。
	if _, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	_, _, _ = sched.Chat(context.Background(), runtime.ChatRequest{Model: "definitely-not-deployed:70b"})

	allowed := map[string]bool{
		scheduler.LabelNodeID:     true,
		scheduler.LabelRuntimeID:  true,
		scheduler.LabelResult:     true,
		scheduler.LabelCapability: true,
	}
	for _, s := range mx.All() {
		for key, value := range s.Labels {
			if !allowed[key] {
				t.Errorf("metric %s carries label %q, which is outside the closed vocabulary", s.Name, key)
			}
			if value == "qwen3:8b" || value == "definitely-not-deployed:70b" {
				t.Errorf("metric %s label %s = %q: a model name reached a label", s.Name, key, value)
			}
		}
	}
}

// TestSelectionMetrics asserts the candidate-set size is observed for every
// request, and that a request with no candidate is counted separately rather
// than being invisible.
//
// TestSelectionMetrics 断言每个请求的候选集大小都被观测，且没有候选的请求被单独计数，
// 而不是变得不可见。
func TestSelectionMetrics(t *testing.T) {
	mx := metricstest.New()
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler("node-a", nil))
	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, Metrics: mx})

	chat := map[string]string{scheduler.LabelCapability: string(runtime.CapabilityChat)}

	if _, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if s := mx.Find(scheduler.MetricCandidates, chat); s == nil || s.Value() != 1 {
		t.Errorf("%s = %v for a request with one candidate, want 1", scheduler.MetricCandidates, s)
	}
	if got := mx.Sum(scheduler.MetricNoCandidateTotal, chat); got != 0 {
		t.Errorf("%s = %v after a served request, want 0", scheduler.MetricNoCandidateTotal, got)
	}

	_, _, _ = sched.Chat(context.Background(), runtime.ChatRequest{Model: "nothing-deploys-this"})
	if got := mx.Sum(scheduler.MetricNoCandidateTotal, chat); got != 1 {
		t.Errorf("%s = %v after an undeployable model, want 1", scheduler.MetricNoCandidateTotal, got)
	}
}

// TestDispatchAndRetryMetrics asserts every attempt is counted with its own
// result, and that a move to the next candidate is visible as a retry.
//
// TestDispatchAndRetryMetrics 断言每次尝试都带着自己的结果被计数，且切换到下一个候选
// 会以 retry 的形式可见。
func TestDispatchAndRetryMetrics(t *testing.T) {
	mx := metricstest.New()
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var failures, successes atomic.Int32
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), failingHandler(&failures))
	connectNode(t, h, "node-b", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), chatHandler("node-b", &successes))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, Metrics: mx})
	// Both nodes hold one idle slot, so the ordering between them is a
	// coin toss; run until the failing node has been tried at least once,
	// which is the state this test is about.
	//
	// 两个节点各有一个空闲槽，它们之间的顺序是随机的；一直跑到那个会失败的节点至少被
	// 试过一次为止，那才是本测试要考察的状态。
	for range 10 {
		_, _, _ = sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
		if failures.Load() > 0 {
			break
		}
		gatewaytest.WaitFor(t, "slots to re-park", func() bool {
			return gatewaytest.IdleCount(h, "node-a") == 1 && gatewaytest.IdleCount(h, "node-b") == 1
		})
	}
	if failures.Load() == 0 {
		t.Skip("the failing node was never selected in 10 tries; nothing to assert about retries")
	}

	failed := mx.Sum(scheduler.MetricDispatchesTotal, map[string]string{
		scheduler.LabelNodeID: "node-a",
		scheduler.LabelResult: "upstream_error",
	})
	if failed < 1 {
		t.Errorf("%s{node_id=node-a,result=upstream_error} = %v, want at least 1",
			scheduler.MetricDispatchesTotal, failed)
	}
	retries := mx.Sum(scheduler.MetricRetriesTotal, map[string]string{
		scheduler.LabelCapability: string(runtime.CapabilityChat),
	})
	if retries < 1 {
		t.Errorf("%s = %v after a retryable failure, want at least 1", scheduler.MetricRetriesTotal, retries)
	}
}

// TestBreakerMetrics asserts the breaker's decisions are visible: the trip
// counter moves once, the gauge reads open while the candidate is excluded,
// and it returns to zero once the cooldown makes the candidate eligible again.
// This is what README's outstanding item asked for — before it, a tripped
// breaker changed selections with no signal of its own.
//
// TestBreakerMetrics 断言熔断器的决定可见：跳闸计数器走一次，量表在候选被排除期间读作
// open，并在冷却结束、候选重新可选后回到零。这正是 README 那条待办所要的——在此之前，
// 跳闸的熔断器改变了选择结果，自己却没有任何信号。
func TestBreakerMetrics(t *testing.T) {
	mx := metricstest.New()
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"),
		flakyThenWorkingHandler("node-a", 2, &count))

	sched := scheduler.New(h.Srv, scheduler.Config{
		Clock: h.Clock, FailureThreshold: 2, BaseCooldown: 10 * time.Second, MaxCooldown: time.Minute,
		Metrics: mx,
	})
	candidate := map[string]string{
		scheduler.LabelNodeID:    "node-a",
		scheduler.LabelRuntimeID: "backend-1",
	}
	waitForSlot := func() {
		t.Helper()
		gatewaytest.WaitFor(t, "node-a's slot to re-park", func() bool { return gatewaytest.IdleCount(h, "node-a") == 1 })
	}

	waitForSlot()
	for range 2 {
		_, _, _ = sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
		waitForSlot()
	}

	if got := mx.Sum(scheduler.MetricBreakerTripsTotal, candidate); got != 1 {
		t.Errorf("%s = %v after the threshold was reached, want 1", scheduler.MetricBreakerTripsTotal, got)
	}
	if got := mx.Sum(scheduler.MetricBreakerOpen, candidate); got != 1 {
		t.Errorf("%s = %v while the candidate is excluded, want 1", scheduler.MetricBreakerOpen, got)
	}

	h.Clock.Advance(10 * time.Second)
	if _, _, err := sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"}); err != nil {
		t.Fatalf("Chat after the cooldown elapsed: %v", err)
	}
	if got := mx.Sum(scheduler.MetricBreakerOpen, candidate); got != 0 {
		t.Errorf("%s = %v after the candidate became eligible again, want 0", scheduler.MetricBreakerOpen, got)
	}
	if got := mx.Sum(scheduler.MetricBreakerTripsTotal, candidate); got != 1 {
		t.Errorf("%s = %v after a recovery, want it unchanged at 1", scheduler.MetricBreakerTripsTotal, got)
	}
}

// TestBackpressureIsNotCountedAsABreakerFailure mirrors the scheduling rule in
// metric form: a busy node must not look like a broken one.
//
// TestBackpressureIsNotCountedAsABreakerFailure 以指标形式复刻那条调度规则：忙碌的
// 节点不能看起来像坏掉的节点。
func TestBackpressureIsNotCountedAsABreakerFailure(t *testing.T) {
	mx := metricstest.New()
	h := gatewaytest.NewHarness(t, tunnelserver.Config{})
	var count atomic.Int32
	connectNode(t, h, "node-a", "backend-1", chatCapableSnapshot("backend-1", "qwen3:8b"), backpressureHandler(&count))

	sched := scheduler.New(h.Srv, scheduler.Config{Clock: h.Clock, FailureThreshold: 1, Metrics: mx})
	for range 3 {
		_, _, _ = sched.Chat(context.Background(), runtime.ChatRequest{Model: "qwen3:8b"})
		gatewaytest.WaitFor(t, "node-a's slot to re-park", func() bool { return gatewaytest.IdleCount(h, "node-a") == 1 })
	}

	candidate := map[string]string{scheduler.LabelNodeID: "node-a", scheduler.LabelRuntimeID: "backend-1"}
	if got := mx.Sum(scheduler.MetricBreakerTripsTotal, candidate); got != 0 {
		t.Errorf("%s = %v after repeated backpressure, want 0: busy is not broken",
			scheduler.MetricBreakerTripsTotal, got)
	}
	if got := mx.Sum(scheduler.MetricDispatchesTotal, map[string]string{
		scheduler.LabelNodeID: "node-a",
		scheduler.LabelResult: "backpressure",
	}); got != 3 {
		t.Errorf("%s{result=backpressure} = %v, want 3", scheduler.MetricDispatchesTotal, got)
	}
}
