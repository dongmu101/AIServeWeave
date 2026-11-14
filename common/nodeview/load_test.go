package nodeview

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"AIServeWeave/common/nodeload"
)

func TestLoadFromReportNilMeansNotReported(t *testing.T) {
	if LoadFromReport(nil) != nil {
		t.Fatal("a node without a load report must render no load")
	}
}

func TestLoadFromReportAllowlist(t *testing.T) {
	cpu, total, avail := 0.5, int64(8<<30), int64(2<<30)
	sampled := time.Date(2026, 11, 14, 1, 2, 3, 0, time.FixedZone("x", 3600))
	report := &nodeload.Report{
		SampledAt:            sampled,
		CPUUtilization:       &cpu,
		MemoryAvailableBytes: &avail,
		MemoryTotalBytes:     &total,
		Pressure:             true,
		PressureReasons:      []nodeload.Reason{nodeload.ReasonMemory, "bogus"},
		AdmissionWaiting:     -1,
	}
	got := LoadFromReport(report)
	if got.SampledAt == nil || !got.SampledAt.Equal(sampled) || got.SampledAt.Location() != time.UTC {
		t.Fatalf("sampled_at not rendered in UTC: %v", got.SampledAt)
	}
	if !reflect.DeepEqual(got.PressureReasons, []string{"memory"}) {
		t.Fatalf("unknown reason leaked: %v", got.PressureReasons)
	}
	if got.AdmissionWaiting != 0 {
		t.Fatalf("negative waiting not clamped: %d", got.AdmissionWaiting)
	}
	cpu = 0.9
	if *got.CPUUtilization != 0.5 {
		t.Fatal("view must not alias the report's pointers")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["load_average_1m"]; ok {
		t.Fatal("an unmeasured load average must be absent, not zero")
	}
	if keys["pressure"] != true {
		t.Fatalf("pressure missing: %s", raw)
	}
}
