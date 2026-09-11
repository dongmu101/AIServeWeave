package controlplaneclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const (
	// DefaultRevocationWatchTimeout bounds a silent watch failure.
	//
	// DefaultRevocationWatchTimeout 限制静默监听故障的检测时间。
	DefaultRevocationWatchTimeout = 5 * time.Second
	// DefaultRevocationRetryMin is the first reconnect delay.
	//
	// DefaultRevocationRetryMin 是首次重连等待时间。
	DefaultRevocationRetryMin = 100 * time.Millisecond
	// DefaultRevocationRetryMax caps reconnect backoff.
	//
	// DefaultRevocationRetryMax 限制重连退避的上限。
	DefaultRevocationRetryMax  = 2 * time.Second
	maxRevocationResponseBytes = 1 << 10
)

// RunRevocationWatch keeps the local positive cache aligned with the
// ControlPlane generation until ctx is canceled.
//
// RunRevocationWatch 持续让本地正向缓存与控制面 generation 保持一致，直到 ctx
// 被取消。
func (v *Verifier) RunRevocationWatch(ctx context.Context) {
	retry := v.retryMin
	for {
		generation, err := v.watchGeneration(ctx, v.currentGeneration())
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			recovered, reset := v.applyGeneration(generation)
			if reset {
				v.logger.Warn("API key revocation generation reset; cleared the local verification cache")
			}
			if recovered {
				v.logger.Info("API key revocation watch healthy; local verification caching enabled")
			}
			retry = v.retryMin
			continue
		}

		if v.disableCache() {
			v.logger.Warn("API key revocation watch unhealthy; local verification caching disabled")
		}
		ch, stop := v.clock.NewTimer(retry)
		select {
		case <-ctx.Done():
			stop()
			return
		case <-ch:
			stop()
		}
		retry = nextRetry(retry, v.retryMax)
	}
}

// watchGeneration performs one bounded long-poll and decodes its generation.
//
// watchGeneration 执行一次有界长轮询并解码其中的 generation。
func (v *Verifier) watchGeneration(ctx context.Context, after int64) (int64, error) {
	endpoint := v.endpoint + "/internal/v1/apikeys/revocations/watch?after=" + strconv.FormatInt(after, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, errors.New("controlplaneclient: building the revocation watch request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.token)
	resp, err := v.watchClient.Do(req)
	if err != nil {
		return 0, errors.New("controlplaneclient: revocation watch unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("controlplaneclient: revocation watch answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRevocationResponseBytes+1))
	if err != nil || len(body) > maxRevocationResponseBytes {
		return 0, errors.New("controlplaneclient: invalid revocation watch response")
	}
	var decoded struct {
		Generation *int64 `json:"generation"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Generation == nil || *decoded.Generation < 0 {
		return 0, errors.New("controlplaneclient: invalid revocation watch response")
	}
	return *decoded.Generation, nil
}

// currentGeneration returns the cursor sent on the next watch request.
//
// currentGeneration 返回下一次监听请求携带的游标。
func (v *Verifier) currentGeneration() int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.generation
}

// applyGeneration clears stale entries before enabling caching at generation.
//
// applyGeneration 在 generation 上启用缓存之前清除陈旧条目。
func (v *Verifier) applyGeneration(generation int64) (recovered, reset bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	recovered = !v.cacheHealthy
	reset = v.cacheHealthy && generation < v.generation
	if recovered || generation != v.generation {
		v.epoch++
		clear(v.entries)
	}
	v.generation = generation
	v.cacheHealthy = true
	return recovered, reset
}

// disableCache clears every positive entry before marking notifications
// unhealthy. Its result reports a health transition for rate-limited logging.
//
// disableCache 在把通知标为不健康之前清除全部正向条目。返回值报告健康状态变化，
// 供限频日志使用。
func (v *Verifier) disableCache() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	changed := v.cacheHealthy
	if v.cacheHealthy || len(v.entries) > 0 {
		v.epoch++
		clear(v.entries)
	}
	v.cacheHealthy = false
	return changed
}

// nextRetry doubles current without exceeding maximum or overflowing.
//
// nextRetry 将 current 加倍，但不超过 maximum，也不发生溢出。
func nextRetry(current, maximum time.Duration) time.Duration {
	if current >= maximum || current > maximum-current {
		return maximum
	}
	return current * 2
}
