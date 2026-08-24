// Package metricstest provides the in-memory runtime.Metrics every service's
// tests record against. It keeps each sample under the full label set it was
// given and normalizes nothing, because label cardinality — not the numbers —
// is what most assertions against it are actually about.
//
// It lives beside the real registry rather than inside one service, because
// every service in this repository asks the same two questions of its metrics
// (did this get recorded, and under exactly which labels), and a per-service
// copy of the answer is a per-service opportunity for the answers to differ.
// The Agent's tunnel package predates this one and still carries its own
// equivalent under tunnel/internal/tunneltest; new tests use this package.
//
// metricstest 提供各服务测试所记录的内存版 runtime.Metrics。它按拿到的完整标签集
// 保存每个样本，且不做任何归一化，因为针对它的多数断言真正关心的是标签基数，而不是
// 数值本身。
//
// 它与真实注册表放在一起，而不是放进某一个服务里，因为本仓库每个服务对自己的指标问
// 的都是同两个问题（有没有被记录、记在了哪一组标签下），而每个服务各存一份答案，就
// 是每个服务各有一次让答案产生分歧的机会。Agent 的 tunnel 包早于本包存在，仍带着
// 自己那份等价实现（tunnel/internal/tunneltest）；新写的测试用本包。
package metricstest

import (
	"sort"
	"strings"
	"sync"

	"AIServeWeave/common/runtime"
)

// Collector is an in-memory runtime.Metrics that keeps every sample it is
// given. Every method is safe for concurrent use, because the code under test
// records from several goroutines at once.
//
// Collector 是保存所有样本的内存版 runtime.Metrics。所有方法都可并发使用，因为被测
// 代码会同时从多个协程记录。
type Collector struct {
	mu     sync.Mutex
	series map[string]*Series
	order  []string
}

// Series is one instrument identified by its name and its full label set.
//
// Series 是由名称与完整标签集共同标识的一个仪器。
type Series struct {
	// Name is the metric name.
	//
	// Name 是指标名。
	Name string
	// Labels is the label set this series was recorded under.
	//
	// Labels 是这条序列被记录时所带的标签集。
	Labels map[string]string

	mu     sync.Mutex
	value  float64
	count  int
	values []float64
}

// New returns an empty collector.
//
// New 返回一个空的收集器。
func New() *Collector {
	return &Collector{series: map[string]*Series{}}
}

// Counter implements runtime.Metrics.
//
// Counter 实现 runtime.Metrics。
func (c *Collector) Counter(name string, labels map[string]string) runtime.Counter {
	return c.lookup(name, labels)
}

// Gauge implements runtime.Metrics.
//
// Gauge 实现 runtime.Metrics。
func (c *Collector) Gauge(name string, labels map[string]string) runtime.Gauge {
	return c.lookup(name, labels)
}

// Histogram implements runtime.Metrics.
//
// Histogram 实现 runtime.Metrics。
func (c *Collector) Histogram(name string, labels map[string]string) runtime.Histogram {
	return c.lookup(name, labels)
}

func (c *Collector) lookup(name string, labels map[string]string) *Series {
	key := seriesKey(name, labels)

	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.series[key]
	if !ok {
		s = &Series{Name: name, Labels: cloneLabels(labels)}
		c.series[key] = s
		c.order = append(c.order, key)
	}
	return s
}

// Add implements runtime.Counter.
//
// Add 实现 runtime.Counter。
func (s *Series) Add(delta float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value += delta
	s.count++
	s.values = append(s.values, delta)
}

// Set implements runtime.Gauge.
//
// Set 实现 runtime.Gauge。
func (s *Series) Set(value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	s.count++
	s.values = append(s.values, value)
}

// Observe implements runtime.Histogram.
//
// Observe 实现 runtime.Histogram。
func (s *Series) Observe(value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	s.count++
	s.values = append(s.values, value)
}

// Value reports a counter's total, a gauge's current value, or a histogram's
// most recent observation.
//
// Value 返回计数器的总量、量表的当前值，或直方图最近一次的观测值。
func (s *Series) Value() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

// Count reports how many samples this series has taken.
//
// Count 返回这条序列已接收多少个样本。
func (s *Series) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// Values reports every sample in the order it was recorded.
//
// Values 按记录顺序返回全部样本。
func (s *Series) Values() []float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]float64(nil), s.values...)
}

// All returns every series recorded so far, in first-touch order.
//
// All 按首次触及的顺序返回目前记录的全部序列。
func (c *Collector) All() []*Series {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Series, 0, len(c.order))
	for _, key := range c.order {
		out = append(out, c.series[key])
	}
	return out
}

// Names lists the distinct metric names recorded so far, sorted.
//
// Names 返回目前记录过的不重复指标名，已排序。
func (c *Collector) Names() []string {
	seen := map[string]bool{}
	for _, s := range c.All() {
		seen[s.Name] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Find returns the series named name whose labels include every pair in match,
// or nil. match is a filter, not the full label set, so a test names only the
// labels it cares about.
//
// Find 返回名为 name 且标签包含 match 中每一对的序列，没有则返回 nil。match 是过滤
// 条件而不是完整标签集，因此测试只需写出它关心的那些标签。
func (c *Collector) Find(name string, match map[string]string) *Series {
	for _, s := range c.All() {
		if s.Name == name && matches(s.Labels, match) {
			return s
		}
	}
	return nil
}

// Sum totals every series named name whose labels include every pair in match.
// A missing series totals zero, so a test can assert "this never happened" the
// same way it asserts a count.
//
// Sum 汇总名为 name 且标签包含 match 中每一对的所有序列。不存在的序列合计为零，
// 因此测试可以用断言计数的同一种写法断言「这件事从未发生」。
func (c *Collector) Sum(name string, match map[string]string) float64 {
	total := 0.0
	for _, s := range c.All() {
		if s.Name == name && matches(s.Labels, match) {
			total += s.Value()
		}
	}
	return total
}

// SeriesCount reports how many distinct label sets exist under name, which is
// the number a cardinality assertion is really about.
//
// SeriesCount 返回 name 之下有多少组不同的标签集，这正是基数断言真正要看的数字。
func (c *Collector) SeriesCount(name string) int {
	n := 0
	for _, s := range c.All() {
		if s.Name == name {
			n++
		}
	}
	return n
}

// LabelKeys returns the union of label keys seen under name, sorted. It is
// what a cardinality test asserts against: the set of keys a metric carries is
// part of its contract, and a key appearing on only some series of one family
// is a defect either way.
//
// LabelKeys 返回 name 之下出现过的标签键并集，已排序。基数测试就是拿它做断言的：
// 一个指标携带哪些键属于它契约的一部分，而某个键只出现在同一族的部分序列上，无论
// 哪种情况都是缺陷。
func (c *Collector) LabelKeys(name string) []string {
	seen := map[string]bool{}
	for _, s := range c.All() {
		if s.Name != name {
			continue
		}
		for k := range s.Labels {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func matches(labels, match map[string]string) bool {
	for k, v := range match {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func cloneLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}

// seriesKey renders a name and its labels as a stable identity.
//
// seriesKey 把名称与标签渲染成稳定的标识。
func seriesKey(name string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(labels[k])
	}
	return b.String()
}
