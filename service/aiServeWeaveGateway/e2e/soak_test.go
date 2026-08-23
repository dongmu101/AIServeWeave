package e2e

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	runtimepkg "AIServeWeave/common/runtime"
)

// TestSoak is the tunnel README's 24h long-soak scenario: run a real fleet
// (real TCP, real mTLS, real slot pool) under steady traffic for a long time
// and watch three things a short test cannot: memory growth, goroutine
// leaks, and whether connection/slot counts drift instead of holding steady.
//
// It is opt-in and duration-driven, unlike the fault-injection tests in this
// package: set AISW_SOAK_DURATION (a time.Duration string, e.g. "24h" for the
// real run or "2m" to smoke-test this file itself) to run it. Because the
// real run is far longer than `go test`'s default 10-minute timeout, invoke
// it with -timeout 0, e.g.:
//
//	AISW_SOAK_DURATION=24h go test ./service/aiServeWeaveGateway/e2e/ \
//	  -run TestSoak -v -timeout 0
//
// AISW_SOAK_SAMPLE_INTERVAL overrides the sampling period (default 5m; the
// smoke test overrides it to something much shorter). AISW_SOAK_OUTPUT
// overrides where the CSV report is written (default a timestamped file
// under os.TempDir(), logged at the start of the run so a 24h run's output
// can be found again after the test process exits).
func TestSoak(t *testing.T) {
	durationStr := os.Getenv("AISW_SOAK_DURATION")
	if durationStr == "" {
		t.Skip("set AISW_SOAK_DURATION (e.g. 24h, or 2m to smoke-test this file) to run the long-soak scenario")
	}
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		t.Fatalf("AISW_SOAK_DURATION=%q: %v", durationStr, err)
	}

	sampleInterval := 5 * time.Minute
	if s := os.Getenv("AISW_SOAK_SAMPLE_INTERVAL"); s != "" {
		sampleInterval, err = time.ParseDuration(s)
		if err != nil {
			t.Fatalf("AISW_SOAK_SAMPLE_INTERVAL=%q: %v", s, err)
		}
	}
	// A run needs at least a handful of samples to say anything about a
	// trend; a duration shorter than a few sampling periods is almost
	// certainly a mistake in the env var rather than an intentionally tiny
	// smoke test.
	if duration < 3*sampleInterval {
		t.Fatalf("AISW_SOAK_DURATION=%s is less than 3 sample intervals (%s); either shorten the interval or lengthen the run",
			duration, sampleInterval)
	}

	outputPath := os.Getenv("AISW_SOAK_OUTPUT")
	if outputPath == "" {
		outputPath = filepath.Join(os.TempDir(), fmt.Sprintf("aisw-soak-%s.csv", time.Now().Format("20060102-150405")))
	}
	report, err := newSoakReport(outputPath)
	if err != nil {
		t.Fatalf("creating soak report %s: %v", outputPath, err)
	}
	defer report.close()
	t.Logf("soak: running for %s, sampling every %s, report at %s", duration, sampleInterval, outputPath)

	const replicaCount = 3
	f := newFleet(t, replicaCount)
	f.awaitReady(f.replicas...)

	loadCtx, stopLoad := context.WithCancel(context.Background())
	var (
		requests, failures atomic.Int64
		loadWG             sync.WaitGroup
	)
	// Steady, modest traffic: enough to keep every replica's slot pool
	// cycling (acquire/release, not sitting idle at floor forever) without
	// generating enough goroutines or objects itself to be mistaken for a
	// leak in the sampled numbers.
	for _, r := range f.replicas {
		loadWG.Add(1)
		go func(r *replica) {
			defer loadWG.Done()
			rt := r.server.Runtime("mac-mini-01", "backend-1")
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-loadCtx.Done():
					return
				case <-ticker.C:
				}
				ctx, cancel := context.WithTimeout(loadCtx, 5*time.Second)
				_, err := rt.Chat(ctx, runtimepkg.ChatRequest{
					Model:    "e2e-model",
					Messages: []runtimepkg.ChatMessage{{Role: "user", Content: "soak"}},
				})
				cancel()
				requests.Add(1)
				if err != nil && loadCtx.Err() == nil {
					failures.Add(1)
				}
			}
		}(r)
	}

	// Warm-up: let the load generator and the slot pool settle into steady
	// state (pool sizing, GC's first cycles) before the first sample, so the
	// baseline is not an artifact of startup transients.
	const warmup = 5 * time.Second
	time.Sleep(min(warmup, duration/10))

	var samples []soakSample
	deadline := time.Now().Add(duration)
	first := true
	for time.Now().Before(deadline) {
		s := sampleSoak(f, replicaCount)
		samples = append(samples, s)
		if err := report.write(s); err != nil {
			t.Fatalf("writing soak sample: %v", err)
		}
		if first || len(samples)%12 == 0 { // log roughly hourly at the default 5m interval
			t.Logf("soak: t+%s goroutines=%d heap_alloc=%dMB connected=%d idle_slots=%d requests=%d failures=%d",
				time.Since(samples[0].at).Round(time.Second), s.goroutines, s.heapAllocBytes/1e6, s.connectedReplicas, s.idleSlots,
				requests.Load(), failures.Load())
			first = false
		}
		time.Sleep(min(sampleInterval, time.Until(deadline)))
	}

	stopLoad()
	loadWG.Wait()

	if len(samples) < 3 {
		t.Fatalf("only collected %d samples; the run was too short relative to the sample interval", len(samples))
	}
	analyzeSoak(t, samples, replicaCount, requests.Load(), failures.Load())
}

type soakSample struct {
	at                time.Time
	goroutines        int
	heapAllocBytes    uint64
	heapObjects       uint64
	connectedReplicas int
	idleSlots         int
}

// sampleSoak reads the three signals the README's acceptance criterion asks
// for: goroutine count (leaks), heap size (growth), and connection/slot
// counts (drift). runtime.GC runs first so heapAllocBytes reflects live
// objects rather than whatever garbage happens to be sitting around at the
// moment of the sample — without it, GC timing noise would dwarf any real
// trend over a 24h window.
func sampleSoak(f *fleet, replicaCount int) soakSample {
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	idle := 0
	for _, r := range f.replicas[:replicaCount] {
		if info, ok := r.server.Node("mac-mini-01"); ok {
			idle += int(info.IdleSlots[tunnelv1.SlotClass_SLOT_CLASS_INFERENCE])
		}
	}

	return soakSample{
		at:                time.Now(),
		goroutines:        runtime.NumGoroutine(),
		heapAllocBytes:    mem.HeapAlloc,
		heapObjects:       mem.HeapObjects,
		connectedReplicas: f.tunnels.ConnectedReplicas(),
		idleSlots:         idle,
	}
}

// analyzeSoak turns the collected samples into the three pass/fail
// judgments the README's acceptance criterion names, using the run's own
// steady-state (the median of the first quarter of samples) as the baseline
// rather than a fixed constant: the right absolute numbers depend on the
// machine and the SlotHint the fleet was built with, but a large relative
// move away from where the run settled does not.
func analyzeSoak(t *testing.T, samples []soakSample, replicaCount int, requests, failures int64) {
	t.Helper()

	baselineN := max(1, len(samples)/4)
	var baseGoroutines, baseHeap float64
	for _, s := range samples[:baselineN] {
		baseGoroutines += float64(s.goroutines)
		baseHeap += float64(s.heapAllocBytes)
	}
	baseGoroutines /= float64(baselineN)
	baseHeap /= float64(baselineN)

	last := samples[len(samples)-1]
	goroutineGrowth := (float64(last.goroutines) - baseGoroutines) / baseGoroutines
	heapGrowth := (float64(last.heapAllocBytes) - baseHeap) / baseHeap

	minConnected, maxConnected := samples[0].connectedReplicas, samples[0].connectedReplicas
	minIdle, maxIdle := samples[0].idleSlots, samples[0].idleSlots
	for _, s := range samples {
		minConnected = min(minConnected, s.connectedReplicas)
		maxConnected = max(maxConnected, s.connectedReplicas)
		minIdle = min(minIdle, s.idleSlots)
		maxIdle = max(maxIdle, s.idleSlots)
	}

	t.Logf("soak summary: %d samples over %s, %d requests (%d failures, %.3f%%)",
		len(samples), last.at.Sub(samples[0].at).Round(time.Second), requests, failures,
		100*float64(failures)/float64(max(1, requests)))
	t.Logf("soak summary: goroutines baseline=%.0f final=%d (%.1f%%), heap baseline=%.1fMB final=%.1fMB (%.1f%%)",
		baseGoroutines, last.goroutines, 100*goroutineGrowth, baseHeap/1e6, float64(last.heapAllocBytes)/1e6, 100*heapGrowth)
	t.Logf("soak summary: connected replicas min=%d max=%d (want %d throughout), idle slots min=%d max=%d",
		minConnected, maxConnected, replicaCount, minIdle, maxIdle)

	if minConnected != replicaCount || maxConnected != replicaCount {
		t.Errorf("connected replicas ranged [%d, %d], want a steady %d throughout the run", minConnected, maxConnected, replicaCount)
	}
	// A doubling of goroutines or heap over the run is generous headroom
	// above normal GC/scheduler noise but well below what an actual leak
	// produces over a long enough window — a real leak grows without bound,
	// so even a conservative threshold catches it given enough samples.
	const growthCeiling = 1.0
	if goroutineGrowth > growthCeiling {
		t.Errorf("goroutine count grew %.0f%% from baseline (%.0f -> %d); suspect a leak", 100*goroutineGrowth, baseGoroutines, last.goroutines)
	}
	if heapGrowth > growthCeiling {
		t.Errorf("heap grew %.0f%% from baseline (%.1fMB -> %.1fMB); suspect a leak", 100*heapGrowth, baseHeap/1e6, float64(last.heapAllocBytes)/1e6)
	}
	if failures > 0 {
		rate := float64(failures) / float64(max(1, requests))
		// A handful of failures from requests in flight exactly as the load
		// generator's context is canceled at shutdown is expected; a
		// sustained failure rate is not.
		if rate > 0.01 {
			t.Errorf("soak traffic failure rate %.2f%% (%d/%d) exceeds 1%%", 100*rate, failures, requests)
		}
	}
}

// soakReport is a CSV sink for soak samples, flushed after every write so a
// run killed partway through (or one still running 20 hours from now) always
// leaves a readable file on disk.
type soakReport struct {
	f *os.File
	w *csv.Writer
}

func newSoakReport(path string) (*soakReport, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := csv.NewWriter(f)
	if err := w.Write([]string{"unix_ms", "goroutines", "heap_alloc_bytes", "heap_objects", "connected_replicas", "idle_slots"}); err != nil {
		f.Close()
		return nil, err
	}
	w.Flush()
	return &soakReport{f: f, w: w}, nil
}

func (r *soakReport) write(s soakSample) error {
	if err := r.w.Write([]string{
		strconv.FormatInt(s.at.UnixMilli(), 10),
		strconv.Itoa(s.goroutines),
		strconv.FormatUint(s.heapAllocBytes, 10),
		strconv.FormatUint(s.heapObjects, 10),
		strconv.Itoa(s.connectedReplicas),
		strconv.Itoa(s.idleSlots),
	}); err != nil {
		return err
	}
	r.w.Flush()
	return r.w.Error()
}

func (r *soakReport) close() {
	r.w.Flush()
	r.f.Close()
}
