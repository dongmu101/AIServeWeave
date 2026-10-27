package e2e

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
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

// TestSoak observes a real loopback mTLS fleet under synthetic Chat traffic.
// AISW_SOAK_DURATION enables it; AISW_SOAK_SAMPLE_INTERVAL defaults to 5m.
// AISW_SOAK_OUTPUT names a new CSV and its .summary.json sidecar; existing
// files are never overwritten. Without it, both files remain in os.TempDir().
// Setup and warm-up are excluded from the requested observation duration.
// A killed process may leave an empty summary, which is incomplete evidence.
//
// TestSoak 在真实回环 mTLS 机群上观测合成 Chat 流量。
// AISW_SOAK_DURATION 启用测试；AISW_SOAK_SAMPLE_INTERVAL 默认 5m。
// AISW_SOAK_OUTPUT 指定新 CSV 及其 .summary.json 摘要，已有文件不会被覆盖；
// 未指定时，两份文件保留在 os.TempDir()。请求时长不包含启动与预热。
// 进程被强制结束时可能留下空摘要，这属于未完成的证据。
//
//	AISW_SOAK_DURATION=24h go test ./service/aiServeWeaveGateway/e2e \
//	  -run '^TestSoak$' -count=1 -race -v -timeout 0
func TestSoak(t *testing.T) {
	durationStr := os.Getenv("AISW_SOAK_DURATION")
	if durationStr == "" {
		t.Skip("set AISW_SOAK_DURATION (e.g. 24h) and optionally AISW_SOAK_SAMPLE_INTERVAL to run the soak scenario")
	}
	cfg, err := parseSoakConfig(durationStr, os.Getenv("AISW_SOAK_SAMPLE_INTERVAL"))
	if err != nil {
		t.Fatal(err)
	}

	outputPath := os.Getenv("AISW_SOAK_OUTPUT")
	if outputPath == "" {
		outputPath = filepath.Join(os.TempDir(), fmt.Sprintf("aisw-soak-%s.csv", time.Now().UTC().Format("20060102T150405.000000000Z")))
	}
	report, err := newSoakReport(outputPath)
	if err != nil {
		t.Fatalf("creating soak report %s: %v", outputPath, err)
	}
	const replicaCount = 3
	samples := make([]soakSample, 0, cfg.maxSamples)
	var requests, failures atomic.Int64
	completed := false
	t.Cleanup(func() {
		summary := analyzeSoak(samples, replicaCount, requests.Load(), failures.Load())
		summary.recordRun(cfg, completed)
		if t.Failed() {
			summary.fail("test failed before finalization; inspect the test log")
		}
		if err := report.finish(&summary); err != nil {
			t.Errorf("finalizing soak report: %v", err)
		}
		for _, failure := range summary.Errors {
			t.Error(failure)
		}
		t.Logf("soak summary: passed=%t completed=%t samples=%d observed=%.3fs requests=%d failures=%d goroutine_growth=%.3f heap_growth=%.3f connected=[%d,%d] idle=[%d,%d] max_gap=%.3fs; %s.summary.json",
			summary.Passed, summary.Completed, summary.SampleCount, summary.ObservedSeconds,
			summary.Requests, summary.Failures, summary.Goroutines.Fraction, summary.HeapAllocBytes.Fraction,
			summary.ConnectedReplicas.Min, summary.ConnectedReplicas.Max, summary.IdleSlots.Min, summary.IdleSlots.Max,
			summary.MaxSampleGapSeconds, outputPath)
	})
	t.Logf("soak: synthetic backend, %s observation, %s sampling, CSV %s, summary %s.summary.json", cfg.duration, cfg.sampleInterval, outputPath, outputPath)

	f := newFleet(t, replicaCount)
	f.awaitReady(f.replicas...)
	loadCtx, stopLoad := context.WithCancel(context.Background())
	var loadWG sync.WaitGroup
	t.Cleanup(func() {
		stopLoad()
		loadWG.Wait()
	})

	// Modest traffic cycles each replica's slots without dominating sampled resources.
	// 适度流量让每个副本持续借还槽位，同时避免流量发生器主导资源采样。
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
				if err != nil && loadCtx.Err() != nil {
					return
				}
				requests.Add(1)
				if err != nil {
					failures.Add(1)
				}
			}
		}(r)
	}

	// Only this opt-in soak waits for a real observation window; unit tests use fixed samples.
	// 仅此显式启用的长稳测试等待真实观测窗口；单元测试使用固定样本。
	<-time.After(min(5*time.Second, cfg.duration/10))
	capture := func() {
		if len(samples) == cfg.maxSamples {
			t.Fatalf("sample count reached %d, want at most the configured bound", cfg.maxSamples)
		}
		s := sampleSoak(f, replicaCount)
		samples = append(samples, s)
		if err := report.write(s); err != nil {
			t.Fatalf("writing soak sample: %v", err)
		}
		if len(samples) == 1 || len(samples)%12 == 0 {
			t.Logf("soak: t+%s goroutines=%d heap_alloc=%dMB connected=%d idle_slots=%d requests=%d failures=%d",
				s.at.Sub(samples[0].at).Round(time.Second), s.goroutines, s.heapAllocBytes/1e6,
				s.connectedReplicas, s.idleSlots, requests.Load(), failures.Load())
		}
	}
	capture()
	deadline := samples[0].at.Add(cfg.duration)
	for {
		<-time.After(min(cfg.sampleInterval, time.Until(deadline)))
		capture()
		if !samples[len(samples)-1].at.Before(deadline) {
			completed = true
			return
		}
	}
}

type soakConfig struct {
	duration       time.Duration
	sampleInterval time.Duration
	maxSamples     int
}

func parseSoakConfig(durationStr, intervalStr string) (soakConfig, error) {
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		return soakConfig{}, fmt.Errorf("AISW_SOAK_DURATION: %w", err)
	}
	sampleInterval := 5 * time.Minute
	if intervalStr != "" {
		sampleInterval, err = time.ParseDuration(intervalStr)
		if err != nil {
			return soakConfig{}, fmt.Errorf("AISW_SOAK_SAMPLE_INTERVAL: %w", err)
		}
	}
	if duration <= 0 || sampleInterval <= 0 {
		return soakConfig{}, fmt.Errorf("soak duration and sample interval must be positive, got %s and %s", duration, sampleInterval)
	}
	periods := duration / sampleInterval
	if periods < 3 {
		return soakConfig{}, fmt.Errorf("AISW_SOAK_DURATION=%s is less than 3 sample intervals (%s)", duration, sampleInterval)
	}
	// Leave room for the initial sample and a final sample at the deadline.
	// 为初始样本与截止时刻的最终样本保留空间。
	if periods > 9998 {
		return soakConfig{}, fmt.Errorf("soak sampling exceeds the 10000-sample limit; increase AISW_SOAK_SAMPLE_INTERVAL")
	}
	return soakConfig{duration: duration, sampleInterval: sampleInterval, maxSamples: int(periods) + 2}, nil
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
//
// sampleSoak 读取协程、存活堆、连接与槽数。采样前执行 GC，避免待回收垃圾
// 的波动掩盖 24h 窗口中的真实增长。
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

// soakReport is a CSV sink for soak samples, flushed after every write so a
// run killed partway through (or one still running 20 hours from now) always
// leaves a readable file on disk.
//
// soakReport 每次采样后刷新 CSV，让中断或尚在运行的测试也留下可读记录。
type soakReport struct {
	f       *os.File
	w       *csv.Writer
	summary *os.File
}

func newSoakReport(path string) (*soakReport, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	summary, err := os.OpenFile(path+".summary.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.Join(err, f.Close(), os.Remove(path))
	}
	w := csv.NewWriter(f)
	r := &soakReport{f: f, w: w, summary: summary}
	err = w.Write([]string{"unix_ms", "goroutines", "heap_alloc_bytes", "heap_objects", "connected_replicas", "idle_slots"})
	w.Flush()
	if err = errors.Join(err, w.Error()); err != nil {
		return nil, errors.Join(err, r.close(), os.Remove(path), os.Remove(path+".summary.json"))
	}
	return r, nil
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

func (r *soakReport) close() error {
	r.w.Flush()
	return errors.Join(r.w.Error(), r.f.Sync(), r.f.Close(), r.summary.Close())
}

func (r *soakReport) finish(summary *soakSummary) error {
	r.w.Flush()
	csvErr := errors.Join(r.w.Error(), r.f.Sync(), r.f.Close())
	if csvErr != nil {
		summary.fail(fmt.Sprintf("CSV finalization failed: %v", csvErr))
	}
	encoder := json.NewEncoder(r.summary)
	encoder.SetIndent("", "  ")
	return errors.Join(csvErr, encoder.Encode(summary), r.summary.Sync(), r.summary.Close())
}
