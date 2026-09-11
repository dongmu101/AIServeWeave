package metrics_test

import (
	"strings"
	"testing"

	"AIServeWeave/common/metrics"
)

func TestParseExpositionRoundTripsCounterAndGauge(t *testing.T) {
	text := `# HELP demo_requests_total a counter
# TYPE demo_requests_total counter
demo_requests_total{endpoint="chat",status="200"} 3
# HELP demo_inflight a gauge
# TYPE demo_inflight gauge
demo_inflight 2
`
	samples, err := metrics.ParseExposition(strings.NewReader(text))
	if err != nil {
		t.Fatalf("ParseExposition() error = %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("len(samples) = %d, want 2", len(samples))
	}
	if samples[0].Name != "demo_requests_total" || samples[0].Value != 3 {
		t.Errorf("samples[0] = %+v, want name=demo_requests_total value=3", samples[0])
	}
	if got, want := samples[0].Labels["endpoint"], "chat"; got != want {
		t.Errorf("labels[endpoint] = %q, want %q", got, want)
	}
	if samples[1].Name != "demo_inflight" || samples[1].Value != 2 || len(samples[1].Labels) != 0 {
		t.Errorf("samples[1] = %+v, want name=demo_inflight value=2 no labels", samples[1])
	}
}

func TestParseExpositionRoundTripsHistogram(t *testing.T) {
	text := `# HELP demo_duration_seconds a histogram
# TYPE demo_duration_seconds histogram
demo_duration_seconds_bucket{endpoint="chat",le="0.1"} 1
demo_duration_seconds_bucket{endpoint="chat",le="+Inf"} 4
demo_duration_seconds_sum{endpoint="chat"} 1.5
demo_duration_seconds_count{endpoint="chat"} 4
`
	samples, err := metrics.ParseExposition(strings.NewReader(text))
	if err != nil {
		t.Fatalf("ParseExposition() error = %v", err)
	}
	if len(samples) != 4 {
		t.Fatalf("len(samples) = %d, want 4", len(samples))
	}
	if samples[1].Name != "demo_duration_seconds_bucket" || samples[1].Labels["le"] != "+Inf" || samples[1].Value != 4 {
		t.Errorf("samples[1] = %+v, want +Inf bucket with value 4", samples[1])
	}
	if samples[3].Name != "demo_duration_seconds_count" || samples[3].Value != 4 {
		t.Errorf("samples[3] = %+v, want demo_duration_seconds_count value 4", samples[3])
	}
}

func TestParseExpositionRejectsMalformedLine(t *testing.T) {
	if _, err := metrics.ParseExposition(strings.NewReader("not_a_valid_line\n")); err == nil {
		t.Fatal("ParseExposition() error = nil, want error for a line with no value")
	}
}

func TestParseExpositionRoundTripsRegistryRender(t *testing.T) {
	catalogue := metrics.Descriptions{
		"demo_dispatch_total": {Kind: metrics.KindCounter, Help: "dispatch attempts"},
	}
	reg := metrics.New(catalogue)
	reg.Counter("demo_dispatch_total", map[string]string{"result": "success"}).Add(2)

	var buf strings.Builder
	if err := reg.Render(&buf); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	samples, err := metrics.ParseExposition(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("ParseExposition() error = %v", err)
	}
	found := false
	for _, s := range samples {
		if s.Name == "demo_dispatch_total" && s.Labels["result"] == "success" && s.Value == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("samples = %+v, want a demo_dispatch_total{result=success} = 2 sample", samples)
	}
}
