package jobqueue

import (
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
)

// Metric names.
const (
	MetricDepth            = "gateway_workflow_queue_depth"
	MetricWaitSeconds      = "gateway_workflow_queue_wait_seconds"
	MetricRejectedTotal    = "gateway_workflow_queue_rejected_total"
	MetricStoreErrorsTotal = "gateway_workflow_queue_store_errors_total"
	MetricHeld             = "gateway_workflow_queue_held"
)

// Descriptions is this package's metric catalogue.
// Descriptions 是本包的指标目录。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricDepth: {
			Kind: metrics.KindGauge,
			Help: "Workflow jobs admitted to the cluster-wide queue, sampled by each replica every 10s; use max() across replicas.",
		},
		MetricWaitSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Time from enqueue until a queued workflow job left the queue, by outcome (dispatched, timeout, cancelled, failed, unknown).",
			Buckets: []float64{0.5, 1, 5, 15, 30, 60, 120, 300, 600, 1800},
		},
		MetricRejectedTotal: {
			Kind: metrics.KindCounter,
			Help: "Workflow submissions the queue did not take, by reason (full, unavailable).",
		},
		MetricStoreErrorsTotal: {
			Kind: metrics.KindCounter,
			Help: "Failed Redis or asynq calls of the workflow queue, by op.",
		},
		MetricHeld: {
			Kind: metrics.KindGauge,
			Help: "Queued workflow tasks this replica's workers currently hold while waiting for capacity.",
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
func (r recorder) held(n int)  { r.sink.Gauge(MetricHeld, nil).Set(float64(n)) }

func (r recorder) wait(d time.Duration, outcome string) {
	r.sink.Histogram(MetricWaitSeconds, map[string]string{"outcome": outcome}).Observe(d.Seconds())
}

func (r recorder) rejected(reason string) {
	r.sink.Counter(MetricRejectedTotal, map[string]string{"reason": reason}).Add(1)
}

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
