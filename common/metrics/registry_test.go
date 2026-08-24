package metrics

import (
	"math"
	"strings"
	"sync"
	"testing"
)

// testCatalogue is the description table the tests in this package record
// against. It mirrors the shape a real service hands to New: one counter, one
// gauge and two histograms with different bucket families.
//
// testCatalogue 是本包测试所记录的描述表。它复刻了真实服务交给 New 的形态：
// 一个计数器、一个量表，以及两个使用不同分桶族的直方图。
func testCatalogue() Descriptions {
	return Descriptions{
		"test_requests_total": {Kind: KindCounter, Help: "Requests handled."},
		"test_slots":          {Kind: KindGauge, Help: "Slots currently parked."},
		"test_latency_seconds": {
			Kind:    KindHistogram,
			Help:    "Request latency.",
			Buckets: []float64{0.1, 1},
		},
		"test_frame_bytes": {
			Kind:    KindHistogram,
			Help:    "Frame size.",
			Buckets: []float64{1024, 4096},
		},
	}
}

func TestRegistryRecordsEachInstrumentKind(t *testing.T) {
	tests := []struct {
		name   string
		record func(r *Registry)
		want   []string
	}{
		{
			name: "counter accumulates across calls",
			record: func(r *Registry) {
				r.Counter("test_requests_total", map[string]string{"result": "success"}).Add(2)
				r.Counter("test_requests_total", map[string]string{"result": "success"}).Add(3)
			},
			want: []string{`test_requests_total{result="success"} 5`},
		},
		{
			name: "counter keeps distinct label sets apart",
			record: func(r *Registry) {
				r.Counter("test_requests_total", map[string]string{"result": "success"}).Add(1)
				r.Counter("test_requests_total", map[string]string{"result": "timeout"}).Add(7)
			},
			want: []string{
				`test_requests_total{result="success"} 1`,
				`test_requests_total{result="timeout"} 7`,
			},
		},
		{
			name: "counter drops a negative delta",
			record: func(r *Registry) {
				c := r.Counter("test_requests_total", nil)
				c.Add(4)
				c.Add(-10)
			},
			want: []string{`test_requests_total 4`},
		},
		{
			name: "gauge reports the last value written",
			record: func(r *Registry) {
				g := r.Gauge("test_slots", map[string]string{"class": "inference"})
				g.Set(8)
				g.Set(3)
			},
			want: []string{`test_slots{class="inference"} 3`},
		},
		{
			name: "gauge accepts a decrease",
			record: func(r *Registry) {
				g := r.Gauge("test_slots", nil)
				g.Set(5)
				g.Set(0)
			},
			want: []string{`test_slots 0`},
		},
		{
			name: "histogram buckets are cumulative",
			record: func(r *Registry) {
				h := r.Histogram("test_latency_seconds", nil)
				h.Observe(0.05)
				h.Observe(0.5)
				h.Observe(30)
			},
			want: []string{
				`test_latency_seconds_bucket{le="0.1"} 1`,
				`test_latency_seconds_bucket{le="1"} 2`,
				`test_latency_seconds_bucket{le="+Inf"} 3`,
				`test_latency_seconds_sum 30.55`,
				`test_latency_seconds_count 3`,
			},
		},
		{
			name: "histogram counts an observation exactly on a bound",
			record: func(r *Registry) {
				r.Histogram("test_latency_seconds", nil).Observe(0.1)
			},
			want: []string{
				`test_latency_seconds_bucket{le="0.1"} 1`,
				`test_latency_seconds_count 1`,
			},
		},
		{
			name: "histogram drops NaN",
			record: func(r *Registry) {
				h := r.Histogram("test_latency_seconds", nil)
				h.Observe(math.NaN())
				h.Observe(0.05)
			},
			want: []string{
				`test_latency_seconds_sum 0.05`,
				`test_latency_seconds_count 1`,
			},
		},
		{
			name: "le carries the series labels alongside it",
			record: func(r *Registry) {
				r.Histogram("test_frame_bytes", map[string]string{"direction": "inbound"}).Observe(2048)
			},
			want: []string{
				`test_frame_bytes_bucket{direction="inbound",le="1024"} 0`,
				`test_frame_bytes_bucket{direction="inbound",le="4096"} 1`,
				`test_frame_bytes_bucket{direction="inbound",le="+Inf"} 1`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(testCatalogue())
			tt.record(r)
			got := render(t, r)
			for _, want := range tt.want {
				if !containsLine(got, want) {
					t.Errorf("exposition is missing a line\nwant line: %s\ngot:\n%s", want, got)
				}
			}
		})
	}
}

func TestRegistryLabelHandling(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{
			name:   "labels are emitted in sorted order",
			labels: map[string]string{"replica_id": "r1", "node_id": "n1", "class": "bulk"},
			want:   `test_requests_total{class="bulk",node_id="n1",replica_id="r1"} 1`,
		},
		{
			name:   "no labels emits no brace group",
			labels: nil,
			want:   `test_requests_total 1`,
		},
		{
			name:   "a quote in a value is escaped",
			labels: map[string]string{"result": `say "hi"`},
			want:   `test_requests_total{result="say \"hi\""} 1`,
		},
		{
			name:   "a backslash in a value is escaped",
			labels: map[string]string{"result": `a\b`},
			want:   `test_requests_total{result="a\\b"} 1`,
		},
		{
			name:   "a newline in a value is escaped",
			labels: map[string]string{"result": "a\nb"},
			want:   `test_requests_total{result="a\nb"} 1`,
		},
		{
			name:   "an invalid label name is dropped",
			labels: map[string]string{"result": "ok", "not-a-name": "x"},
			want:   `test_requests_total{result="ok"} 1`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(testCatalogue())
			r.Counter("test_requests_total", tt.labels).Add(1)
			got := render(t, r)
			if !containsLine(got, tt.want) {
				t.Errorf("exposition is missing a line\nwant line: %s\ngot:\n%s", tt.want, got)
			}
		})
	}
}

// TestRegistryLabelSetsDoNotCollide guards the NUL-delimited key encoding: two
// label sets whose flattened text is identical must still be two series.
//
// TestRegistryLabelSetsDoNotCollide 守住 NUL 分隔的键编码：两组展平后文本相同的
// 标签，仍然必须是两条序列。
func TestRegistryLabelSetsDoNotCollide(t *testing.T) {
	r := New(testCatalogue())
	r.Counter("test_requests_total", map[string]string{"a": "b", "c": "d"}).Add(1)
	r.Counter("test_requests_total", map[string]string{"a": "bcd"}).Add(2)

	got := render(t, r)
	for _, want := range []string{
		`test_requests_total{a="b",c="d"} 1`,
		`test_requests_total{a="bcd"} 2`,
	} {
		if !containsLine(got, want) {
			t.Errorf("exposition is missing a line\nwant line: %s\ngot:\n%s", want, got)
		}
	}
}

func TestRegistryKindConflict(t *testing.T) {
	tests := []struct {
		name string
		use  func(r *Registry)
	}{
		{
			name: "described counter recorded as a gauge",
			use:  func(r *Registry) { r.Gauge("test_requests_total", nil).Set(9) },
		},
		{
			name: "described counter recorded as a histogram",
			use:  func(r *Registry) { r.Histogram("test_requests_total", nil).Observe(9) },
		},
		{
			name: "described gauge recorded as a counter",
			use:  func(r *Registry) { r.Counter("test_slots", nil).Add(9) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(testCatalogue())
			tt.use(r)

			got := render(t, r)
			if !containsLine(got, MetricConflictsTotal+" 1") {
				t.Errorf("a kind conflict was not reported through %s\ngot:\n%s", MetricConflictsTotal, got)
			}
			if strings.Contains(got, " 9") {
				t.Errorf("a conflicting record reached the exposition\ngot:\n%s", got)
			}
		})
	}
}

// TestRegistryUndescribedMetricStillRecords states the deliberate trade in
// New's doc comment: a missing description costs the HELP line, never the
// data.
//
// TestRegistryUndescribedMetricStillRecords 说明 New 文档注释里那个有意的取舍：
// 缺少描述只会失去 HELP 行，永远不会失去数据。
func TestRegistryUndescribedMetricStillRecords(t *testing.T) {
	r := New(testCatalogue())
	r.Counter("test_undescribed_total", nil).Add(4)

	got := render(t, r)
	if !containsLine(got, "test_undescribed_total 4") {
		t.Errorf("an undescribed metric did not record\ngot:\n%s", got)
	}
	if containsLine(got, "# HELP test_undescribed_total ") {
		t.Errorf("an undescribed metric emitted a HELP line\ngot:\n%s", got)
	}
	if !containsLine(got, "# TYPE test_undescribed_total counter") {
		t.Errorf("an undescribed metric emitted no TYPE line\ngot:\n%s", got)
	}
}

// TestRegistryDuplicateDescriptionKeepsTheFirst asserts New's rule for two
// packages describing one name: first wins, disagreement is reported.
//
// TestRegistryDuplicateDescriptionKeepsTheFirst 断言 New 对「两个包描述同一个名字」
// 的规则：先到者胜，分歧被上报。
func TestRegistryDuplicateDescriptionKeepsTheFirst(t *testing.T) {
	first := Descriptions{"dup_total": {Kind: KindCounter, Help: "first."}}
	second := Descriptions{"dup_total": {Kind: KindCounter, Help: "second."}}

	r := New(first, second)
	r.Counter("dup_total", nil).Add(1)

	got := render(t, r)
	if !containsLine(got, "# HELP dup_total first.") {
		t.Errorf("the first description did not win\ngot:\n%s", got)
	}
	if !containsLine(got, MetricConflictsTotal+" 1") {
		t.Errorf("a description disagreement was not reported\ngot:\n%s", got)
	}
}

func TestRegistryDescribedNames(t *testing.T) {
	r := New(Descriptions{"z_total": {Kind: KindCounter}, "a_total": {Kind: KindCounter}})

	got := r.DescribedNames()
	want := []string{"a_total", MetricGoGoroutines, MetricGoHeapBytes, MetricConflictsTotal, "z_total"}
	sortFloatsNames(want)

	if len(got) != len(want) {
		t.Fatalf("DescribedNames returned %d names, want %d: got %v, want %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DescribedNames is not sorted or complete: got %v, want %v", got, want)
		}
	}
}

// TestRegistryConcurrentRecording is the -race check: many goroutines create
// and update series of every kind at once while a scrape runs alongside them.
//
// TestRegistryConcurrentRecording 是 -race 下的检查：大量协程同时创建并更新各种
// 类型的序列，与此同时还有一次抓取在并行进行。
func TestRegistryConcurrentRecording(t *testing.T) {
	const (
		workers = 16
		rounds  = 200
	)
	r := New(testCatalogue())

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			labels := map[string]string{"result": "success"}
			for range rounds {
				r.Counter("test_requests_total", labels).Add(1)
				r.Gauge("test_slots", map[string]string{"class": "inference"}).Set(float64(w))
				r.Histogram("test_latency_seconds", nil).Observe(0.05)
			}
		}(w)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range rounds {
			var sink strings.Builder
			if err := r.Render(&sink); err != nil {
				t.Errorf("Render failed during concurrent recording: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	got := render(t, r)
	wantTotal := workers * rounds
	if !containsLine(got, `test_requests_total{result="success"} `+itoa(wantTotal)) {
		t.Errorf("concurrent counter adds were lost: want %d\ngot:\n%s", wantTotal, got)
	}
	if !containsLine(got, "test_latency_seconds_count "+itoa(wantTotal)) {
		t.Errorf("concurrent histogram observations were lost: want %d\ngot:\n%s", wantTotal, got)
	}
}

func TestNormalizeBuckets(t *testing.T) {
	tests := []struct {
		name  string
		input []float64
		want  []float64
	}{
		{name: "sorts ascending", input: []float64{1, 0.1, 0.5}, want: []float64{0.1, 0.5, 1}},
		{name: "removes duplicates", input: []float64{1, 1, 2}, want: []float64{1, 2}},
		{name: "drops infinities", input: []float64{math.Inf(1), 1, math.Inf(-1)}, want: []float64{1}},
		{name: "drops NaN", input: []float64{math.NaN(), 1}, want: []float64{1}},
		{name: "empty stays empty", input: nil, want: []float64{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeBuckets(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("normalizeBuckets(%v) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("normalizeBuckets(%v) = %v, want %v", tt.input, got, tt.want)
				}
			}
		})
	}
}

// TestBucketHelpersReturnFreshSlices guards the immutability the three helpers
// promise: a caller that edits what it got must not change the next caller's
// buckets.
//
// TestBucketHelpersReturnFreshSlices 守住三个辅助函数承诺的不可变性：调用方修改
// 自己拿到的切片，不得改变下一个调用方的分桶。
func TestBucketHelpersReturnFreshSlices(t *testing.T) {
	tests := []struct {
		name string
		fn   func() []float64
	}{
		{name: "SecondsBuckets", fn: SecondsBuckets},
		{name: "BytesBuckets", fn: BytesBuckets},
		{name: "TokenBuckets", fn: TokenBuckets},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := tt.fn()
			original := first[0]
			first[0] = -1

			if second := tt.fn(); second[0] != original {
				t.Errorf("%s() shares state across calls: got %v after a caller edited its slice, want %v",
					tt.name, second[0], original)
			}
		})
	}
}

// render returns the registry's exposition with the built-in runtime gauges
// removed, so a test asserting on whole output is not rewritten every time the
// Go runtime figures move.
//
// render 返回注册表的导出内容，并去掉内置的运行时量表，这样断言整段输出的测试
// 不会因为 Go 运行时的数字变动而每次都要改。
func render(t *testing.T, r *Registry) string {
	t.Helper()
	var b strings.Builder
	if err := r.Render(&b); err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	var kept []string
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, MetricGoGoroutines) || strings.Contains(line, MetricGoHeapBytes) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// containsLine reports whether the exposition holds want as a complete line.
// Substring matching would let test_requests_total match
// test_requests_total_extra.
//
// containsLine 报告导出内容中是否存在完整为 want 的一行。用子串匹配会让
// test_requests_total 命中 test_requests_total_extra。
func containsLine(exposition, want string) bool {
	for line := range strings.SplitSeq(exposition, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// sortFloatsNames sorts a name list in place, reusing the package's insertion
// sort shape so the test needs no import of sort.
//
// sortFloatsNames 原地排序名字列表，沿用本包插入排序的写法，使测试无需引入 sort。
func sortFloatsNames(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
