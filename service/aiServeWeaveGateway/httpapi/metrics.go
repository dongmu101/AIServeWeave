package httpapi

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
)

// This file is the front door's observability surface. It answers the request
// half of README's 可观测性 list — request volume and concurrency, TTFT, total
// response time, prompt and completion tokens, output tokens per second — and
// it answers them without ever letting a caller decide a label value.
//
// Two values arrive from the public API and are excluded from every label
// here:
//
//   - The model name. It is free text in a request body: one client asking for
//     a thousand misspelled models would mint a thousand series. Per-model
//     accounting belongs in usage records, which are bounded by a catalogue of
//     models this deployment actually has.
//   - The request path. Only the three routes this package serves become
//     endpoint values; anything else is "other", so a scan for /wp-admin.php
//     cannot grow the metric.
//
// 本文件是前门的可观测性界面。它回答 README「可观测性」清单中属于请求的那一半——
// 请求量与并发、TTFT、总响应时间、输入与输出 token、每秒输出 token——并且在回答时
// 从不让调用方决定任何一个标签的取值。
//
// 有两个来自公开 API 的值被排除在此处每一个标签之外：
//
//   - 模型名。它是请求体里的自由文本：一个客户端请求一千个拼错的模型，就会造出一千条
//     序列。按模型记账属于用量记录，那里由本部署实际拥有的模型目录来约束。
//   - 请求路径。只有本包服务的三条路由会成为 endpoint 取值；其余一律记为 "other"，
//     因此对 /wp-admin.php 的扫描无法把指标撑大。

// Metric names, one per instrument this package records.
//
// 指标名，本包记录的每个仪器一个。
const (
	// MetricRequestsTotal counts finished HTTP requests by endpoint and
	// status.
	//
	// MetricRequestsTotal 按 endpoint 与 status 统计已完成的 HTTP 请求。
	MetricRequestsTotal = "gateway_http_requests_total"
	// MetricRequestDurationSeconds observes total response time: for a
	// streamed response that is the whole stream, not its first byte, which
	// is what MetricTTFTSeconds is for.
	//
	// MetricRequestDurationSeconds 观测总响应时间：对流式响应，这是整条流的时间而
	// 不是首字节的时间——首字节由 MetricTTFTSeconds 负责。
	MetricRequestDurationSeconds = "gateway_http_request_duration_seconds"
	// MetricInflightRequests is how many requests are being served right now.
	// It is README's 并发数, and the figure a rate limit will eventually be
	// calibrated against.
	//
	// MetricInflightRequests 是当前正在服务的请求数。它就是 README 的「并发数」，
	// 也是将来校准限流阈值时要对照的那个数字。
	MetricInflightRequests = "gateway_http_inflight_requests"
	// MetricTTFTSeconds observes time to first token: from this handler
	// receiving the request to the first byte of content reaching the client.
	// Its difference with the tunnel's own first-event metric is the front
	// door's share of that latency.
	//
	// MetricTTFTSeconds 观测首 token 延迟：从本处理器收到请求，到第一个字节的内容
	// 到达客户端。它与隧道自己的首帧指标之差，就是前门在这段延迟里所占的份额。
	MetricTTFTSeconds = "gateway_http_ttft_seconds"
	// MetricTokensTotal counts tokens by direction, as reported by the
	// backend. It is a backend's count, not this Gateway's: nothing here
	// tokenizes anything, and a total that disagreed with the backend's own
	// billing figure would be worse than no total.
	//
	// MetricTokensTotal 按方向统计 token，数据来自后端上报。它是后端的计数而不是
	// 本 Gateway 的：这里不对任何内容做分词，一个与后端自身计费数字不一致的合计，
	// 比没有合计更糟。
	MetricTokensTotal = "gateway_tokens_total"
	// MetricOutputTokensPerSecond observes completion tokens divided by the
	// time the request took. It is only recorded when the backend reported a
	// completion token count, so a backend that reports no usage leaves the
	// distribution empty rather than filling it with zeros.
	//
	// MetricOutputTokensPerSecond 观测输出 token 数除以该请求耗时。只有后端上报了
	// 输出 token 数时才记录，因此不上报用量的后端会让这个分布保持为空，而不是往里
	// 灌零。
	MetricOutputTokensPerSecond = "gateway_output_tokens_per_second"
)

// Label keys. The set is closed, and deliberately holds no key for the model
// or the raw path.
//
// 标签键。集合是封闭的，并且刻意没有为模型名或原始路径留位置。
const (
	LabelEndpoint  = "endpoint"
	LabelStatus    = "status"
	LabelDirection = "direction"
)

// Endpoint label values, one per route this package serves plus the catch-all
// every other path collapses into.
//
// endpoint 标签取值，本包服务的每条路由各一个，外加一个把其余所有路径收拢进来的兜底值。
const (
	EndpointModels          = "models"
	EndpointChatCompletions = "chat_completions"
	EndpointEmbeddings      = "embeddings"
	EndpointOther           = "other"
)

// Token direction label values on MetricTokensTotal.
//
// MetricTokensTotal 上的 token 方向标签取值。
const (
	DirectionPrompt     = "prompt"
	DirectionCompletion = "completion"
)

// Descriptions is this package's metric catalogue, for a service to hand to
// metrics.New.
//
// Descriptions 是本包的指标目录，供服务交给 metrics.New。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricRequestsTotal: {
			Kind: metrics.KindCounter,
			Help: "Finished HTTP requests, by endpoint and status code.",
		},
		MetricRequestDurationSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Total response time, by endpoint.",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricInflightRequests: {
			Kind: metrics.KindGauge,
			Help: "Requests currently being served, by endpoint.",
		},
		MetricTTFTSeconds: {
			Kind:    metrics.KindHistogram,
			Help:    "Time from receiving a request to the first byte of content reaching the client.",
			Buckets: metrics.SecondsBuckets(),
		},
		MetricTokensTotal: {
			Kind: metrics.KindCounter,
			Help: "Tokens reported by the backend, by direction.",
		},
		MetricOutputTokensPerSecond: {
			Kind:    metrics.KindHistogram,
			Help:    "Completion tokens divided by the time the request took.",
			Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000},
		},
	}
}

// endpointFor maps a request path onto the closed endpoint vocabulary. An
// unrecognized path is "other" rather than itself, so an internet-facing
// listener's 404 traffic cannot become label cardinality.
//
// endpointFor 把请求路径映射到封闭的 endpoint 词汇上。无法识别的路径记为 "other"
// 而不是它本身，这样一个面向公网的监听器上的 404 流量就无法变成标签基数。
func endpointFor(path string) string {
	switch path {
	case "/v1/models":
		return EndpointModels
	case "/v1/chat/completions":
		return EndpointChatCompletions
	case "/v1/embeddings":
		return EndpointEmbeddings
	default:
		return EndpointOther
	}
}

// recorder is the front door's typed view of runtime.Metrics: every recording
// site calls one of its methods, so the label vocabulary above is enforced by
// the type system rather than by review.
//
// recorder 是前门对 runtime.Metrics 的类型化视图：每个记录点都调用它的某个方法，
// 因此上面那套标签词汇由类型系统而非评审来保证。
type recorder struct {
	sink runtime.Metrics

	// inflight holds one counter per endpoint. A gauge cannot be incremented
	// through runtime.Metrics — it only takes a value — so the count is kept
	// here and published on every change.
	//
	// inflight 为每个 endpoint 各持一个计数。通过 runtime.Metrics 无法对量表做自增
	// ——它只接受一个值——因此计数保存在此，每次变化时发布出去。
	mu       sync.Mutex
	inflight map[string]int
}

// newRecorder returns a recorder. A nil sink discards everything.
//
// newRecorder 返回一个记录器。sink 为 nil 时全部丢弃。
func newRecorder(sink runtime.Metrics) *recorder {
	if sink == nil {
		sink = discardMetrics{}
	}
	return &recorder{sink: sink, inflight: make(map[string]int)}
}

// RequestStarted records one request entering the handler and returns the
// function that records it leaving. Pairing the two in one call is what keeps
// the in-flight gauge honest: a caller cannot forget the decrement without
// also losing the increment.
//
// RequestStarted 记录一个请求进入处理器，并返回记录它离开的函数。把两者绑在一次调用
// 里，正是让并发量表保持诚实的办法：调用方不可能只忘掉减一而不同时失去加一。
func (r *recorder) RequestStarted(endpoint string) func(status int, d time.Duration) {
	r.addInflight(endpoint, 1)
	return func(status int, d time.Duration) {
		r.addInflight(endpoint, -1)
		r.sink.Counter(MetricRequestsTotal, map[string]string{
			LabelEndpoint: endpoint,
			LabelStatus:   strconv.Itoa(status),
		}).Add(1)
		r.sink.Histogram(MetricRequestDurationSeconds, map[string]string{
			LabelEndpoint: endpoint,
		}).Observe(d.Seconds())
	}
}

func (r *recorder) addInflight(endpoint string, delta int) {
	r.mu.Lock()
	r.inflight[endpoint] += delta
	n := r.inflight[endpoint]
	r.mu.Unlock()
	r.sink.Gauge(MetricInflightRequests, map[string]string{LabelEndpoint: endpoint}).Set(float64(n))
}

// TTFT observes the time to the first byte of content.
//
// TTFT 观测到第一个内容字节的时间。
func (r *recorder) TTFT(endpoint string, d time.Duration) {
	r.sink.Histogram(MetricTTFTSeconds, map[string]string{LabelEndpoint: endpoint}).Observe(d.Seconds())
}

// Usage records the token counts a backend reported for one request, and the
// output rate they imply. A zero count is not recorded: it means the backend
// said nothing, not that it produced nothing, and the two must not average
// together.
//
// Usage 记录后端为一次请求上报的 token 数，以及由此得出的输出速率。计数为零时不记录：
// 那意味着后端什么都没说，而不是它什么都没产出，这两件事不能被平均到一起。
func (r *recorder) Usage(usage runtime.Usage, d time.Duration) {
	if usage.PromptTokens > 0 {
		r.sink.Counter(MetricTokensTotal, map[string]string{LabelDirection: DirectionPrompt}).
			Add(float64(usage.PromptTokens))
	}
	if usage.CompletionTokens <= 0 {
		return
	}
	r.sink.Counter(MetricTokensTotal, map[string]string{LabelDirection: DirectionCompletion}).
		Add(float64(usage.CompletionTokens))
	if seconds := d.Seconds(); seconds > 0 {
		r.sink.Histogram(MetricOutputTokensPerSecond, nil).Observe(float64(usage.CompletionTokens) / seconds)
	}
}

// statusOf reports the status a finished response carried, defaulting to 200
// for a handler that wrote a body without ever calling WriteHeader.
//
// statusOf 返回一个已完成响应所携带的状态码；对于写了响应体却从未调用 WriteHeader 的
// 处理器，默认为 200。
func statusOf(w *statusWriter) int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
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
