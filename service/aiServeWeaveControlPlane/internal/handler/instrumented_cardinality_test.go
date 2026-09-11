package handler

import (
	"errors"
	"strings"
	"testing"

	commonmetrics "AIServeWeave/common/metrics"
	cpmetrics "AIServeWeave/service/aiServeWeaveControlPlane/internal/metrics"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// TestFleetAndRegistryClientCallResultsAreBounded drives
// recordFleetCall/recordRegistryClientCall through both a success and a
// failure, then reads the real registry back via ParseExposition (the
// exposition writer's inverse, common/metrics) rather than reimplementing
// the two functions' own success/error branching a second time — the point
// is to prove the wiring end to end, not to restate the same two-line
// ternary as an assertion.
//
// TestFleetAndRegistryClientCallResultsAreBounded 驱动
// recordFleetCall/recordRegistryClientCall 各走一次成功与一次失败，再通过
// ParseExposition(common/metrics 导出写入器的逆操作)把注册表读回来，而不是把
// 这两个函数自己的成功/失败判断再重述一遍当断言——目的是端到端证明这条接线，
// 而不是重复那个两行的三元判断。
func TestFleetAndRegistryClientCallResultsAreBounded(t *testing.T) {
	registry := commonmetrics.New(cpmetrics.Descriptions())
	ctx := &svc.ServiceContext{MetricsRegistry: registry}

	recordFleetCall(ctx, nil)
	recordFleetCall(ctx, errors.New("unreachable"))
	recordRegistryClientCall(ctx, nil)
	recordRegistryClientCall(ctx, errors.New("unreachable"))

	var buf strings.Builder
	if err := registry.Render(&buf); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	samples, err := commonmetrics.ParseExposition(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("ParseExposition() error = %v", err)
	}

	allowed := map[string]bool{"success": true, "error": true}
	found := map[string]bool{}
	for _, s := range samples {
		if s.Name != cpmetrics.MetricFleetCallsTotal && s.Name != cpmetrics.MetricRegistryClientCallsTotal {
			continue
		}
		result := s.Labels["result"]
		if !allowed[result] {
			t.Errorf("metric %s label result = %q, which is not a bounded value", s.Name, result)
		}
		found[s.Name+"/"+result] = true
	}
	for _, want := range []string{
		cpmetrics.MetricFleetCallsTotal + "/success", cpmetrics.MetricFleetCallsTotal + "/error",
		cpmetrics.MetricRegistryClientCallsTotal + "/success", cpmetrics.MetricRegistryClientCallsTotal + "/error",
	} {
		if !found[want] {
			t.Errorf("expected to find a recorded series for %s, found none", want)
		}
	}
}
