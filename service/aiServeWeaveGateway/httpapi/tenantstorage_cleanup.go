package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"time"

	"AIServeWeave/common/objectstore"
	"AIServeWeave/common/runtime"
)

// DefaultTenantStorageCleanupInterval is how often the tenant storage
// retention sweep runs.
//
// DefaultTenantStorageCleanupInterval 是租户存储保留期清理的运行间隔。
const DefaultTenantStorageCleanupInterval = time.Hour

// tenantStorageCleaner deletes, from each tenant's own storage, the files
// this platform wrote there that are older than the tenant's retention. It
// lists only the two prefixes the platform writes under, "<tenant>/" (job
// artifacts) and "studio/<tenant>/" (Studio files), and skips tenants with
// no retention, whose files the platform never deletes. As studioCleaner, a
// failed delete is found again by the next sweep, and one sweep is bounded
// by the interval.
//
// tenantStorageCleaner 从每个租户的自有存储中删除平台写入、且超过该租户保留期的文件。它只
// 列举平台写入的两个前缀："<租户>/"（job 产物）与 "studio/<租户>/"（Studio 文件），并跳过
// 未设保留期的租户——平台从不删除它们的文件。与 studioCleaner 一样，删除失败的文件由下一轮
// 再次找到，一轮扫描以间隔为期限。
type tenantStorageCleaner struct {
	source      TenantStorageSource
	build       func(objectstore.Config, bool) (objectstore.Backend, error)
	clock       runtime.Clock
	logger      *slog.Logger
	interval    time.Duration
	callTimeout time.Duration

	stop chan struct{}
	done chan struct{}
}

func newTenantStorageCleaner(source TenantStorageSource, clock runtime.Clock, logger *slog.Logger, interval time.Duration) *tenantStorageCleaner {
	if interval <= 0 {
		interval = DefaultTenantStorageCleanupInterval
	}
	return &tenantStorageCleaner{source: source, build: objectstore.NewTenant, clock: clock, logger: logger, interval: interval,
		callTimeout: DefaultArtifactCleanupCallTimeout, stop: make(chan struct{}), done: make(chan struct{})}
}

func (c *tenantStorageCleaner) run() {
	defer close(c.done)
	for {
		tick, stopTimer := c.clock.NewTimer(c.interval)
		select {
		case <-c.stop:
			stopTimer()
			return
		case <-tick:
			c.sweep()
		}
	}
}

// sweep pages through the tenants with a retention and cleans each one.
// sweep 逐页遍历设有保留期的租户，并逐个清理。
func (c *tenantStorageCleaner) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), c.interval)
	defer cancel()
	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	after := ""
	for {
		page, next, err := c.source.RetainedTenantStorage(ctx, after)
		if err != nil {
			if ctx.Err() == nil {
				c.logger.Warn("listing tenants with a storage retention failed; the next sweep retries", slog.Any("error", err))
			}
			return
		}
		for _, cfg := range page {
			if ctx.Err() != nil {
				return
			}
			c.sweepTenant(ctx, cfg)
		}
		if next == "" || next <= after {
			return
		}
		after = next
	}
}

func (c *tenantStorageCleaner) sweepTenant(ctx context.Context, cfg TenantStorageConfig) {
	if !cfg.Configured || cfg.RetentionDays <= 0 || cfg.TenantID == "" {
		return
	}
	backend, err := c.build(cfg.Backend, cfg.AllowPrivate)
	if err != nil {
		c.logger.Warn("a tenant's storage settings do not build a backend; its retention sweep is skipped",
			slog.String("tenant_id", cfg.TenantID), slog.Any("error", err))
		return
	}
	cutoff := c.clock.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)
	deleted, failed := 0, 0
	for _, prefix := range []string{cfg.TenantID, path.Join(studioStoragePrefix, cfg.TenantID)} {
		err := backend.List(ctx, prefix, func(e objectstore.ObjectEntry) error {
			if !e.ModTime.Before(cutoff) {
				return nil
			}
			callCtx, callCancel := context.WithTimeout(ctx, c.callTimeout)
			err := backend.Delete(callCtx, e.Key)
			callCancel()
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				failed++
				return nil
			}
			deleted++
			return nil
		})
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return
			}
			c.logger.Warn("listing a tenant's storage failed; its sweep continues next time",
				slog.String("tenant_id", cfg.TenantID), slog.String("prefix", prefix), slog.Any("error", err))
		}
	}
	if deleted > 0 || failed > 0 {
		c.logger.Info("tenant storage retention sweep", slog.String("tenant_id", cfg.TenantID),
			slog.Int("deleted", deleted), slog.Int("failed", failed))
	}
}

// Stop ends the loop and waits for the sweep in progress, which it cuts
// short.
// Stop 结束循环并等待进行中的扫描（会被提前截断）结束。
func (c *tenantStorageCleaner) Stop() {
	close(c.stop)
	<-c.done
}
