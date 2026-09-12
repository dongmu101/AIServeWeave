package e2e

import (
	"fmt"
	"runtime"
	"time"
)

type soakRange struct {
	// Min is the smallest observed value. / Min 是观测到的最小值。
	Min int `json:"min"`
	// Max is the largest observed value. / Max 是观测到的最大值。
	Max int `json:"max"`
}

type soakGrowth struct {
	// Baseline is the first-quarter arithmetic mean. / Baseline 是前四分之一样本的算术均值。
	Baseline float64 `json:"baseline"`
	// Final is the final sampled value. / Final 是最后一次采样值。
	Final float64 `json:"final"`
	// Fraction is relative growth from baseline. / Fraction 是相对基线的增长比例。
	Fraction float64 `json:"growth_fraction"`
}

type soakSummary struct {
	// SchemaVersion identifies the report format. / SchemaVersion 标识报告格式版本。
	SchemaVersion int `json:"schema_version"`
	// Provenance describes the harness and toolchain. / Provenance 说明测试环境与工具链。
	Provenance map[string]string `json:"provenance"`
	// RequestedDuration excludes setup and warm-up. / RequestedDuration 不包含启动与预热。
	RequestedDuration string `json:"requested_duration"`
	// SampleInterval is the configured sampling period. / SampleInterval 是配置的采样周期。
	SampleInterval string `json:"sample_interval"`
	// StartedAt is the first sample timestamp. / StartedAt 是第一个样本的时间戳。
	StartedAt time.Time `json:"started_at"`
	// FinishedAt is the final sample timestamp. / FinishedAt 是最后一个样本的时间戳。
	FinishedAt time.Time `json:"finished_at"`
	// ObservedSeconds measures the sampled window. / ObservedSeconds 记录实际采样窗口。
	ObservedSeconds float64 `json:"observed_seconds"`
	// MaxSampleGapSeconds is the largest wall-clock sampling gap. / MaxSampleGapSeconds 是最大的墙钟采样间隔。
	MaxSampleGapSeconds float64 `json:"max_sample_gap_seconds"`
	// AllowedSampleGapSeconds bounds pauses at twice the sampling interval. / AllowedSampleGapSeconds 将停顿限制为两倍采样周期。
	AllowedSampleGapSeconds float64 `json:"allowed_sample_gap_seconds"`
	// Completed records whether the requested duration elapsed. / Completed 表示请求的采样时长是否已完整经过。
	Completed bool `json:"completed"`
	// SampleCount is the number of retained samples. / SampleCount 是保留的样本数。
	SampleCount int `json:"sample_count"`
	// BaselineSamples counts samples in the arithmetic mean. / BaselineSamples 是算术均值采用的样本数。
	BaselineSamples int `json:"baseline_samples"`
	// Requests includes warm-up traffic, excluding shutdown cancellation. / Requests 包含预热流量，不含结束时取消的请求。
	Requests int64 `json:"requests"`
	// Failures counts errors excluding shutdown cancellation. / Failures 记录结束时取消之外的请求错误。
	Failures int64 `json:"failures"`
	// FailureRate is the failed fraction of traffic. / FailureRate 是请求失败比例。
	FailureRate float64 `json:"failure_rate"`
	// Goroutines describes process-wide goroutine growth. / Goroutines 说明整个进程的协程增长。
	Goroutines soakGrowth `json:"goroutines"`
	// HeapAllocBytes describes live heap growth after GC. / HeapAllocBytes 说明 GC 后存活堆的增长。
	HeapAllocBytes soakGrowth `json:"heap_alloc_bytes"`
	// ConnectedReplicas is the sampled connection range. / ConnectedReplicas 是连接数的采样范围。
	ConnectedReplicas soakRange `json:"connected_replicas"`
	// IdleSlots is the sampled total inference slot range. / IdleSlots 是推理空闲槽总数的采样范围。
	IdleSlots soakRange `json:"idle_slots"`
	// ExpectedReplicas is the required connection count. / ExpectedReplicas 是要求的连接数量。
	ExpectedReplicas int `json:"expected_replicas"`
	// AllowedIdleSlots bounds the sampled inference slot total. / AllowedIdleSlots 限定推理空闲槽采样总数。
	AllowedIdleSlots soakRange `json:"allowed_idle_slots"`
	// GrowthCeiling is the maximum accepted relative growth. / GrowthCeiling 是允许的最大相对增长。
	GrowthCeiling float64 `json:"growth_ceiling"`
	// FailureRateCeiling is the maximum accepted failure fraction. / FailureRateCeiling 是允许的最大失败比例。
	FailureRateCeiling float64 `json:"failure_rate_ceiling"`
	// Passed reports whether the recorded checks passed. / Passed 表示报告中记录的验收是否通过。
	Passed bool `json:"passed"`
	// Errors records failed acceptance checks without request contents. / Errors 记录失败的验收项，不包含请求内容。
	Errors []string `json:"errors"`
}

// analyzeSoak uses the first quarter's arithmetic mean to assess resource growth.
// analyzeSoak 使用前四分之一样本的算术均值评估资源增长。
func analyzeSoak(samples []soakSample, replicaCount int, requests, failures int64) soakSummary {
	summary := soakSummary{
		SchemaVersion:      1,
		SampleCount:        len(samples),
		BaselineSamples:    max(1, len(samples)/4),
		Requests:           requests,
		Failures:           failures,
		FailureRate:        float64(failures) / float64(max(1, requests)),
		ExpectedReplicas:   replicaCount,
		AllowedIdleSlots:   soakRange{Min: 1, Max: fleetMaxInferenceSlots * replicaCount},
		GrowthCeiling:      1,
		FailureRateCeiling: 0.01,
		Passed:             true,
		Errors:             []string{},
	}
	if requests == 0 {
		summary.fail("completed requests = 0, want active traffic")
	}
	if len(samples) < 3 {
		summary.fail(fmt.Sprintf("collected %d samples, want at least 3", len(samples)))
	}
	if len(samples) == 0 {
		summary.BaselineSamples = 0
		return summary
	}
	first, last := samples[0], samples[len(samples)-1]
	summary.StartedAt, summary.FinishedAt = first.at, last.at
	summary.ObservedSeconds = last.at.Sub(first.at).Seconds()
	summary.ConnectedReplicas = soakRange{Min: first.connectedReplicas, Max: first.connectedReplicas}
	summary.IdleSlots = soakRange{Min: first.idleSlots, Max: first.idleSlots}
	for i, s := range samples {
		if i > 0 {
			// Strip monotonic readings so a suspended host leaves a visible wall-clock gap.
			// 去除单调时钟读数，让主机休眠留下可见的墙钟间隔。
			gap := s.at.Round(0).Sub(samples[i-1].at.Round(0)).Seconds()
			summary.MaxSampleGapSeconds = max(summary.MaxSampleGapSeconds, gap)
		}
		if i < summary.BaselineSamples {
			summary.Goroutines.Baseline += float64(s.goroutines)
			summary.HeapAllocBytes.Baseline += float64(s.heapAllocBytes)
		}
		summary.ConnectedReplicas.Min = min(summary.ConnectedReplicas.Min, s.connectedReplicas)
		summary.ConnectedReplicas.Max = max(summary.ConnectedReplicas.Max, s.connectedReplicas)
		summary.IdleSlots.Min = min(summary.IdleSlots.Min, s.idleSlots)
		summary.IdleSlots.Max = max(summary.IdleSlots.Max, s.idleSlots)
	}
	summary.Goroutines.Baseline /= float64(summary.BaselineSamples)
	summary.HeapAllocBytes.Baseline /= float64(summary.BaselineSamples)
	summary.Goroutines.Final = float64(last.goroutines)
	summary.HeapAllocBytes.Final = float64(last.heapAllocBytes)
	if summary.Goroutines.Baseline > 0 && summary.HeapAllocBytes.Baseline > 0 {
		summary.Goroutines.Fraction = (summary.Goroutines.Final - summary.Goroutines.Baseline) / summary.Goroutines.Baseline
		summary.HeapAllocBytes.Fraction = (summary.HeapAllocBytes.Final - summary.HeapAllocBytes.Baseline) / summary.HeapAllocBytes.Baseline
	} else {
		summary.fail("goroutine and heap baselines must be positive")
	}
	if summary.ConnectedReplicas.Min != replicaCount || summary.ConnectedReplicas.Max != replicaCount {
		summary.fail(fmt.Sprintf("connected replicas ranged [%d, %d], want %d throughout", summary.ConnectedReplicas.Min, summary.ConnectedReplicas.Max, replicaCount))
	}
	if summary.IdleSlots.Min < summary.AllowedIdleSlots.Min || summary.IdleSlots.Max > summary.AllowedIdleSlots.Max {
		summary.fail(fmt.Sprintf("idle inference slots ranged [%d, %d], want within [%d, %d]", summary.IdleSlots.Min, summary.IdleSlots.Max, summary.AllowedIdleSlots.Min, summary.AllowedIdleSlots.Max))
	}
	if summary.Goroutines.Fraction > summary.GrowthCeiling {
		summary.fail(fmt.Sprintf("goroutine growth %.2f exceeds %.2f", summary.Goroutines.Fraction, summary.GrowthCeiling))
	}
	if summary.HeapAllocBytes.Fraction > summary.GrowthCeiling {
		summary.fail(fmt.Sprintf("heap growth %.2f exceeds %.2f", summary.HeapAllocBytes.Fraction, summary.GrowthCeiling))
	}
	if summary.FailureRate > summary.FailureRateCeiling {
		summary.fail(fmt.Sprintf("traffic failure rate %.4f exceeds %.4f", summary.FailureRate, summary.FailureRateCeiling))
	}
	return summary
}

func (s *soakSummary) fail(message string) {
	s.Passed = false
	s.Errors = append(s.Errors, message)
}

func (s *soakSummary) recordRun(cfg soakConfig, completed bool) {
	s.Provenance = map[string]string{
		"backend":    "scriptedRuntime",
		"traffic":    "synthetic fixed Chat responses; no Ollama server, real model, or GPU",
		"topology":   fmt.Sprintf("one Agent and %d Gateway replicas in one test process", s.ExpectedReplicas),
		"transport":  "loopback TCP with mTLS",
		"workload":   "one Chat worker per replica, 200ms interval, 5s request timeout",
		"go_version": runtime.Version(),
		"platform":   runtime.GOOS + "/" + runtime.GOARCH,
	}
	s.RequestedDuration = cfg.duration.String()
	s.SampleInterval = cfg.sampleInterval.String()
	s.Completed = completed && s.ObservedSeconds >= cfg.duration.Seconds()
	if !s.Completed {
		s.fail(fmt.Sprintf("observation window %.3fs did not complete requested duration %s", s.ObservedSeconds, cfg.duration))
	}
	s.AllowedSampleGapSeconds = 2 * cfg.sampleInterval.Seconds()
	if s.MaxSampleGapSeconds > s.AllowedSampleGapSeconds {
		s.fail(fmt.Sprintf("maximum sample gap %.3fs exceeds allowed %.3fs; continuous coverage is incomplete", s.MaxSampleGapSeconds, s.AllowedSampleGapSeconds))
	}
}
