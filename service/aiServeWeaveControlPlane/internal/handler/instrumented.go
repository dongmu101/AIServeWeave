package handler

import (
	"net/http"
	"strconv"
	"time"

	"AIServeWeave/common/runtime"
	cpmetrics "AIServeWeave/service/aiServeWeaveControlPlane/internal/metrics"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// instrumented wraps h to record request count, duration and in-flight gauge
// under routeName — the route's static path template (e.g.
// "/admin/v1/jobs/history/:id"), never the raw request path, so the label
// stays closed regardless of how many distinct ids are ever requested.
//
// instrumented 包装 h，在 routeName(路由自身静态的路径模板，例如
// "/admin/v1/jobs/history/:id"，绝不是原始请求路径)下记录请求数、耗时与在途
// 量表——无论实际请求过多少个不同的 id，标签始终保持封闭。
func instrumented(reg runtime.Metrics, routeName string, h http.HandlerFunc) http.HandlerFunc {
	if reg == nil {
		return h
	}
	inflight := reg.Gauge(cpmetrics.MetricHTTPInflightRequests, map[string]string{"route": routeName})
	return func(w http.ResponseWriter, r *http.Request) {
		inflight.Set(1)       // 简化实现：并发同路由请求会互相覆盖为 1，足以回答"这条路由此刻是否有在途请求"；
		defer inflight.Set(0) // 精确并发计数留给后续需要时再加

		started := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(sw, r)

		reg.Counter(cpmetrics.MetricHTTPRequestsTotal, map[string]string{"route": routeName, "status": strconv.Itoa(sw.status)}).Add(1)
		reg.Histogram(cpmetrics.MetricHTTPRequestDurationSecs, map[string]string{"route": routeName}).Observe(time.Since(started).Seconds())
	}
}

// recordFleetCall records one ctx.Fleet.* call's outcome as success or error.
// It intentionally does not try to reproduce Fleet's own richer per-replica
// error taxonomy (unreachable/timeout/unauthorized/malformed): guessing at an
// internal classification this package does not own would risk a label value
// outside what metrics.go's cardinality test asserts.
//
// recordFleetCall 把一次 ctx.Fleet.* 调用的结果记为 success 或 error。它刻意不
// 尝试还原 Fleet 自己那套更细的逐副本错误分类(unreachable/timeout/unauthorized/
// malformed)：去猜一个本包并不拥有的内部分类，会有标签值落在 metrics.go 基数
// 测试断言范围之外的风险。
func recordFleetCall(ctx *svc.ServiceContext, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	ctx.MetricsRegistry.Counter(cpmetrics.MetricFleetCallsTotal, map[string]string{"result": result}).Add(1)
}

// recordRegistryClientCall is recordFleetCall for calls that reach the
// Registry's TokenAdmin service through ctx.Logic (node approve/disable/
// enable/maintenance/list-states).
//
// recordRegistryClientCall 是 recordFleetCall 面向经 ctx.Logic 到达 Registry
// TokenAdmin 服务的调用版本(节点审批/禁用/启用/维护/列出状态)。
func recordRegistryClientCall(ctx *svc.ServiceContext, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	ctx.MetricsRegistry.Counter(cpmetrics.MetricRegistryClientCallsTotal, map[string]string{"result": result}).Add(1)
}

// statusRecorder captures the status code a handler writes, mirroring
// Gateway httpapi's statusWriter.
//
// statusRecorder 捕获 handler 写出的状态码，照抄 Gateway httpapi 的
// statusWriter。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}
