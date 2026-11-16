package httpapi

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/pricing"
	"AIServeWeave/common/runtime"
)

// countingMetrics is a runtime.Metrics that only adds up counters.
// countingMetrics 是只累加计数器的 runtime.Metrics。
type countingMetrics struct {
	mu     sync.Mutex
	counts map[string]float64
}

type countingCounter struct {
	m    *countingMetrics
	name string
}

func (c countingCounter) Add(delta float64) {
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	c.m.counts[c.name] += delta
}

type discardGauge struct{}

func (discardGauge) Set(float64) {}

type discardHistogram struct{}

func (discardHistogram) Observe(float64) {}

func (m *countingMetrics) Counter(name string, _ map[string]string) runtime.Counter {
	return countingCounter{m: m, name: name}
}
func (m *countingMetrics) Gauge(string, map[string]string) runtime.Gauge { return discardGauge{} }
func (m *countingMetrics) Histogram(string, map[string]string) runtime.Histogram {
	return discardHistogram{}
}

// TestLedgerRecordCarriesFilteredDetails checks that a ledger record carries
// the backend's details plus the reasoning share, and that a malformed entry
// is dropped and counted while the record itself is kept.
// TestLedgerRecordCarriesFilteredDetails 检查账本记录携带后端的明细与推理部分，以及
// 格式不对的项被丢弃并计数、而记录本身保留。
func TestLedgerRecordCarriesFilteredDetails(t *testing.T) {
	ledger := &fakeUsageLedgerSink{}
	metrics := &countingMetrics{counts: map[string]float64{}}
	h := &handlers{usageLedger: ledger, metrics: newRecorder(metrics), clock: runtime.NewSystemClock()}
	ctx := context.WithValue(context.Background(), identityKey{}, Identity{TenantID: "tnt_1", KeyID: "key_1"})
	usage := runtime.Usage{
		PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050, ReasoningTokens: 20,
		Details:    map[string]int64{pricing.MeterCacheRead: 600, "Bad Name": 3},
		Attributes: map[string]string{"service_tier": "flex", "1bad": "x"},
	}
	h.recordUsage(ctx, usage, time.Millisecond, UsageEndpointChat, "llama3")

	if len(ledger.records) != 1 {
		t.Fatalf("ledgered %d records, want 1", len(ledger.records))
	}
	got := ledger.records[0]
	if len(got.Details) != 2 || got.Details[pricing.MeterCacheRead] != 600 || got.Details[pricing.MeterReasoning] != 20 {
		t.Errorf("details = %v, want input.cache_read=600 and output.reasoning=20", got.Details)
	}
	if len(got.Attributes) != 1 || got.Attributes["service_tier"] != "flex" {
		t.Errorf("attributes = %v, want only service_tier=flex", got.Attributes)
	}
	if n := metrics.counts[MetricUsageDetailsDroppedTotal]; n != 2 {
		t.Errorf("%s = %v, want 2", MetricUsageDetailsDroppedTotal, n)
	}
	if usage.Details[pricing.MeterReasoning] != 0 {
		t.Error("adding the reasoning share must not modify the backend's map")
	}

	ledger.records = nil
	h.recordUsage(ctx, runtime.Usage{PromptTokens: 3, TotalTokens: 3}, time.Millisecond, UsageEndpointChat, "llama3")
	if len(ledger.records) != 1 || ledger.records[0].Details != nil || ledger.records[0].Attributes != nil {
		t.Errorf("records = %+v, want one record without details or attributes", ledger.records)
	}
}

// TestNativeMessagesUsageCountsCacheTokens checks that native Messages usage,
// whose input_tokens leaves the cache out, bills cache reads and writes as
// input — at the top level and in every sampling iteration — and keeps them
// as details.
// TestNativeMessagesUsageCountsCacheTokens 检查原生 Messages 用量（其 input_tokens
// 不含缓存）把缓存读取与写入计为输入——无论在顶层还是每次采样中——并保留为明细。
func TestNativeMessagesUsageCountsCacheTokens(t *testing.T) {
	decode := func(raw string) map[string]any {
		var value map[string]any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	top := decode(`{"input_tokens":10,"output_tokens":7,"cache_read_input_tokens":5000,"cache_creation_input_tokens":300,
		"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":100}}`)
	for _, withIterations := range []bool{true, false} {
		usage, err := nativeMessagesUsage(top, withIterations)
		if err != nil {
			t.Fatal(err)
		}
		if usage.PromptTokens != 5310 || usage.CompletionTokens != 7 || usage.TotalTokens != 5317 {
			t.Fatalf("usage = %+v, want prompt 5310, completion 7", usage)
		}
		if usage.Details[pricing.MeterCacheRead] != 5000 || usage.Details[pricing.MeterCacheWrite] != 300 || usage.Details[pricing.MeterCacheWrite1h] != 100 {
			t.Fatalf("details = %v", usage.Details)
		}
	}

	iterations := decode(`{"input_tokens":3,"output_tokens":7,"cache_read_input_tokens":50,"iterations":[
		{"type":"compaction","input_tokens":100,"output_tokens":20,"cache_read_input_tokens":900},
		{"type":"message","input_tokens":3,"output_tokens":7,"cache_read_input_tokens":50}]}`)
	billed, err := nativeMessagesBilledUsage(iterations)
	if err != nil {
		t.Fatal(err)
	}
	if billed.PromptTokens != 1053 || billed.CompletionTokens != 27 || billed.Details[pricing.MeterCacheRead] != 950 {
		t.Fatalf("billed = %+v, want every iteration with its cache reads", billed)
	}
	own, err := nativeMessagesUsage(iterations, false)
	if err != nil || own.PromptTokens != 53 || own.Details[pricing.MeterCacheRead] != 50 {
		t.Fatalf("top-level usage = %+v, %v; want prompt 53", own, err)
	}

	for _, raw := range []string{
		`{"input_tokens":1,"cache_read_input_tokens":-1}`,
		`{"input_tokens":1,"cache_creation_input_tokens":2.5}`,
		`{"input_tokens":1,"cache_creation":{"ephemeral_1h_input_tokens":-4}}`,
		`{"input_tokens":9223372036854775807,"cache_read_input_tokens":1}`,
	} {
		if _, err := nativeMessagesUsage(decode(raw), true); err == nil {
			t.Errorf("invalid usage accepted: %s", raw)
		}
	}
}
