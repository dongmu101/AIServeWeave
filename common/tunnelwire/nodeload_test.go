package tunnelwire

import (
	"math"
	"reflect"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/nodeload"
)

func ptr[T any](v T) *T { return &v }

func TestNodeLoadRoundTrip(t *testing.T) {
	sampled := time.UnixMilli(1_700_000_000_000).UTC()
	in := nodeload.Report{
		SampledAt:            sampled,
		CPUUtilization:       ptr(0.42),
		CPUEstimated:         true,
		LoadAverage1m:        ptr(3.5),
		MemoryAvailableBytes: ptr(int64(4 << 30)),
		MemoryTotalBytes:     ptr(int64(16 << 30)),
		Pressure:             true,
		PressureReasons:      []nodeload.Reason{nodeload.ReasonCPU, nodeload.ReasonMemory},
		AdmissionWaiting:     3,
	}
	got, ok := NodeLoadFromProto(NodeLoadToProto(in))
	if !ok {
		t.Fatal("round trip reported no load")
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip:\nwant %+v\n got %+v", in, got)
	}
}

func TestNodeLoadAbsentMeansNotReported(t *testing.T) {
	if _, ok := NodeLoadFromProto(nil); ok {
		t.Fatal("nil load must decode as not reported")
	}
	got, ok := NodeLoadFromProto(&tunnelv1.NodeLoad{})
	if !ok {
		t.Fatal("an empty load is still a report")
	}
	if got.CPUUtilization != nil || got.MemoryAvailableBytes != nil || got.LoadAverage1m != nil || got.MemoryTotalBytes != nil {
		t.Fatalf("unset measurements must stay nil, got %+v", got)
	}
}

func TestNodeLoadFromProtoSanitizes(t *testing.T) {
	pb := &tunnelv1.NodeLoad{
		CpuUtilization:       ptr(7.0),
		LoadAverage_1M:       ptr(math.NaN()),
		MemoryAvailableBytes: ptr(int64(32 << 30)),
		MemoryTotalBytes:     ptr(int64(16 << 30)),
		AdmissionWaiting:     -4,
		PressureReasons: []tunnelv1.LoadPressureReason{
			tunnelv1.LoadPressureReason_LOAD_PRESSURE_REASON_MEMORY,
			tunnelv1.LoadPressureReason(99),
			tunnelv1.LoadPressureReason_LOAD_PRESSURE_REASON_MEMORY,
		},
	}
	got, _ := NodeLoadFromProto(pb)
	if got.CPUUtilization == nil || *got.CPUUtilization != 1 {
		t.Fatalf("cpu not clamped to 1: %v", got.CPUUtilization)
	}
	if got.LoadAverage1m != nil {
		t.Fatalf("NaN load average must be dropped, got %v", *got.LoadAverage1m)
	}
	if *got.MemoryAvailableBytes != 16<<30 {
		t.Fatalf("available must not exceed total, got %d", *got.MemoryAvailableBytes)
	}
	if got.AdmissionWaiting != 0 {
		t.Fatalf("negative waiting must clamp to 0, got %d", got.AdmissionWaiting)
	}
	if !reflect.DeepEqual(got.PressureReasons, []nodeload.Reason{nodeload.ReasonMemory}) {
		t.Fatalf("reasons not deduplicated/filtered: %v", got.PressureReasons)
	}

	neg, _ := NodeLoadFromProto(&tunnelv1.NodeLoad{CpuUtilization: ptr(-1.0), MemoryAvailableBytes: ptr(int64(-5))})
	if *neg.CPUUtilization != 0 || *neg.MemoryAvailableBytes != 0 {
		t.Fatalf("negative values not clamped: %+v", neg)
	}
}
