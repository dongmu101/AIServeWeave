// metrics.go is the ComfyUI adapter's observability surface (STATUS.md's
// A06): queue depth, sourced from the same GET /queue call Status, Cancel
// and Health already make (Health added it as the P2 realtime-utilization
// occupancy signal, STATUS.md), so no dedicated polling loop is needed to
// produce it.
//
// metrics.go 是 ComfyUI 适配器的可观测性界面（STATUS.md 的 A06）：队列深度，
// 取自 Status、Cancel 与 Health 本就会发起的同一次 GET /queue 调用（Health 是
// 为了 P2 实时利用率占用信号才加上这一次调用的，见 STATUS.md），因此不需要专门的
// 后台轮询循环来产生它。
package comfyui

import (
	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
)

// Metric names, one per instrument this package records.
//
// 指标名，本包记录的每个仪器一个。
const (
	// MetricQueueRunning is how many prompts this instance's queue reports
	// running, by runtime_id. ComfyUI runs at most one at a time outside a
	// custom build, so this is normally 0 or 1, but the source is the
	// backend's own array length, not an assumption this package makes.
	//
	// MetricQueueRunning 是本实例队列上报的正在运行的 prompt 数，按 runtime_id
	// 分类。ComfyUI 在非定制构建下同一时间至多运行一个，因此这个数值通常是 0
	// 或 1，但它的来源是后端自己数组的长度，不是本包做的假设。
	MetricQueueRunning = "comfyui_queue_running"
	// MetricQueuePending is how many prompts are waiting, by runtime_id.
	//
	// MetricQueuePending 是等待中的 prompt 数，按 runtime_id 分类。
	MetricQueuePending = "comfyui_queue_pending"
)

// Label keys.
//
// 标签键。
const (
	LabelRuntimeID = "runtime_id"
)

// Descriptions returns this package's metric catalogue, for a service to
// hand to metrics.New.
//
// Descriptions 返回本包的指标目录，供服务交给 metrics.New。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricQueueRunning: {
			Kind: metrics.KindGauge,
			Help: "Prompts this ComfyUI instance's queue reports running, by runtime_id.",
		},
		MetricQueuePending: {
			Kind: metrics.KindGauge,
			Help: "Prompts waiting in this ComfyUI instance's queue, by runtime_id.",
		},
	}
}

// recorder is this package's typed view of runtime.Metrics: every recording
// site calls one of its methods, so the label vocabulary above is enforced
// by the type system rather than by review.
//
// recorder 是本包对 runtime.Metrics 的类型化视图：每个记录点都调用它的某个
// 方法，因此上面那套标签词汇由类型系统而非评审来保证。
type recorder struct {
	sink runtime.Metrics
}

// newRecorder returns a recorder. A nil sink discards everything, which is
// what an Agent with no -metrics-addr configured gets.
//
// newRecorder 返回一个记录器。sink 为 nil 时全部丢弃，这也是未配置
// -metrics-addr 的 Agent 所得到的行为。
func newRecorder(sink runtime.Metrics) *recorder {
	if sink == nil {
		sink = discardMetrics{}
	}
	return &recorder{sink: sink}
}

// Queue publishes the current queue depth this instance's GET /queue
// reported. It is a gauge republish, not a counter: calling it again with
// the same numbers is a no-op in effect, and calling it from every
// Status/Cancel poll is exactly what keeps it fresh without any dedicated
// background polling loop.
//
// Queue 发布本实例 GET /queue 刚刚上报的队列深度。它是量表的重新发布，不是
// 计数器：用相同的数字再调用一次，效果上是空操作；而每次 Status/Cancel 轮询
// 都调用它，正是在不引入专门后台轮询循环的前提下让它保持新鲜的办法。
func (r *recorder) Queue(runtimeID string, running, pending int) {
	labels := map[string]string{LabelRuntimeID: runtimeID}
	r.sink.Gauge(MetricQueueRunning, labels).Set(float64(running))
	r.sink.Gauge(MetricQueuePending, labels).Set(float64(pending))
}

// discardMetrics is the sink used when no metrics backend is configured.
//
// discardMetrics 是未配置指标后端时使用的下沉端。
type discardMetrics struct{}

func (discardMetrics) Counter(string, map[string]string) runtime.Counter {
	return discardInstrument{}
}

func (discardMetrics) Gauge(string, map[string]string) runtime.Gauge {
	return discardInstrument{}
}

func (discardMetrics) Histogram(string, map[string]string) runtime.Histogram {
	return discardInstrument{}
}

// discardInstrument drops every sample.
//
// discardInstrument 丢弃每一个样本。
type discardInstrument struct{}

func (discardInstrument) Add(float64)     {}
func (discardInstrument) Set(float64)     {}
func (discardInstrument) Observe(float64) {}
