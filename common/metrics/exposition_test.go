package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExpositionIsStable asserts the property a golden-file comparison relies
// on: two scrapes of an unchanged registry are byte-identical.
//
// TestExpositionIsStable 断言 golden 比对所依赖的性质：对未变化的注册表抓取两次
// 得到完全相同的字节。
func TestExpositionIsStable(t *testing.T) {
	r := New(testCatalogue())
	r.Counter("test_requests_total", map[string]string{"result": "success"}).Add(1)
	r.Counter("test_requests_total", map[string]string{"result": "timeout"}).Add(1)
	r.Gauge("test_slots", map[string]string{"class": "bulk"}).Set(2)
	r.Histogram("test_latency_seconds", nil).Observe(0.2)

	first := render(t, r)
	second := render(t, r)
	if first != second {
		t.Errorf("two scrapes of an unchanged registry differ\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestExpositionFamilyLayout asserts the shape a scraper requires: one HELP
// and one TYPE line per family, immediately before that family's series.
//
// TestExpositionFamilyLayout 断言抓取方要求的形态：每个指标族有且只有一行 HELP
// 与一行 TYPE，且紧跟在该指标族的序列之前。
func TestExpositionFamilyLayout(t *testing.T) {
	r := New(testCatalogue())
	r.Counter("test_requests_total", map[string]string{"result": "success"}).Add(1)
	r.Counter("test_requests_total", map[string]string{"result": "timeout"}).Add(1)

	got := render(t, r)
	if n := strings.Count(got, "# HELP test_requests_total "); n != 1 {
		t.Errorf("got %d HELP lines for one family, want 1\ngot:\n%s", n, got)
	}
	if n := strings.Count(got, "# TYPE test_requests_total "); n != 1 {
		t.Errorf("got %d TYPE lines for one family, want 1\ngot:\n%s", n, got)
	}

	lines := strings.Split(got, "\n")
	helpAt, typeAt, firstSeriesAt := -1, -1, -1
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "# HELP test_requests_total "):
			helpAt = i
		case strings.HasPrefix(line, "# TYPE test_requests_total "):
			typeAt = i
		case strings.HasPrefix(line, "test_requests_total{") && firstSeriesAt < 0:
			firstSeriesAt = i
		}
	}
	if !(helpAt >= 0 && helpAt < typeAt && typeAt < firstSeriesAt) {
		t.Errorf("family header is out of order: HELP at %d, TYPE at %d, first series at %d\ngot:\n%s",
			helpAt, typeAt, firstSeriesAt, got)
	}
}

// TestExpositionEscapesHelpText asserts a help string cannot break the format,
// even though every help string in this repository is a literal.
//
// TestExpositionEscapesHelpText 断言 help 文本无法破坏格式，尽管本仓库里每一条
// help 文本都是字面量。
func TestExpositionEscapesHelpText(t *testing.T) {
	r := New(Descriptions{"odd_total": {Kind: KindCounter, Help: "line\nbreak and a \\ slash"}})
	r.Counter("odd_total", nil).Add(1)

	got := render(t, r)
	if !containsLine(got, `# HELP odd_total line\nbreak and a \\ slash`) {
		t.Errorf("help text was not escaped\ngot:\n%s", got)
	}
}

// TestExpositionDropsInvalidMetricName asserts one malformed family is
// excluded rather than allowed to make a scraper reject the whole scrape.
//
// TestExpositionDropsInvalidMetricName 断言单个格式非法的指标族会被排除，而不是
// 放任它让抓取方拒绝整次抓取。
func TestExpositionDropsInvalidMetricName(t *testing.T) {
	tests := []struct {
		name   string
		metric string
		valid  bool
	}{
		{name: "plain name", metric: "ok_total", valid: true},
		{name: "leading underscore", metric: "_ok_total", valid: true},
		{name: "colon is legal in a metric name", metric: "job:ok_total", valid: true},
		{name: "hyphen is not", metric: "not-ok-total", valid: false},
		{name: "leading digit is not", metric: "1_total", valid: false},
		{name: "empty is not", metric: "", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New()
			r.Counter(tt.metric, nil).Add(1)

			got := render(t, r)
			present := containsLine(got, tt.metric+" 1")
			if present != tt.valid {
				t.Errorf("metric %q present in exposition = %v, want %v\ngot:\n%s",
					tt.metric, present, tt.valid, got)
			}
		})
	}
}

// TestExpositionCarriesRuntimeGauges asserts the built-in figures the soak
// test reads are actually served — render strips them, so this is the one test
// that looks at the raw output.
//
// TestExpositionCarriesRuntimeGauges 断言长稳测试要读的内置数字确实被导出——
// render 会把它们过滤掉，因此这是唯一直接看原始输出的测试。
func TestExpositionCarriesRuntimeGauges(t *testing.T) {
	r := New()
	var b strings.Builder
	if err := r.Render(&b); err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	got := b.String()

	for _, name := range []string{MetricGoGoroutines, MetricGoHeapBytes} {
		if !strings.Contains(got, "\n"+name+" ") && !strings.HasPrefix(got, name+" ") {
			t.Errorf("built-in gauge %s is missing from the exposition\ngot:\n%s", name, got)
		}
	}
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		wantBody   bool
		wantStatus int
	}{
		{name: "GET serves the exposition", method: http.MethodGet, wantBody: true, wantStatus: http.StatusOK},
		{name: "HEAD serves headers only", method: http.MethodHead, wantBody: false, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(testCatalogue())
			r.Counter("test_requests_total", nil).Add(1)

			rec := httptest.NewRecorder()
			r.Handler().ServeHTTP(rec, httptest.NewRequest(tt.method, "/metrics", nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("Content-Type"); got != ContentType {
				t.Errorf("Content-Type = %q, want %q", got, ContentType)
			}
			if got := rec.Body.Len() > 0; got != tt.wantBody {
				t.Errorf("response has a body = %v, want %v", got, tt.wantBody)
			}
			if tt.wantBody && !containsLine(rec.Body.String(), "test_requests_total 1") {
				t.Errorf("the handler did not serve the recorded series\ngot:\n%s", rec.Body.String())
			}
		})
	}
}
