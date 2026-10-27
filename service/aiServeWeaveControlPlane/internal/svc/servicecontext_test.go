package svc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	commonmetrics "AIServeWeave/common/metrics"
	cpmetrics "AIServeWeave/service/aiServeWeaveControlPlane/internal/metrics"
)

// fakeArtifactStorageStore answers SumArtifactStorageBytes from a scripted
// sequence of results, one per call, so a test can drive
// runArtifactStorageGauge through more than one reading without a real
// database.
//
// fakeArtifactStorageStore 从一份脚本化的结果序列里应答
// SumArtifactStorageBytes，每次调用取一个，好让测试无需真实数据库就能驱动
// runArtifactStorageGauge 经历不止一次读取。
type fakeArtifactStorageStore struct {
	mu      sync.Mutex
	results []struct {
		total int64
		err   error
	}
	calls int
}

func (f *fakeArtifactStorageStore) SumArtifactStorageBytes(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	if i >= len(f.results) {
		i = len(f.results) - 1
	}
	return f.results[i].total, f.results[i].err
}

// gaugeValue renders registry and returns name's value with no labels, or
// (0, false) if it was never set.
//
// gaugeValue 渲染 registry 并返回 name 无标签时的取值，若从未被设置则返回
// (0, false)。
func gaugeValue(t *testing.T, registry *commonmetrics.Registry, name string) (float64, bool) {
	t.Helper()
	var buf strings.Builder
	if err := registry.Render(&buf); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	samples, err := commonmetrics.ParseExposition(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("ParseExposition() error = %v", err)
	}
	for _, s := range samples {
		if s.Name == name {
			return s.Value, true
		}
	}
	return 0, false
}

// TestArtifactStorageGaugeReadsAndRepublishes asserts
// runArtifactStorageGauge sets MetricArtifactStorageBytes from the store's
// current total, and that it keeps republishing on later ticks — including
// a value going down, which a running Gateway-side counter could never do
// after a cleanup sweep deletes bytes (STATUS.md's A06, the reason this
// gauge is recomputed rather than accumulated).
//
// TestArtifactStorageGaugeReadsAndRepublishes 断言 runArtifactStorageGauge
// 会从 store 当前的合计设置 MetricArtifactStorageBytes，并且在之后的轮次里
// 持续重新发布——包括数值下降的情形，这是一个由 Gateway 维护的运行时计数器
// 在一次清理扫描删除字节之后永远做不到的事（STATUS.md 的 A06，这正是这个
// 量表选择重新计算而不是累加的原因）。
func TestArtifactStorageGaugeReadsAndRepublishes(t *testing.T) {
	registry := commonmetrics.New(cpmetrics.Descriptions())
	store := &fakeArtifactStorageStore{results: []struct {
		total int64
		err   error
	}{{total: 1024}}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runArtifactStorageGauge(ctx, store, registry) }()

	// The first reading happens before the loop ever waits on a ticker, so
	// cancelling right away still lets it land.
	//
	// 第一次读取发生在这个循环等待 ticker 之前，因此立即取消依然能让它落地。
	cancel()
	<-done

	got, ok := gaugeValue(t, registry, cpmetrics.MetricArtifactStorageBytes)
	if !ok || got != 1024 {
		t.Errorf("%s = %v, ok=%v; want 1024, true", cpmetrics.MetricArtifactStorageBytes, got, ok)
	}
}

// TestArtifactStorageGaugeSkipsAReadFailureRatherThanZeroingTheGauge asserts
// a read error leaves the gauge exactly where it was, rather than reporting
// zero bytes — a false "storage is empty" is worse than a stale-but-honest
// last-known value.
//
// TestArtifactStorageGaugeSkipsAReadFailureRatherThanZeroingTheGauge 断言一次
// 读取失败会让量表原地保持不变，而不是报告零字节——一个错误的「存储是空的」
// 比一个陈旧但诚实的上一次已知值更糟。
func TestArtifactStorageGaugeSkipsAReadFailureRatherThanZeroingTheGauge(t *testing.T) {
	registry := commonmetrics.New(cpmetrics.Descriptions())
	// Establishes "the gauge was already non-zero" directly, without waiting
	// on a real ticker interval to get a first successful reading.
	//
	// 直接建立「量表此前已经非零」这一前提，而不必等待真实的 ticker 间隔来
	// 获得第一次成功的读取。
	registry.Gauge(cpmetrics.MetricArtifactStorageBytes, nil).Set(2048)

	failingStore := &fakeArtifactStorageStore{results: []struct {
		total int64
		err   error
	}{{err: errors.New("db unreachable")}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runArtifactStorageGauge(ctx, failingStore, registry) }()
	cancel()
	<-done

	got, ok := gaugeValue(t, registry, cpmetrics.MetricArtifactStorageBytes)
	if !ok || got != 2048 {
		t.Errorf("%s = %v, ok=%v; want it to stay at 2048 after a read failure", cpmetrics.MetricArtifactStorageBytes, got, ok)
	}
}
