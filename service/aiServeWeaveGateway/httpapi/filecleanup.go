package httpapi

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
)

const (
	// DefaultStudioArtifactRetention is how long the files Studio writes to
	// the deployment's storage (inference images, run inputs) are kept,
	// matching DefaultArtifactRetention for a job's output artifacts.
	// DefaultStudioArtifactRetention 是 Studio 写进部署存储的文件（推理图片、运行输入）的
	// 保留时长，与 job 输出产物的 DefaultArtifactRetention 一致。
	DefaultStudioArtifactRetention = DefaultArtifactRetention
	// DefaultPendingFileTTL is how long a file may stay pending before the
	// cleanup takes it for a write that never finished. It is far above any
	// write's own deadline.
	// DefaultPendingFileTTL 是文件停留在 pending 的最长时间，超过后清理将其视为从未完成的
	// 写入。它远大于任何写入自己的期限。
	DefaultPendingFileTTL = 24 * time.Hour
	// studioStoragePrefix is the key space Studio owns in object storage.
	// studioStoragePrefix 是 Studio 在对象存储中拥有的键空间。
	studioStoragePrefix = "studio"
	// fileCleanupPage is how many records one ListFiles call reads.
	// fileCleanupPage 是一次 ListFiles 读取的记录数。
	fileCleanupPage = 500
)

// fileCleanupConfig is when registered files expire.
// fileCleanupConfig 规定已登记的文件何时过期。
type fileCleanupConfig struct {
	Interval    time.Duration
	CallTimeout time.Duration
	// JobRetention is, by artifact type, how long a job's file in the
	// deployment's storage is kept. A type not listed is kept.
	// JobRetention 按产物类型给出 job 文件在部署存储中的保留时长。未列出的类型不删除。
	JobRetention map[string]time.Duration
	// StudioRetention is how long a Studio file in the deployment's storage
	// is kept.
	// StudioRetention 是 Studio 文件在部署存储中的保留时长。
	StudioRetention time.Duration
	PendingTTL      time.Duration
}

// fileCleaner deletes expired files by their records in the control plane's
// files table: the object first, then a job artifact's own record, then the
// file's record. A file whose object cannot be deleted keeps its records and
// is tried again next time. A tenant's files follow its retention_days (0
// keeps them); a record of a tenant that no longer has its own storage is
// dropped, its object staying where it is.
//
// fileCleaner 按控制面 files 表中的记录删除过期文件：先删对象，再删 job 产物自己的记录，最后
// 删文件记录。对象删不掉的文件保留其记录，下次再试。租户的文件按其 retention_days 清理（0 表示
// 保留）；租户已不再有自有存储时只删记录，对象留在原处。
type fileCleaner struct {
	registry  FileRegistry
	artifacts ArtifactCleanupClient
	files     *fileStore
	source    TenantStorageSource
	clock     runtime.Clock
	logger    *slog.Logger
	cfg       fileCleanupConfig

	stop chan struct{}
	done chan struct{}
}

func newFileCleaner(registry FileRegistry, artifacts ArtifactCleanupClient, files *fileStore, source TenantStorageSource, clock runtime.Clock, logger *slog.Logger, cfg fileCleanupConfig) *fileCleaner {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultArtifactCleanupInterval
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = DefaultArtifactCleanupCallTimeout
	}
	if cfg.StudioRetention <= 0 {
		cfg.StudioRetention = DefaultStudioArtifactRetention
	}
	if cfg.PendingTTL <= 0 {
		cfg.PendingTTL = DefaultPendingFileTTL
	}
	return &fileCleaner{registry: registry, artifacts: artifacts, files: files, source: source, clock: clock, logger: logger, cfg: cfg,
		stop: make(chan struct{}), done: make(chan struct{})}
}

func (c *fileCleaner) run() {
	defer close(c.done)
	for {
		tick, stopTimer := c.clock.NewTimer(c.cfg.Interval)
		select {
		case <-c.stop:
			stopTimer()
			return
		case <-tick:
			c.sweep()
		}
	}
}

// sweep runs one pass, bounded by the interval and ended early by Stop.
// sweep 执行一轮，以间隔为期限，Stop 时提前结束。
func (c *fileCleaner) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Interval)
	defer cancel()
	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	now := c.clock.Now()
	queries := []FileQuery{{Status: filePending, Before: now.Add(-c.cfg.PendingTTL)}}
	if c.files.deployment != nil {
		for kind, retention := range c.cfg.JobRetention {
			if retention > 0 {
				queries = append(queries, FileQuery{Storage: fileStorageDeployment, BizType: fileBizJob, Kind: kind, Before: now.Add(-retention)})
			}
		}
		queries = append(queries, FileQuery{Storage: fileStorageDeployment, BizType: fileBizStudioRun, Before: now.Add(-c.cfg.StudioRetention)})
	}
	for _, q := range queries {
		c.drain(ctx, q)
	}
	if c.source == nil {
		return
	}
	for after := ""; ctx.Err() == nil; {
		page, next, err := c.source.RetainedTenantStorage(ctx, after)
		if err != nil {
			if ctx.Err() == nil {
				c.logger.Warn("listing tenants with a storage retention failed; the next sweep retries", slog.Any("error", err))
			}
			return
		}
		for _, cfg := range page {
			if cfg.Configured && cfg.RetentionDays > 0 && cfg.TenantID != "" {
				c.drain(ctx, FileQuery{Storage: fileStorageTenant, TenantID: cfg.TenantID,
					Before: now.Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)})
			}
		}
		if next == "" || next <= after {
			return
		}
		after = next
	}
}

// drain reaps the files q selects, a page at a time, until a page is short
// or nothing on it could be reaped.
// drain 逐页回收 q 选出的文件，直到某一页不满或其中没有一个能回收。
func (c *fileCleaner) drain(ctx context.Context, q FileQuery) {
	q.Limit = fileCleanupPage
	deleted, failed := 0, 0
	defer func() {
		if deleted > 0 || failed > 0 {
			c.logger.Info("file retention sweep", slog.String("storage", q.Storage), slog.String("tenant_id", q.TenantID),
				slog.String("biz_type", q.BizType), slog.String("kind", q.Kind), slog.String("status", q.Status),
				slog.Int("deleted", deleted), slog.Int("failed", failed))
		}
	}()
	for ctx.Err() == nil {
		callCtx, cancel := context.WithTimeout(ctx, c.cfg.CallTimeout)
		page, err := c.registry.ListFiles(callCtx, q)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				c.logger.Warn("listing expired files failed; the next sweep retries", slog.Any("error", err))
			}
			return
		}
		progressed := 0
		for _, f := range page {
			if ctx.Err() != nil {
				return
			}
			if c.reap(ctx, f) {
				progressed++
			}
		}
		deleted += progressed
		// Failed records come back on the next page, so only the last
		// page's count is reported.
		// 失败的记录会出现在下一页，因此只报告最后一页的失败数。
		failed = len(page) - progressed
		if len(page) < q.Limit || progressed == 0 {
			return
		}
	}
}

// reap deletes one file and its records, reporting whether all of it went.
// reap 删除一个文件及其记录，报告是否全部删除。
func (c *fileCleaner) reap(ctx context.Context, f StoredFile) bool {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.CallTimeout)
	defer cancel()
	backend := c.files.deployment
	if f.Storage == fileStorageTenant {
		var err error
		if backend, err = c.files.tenant(ctx, f.TenantID); err != nil {
			return false
		}
	} else if backend == nil {
		// The deployment's storage is not configured on this replica; its
		// records wait for one that has it.
		// 本副本没有配置部署存储；这些记录等待配置了它的副本处理。
		return false
	}
	if backend != nil {
		if err := backend.Delete(ctx, f.ObjectKey); err != nil {
			c.logger.Warn("deleting an expired file failed; its record stays for the next sweep",
				slog.String("file_id", f.ID), slog.Any("error", err))
			return false
		}
	}
	if f.BizType == fileBizJob && c.artifacts != nil {
		if err := c.artifacts.DeleteJobArtifact(ctx, f.BizID, f.ID); err != nil {
			c.logger.Warn("deleting an expired file's artifact record failed; the next sweep retries",
				slog.String("file_id", f.ID), slog.Any("error", err))
			return false
		}
	}
	if err := c.registry.DeleteFile(ctx, f.ID); err != nil {
		c.logger.Warn("deleting an expired file's record failed; the next sweep retries",
			slog.String("file_id", f.ID), slog.Any("error", err))
		return false
	}
	return true
}

// Stop ends the loop, cancelling a sweep in progress, and waits for it.
// Stop 结束循环，取消进行中的扫描并等待其结束。
func (c *fileCleaner) Stop() {
	close(c.stop)
	<-c.done
}
