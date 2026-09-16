package comfyui_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/runtimetest"
	"AIServeWeave/common/runtime/workflow/comfyui"
)

// comfyuiMetrics is every instrument this package records, restated so the
// catalogue test iterates a list rather than whatever happened to be
// recorded.
//
// comfyuiMetrics 是本包记录的全部仪器，在此重述，好让目录测试遍历一份清单，
// 而不是遍历「碰巧被记录到的那些」。
var comfyuiMetrics = []string{
	comfyui.MetricQueueRunning,
	comfyui.MetricQueuePending,
}

func TestComfyUIDescriptionsCoverEveryMetric(t *testing.T) {
	descs := comfyui.Descriptions()

	for _, name := range comfyuiMetrics {
		if _, ok := descs[name]; !ok {
			t.Errorf("metric %s has no description", name)
		}
	}
	known := make(map[string]bool, len(comfyuiMetrics))
	for _, name := range comfyuiMetrics {
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

// newRuntimeWithMetrics is newRuntime plus a metrics sink, kept separate
// rather than added to the shared helper so every other test in this
// package stays unaffected by a dependency only this file needs.
//
// newRuntimeWithMetrics 是加了指标下沉端的 newRuntime，独立于共享辅助函数存在，
// 这样本包其余测试不受一个只有本文件需要的依赖影响。
func newRuntimeWithMetrics(t *testing.T, f *fakeComfy, ws runtime.WSDialer, sink runtime.Metrics) *comfyui.Runtime {
	t.Helper()
	cfg := runtime.Config{
		ID:      "test-comfyui",
		Kind:    runtime.KindComfyUI,
		BaseURL: f.server.URL,
	}
	rt, err := comfyui.New(cfg.Normalize(), runtime.Dependencies{
		HTTPClient: f.server.Client(),
		WSDialer:   ws,
		Clock:      runtimetest.NewClock(time.Unix(1700000000, 0)),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:    sink,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	adapter, ok := rt.(*comfyui.Runtime)
	if !ok {
		t.Fatalf("New returned %T, want *comfyui.Runtime", rt)
	}
	return adapter
}

// TestStatusPublishesQueueDepth asserts Status republishes the queue gauges
// from the same GET /queue it already makes, with no extra request to the
// backend (STATUS.md's A06).
//
// TestStatusPublishesQueueDepth 断言 Status 会从它本就发起的 GET /queue 重新
// 发布队列量表，不向后端产生任何额外请求（STATUS.md 的 A06）。
func TestStatusPublishesQueueDepth(t *testing.T) {
	f := newFakeComfy(t)
	mx := metricstest.New()
	rt := newRuntimeWithMetrics(t, f, newScriptedWS(1), mx)

	f.setQueue([]string{otherRunID}, []string{"prompt-0", testRunID})
	if _, err := rt.Status(context.Background(), testRunID); err != nil {
		t.Fatalf("Status: %v", err)
	}

	labels := map[string]string{comfyui.LabelRuntimeID: "test-comfyui"}
	if got := mx.Find(comfyui.MetricQueueRunning, labels); got == nil || got.Value() != 1 {
		t.Errorf("%s = %v, want 1 (otherRunID running)", comfyui.MetricQueueRunning, got)
	}
	if got := mx.Find(comfyui.MetricQueuePending, labels); got == nil || got.Value() != 2 {
		t.Errorf("%s = %v, want 2 (prompt-0 and testRunID pending)", comfyui.MetricQueuePending, got)
	}

	// The gauge is republished, not accumulated: an emptied queue must read
	// back to zero, not linger at the previous reading.
	//
	// 量表是重新发布，不是累加：清空后的队列必须重新读回零，而不是停留在上一次的读数。
	f.setQueue(nil, nil)
	if _, err := rt.Status(context.Background(), testRunID); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got := mx.Find(comfyui.MetricQueueRunning, labels); got == nil || got.Value() != 0 {
		t.Errorf("%s = %v after the queue emptied, want 0", comfyui.MetricQueueRunning, got)
	}
	if got := mx.Find(comfyui.MetricQueuePending, labels); got == nil || got.Value() != 0 {
		t.Errorf("%s = %v after the queue emptied, want 0", comfyui.MetricQueuePending, got)
	}
}

// TestCancelPublishesQueueDepth is TestStatusPublishesQueueDepth for the
// other call site that reads GET /queue.
//
// TestCancelPublishesQueueDepth 是 TestStatusPublishesQueueDepth 之于另一个
// 会读取 GET /queue 的调用点的等价测试。
func TestCancelPublishesQueueDepth(t *testing.T) {
	f := newFakeComfy(t)
	mx := metricstest.New()
	rt := newRuntimeWithMetrics(t, f, newScriptedWS(1), mx)
	mustDiscover(t, rt)

	f.setQueue(nil, []string{testRunID})
	if err := rt.Cancel(context.Background(), testRunID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	labels := map[string]string{comfyui.LabelRuntimeID: "test-comfyui"}
	if got := mx.Find(comfyui.MetricQueuePending, labels); got == nil || got.Value() != 1 {
		t.Errorf("%s = %v, want 1", comfyui.MetricQueuePending, got)
	}
}

// TestComfyUIMetricsCarryOnlyRuntimeIDLabel is the executable form of
// metrics.go's closed label vocabulary.
//
// TestComfyUIMetricsCarryOnlyRuntimeIDLabel 是 metrics.go 那套封闭标签词汇的
// 可执行版本。
func TestComfyUIMetricsCarryOnlyRuntimeIDLabel(t *testing.T) {
	f := newFakeComfy(t)
	mx := metricstest.New()
	rt := newRuntimeWithMetrics(t, f, newScriptedWS(1), mx)

	f.setQueue([]string{testRunID}, nil)
	if _, err := rt.Status(context.Background(), testRunID); err != nil {
		t.Fatalf("Status: %v", err)
	}

	allowed := map[string]bool{comfyui.LabelRuntimeID: true}
	for _, s := range mx.All() {
		for key := range s.Labels {
			if !allowed[key] {
				t.Errorf("metric %s carries label %q, which is outside the closed vocabulary", s.Name, key)
			}
		}
	}
}
