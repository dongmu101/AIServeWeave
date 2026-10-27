package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestSoakAnalysisRejectsFalsePasses exercises acceptance boundaries with fixed samples.
// TestSoakAnalysisRejectsFalsePasses 使用固定样本检查验收边界，避免误判通过。
func TestSoakAnalysisRejectsFalsePasses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func([]soakSample)
		requests int64
		failures int64
		wantPass bool
	}{
		{name: "stable fleet", requests: 1000, wantPass: true},
		{name: "goroutines at ceiling", requests: 1000, wantPass: true, change: func(s []soakSample) { s[7].goroutines = 200 }},
		{name: "heap at ceiling", requests: 1000, wantPass: true, change: func(s []soakSample) { s[7].heapAllocBytes = 2000 }},
		{name: "goroutine leak", requests: 1000, change: func(s []soakSample) { s[7].goroutines = 201 }},
		{name: "heap growth", requests: 1000, change: func(s []soakSample) { s[7].heapAllocBytes = 2001 }},
		{name: "one lost connection", requests: 1000, change: func(s []soakSample) { s[3].connectedReplicas = 2 }},
		{name: "extra connection", requests: 1000, change: func(s []soakSample) { s[3].connectedReplicas = 4 }},
		{name: "slot pool exhausted", requests: 1000, change: func(s []soakSample) { s[3].idleSlots = 0 }},
		{name: "slot pool exceeds limit", requests: 1000, change: func(s []soakSample) { s[3].idleSlots = 13 }},
		{name: "no traffic", requests: 0},
		{name: "failure rate at ceiling", requests: 1000, failures: 10, wantPass: true},
		{name: "failure rate exceeds ceiling", requests: 1000, failures: 11},
		{name: "invalid baseline", requests: 1000, change: func(s []soakSample) {
			s[0].goroutines, s[1].goroutines = 0, 0
			s[0].heapAllocBytes, s[1].heapAllocBytes = 0, 0
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples := make([]soakSample, 8)
			for i := range samples {
				samples[i] = soakSample{at: time.Unix(100+int64(i), 0), goroutines: 100, heapAllocBytes: 1000, connectedReplicas: 3, idleSlots: 6}
			}
			if tc.change != nil {
				tc.change(samples)
			}
			got := analyzeSoak(samples, 3, tc.requests, tc.failures)
			if got.Passed != tc.wantPass {
				t.Errorf("Passed = %t with errors %v, want %t", got.Passed, got.Errors, tc.wantPass)
			}
			if _, err := json.Marshal(got); err != nil {
				t.Errorf("summary JSON error = %v, want nil even for failed analysis", err)
			}
		})
	}
}

// TestSoakAnalysisHandlesIncompleteEvidence keeps partial runs inspectable.
// TestSoakAnalysisHandlesIncompleteEvidence 保证未完成的运行仍可检查。
func TestSoakAnalysisHandlesIncompleteEvidence(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(count)+" samples", func(t *testing.T) {
			samples := make([]soakSample, count)
			for i := range samples {
				samples[i] = soakSample{at: time.Unix(100+int64(i), 0), goroutines: 100, heapAllocBytes: 1000, connectedReplicas: 3, idleSlots: 6}
			}
			got := analyzeSoak(samples, 3, 1000, 0)
			if got.Passed || len(got.Errors) == 0 {
				t.Errorf("partial summary = %+v, want failed acceptance with reasons", got)
			}
		})
	}
}

// TestSoakSummaryPersistsOutcome preserves completed and interrupted acceptance evidence.
// TestSoakSummaryPersistsOutcome 保留完整运行与中断运行的验收证据。
func TestSoakSummaryPersistsOutcome(t *testing.T) {
	for _, tc := range []struct {
		name          string
		completed     bool
		finalSecond   int64
		closeCSV      bool
		sampleGap     bool
		wantPass      bool
		wantCompleted bool
	}{
		{name: "complete", completed: true, finalSecond: 103, wantPass: true, wantCompleted: true},
		{name: "gap at allowed ceiling", completed: true, finalSecond: 104, wantPass: true, wantCompleted: true},
		{name: "interrupted", finalSecond: 102},
		{name: "short window cannot claim completion", completed: true, finalSecond: 102},
		{name: "CSV close failure", completed: true, finalSecond: 103, closeCSV: true, wantCompleted: true},
		{name: "suspended collection", completed: true, finalSecond: 110, sampleGap: true, wantCompleted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "soak.csv")
			report, err := newSoakReport(path)
			if err != nil {
				t.Fatal(err)
			}
			samples := make([]soakSample, 4)
			for i := range samples {
				samples[i] = soakSample{at: time.Unix(100+int64(i), 0), goroutines: 100, heapAllocBytes: 1000, connectedReplicas: 3, idleSlots: 6}
			}
			samples[3].at = time.Unix(tc.finalSecond, 0)
			for _, sample := range samples {
				if err := report.write(sample); err != nil {
					t.Fatal(err)
				}
			}
			summary := analyzeSoak(samples, 3, 1000, 0)
			summary.recordRun(soakConfig{duration: 3 * time.Second, sampleInterval: time.Second}, tc.completed)
			if tc.closeCSV {
				if err := report.f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			err = report.finish(&summary)
			if tc.closeCSV {
				if !errors.Is(err, os.ErrClosed) {
					t.Errorf("finish error = %v, want closed-file error", err)
				}
			} else if err != nil {
				t.Errorf("finish error = %v, want nil", err)
			}
			data, err := os.ReadFile(path + ".summary.json")
			if err != nil {
				t.Fatal(err)
			}
			var got soakSummary
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("summary JSON = %q: %v, want valid final report", data, err)
			}
			if got.Passed != tc.wantPass || got.Completed != tc.wantCompleted {
				t.Errorf("Passed/Completed = %t/%t with errors %v, want %t/%t", got.Passed, got.Completed, got.Errors, tc.wantPass, tc.wantCompleted)
			}
			if got.SampleCount != 4 || got.Requests != 1000 || got.ObservedSeconds != float64(tc.finalSecond-100) {
				t.Errorf("samples/requests/duration = %d/%d/%v, want 4/1000/%d", got.SampleCount, got.Requests, got.ObservedSeconds, tc.finalSecond-100)
			}
			if got.Provenance["backend"] != "scriptedRuntime" || got.Provenance["go_version"] == "" {
				t.Errorf("provenance = %v, want explicit scripted backend and Go version", got.Provenance)
			}
			if got.RequestedDuration != "3s" || got.SampleInterval != "1s" {
				t.Errorf("duration/interval = %q/%q, want 3s/1s", got.RequestedDuration, got.SampleInterval)
			}
			if tc.sampleGap && got.MaxSampleGapSeconds != 8 {
				t.Errorf("maximum sample gap = %v, want 8 seconds", got.MaxSampleGapSeconds)
			}
		})
	}
}

// TestSoakAnalysisReportsMeasuredValues verifies report arithmetic against hand-checked samples.
// TestSoakAnalysisReportsMeasuredValues 用手工核算的样本验证报告数值。
func TestSoakAnalysisReportsMeasuredValues(t *testing.T) {
	samples := make([]soakSample, 8)
	for i := range samples {
		samples[i] = soakSample{at: time.Unix(100+int64(i), 0), goroutines: 100, heapAllocBytes: 1000, connectedReplicas: 3, idleSlots: 6}
	}
	samples[1].goroutines, samples[1].heapAllocBytes = 140, 1400
	samples[7].goroutines, samples[7].heapAllocBytes = 240, 2400
	samples[2].idleSlots, samples[4].idleSlots = 5, 8
	got := analyzeSoak(samples, 3, 1000, 1)
	if !got.Passed || got.BaselineSamples != 2 || got.ObservedSeconds != 7 || got.FailureRate != 0.001 {
		t.Errorf("pass/baseline samples/duration/failure rate = %t/%d/%v/%v, want true/2/7/0.001", got.Passed, got.BaselineSamples, got.ObservedSeconds, got.FailureRate)
	}
	if got.Goroutines != (soakGrowth{Baseline: 120, Final: 240, Fraction: 1}) || got.HeapAllocBytes != (soakGrowth{Baseline: 1200, Final: 2400, Fraction: 1}) {
		t.Errorf("growth = %+v/%+v, want goroutines 120→240 and heap 1200→2400 (both 100%%)", got.Goroutines, got.HeapAllocBytes)
	}
	if got.ConnectedReplicas != (soakRange{Min: 3, Max: 3}) || got.IdleSlots != (soakRange{Min: 5, Max: 8}) {
		t.Errorf("connection/idle ranges = %+v/%+v, want [3,3]/[5,8]", got.ConnectedReplicas, got.IdleSlots)
	}
}

// TestSoakConfigRejectsUnsafeSampling prevents invalid timers and unbounded collection.
// TestSoakConfigRejectsUnsafeSampling 防止无效定时器与无界采样积累。
func TestSoakConfigRejectsUnsafeSampling(t *testing.T) {
	for _, tc := range []struct {
		name, duration, interval string
	}{
		{name: "invalid duration", duration: "yesterday", interval: "1s"},
		{name: "zero duration", duration: "0", interval: "1s"},
		{name: "negative duration", duration: "-3s", interval: "1s"},
		{name: "invalid interval", duration: "3s", interval: "soon"},
		{name: "zero interval", duration: "3s", interval: "0"},
		{name: "negative interval", duration: "3s", interval: "-1s"},
		{name: "too few periods", duration: "2s", interval: "1s"},
		{name: "multiplication overflow", duration: "1h", interval: "1000000h"},
		{name: "too many samples", duration: "24h", interval: "1ms"},
		{name: "one sample beyond limit", duration: "9999s", interval: "1s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseSoakConfig(tc.duration, tc.interval)
			if err == nil {
				t.Errorf("parseSoakConfig(%q, %q) = %+v, want validation error", tc.duration, tc.interval, cfg)
			}
		})
	}
}

// TestSoakConfigBoundsCollection reserves a finite buffer for initial and final samples.
// TestSoakConfigBoundsCollection 为首尾样本保留有限的采样缓冲区。
func TestSoakConfigBoundsCollection(t *testing.T) {
	for _, tc := range []struct {
		name, duration, interval string
		wantInterval             time.Duration
		wantBound                int
	}{
		{name: "24 hour defaults", duration: "24h", wantInterval: 5 * time.Minute, wantBound: 290},
		{name: "short smoke", duration: "3s", interval: "1s", wantInterval: time.Second, wantBound: 5},
		{name: "largest accepted buffer", duration: "9998s", interval: "1s", wantInterval: time.Second, wantBound: 10000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseSoakConfig(tc.duration, tc.interval)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.sampleInterval != tc.wantInterval || cfg.maxSamples != tc.wantBound {
				t.Errorf("interval/bound = %v/%d, want %v/%d", cfg.sampleInterval, cfg.maxSamples, tc.wantInterval, tc.wantBound)
			}
		})
	}
}

// TestSoakReportPreservesExistingEvidence rejects either occupied report path.
// TestSoakReportPreservesExistingEvidence 拒绝覆盖任一已存在的报告文件。
func TestSoakReportPreservesExistingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
	}{
		{name: "CSV already exists", suffix: ""},
		{name: "summary already exists", suffix: ".summary.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "soak.csv")
			occupied := path + tc.suffix
			if err := os.WriteFile(occupied, []byte("prior evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			report, err := newSoakReport(path)
			if report != nil {
				report.close()
			}
			if err == nil {
				t.Errorf("newSoakReport error = nil, want occupied-path error for %s", occupied)
			}
			data, readErr := os.ReadFile(occupied)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(data) != "prior evidence" {
				t.Errorf("existing report = %q, want %q", data, "prior evidence")
			}
		})
	}
}
