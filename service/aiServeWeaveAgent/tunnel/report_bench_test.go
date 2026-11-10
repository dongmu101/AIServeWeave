package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// benchRuntime is the smallest Runtime that can carry a large ComfyUI-sized
// Discovery; only Discover and Health matter to the status report.
//
// benchRuntime 是能承载 ComfyUI 规模 Discovery 的最小 Runtime；状态上报只关心
// Discover 与 Health。
type benchRuntime struct {
	id        string
	discovery runtime.Discovery
}

func (r *benchRuntime) Descriptor() runtime.Descriptor {
	return runtime.Descriptor{ID: r.id, Kind: runtime.KindVLLM, BaseURL: "http://127.0.0.1:8188"}
}

func (r *benchRuntime) Probe(context.Context) (runtime.ProbeResult, error) {
	return runtime.ProbeResult{Kind: runtime.KindVLLM, IdentityVerified: true, ProbedAt: time.Now()}, nil
}

func (r *benchRuntime) Health(context.Context) (runtime.HealthReport, error) {
	return runtime.HealthReport{State: runtime.StateHealthy, CheckedAt: time.Now()}, nil
}

func (r *benchRuntime) Discover(context.Context) (runtime.Discovery, error) {
	d := r.discovery
	d.DiscoveredAt = time.Now()
	return d, nil
}

func (r *benchRuntime) Close() error { return nil }

// countingStream records Control frames and discards them.
type countingStream struct{ sent int }

func (s *countingStream) Send(*tunnelv1.AgentControl) error { s.sent++; return nil }
func (s *countingStream) Recv() (*tunnelv1.GatewayControl, error) {
	return nil, context.Canceled
}
func (s *countingStream) CloseSend() error { return nil }

// benchMetrics satisfies the dependency check; the benchmark runtime never
// records anything.
type benchMetrics struct{}

func (benchMetrics) Counter(string, map[string]string) runtime.Counter     { return benchInstrument{} }
func (benchMetrics) Gauge(string, map[string]string) runtime.Gauge         { return benchInstrument{} }
func (benchMetrics) Histogram(string, map[string]string) runtime.Histogram { return benchInstrument{} }

type benchInstrument struct{}

func (benchInstrument) Add(float64)     {}
func (benchInstrument) Set(float64)     {}
func (benchInstrument) Observe(float64) {}

func newBenchManager(tb testing.TB, models, nodeTypes int) runtime.Manager {
	tb.Helper()
	d := runtime.Discovery{Version: "0.3.0"}
	for i := range models {
		d.Models = append(d.Models, runtime.Model{ID: fmt.Sprintf("checkpoints/model-%05d.safetensors", i)})
	}
	for i := range nodeTypes {
		d.NodeTypes = append(d.NodeTypes, fmt.Sprintf("NodeType%05d", i))
	}
	reg := runtime.NewRegistry()
	if err := reg.Register(runtime.KindVLLM, func(runtime.Config, runtime.Dependencies) (runtime.Runtime, error) {
		return &benchRuntime{id: "comfy", discovery: d}, nil
	}); err != nil {
		tb.Fatal(err)
	}
	m := runtime.NewManager(reg, runtime.Dependencies{HTTPClient: &http.Client{}, Logger: slog.Default(), Metrics: benchMetrics{}})
	tb.Cleanup(func() { m.Close(context.Background()) })
	err := m.Add(context.Background(), runtime.Config{
		ID:                "comfy",
		Kind:              runtime.KindVLLM,
		BaseURL:           "http://127.0.0.1:8188",
		HealthInterval:    time.Hour,
		DiscoveryInterval: time.Hour,
	})
	if err != nil {
		tb.Fatal(err)
	}
	return m
}

func newBenchSession(m runtime.Manager) (*controlSession, *countingStream) {
	c := &Client{cfg: ClientConfig{Manager: m}, clock: runtime.NewSystemClock()}
	stream := &countingStream{}
	return &controlSession{client: c, stream: stream, reported: map[string][]byte{}}, stream
}

// BenchmarkStatusPollUnchanged measures the steady-state cost of one
// StatusPollInterval tick when nothing changed: one ComfyUI runtime carrying
// 5000 models and 5000 node types, observed by three replica connections.
//
// BenchmarkStatusPollUnchanged 度量无变化时一次 StatusPollInterval 的稳态开销：
// 一个带 5000 个 model 和 5000 个 node type 的 ComfyUI 运行时，被三个副本连接观察。
func BenchmarkStatusPollUnchanged(b *testing.B) {
	const replicas = 3
	m := newBenchManager(b, 5000, 5000)
	sessions := make([]*controlSession, replicas)
	streams := make([]*countingStream, replicas)
	for i := range sessions {
		sessions[i], streams[i] = newBenchSession(m)
		if err := sessions[i].report(true); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, s := range sessions {
			if err := s.report(false); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.StopTimer()
	for i, st := range streams {
		if st.sent != 1 {
			b.Fatalf("replica %d sent %d frames, want only the initial full report", i, st.sent)
		}
	}
}
