package metrics

import (
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"AIServeWeave/common/runtime"
)

// MetricConflictsTotal counts calls that asked for a metric name under a kind
// other than the one it was described with. The registry answers those with a
// discarding instrument rather than crashing a serving process over a
// bookkeeping mistake, so this counter is the only place such a mistake
// becomes visible. It should read zero forever; anything else is a defect in
// whichever package recorded.
//
// MetricConflictsTotal 统计以与声明类型不符的类型索取某指标名的调用次数。注册表
// 对这类调用返回丢弃仪器，而不是让一个正在服务的进程因为记账失误而崩溃，因此这个
// 计数器是这类失误唯一可见的地方。它应当永远为零；不为零就说明某个记录方有缺陷。
const MetricConflictsTotal = "aisw_metric_conflicts_total"

// Registry is the process-wide metrics sink. It implements runtime.Metrics, so
// the runtime layer, the tunnel and the HTTP front door all record into one
// registry and one /metrics endpoint — a node with two metric backends is a
// node whose numbers cannot be compared.
//
// The zero value is not usable; construct one with New.
//
// Registry 是进程级的指标下沉端。它实现 runtime.Metrics，因此运行时层、隧道与
// HTTP 前门都记录进同一个注册表、同一个 /metrics 端点——一个节点有两套指标后端，
// 就是一个数字之间无法互相对照的节点。
//
// 零值不可用，用 New 构造。
type Registry struct {
	// descs is written once by New and only read afterwards, so it needs no
	// lock: a metric whose description arrives after the first recording
	// would produce an exposition whose HELP text changes under a scraper.
	//
	// descs 由 New 一次写入，之后只读，因此不需要锁：一个描述晚于首次记录才到达
	// 的指标，会让抓取方看到 HELP 文本中途变化的导出结果。
	descs Descriptions

	mu       sync.RWMutex
	families map[string]*family

	conflicts atomic.Int64
}

// New returns a registry serving the union of the given catalogues. Later
// catalogues do not overwrite earlier ones: a name described twice keeps its
// first description, and the duplicate is reported through
// MetricConflictsTotal, because two packages disagreeing about one metric's
// meaning is exactly the condition that must not be resolved silently.
//
// A metric recorded without a description still records. Losing data because
// somebody forgot a help string would be a worse trade than an exposition with
// one bare series in it; DescribedNames lets a test assert the omission
// instead.
//
// New 返回一个注册表，服务于所给若干目录的并集。后面的目录不会覆盖前面的：同一个
// 名字被描述两次时保留第一份描述，重复项通过 MetricConflictsTotal 上报——两个包
// 对同一指标含义有分歧，正是不该被悄悄消化掉的情况。
//
// 没有描述的指标照样记录。因为谁漏写了一句 help 就丢数据，比导出里多一条光秃秃的
// 序列更糟；要断言这种遗漏，用 DescribedNames 在测试里做。
func New(catalogues ...Descriptions) *Registry {
	r := &Registry{
		descs:    make(Descriptions),
		families: make(map[string]*family),
	}
	for _, catalogue := range append([]Descriptions{builtinDescriptions()}, catalogues...) {
		for name, desc := range catalogue {
			if existing, ok := r.descs[name]; ok {
				if existing.Kind != desc.Kind || existing.Help != desc.Help {
					r.conflicts.Add(1)
				}
				continue
			}
			desc.Buckets = normalizeBuckets(desc.Buckets)
			r.descs[name] = desc
		}
	}
	return r
}

// DescribedNames returns every metric name this registry has a description
// for, sorted. Tests use it to assert that a package's metric constants and
// its catalogue have not drifted apart.
//
// DescribedNames 返回该注册表持有描述的全部指标名，已排序。测试用它断言某个包的
// 指标常量与它的目录没有各走各的。
func (r *Registry) DescribedNames() []string {
	names := make([]string, 0, len(r.descs))
	for name := range r.descs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Counter returns the counter identified by name and labels, creating it on
// first use.
//
// Counter 返回由 name 与 labels 标识的计数器，首次使用时创建。
func (r *Registry) Counter(name string, labels map[string]string) runtime.Counter {
	s := r.series(name, KindCounter, labels)
	if s == nil {
		return discardInstrument{}
	}
	return counterInstrument{s: s}
}

// Gauge returns the gauge identified by name and labels, creating it on first
// use.
//
// Gauge 返回由 name 与 labels 标识的量表，首次使用时创建。
func (r *Registry) Gauge(name string, labels map[string]string) runtime.Gauge {
	s := r.series(name, KindGauge, labels)
	if s == nil {
		return discardInstrument{}
	}
	return gaugeInstrument{s: s}
}

// Histogram returns the histogram identified by name and labels, creating it
// on first use.
//
// Histogram 返回由 name 与 labels 标识的直方图，首次使用时创建。
func (r *Registry) Histogram(name string, labels map[string]string) runtime.Histogram {
	s := r.series(name, KindHistogram, labels)
	if s == nil {
		return discardInstrument{}
	}
	return histogramInstrument{s: s}
}

// series resolves one (name, labels) pair to its storage, creating the family
// and the series as needed. It returns nil when the name is already bound to
// another kind.
//
// series 把一组 (name, labels) 解析到它的存储，按需创建指标族与序列。当该名字已
// 绑定到另一种类型时返回 nil。
func (r *Registry) series(name string, kind Kind, labels map[string]string) *series {
	f := r.family(name, kind)
	if f == nil {
		return nil
	}
	return f.series(labels)
}

// family returns the family for name, creating it on first use. The kind comes
// from the description when there is one, so a described counter recorded
// through Gauge is a conflict rather than a silently retyped metric.
//
// family 返回 name 对应的指标族，首次使用时创建。类型优先取自描述，因此一个被声明
// 为计数器的指标若从 Gauge 记录，就是一次冲突，而不是被悄悄改了类型的指标。
func (r *Registry) family(name string, kind Kind) *family {
	r.mu.RLock()
	f, ok := r.families[name]
	r.mu.RUnlock()
	if ok {
		if f.kind != kind {
			r.conflicts.Add(1)
			return nil
		}
		return f
	}

	desc, described := r.descs[name]
	if described && desc.Kind != kind {
		r.conflicts.Add(1)
		return nil
	}
	if !described {
		desc = Description{Kind: kind}
	}
	if desc.Kind == KindHistogram && len(desc.Buckets) == 0 {
		desc.Buckets = SecondsBuckets()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Another goroutine may have created it between the read unlock and here.
	//
	// 在解读锁与此处之间，可能已有别的协程创建了它。
	if f, ok := r.families[name]; ok {
		if f.kind != kind {
			r.conflicts.Add(1)
			return nil
		}
		return f
	}
	f = &family{
		name:    name,
		kind:    kind,
		desc:    desc,
		entries: make(map[string]*series),
	}
	r.families[name] = f
	return f
}

// family is every series sharing one metric name.
//
// family 是共享同一个指标名的全部序列。
type family struct {
	name string
	kind Kind
	desc Description

	mu      sync.RWMutex
	entries map[string]*series
}

// series returns the series for labels, creating it on first use.
//
// series 返回 labels 对应的序列，首次使用时创建。
func (f *family) series(labels map[string]string) *series {
	key := labelKey(labels)

	f.mu.RLock()
	s, ok := f.entries[key]
	f.mu.RUnlock()
	if ok {
		return s
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.entries[key]; ok {
		return s
	}
	s = &series{labels: copyLabels(labels)}
	if f.kind == KindHistogram {
		s.hist = newHistogram(f.desc.Buckets)
	}
	f.entries[key] = s
	return s
}

// snapshot returns this family's series sorted by their exposition label
// string, so two scrapes of an unchanged registry produce byte-identical
// output.
//
// snapshot 返回该指标族的全部序列，按导出用的标签串排序，因此对未变化的注册表
// 抓取两次会得到逐字节相同的输出。
func (f *family) snapshot() []*series {
	f.mu.RLock()
	out := make([]*series, 0, len(f.entries))
	for _, s := range f.entries {
		out = append(out, s)
	}
	f.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].sortKey() < out[j].sortKey() })
	return out
}

// series is one labelled time series. A counter or gauge stores its value in
// bits as the IEEE-754 bit pattern of a float64; a histogram stores buckets.
//
// series 是一条带标签的时间序列。计数器与量表把值以 float64 的 IEEE-754 位模式
// 存在 bits 里；直方图则存分桶。
type series struct {
	labels map[string]string
	bits   atomic.Uint64
	hist   *histogram

	sortOnce sync.Once
	sorted   string
}

// sortKey returns a stable ordering key derived from the labels.
//
// sortKey 返回一个由标签导出的稳定排序键。
func (s *series) sortKey() string {
	s.sortOnce.Do(func() {
		keys := make([]string, 0, len(s.labels))
		for k := range s.labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(k)
			b.WriteByte(0)
			b.WriteString(s.labels[k])
			b.WriteByte(0)
		}
		s.sorted = b.String()
	})
	return s.sorted
}

// add increases the stored float by delta.
//
// add 把存储的浮点值增加 delta。
func (s *series) add(delta float64) {
	for {
		old := s.bits.Load()
		next := math.Float64bits(math.Float64frombits(old) + delta)
		if s.bits.CompareAndSwap(old, next) {
			return
		}
	}
}

// set replaces the stored float.
//
// set 替换存储的浮点值。
func (s *series) set(v float64) {
	s.bits.Store(math.Float64bits(v))
}

// value reads the stored float.
//
// value 读取存储的浮点值。
func (s *series) value() float64 {
	return math.Float64frombits(s.bits.Load())
}

// histogram is one series' bucketed distribution. A mutex rather than atomics:
// a bucket increment, the sum and the count must move together, or a scrape
// landing between them reports a distribution that never existed.
//
// histogram 是一条序列的分桶分布。用互斥锁而非原子操作：桶计数、总和与总次数必须
// 一起变，否则夹在中间的一次抓取会报出一个从未存在过的分布。
type histogram struct {
	bounds []float64

	mu     sync.Mutex
	counts []uint64
	sum    float64
	count  uint64
}

func newHistogram(bounds []float64) *histogram {
	return &histogram{
		bounds: bounds,
		counts: make([]uint64, len(bounds)),
	}
}

// observe records one value.
//
// observe 记录一次观测。
func (h *histogram) observe(v float64) {
	// NaN belongs in no bucket and would poison the sum for good; dropping it
	// keeps a single bad sample from making the whole series unreadable.
	//
	// NaN 不属于任何一个桶，且会永久污染总和；丢弃它可以避免一个坏样本让整条
	// 序列从此不可读。
	if math.IsNaN(v) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, bound := range h.bounds {
		if v <= bound {
			h.counts[i]++
		}
	}
	h.sum += v
	h.count++
}

// snapshot returns cumulative bucket counts, the sum and the total count. The
// counts are already cumulative because observe increments every bucket a
// value falls into, which is the form the exposition needs.
//
// snapshot 返回累积桶计数、总和与总次数。计数本身已是累积形式——observe 会对一个
// 值所落入的每个桶都加一，这正是导出格式需要的形态。
func (h *histogram) snapshot() (counts []uint64, sum float64, count uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	counts = make([]uint64, len(h.counts))
	copy(counts, h.counts)
	return counts, h.sum, h.count
}

// counterInstrument implements runtime.Counter.
//
// counterInstrument 实现 runtime.Counter。
type counterInstrument struct{ s *series }

// Add increases the counter. A negative delta is dropped: a counter that can
// go backwards makes every rate() over it wrong, and the caller passing one
// has a bug that silently corrupting the series would only hide.
//
// Add 增加计数器。负增量会被丢弃：一个能倒退的计数器会让基于它的每一次 rate()
// 都失真，而传入负值的调用方本身有缺陷——悄悄破坏这条序列只会把缺陷藏起来。
func (c counterInstrument) Add(delta float64) {
	if delta < 0 || math.IsNaN(delta) {
		return
	}
	c.s.add(delta)
}

// gaugeInstrument implements runtime.Gauge.
//
// gaugeInstrument 实现 runtime.Gauge。
type gaugeInstrument struct{ s *series }

// Set replaces the gauge's value.
//
// Set 替换量表的值。
func (g gaugeInstrument) Set(value float64) { g.s.set(value) }

// histogramInstrument implements runtime.Histogram.
//
// histogramInstrument 实现 runtime.Histogram。
type histogramInstrument struct{ s *series }

// Observe records one value.
//
// Observe 记录一次观测值。
func (h histogramInstrument) Observe(value float64) { h.s.hist.observe(value) }

// discardInstrument is returned for a name whose kind conflicts with its
// description. It satisfies all three instrument interfaces so the caller's
// recording site stays unconditional.
//
// discardInstrument 用于类型与描述冲突的指标名。它同时满足三个仪器接口，因此
// 调用方的记录点无需增加条件判断。
type discardInstrument struct{}

func (discardInstrument) Add(float64)     {}
func (discardInstrument) Set(float64)     {}
func (discardInstrument) Observe(float64) {}

// labelKey encodes a label set into a map key. NUL separates keys from values
// and pairs from each other: it cannot appear in a metric label this fleet
// produces — every label value comes from a closed enumeration or from local
// configuration — so no two distinct label sets can collide on one key.
//
// labelKey 把一组标签编码成 map 的键。用 NUL 分隔键与值、以及各对之间：本集群产生
// 的标签值不可能含 NUL——所有标签值要么来自封闭枚举，要么来自本地配置——因此两组
// 不同的标签不会碰撞到同一个键上。
func labelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(labels[k])
		b.WriteByte(0)
	}
	return b.String()
}

// copyLabels takes the caller's map out of play: recording sites build a fresh
// map per call today, but a series that aliased a caller's map would change
// value silently the day one of them stops.
//
// copyLabels 把调用方的 map 复制出来：今天的记录点每次调用都新建一个 map，但只要
// 有一处不再这么做，别名了调用方 map 的序列就会悄悄变值。
func copyLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}
