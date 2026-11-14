package scheduler

import (
	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
)

// This file is the scheduler's observability surface. Its one rule is the
// reason the model is absent from every label here:
//
// The model name is caller-supplied free text. It arrives in the request body
// of a public API, nothing bounds it, and a label carrying it turns one
// malformed client — or one hostile one — into unbounded series in the metrics
// backend. What the scheduler's metrics are for is answering "which node or
// instance is misbehaving", and node_id and runtime_id answer that with a
// bounded vocabulary drawn from verified certificates and local configuration.
// Per-model accounting belongs in the usage records, not here.
//
// 本文件是调度器的可观测性界面。它唯一的规则，也正是此处每个标签里都没有 model 的
// 原因：
//
// 模型名是调用方提供的自由文本。它来自一个公开 API 的请求体，没有任何东西约束它，
// 而携带它的标签会让一个格式错误的客户端——或者一个怀有恶意的客户端——在指标后端里
// 制造出无界的序列。调度器的指标是用来回答「哪个节点或哪个实例出问题了」的，而
// node_id 与 runtime_id 用一套源自已验证证书与本地配置的有界词汇回答了这个问题。
// 按模型的账应该记在用量记录里，不在这里。

// Metric names, one per instrument this package records.
//
// 指标名，本包记录的每个仪器一个。
const (
	// MetricDispatchesTotal counts dispatch attempts by candidate and result.
	// Its difference with the tunnel server's own dispatch counter is what
	// separates a request the scheduler never placed from one the node
	// refused.
	//
	// MetricDispatchesTotal 按候选与结果统计分发尝试。它与隧道服务端自己的分发
	// 计数器之差，正是区分「调度器根本没派出去」与「节点拒绝了」的依据。
	MetricDispatchesTotal = "gateway_scheduler_dispatches_total"
	// MetricNoCandidateTotal counts requests that found no capable node at
	// all. A rising line here with healthy nodes connected means the models
	// asked for are not the models deployed.
	//
	// MetricNoCandidateTotal 统计完全找不到可用节点的请求。在有健康节点连接的情况下
	// 这条线还在涨，说明被请求的模型不是被部署的模型。
	MetricNoCandidateTotal = "gateway_scheduler_no_candidate_total"
	// MetricRetriesTotal counts moves to the next candidate after a retryable
	// failure. A request that produced output is never retried, so this
	// counter only ever grows on failures a caller did not see.
	//
	// MetricRetriesTotal 统计可重试失败后切换到下一个候选的次数。已经产出内容的请求
	// 永不重试，因此这个计数器只会因调用方没看见的失败而增长。
	MetricRetriesTotal = "gateway_scheduler_retries_total"
	// MetricCandidates observes how many candidates a selection had to choose
	// from. A distribution collapsing toward 1 means the fleet has lost its
	// redundancy for that capability long before it loses its last node.
	//
	// MetricCandidates 观测一次选择有多少候选可挑。分布向 1 收拢，说明集群在失去最后
	// 一个节点之前很久，就已经失去了该能力上的冗余。
	MetricCandidates = "gateway_scheduler_candidates"
	// MetricBreakerOpen is 1 while a candidate's circuit breaker excludes it
	// and 0 once it is eligible again. It is the gauge README's 下一步建议
	// asks for: the breaker's decisions were previously visible only in the
	// selection results they silently changed.
	//
	// MetricBreakerOpen 在某候选被熔断器排除期间为 1，恢复可选后为 0。它就是 README
	// 「下一步建议」所要的那个量表：在此之前，熔断器的决定只能从它悄悄改变的选择
	// 结果里间接看到。
	MetricBreakerOpen = "gateway_scheduler_breaker_open"
	// MetricBreakerTripsTotal counts breaker trips. Its slope, not the gauge,
	// is what tells a flapping candidate from a durably broken one.
	//
	// MetricBreakerTripsTotal 统计熔断跳闸次数。区分「反复抖动」与「持续损坏」的是
	// 它的斜率，而不是那个量表。
	MetricBreakerTripsTotal = "gateway_scheduler_breaker_trips_total"
)

// Label keys. The set is closed, and deliberately holds no key for the model.
//
// 标签键。集合是封闭的，并且刻意没有为 model 留位置。
const (
	LabelNodeID     = "node_id"
	LabelRuntimeID  = "runtime_id"
	LabelResult     = "result"
	LabelCapability = "capability"
)

// Descriptions is this package's metric catalogue, for a service to hand to
// metrics.New.
//
// Descriptions 是本包的指标目录，供服务交给 metrics.New。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricDispatchesTotal: {
			Kind: metrics.KindCounter,
			Help: "Dispatch attempts, by candidate and result.",
		},
		MetricNoCandidateTotal: {
			Kind: metrics.KindCounter,
			Help: "Requests that found no node able to serve the model with the requested capability.",
		},
		MetricRetriesTotal: {
			Kind: metrics.KindCounter,
			Help: "Moves to the next candidate after a retryable failure.",
		},
		MetricCandidates: {
			Kind:    metrics.KindHistogram,
			Help:    "Candidates available to one selection.",
			Buckets: []float64{0, 1, 2, 4, 8, 16, 32},
		},
		MetricBreakerOpen: {
			Kind: metrics.KindGauge,
			Help: "1 while a candidate's circuit breaker excludes it from selection, 0 otherwise.",
		},
		MetricBreakerTripsTotal: {
			Kind: metrics.KindCounter,
			Help: "Circuit breaker trips, by candidate.",
		},
	}
}

// recorder is the scheduler's typed view of runtime.Metrics: every recording
// site calls one of its methods, so the label vocabulary above is enforced by
// the type system rather than by review.
//
// recorder 是调度器对 runtime.Metrics 的类型化视图：每个记录点都调用它的某个方法，
// 因此上面那套标签词汇由类型系统而非评审来保证。
type recorder struct {
	sink runtime.Metrics
}

// newRecorder returns a recorder. A nil sink discards everything, which is
// what a Gateway with no metrics backend configured gets.
//
// newRecorder 返回一个记录器。sink 为 nil 时全部丢弃，这也是未配置指标后端的
// Gateway 所得到的行为。
func newRecorder(sink runtime.Metrics) *recorder {
	if sink == nil {
		sink = discardMetrics{}
	}
	return &recorder{sink: sink}
}

// candidateLabels renders one candidate's identity.
//
// candidateLabels 渲染一个候选的身份。
func candidateLabels(c Candidate) map[string]string {
	return map[string]string{LabelNodeID: c.NodeID, LabelRuntimeID: c.RuntimeID}
}

// Dispatch counts one dispatch attempt against a candidate.
//
// Dispatch 为某个候选计一次分发尝试。
func (r *recorder) Dispatch(c Candidate, err error) {
	labels := candidateLabels(c)
	labels[LabelResult] = string(tunnelwire.ResultFor(err))
	r.sink.Counter(MetricDispatchesTotal, labels).Add(1)
}

// Selection records the size of the candidate set one request had, and counts
// the request when that set was empty.
//
// Selection 记录一次请求所拥有的候选集大小，并在集合为空时把这次请求计入。
func (r *recorder) Selection(capability runtime.Capability, n int) {
	labels := map[string]string{LabelCapability: string(capability)}
	r.sink.Histogram(MetricCandidates, labels).Observe(float64(n))
	if n == 0 {
		r.sink.Counter(MetricNoCandidateTotal, labels).Add(1)
	}
}

// Retry counts one move to the next candidate.
//
// Retry 计一次切换到下一个候选。
func (r *recorder) Retry(capability runtime.Capability) {
	r.sink.Counter(MetricRetriesTotal, map[string]string{LabelCapability: string(capability)}).Add(1)
}

// BreakerOpen publishes whether a candidate is currently excluded.
//
// BreakerOpen 发布某个候选当前是否被排除。
func (r *recorder) BreakerOpen(c Candidate, open bool) {
	value := 0.0
	if open {
		value = 1
	}
	r.sink.Gauge(MetricBreakerOpen, candidateLabels(c)).Set(value)
}

// BreakerTrip counts one trip.
//
// BreakerTrip 计一次跳闸。
func (r *recorder) BreakerTrip(c Candidate) {
	r.sink.Counter(MetricBreakerTripsTotal, candidateLabels(c)).Add(1)
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
