package waitqueue

import (
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
)

// Metric names. They keep the names the in-process chat wait used, so
// dashboards survive the move to the shared queue; depth is now this
// replica's waiters, so sum it across replicas for the cluster figure.
//
// 指标名。沿用进程内对话等待时的名字，让看板在迁移到共享队列后仍可使用；depth 现在是
// 本副本的等待者数，集群总数需跨副本求和。
const (
	MetricDepth            = "gateway_scheduler_chat_queue_depth"
	MetricWaitSeconds      = "gateway_scheduler_chat_queue_wait_seconds"
	MetricRejectedTotal    = "gateway_scheduler_chat_queue_rejected_total"
	MetricStoreErrorsTotal = "gateway_scheduler_chat_queue_store_errors_total"
)

// Descriptions is this package's metric catalogue.
// Descriptions 是本包的指标目录。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricDepth: {
			Kind: metrics.KindGauge,
			Help: "Chat or Messages requests this replica holds in the cluster-wide wait queue; sum across replicas for the queue length.",
		},
		MetricWaitSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "How long a queued chat request waited, by outcome (resolved, canceled, timeout, lost, store_error).",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
		},
		MetricRejectedTotal: {
			Kind: metrics.KindCounter,
			Help: "Chat requests answered 429 at once because the cluster-wide wait queue was full.",
		},
		MetricStoreErrorsTotal: {
			Kind: metrics.KindCounter,
			Help: "Failed Redis calls of the chat wait queue, by op (join, poll, leave); each join or poll failure answers 429 without waiting.",
		},
	}
}

type recorder struct{ sink runtime.Metrics }

func newRecorder(sink runtime.Metrics) recorder {
	if sink == nil {
		sink = discard{}
	}
	return recorder{sink: sink}
}

func (r recorder) depth(n int) { r.sink.Gauge(MetricDepth, nil).Set(float64(n)) }

func (r recorder) wait(d time.Duration, outcome string) {
	r.sink.Histogram(MetricWaitSeconds, map[string]string{"outcome": outcome}).Observe(d.Seconds())
}

func (r recorder) rejected() { r.sink.Counter(MetricRejectedTotal, nil).Add(1) }

func (r recorder) storeError(op string) {
	r.sink.Counter(MetricStoreErrorsTotal, map[string]string{"op": op}).Add(1)
}

type discard struct{}

func (discard) Counter(string, map[string]string) runtime.Counter     { return discardInstrument{} }
func (discard) Gauge(string, map[string]string) runtime.Gauge         { return discardInstrument{} }
func (discard) Histogram(string, map[string]string) runtime.Histogram { return discardInstrument{} }

type discardInstrument struct{}

func (discardInstrument) Add(float64)     {}
func (discardInstrument) Set(float64)     {}
func (discardInstrument) Observe(float64) {}
