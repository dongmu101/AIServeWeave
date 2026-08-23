package tunneltest

import (
	"sort"
	"sync"

	"AIServeWeave/common/runtime"
)

// Metrics is an in-memory runtime.Metrics that keeps every sample it is
// given, so a test can assert both on the numbers and on the labels they were
// recorded under. Label cardinality is the point of most of those assertions,
// which is why nothing here collapses or normalizes a label set.
//
// Every method is safe for concurrent use: the tunnel records from the
// connection loop, the maintenance loop and each slot's goroutine at once.
type Metrics struct {
	mu     sync.Mutex
	series map[string]*Series
	order  []string
}

// Series is one instrument identified by its name and its full label set.
type Series struct {
	// Name is the metric name.
	Name string
	// Labels is the label set this series was recorded under.
	Labels map[string]string

	mu     sync.Mutex
	value  float64
	count  int
	values []float64
}

// NewMetrics returns an empty collector.
func NewMetrics() *Metrics {
	return &Metrics{series: map[string]*Series{}}
}

// Counter implements runtime.Metrics.
func (m *Metrics) Counter(name string, labels map[string]string) runtime.Counter {
	return m.lookup(name, labels)
}

// Gauge implements runtime.Metrics.
func (m *Metrics) Gauge(name string, labels map[string]string) runtime.Gauge {
	return m.lookup(name, labels)
}

// Histogram implements runtime.Metrics.
func (m *Metrics) Histogram(name string, labels map[string]string) runtime.Histogram {
	return m.lookup(name, labels)
}

func (m *Metrics) lookup(name string, labels map[string]string) *Series {
	key := seriesKey(name, labels)

	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.series[key]
	if !ok {
		s = &Series{Name: name, Labels: cloneLabels(labels)}
		m.series[key] = s
		m.order = append(m.order, key)
	}
	return s
}

// Add implements runtime.Counter.
func (s *Series) Add(delta float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value += delta
	s.count++
	s.values = append(s.values, delta)
}

// Set implements runtime.Gauge.
func (s *Series) Set(value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	s.count++
	s.values = append(s.values, value)
}

// Observe implements runtime.Histogram.
func (s *Series) Observe(value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	s.count++
	s.values = append(s.values, value)
}

// Value reports a counter's total, a gauge's current value or a histogram's
// most recent observation.
func (s *Series) Value() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

// Count reports how many samples this series has taken.
func (s *Series) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// Values reports every sample in the order it was recorded.
func (s *Series) Values() []float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]float64(nil), s.values...)
}

// All returns every series recorded so far, in first-touch order.
func (m *Metrics) All() []*Series {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Series, 0, len(m.order))
	for _, key := range m.order {
		out = append(out, m.series[key])
	}
	return out
}

// Names lists the distinct metric names recorded so far, sorted.
func (m *Metrics) Names() []string {
	seen := map[string]bool{}
	for _, s := range m.All() {
		seen[s.Name] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Find returns the series named name whose labels include every pair in
// match, or nil. match is a filter, not the full label set, so a test names
// only the labels it cares about.
func (m *Metrics) Find(name string, match map[string]string) *Series {
	for _, s := range m.All() {
		if s.Name != name || !matches(s.Labels, match) {
			continue
		}
		return s
	}
	return nil
}

// Sum totals every series named name whose labels include every pair in
// match. A missing series totals zero, so a test can assert "this never
// happened" the same way it asserts a count.
func (m *Metrics) Sum(name string, match map[string]string) float64 {
	total := 0.0
	for _, s := range m.All() {
		if s.Name == name && matches(s.Labels, match) {
			total += s.Value()
		}
	}
	return total
}

// SeriesCount reports how many distinct label sets exist under name, which is
// the number a cardinality assertion is really about.
func (m *Metrics) SeriesCount(name string) int {
	n := 0
	for _, s := range m.All() {
		if s.Name == name {
			n++
		}
	}
	return n
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
func seriesKey(name string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	key := name
	for _, k := range keys {
		key += "|" + k + "=" + labels[k]
	}
	return key
}
