package metrics

import (
	"bytes"
	"io"
	"net/http"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
)

// ContentType is the media type of the exposition Render produces. It names
// the text format version explicitly, because a scraper that guesses wrong
// silently reads an empty target.
//
// ContentType 是 Render 所产生导出内容的媒体类型。它显式写出文本格式版本，因为
// 猜错格式的抓取方会静默地把目标读成空的。
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Built-in metrics every registry carries. The two Go runtime figures are here
// rather than in a service's own catalogue because the question they answer —
// is this process leaking goroutines or memory over a 24h run — is asked of
// every binary in this repository, and answering it per service would mean
// three copies of the same three lines.
//
// 每个注册表都自带的内置指标。两个 Go 运行时数字放在这里而不是各服务自己的目录里，
// 是因为它们回答的问题——这个进程在 24 小时运行中是否泄漏协程或内存——对本仓库
// 每个二进制都要问一遍，各服务各写一份就是同样三行抄三遍。
const (
	// MetricGoGoroutines is the live goroutine count. A slope that never
	// returns to its baseline is the soak test's leak signal.
	//
	// MetricGoGoroutines 是存活协程数。曲线始终不回到基线，就是长稳测试要找的
	// 泄漏信号。
	MetricGoGoroutines = "go_goroutines"
	// MetricGoHeapBytes is the heap currently allocated and in use.
	//
	// MetricGoHeapBytes 是当前已分配且在用的堆大小。
	MetricGoHeapBytes = "go_memstats_heap_alloc_bytes"
)

// builtinDescriptions is the catalogue New always installs.
//
// builtinDescriptions 是 New 总会安装的目录。
func builtinDescriptions() Descriptions {
	return Descriptions{
		MetricConflictsTotal: {
			Kind: KindCounter,
			Help: "Recording calls refused because the metric name is bound to another instrument kind.",
		},
		MetricGoGoroutines: {
			Kind: KindGauge,
			Help: "Goroutines currently existing in this process.",
		},
		MetricGoHeapBytes: {
			Kind: KindGauge,
			Help: "Heap bytes currently allocated and in use.",
		},
	}
}

// Handler returns the http.Handler serving this registry's exposition. Mount
// it on /metrics.
//
// The exposition is rendered into memory before a byte reaches the client: a
// scrape that fails halfway leaves a truncated series set that a scraper will
// happily parse, and half a histogram is worse than no answer.
//
// Handler 返回服务该注册表导出内容的 http.Handler，挂在 /metrics 上。
//
// 导出内容会先在内存里渲染完再往客户端写：抓取中途失败会留下一份被截断的序列集合，
// 而抓取方会照单全收地解析它——半个直方图比没有答案更糟。
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var buf bytes.Buffer
		if err := r.Render(&buf); err != nil {
			http.Error(w, "rendering metrics failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
		if req.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write(buf.Bytes())
	})
}

// Server returns an http.Server serving this registry at /metrics on addr,
// not yet listening. The caller owns its lifecycle, because a metrics endpoint
// that outlives the process it describes — or dies before it — is a scrape
// gap nobody configured.
//
// Bind it to a loopback address unless something in front of it authenticates:
// the exposition names every node connected to this process, and that is
// inventory a public listener has no business handing out.
//
// Server 返回一个在 addr 上以 /metrics 提供本注册表的 http.Server，尚未开始监听。
// 生命周期由调用方掌握，因为一个比它所描述的进程活得更久——或者更短——的指标端点，
// 就是一段没人配置过的抓取缺口。
//
// 除非前面有东西做鉴权，否则请绑定到回环地址：导出内容会点出连到本进程的每一个节点，
// 而那是一份公网监听器没理由对外派发的资产清单。
func (r *Registry) Server(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", r.Handler())
	return &http.Server{Addr: addr, Handler: mux}
}

// Render renders the whole registry in the Prometheus text exposition format.
// Families and series are both emitted in sorted order, so scraping an
// unchanged registry twice yields identical bytes — which is what makes a
// golden-file test of this format meaningful.
//
// Render 以 Prometheus 文本导出格式渲染整个注册表。指标族与序列都按排序输出，
// 因此对未变化的注册表抓取两次会得到完全相同的字节——这正是让这套格式的
// golden 测试有意义的前提。
func (r *Registry) Render(w io.Writer) error {
	r.collectRuntime()

	r.mu.RLock()
	families := make([]*family, 0, len(r.families))
	for _, f := range r.families {
		families = append(families, f)
	}
	r.mu.RUnlock()
	sort.Slice(families, func(i, j int) bool { return families[i].name < families[j].name })

	var b strings.Builder
	for _, f := range families {
		if !validMetricName(f.name) {
			continue
		}
		writeFamily(&b, f)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// collectRuntime refreshes the built-in gauges. It runs at scrape time rather
// than on a ticker: a gauge nobody reads does not need to be kept current, and
// sampling on the scrape is what makes the value contemporaneous with the rest
// of the exposition.
//
// collectRuntime 刷新内置量表。它在抓取时运行而不是靠定时器：没人读的量表不需要
// 保持最新，而在抓取时采样才能让这个值与导出内容的其余部分处于同一时刻。
func (r *Registry) collectRuntime() {
	r.Gauge(MetricGoGoroutines, nil).Set(float64(goruntime.NumGoroutine()))

	var mem goruntime.MemStats
	goruntime.ReadMemStats(&mem)
	r.Gauge(MetricGoHeapBytes, nil).Set(float64(mem.HeapAlloc))

	if n := r.conflicts.Load(); n > 0 {
		// The counter is authored here rather than incremented at each
		// conflict site, because those sites hold no series and must stay
		// allocation-free on a path that is already reporting a defect. It
		// goes through family directly: Counter() would only offer Add, and
		// the authoritative total is the atomic, not an accumulation of what
		// past scrapes happened to see.
		//
		// 该计数器在此处成文，而不是在每个冲突点自增：那些冲突点手上没有序列，
		// 且它们所在的路径本身已经在上报缺陷，必须保持无分配。这里直接走 family：
		// Counter() 只提供 Add，而权威总数是那个原子量，不是历次抓取所见的累加。
		if f := r.family(MetricConflictsTotal, KindCounter); f != nil {
			f.series(nil).set(float64(n))
		}
	}
}

// writeFamily renders one metric family: its HELP and TYPE lines followed by
// every series.
//
// writeFamily 渲染一个指标族：先是 HELP 与 TYPE 行，然后是全部序列。
func writeFamily(b *strings.Builder, f *family) {
	if f.desc.Help != "" {
		b.WriteString("# HELP ")
		b.WriteString(f.name)
		b.WriteByte(' ')
		b.WriteString(escapeHelp(f.desc.Help))
		b.WriteByte('\n')
	}
	b.WriteString("# TYPE ")
	b.WriteString(f.name)
	b.WriteByte(' ')
	b.WriteString(f.kind.String())
	b.WriteByte('\n')

	for _, s := range f.snapshot() {
		if f.kind == KindHistogram {
			writeHistogramSeries(b, f, s)
			continue
		}
		b.WriteString(f.name)
		writeLabels(b, s.labels, "", "")
		b.WriteByte(' ')
		b.WriteString(formatFloat(s.value()))
		b.WriteByte('\n')
	}
}

// writeHistogramSeries renders one histogram series as its cumulative buckets,
// the +Inf bucket, the sum and the count.
//
// writeHistogramSeries 把一条直方图序列渲染成累积桶、+Inf 桶、总和与总次数。
func writeHistogramSeries(b *strings.Builder, f *family, s *series) {
	counts, sum, count := s.hist.snapshot()
	for i, bound := range s.hist.bounds {
		b.WriteString(f.name)
		b.WriteString("_bucket")
		writeLabels(b, s.labels, "le", formatFloat(bound))
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(counts[i], 10))
		b.WriteByte('\n')
	}
	b.WriteString(f.name)
	b.WriteString("_bucket")
	writeLabels(b, s.labels, "le", "+Inf")
	b.WriteByte(' ')
	b.WriteString(strconv.FormatUint(count, 10))
	b.WriteByte('\n')

	b.WriteString(f.name)
	b.WriteString("_sum")
	writeLabels(b, s.labels, "", "")
	b.WriteByte(' ')
	b.WriteString(formatFloat(sum))
	b.WriteByte('\n')

	b.WriteString(f.name)
	b.WriteString("_count")
	writeLabels(b, s.labels, "", "")
	b.WriteByte(' ')
	b.WriteString(strconv.FormatUint(count, 10))
	b.WriteByte('\n')
}

// writeLabels renders a label set, optionally with one extra pair appended —
// the le of a histogram bucket, which must sit alongside the series' own
// labels rather than in a set of its own.
//
// writeLabels 渲染一组标签，可选地追加一对额外标签——直方图桶的 le，它必须与序列
// 自身的标签并列，而不是自成一组。
func writeLabels(b *strings.Builder, labels map[string]string, extraKey, extraValue string) {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if validLabelName(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 && extraKey == "" {
		return
	}
	sort.Strings(keys)

	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(labels[k]))
		b.WriteByte('"')
	}
	if extraKey != "" {
		if len(keys) > 0 {
			b.WriteByte(',')
		}
		b.WriteString(extraKey)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(extraValue))
		b.WriteByte('"')
	}
	b.WriteByte('}')
}

// formatFloat renders a value the way the text format requires, including the
// three special values it spells out in words.
//
// formatFloat 按文本格式的要求渲染一个值，包括它用词而非数字表达的三个特殊值。
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// escapeHelp escapes the two characters a HELP line may not carry literally.
//
// escapeHelp 转义 HELP 行中不能原样出现的两个字符。
func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

// escapeLabelValue escapes the three characters a label value may not carry
// literally.
//
// escapeLabelValue 转义标签值中不能原样出现的三个字符。
func escapeLabelValue(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// validMetricName reports whether name matches the format's [a-zA-Z_:][a-zA-Z0-9_:]*.
// A name that does not is dropped from the exposition rather than emitted:
// one malformed family would make a scraper reject the whole scrape, taking
// every other metric down with it.
//
// validMetricName 报告 name 是否符合格式要求的 [a-zA-Z_:][a-zA-Z0-9_:]*。不符合的
// 名字会被从导出中丢弃而不是照写：一个格式错误的指标族会让抓取方拒绝整次抓取，把
// 其他所有指标一起拖下水。
func validMetricName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == ':':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// validLabelName reports whether name matches [a-zA-Z_][a-zA-Z0-9_]*. A colon
// is legal in a metric name but not in a label name.
//
// validLabelName 报告 name 是否符合 [a-zA-Z_][a-zA-Z0-9_]*。冒号在指标名里合法，
// 在标签名里不合法。
func validLabelName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
