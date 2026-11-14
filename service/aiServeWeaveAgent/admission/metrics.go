package admission

import (
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/hostresources"
)

// Metric names. They are process-scoped and carry no node_id: one Agent
// process is one machine, and the scrape target already names it.
//
// 指标名。它们是进程级的、不带 node_id：一个 Agent 进程就是一台机器，抓取目标本身已经
// 标明了它。
const (
	MetricPressure                 = "agent_admission_pressure"
	MetricRejectionsTotal          = "agent_admission_rejections_total"
	MetricWaitSeconds              = "agent_admission_wait_seconds"
	MetricInflight                 = "agent_admission_inflight"
	MetricWaiting                  = "agent_admission_waiting"
	MetricHostCPUUtilization       = "agent_host_cpu_utilization"
	MetricHostMemoryAvailableBytes = "agent_host_memory_available_bytes"
	MetricHostMemoryTotalBytes     = "agent_host_memory_total_bytes"
	MetricHostLoadAverage1m        = "agent_host_load_average_1m"
	MetricHostSampleErrorsTotal    = "agent_host_sample_errors_total"
)

// Descriptions is this package's metric catalogue.
// Descriptions 是本包的指标目录。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricPressure: {
			Kind: metrics.KindGauge,
			Help: "1 while the Agent refuses new heavy work because CPU or memory crossed its threshold, else 0.",
		},
		MetricRejectionsTotal: {
			Kind: metrics.KindCounter,
			Help: "Requests refused by node-wide admission, by class (cli, general) and reason (pressure_cpu, pressure_memory, queue_full, wait_timeout).",
		},
		MetricWaitSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Time requests waited on a full class semaphore, by class and outcome (admitted, timeout, canceled).",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricInflight: {
			Kind: metrics.KindGauge,
			Help: "Requests currently holding a class semaphore slot.",
		},
		MetricWaiting: {
			Kind: metrics.KindGauge,
			Help: "Requests currently waiting on a class semaphore.",
		},
		MetricHostCPUUtilization: {
			Kind: metrics.KindGauge,
			Help: "Host CPU busy share over the last sample interval, 0..1 (estimated from load average on macOS).",
		},
		MetricHostMemoryAvailableBytes: {
			Kind: metrics.KindGauge,
			Help: "Host (or cgroup) memory available for new work, in bytes.",
		},
		MetricHostMemoryTotalBytes: {
			Kind: metrics.KindGauge,
			Help: "Host (or cgroup) memory total, in bytes.",
		},
		MetricHostLoadAverage1m: {
			Kind: metrics.KindGauge,
			Help: "Host one-minute load average (absent on Windows).",
		},
		MetricHostSampleErrorsTotal: {
			Kind: metrics.KindCounter,
			Help: "Live host usage samples that failed.",
		},
	}
}

type recorder struct {
	sink runtime.Metrics
}

func newRecorder(sink runtime.Metrics) recorder {
	if sink == nil {
		sink = discard{}
	}
	return recorder{sink: sink}
}

func (r recorder) pressure(on bool) {
	v := 0.0
	if on {
		v = 1
	}
	r.sink.Gauge(MetricPressure, nil).Set(v)
}

func (r recorder) rejection(class, reason string) {
	r.sink.Counter(MetricRejectionsTotal, map[string]string{"class": class, "reason": reason}).Add(1)
}

func (r recorder) wait(class, outcome string, d time.Duration) {
	r.sink.Histogram(MetricWaitSeconds, map[string]string{"class": class, "outcome": outcome}).Observe(d.Seconds())
}

func (r recorder) inflight(class string, n int) {
	r.sink.Gauge(MetricInflight, map[string]string{"class": class}).Set(float64(n))
}

func (r recorder) waiting(class string, n int) {
	r.sink.Gauge(MetricWaiting, map[string]string{"class": class}).Set(float64(n))
}

func (r recorder) sampleError() {
	r.sink.Counter(MetricHostSampleErrorsTotal, nil).Add(1)
}

func (r recorder) usage(u hostresources.Usage) {
	if u.CPUUtilization != nil {
		r.sink.Gauge(MetricHostCPUUtilization, nil).Set(*u.CPUUtilization)
	}
	if u.MemoryAvailableBytes != nil {
		r.sink.Gauge(MetricHostMemoryAvailableBytes, nil).Set(float64(*u.MemoryAvailableBytes))
	}
	if u.MemoryTotalBytes != nil {
		r.sink.Gauge(MetricHostMemoryTotalBytes, nil).Set(float64(*u.MemoryTotalBytes))
	}
	if u.LoadAverage1m != nil {
		r.sink.Gauge(MetricHostLoadAverage1m, nil).Set(*u.LoadAverage1m)
	}
}

type discard struct{}

func (discard) Counter(string, map[string]string) runtime.Counter     { return discardInstrument{} }
func (discard) Gauge(string, map[string]string) runtime.Gauge         { return discardInstrument{} }
func (discard) Histogram(string, map[string]string) runtime.Histogram { return discardInstrument{} }

type discardInstrument struct{}

func (discardInstrument) Add(float64)     {}
func (discardInstrument) Set(float64)     {}
func (discardInstrument) Observe(float64) {}
