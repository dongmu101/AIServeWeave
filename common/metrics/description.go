// Package metrics implements the metrics sink the whole fleet records against:
// an in-process registry satisfying runtime.Metrics, and the Prometheus text
// exposition each service serves on its own /metrics endpoint.
//
// It deliberately has no third-party dependency. The exposition format is a
// stable, documented text protocol, and the alternative — a client library —
// would pull a transitive tree into every binary in exchange for code this
// package states in a few hundred lines. AGENTS.md's rule applies: what the
// standard library can do, no dependency does.
//
// The registry does not invent metric names. Each package that records owns a
// Descriptions table naming its own instruments, their help text and, for a
// histogram, its buckets; a service hands those tables to New at startup. That
// keeps a metric's definition in the same file as its recording sites, which
// is what stops help text and bucket choices from drifting away from the code
// that feeds them.
//
// metrics 包实现全集群共用的指标下沉端：一个满足 runtime.Metrics 的进程内注册表，
// 以及各服务在自己 /metrics 端点上导出的 Prometheus 文本格式。
//
// 它刻意不引入第三方依赖。导出格式本身是稳定且有文档的文本协议，而换成客户端库
// 就是为几百行能写清楚的代码，往每个二进制里拖进一整棵传递依赖。AGENTS.md 的
// 规则在这里适用：标准库能解决的不引入依赖。
//
// 注册表不自造指标名。每个做记录的包各自持有一张 Descriptions 表，声明自己的
// 指标、help 文本以及（直方图的）分桶；服务启动时把这些表交给 New。这样指标的
// 定义与它的记录点留在同一个文件里，help 文本和分桶选择才不会与喂数据的代码脱节。
package metrics

import "math"

// Kind is the instrument type a metric name is bound to. A name carries
// exactly one kind for the life of the process: the same name recorded once as
// a counter and once as a gauge is a bug in the caller, not a metric with two
// meanings.
//
// Kind 是一个指标名绑定的仪器类型。一个名字在整个进程生命周期内只有一种类型：
// 同一个名字既当计数器又当量表记录，是调用方的缺陷，不是一个有两种含义的指标。
type Kind int

// The three instrument kinds, matching runtime.Metrics' three methods.
//
// 三种仪器类型，与 runtime.Metrics 的三个方法一一对应。
const (
	// KindCounter is a monotonically increasing total.
	//
	// KindCounter 是单调递增的累计值。
	KindCounter Kind = iota + 1
	// KindGauge is a value that may move in either direction.
	//
	// KindGauge 是可增可减的瞬时值。
	KindGauge
	// KindHistogram is a bucketed distribution of observations.
	//
	// KindHistogram 是按桶统计的观测值分布。
	KindHistogram
)

// String renders the kind as the token the exposition's # TYPE line uses.
//
// String 返回该类型在导出格式 # TYPE 行中使用的记号。
func (k Kind) String() string {
	switch k {
	case KindCounter:
		return "counter"
	case KindGauge:
		return "gauge"
	case KindHistogram:
		return "histogram"
	default:
		return "untyped"
	}
}

// Description is the metadata one metric name carries. Help is what a person
// reading a dashboard sees; Buckets applies to KindHistogram only and is
// ignored for the other two.
//
// Description 是一个指标名携带的元数据。Help 是看板前的人会读到的说明；Buckets
// 只对 KindHistogram 有意义，另外两种类型会忽略它。
type Description struct {
	Kind    Kind
	Help    string
	Buckets []float64
}

// Descriptions is one package's metric catalogue, keyed by metric name.
//
// Descriptions 是一个包的指标目录，以指标名为键。
type Descriptions map[string]Description

// SecondsBuckets returns the default bucket boundaries for a latency
// histogram, in seconds. It spans a local round trip (5ms) to a slow cold
// start (60s), because this fleet measures both on the same instrument family:
// a tunnel hop and a model's first token differ by four orders of magnitude
// and a single set of buckets has to keep both readable.
//
// It returns a fresh slice on every call, so a caller that trims or extends
// the defaults cannot change what the next caller gets.
//
// SecondsBuckets 返回延迟直方图的默认分桶边界，单位为秒。范围从一次本机往返
// （5ms）到一次缓慢的冷启动（60s）——本集群用同一族仪器同时测这两者：隧道单跳
// 与模型首 token 相差四个数量级，一套分桶必须让两边都还能读。
//
// 每次调用返回新的切片，因此调用方裁剪或扩展默认值不会影响下一个调用方拿到的结果。
func SecondsBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
}

// BytesBuckets returns the default bucket boundaries for a payload size
// histogram, in bytes. The top bucket is the tunnel's default frame limit, so
// an observation landing in +Inf means a frame that should have been rejected.
//
// BytesBuckets 返回负载大小直方图的默认分桶边界，单位为字节。最上面一个桶就是
// 隧道的默认帧上限，因此落进 +Inf 的观测意味着出现了本该被拒绝的帧。
func BytesBuckets() []float64 {
	return []float64{256, 1 << 10, 4 << 10, 16 << 10, 64 << 10, 256 << 10, 1 << 20, 4 << 20}
}

// TokenBuckets returns the default bucket boundaries for a token count
// histogram. It stops at 131072 because a context that large is already a
// different conversation from the ones the smaller buckets describe.
//
// TokenBuckets 返回 token 数直方图的默认分桶边界。上限取到 131072，因为再大的
// 上下文与小桶所描述的请求已经不是同一类对话了。
func TokenBuckets() []float64 {
	return []float64{16, 64, 256, 1024, 4096, 16384, 65536, 131072}
}

// normalizeBuckets returns buckets sorted ascending and free of duplicates,
// NaNs and an explicit +Inf. The registry appends the +Inf bucket itself, so
// accepting one here would produce a family with two of them.
//
// normalizeBuckets 返回升序、去重，且不含 NaN 与显式 +Inf 的分桶。注册表自己会
// 追加 +Inf 桶，若此处放行一个，导出的指标族里就会出现两个。
func normalizeBuckets(buckets []float64) []float64 {
	out := make([]float64, 0, len(buckets))
	for _, b := range buckets {
		if math.IsNaN(b) || math.IsInf(b, 0) {
			continue
		}
		out = append(out, b)
	}
	sortFloats(out)
	deduped := out[:0]
	for i, b := range out {
		if i > 0 && b == out[i-1] {
			continue
		}
		deduped = append(deduped, b)
	}
	return deduped
}

// sortFloats sorts in place. It is insertion sort rather than sort.Slice
// because bucket lists are a dozen values long and this runs once per metric
// at registration.
//
// sortFloats 原地排序。用插入排序而不是 sort.Slice，是因为分桶列表只有十来个
// 元素，且这段代码在注册时每个指标只跑一次。
func sortFloats(v []float64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
