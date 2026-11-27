package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"AIServeWeave/common/objectstore"
	"AIServeWeave/common/runtime"
)

// Retention of the objects Studio writes to object storage itself, under
// studioStoragePrefix: inference images flow runs produced and files flow
// runs were started with. No metadata row records them, so the sweep lists
// the key space and judges each object by when it was written.
//
// Studio 自己写入对象存储（studioStoragePrefix 之下）的对象的保留期：编排运行产出的
// 推理图片，以及启动编排运行时上传的文件。没有元数据行记录它们，因此清理扫描列举该
// 键空间，按每个对象的写入时间判断。
const (
	// DefaultStudioArtifactRetention is how long such an object is kept,
	// matching DefaultArtifactRetention for a job's output artifacts.
	// DefaultStudioArtifactRetention 是这类对象的保留时长，与 job 输出产物的
	// DefaultArtifactRetention 一致。
	DefaultStudioArtifactRetention = DefaultArtifactRetention
	// DefaultStudioCleanupInterval is how often the sweep runs. Retention is
	// counted in days, and each sweep lists every Studio object, so it runs
	// less often than the job artifact sweep.
	// DefaultStudioCleanupInterval 是扫描的间隔。保留期以天计，每轮都要列举全部 Studio
	// 对象，因此比 job 产物扫描跑得少。
	DefaultStudioCleanupInterval = time.Hour
	// studioStoragePrefix is the key space Studio owns in object storage.
	// studioStoragePrefix 是 Studio 在对象存储中拥有的键空间。
	studioStoragePrefix = "studio"
)

// studioCleaner deletes Studio's objects older than its retention. A failed
// delete is simply found again by the next sweep. The listing of one sweep is
// bounded by the interval, so a slow store cannot pile sweeps up.
//
// studioCleaner 删除超过保留期的 Studio 对象。删除失败的对象会被下一轮扫描再次找到。
// 一轮扫描的列举以间隔为期限，因此缓慢的存储不会让扫描堆积。
type studioCleaner struct {
	storage     objectstore.Backend
	clock       runtime.Clock
	logger      *slog.Logger
	interval    time.Duration
	retention   time.Duration
	callTimeout time.Duration

	stop chan struct{}
	done chan struct{}
}

func newStudioCleaner(storage objectstore.Backend, clock runtime.Clock, logger *slog.Logger, interval, retention time.Duration) *studioCleaner {
	if interval <= 0 {
		interval = DefaultStudioCleanupInterval
	}
	if retention <= 0 {
		retention = DefaultStudioArtifactRetention
	}
	return &studioCleaner{storage: storage, clock: clock, logger: logger, interval: interval, retention: retention,
		callTimeout: DefaultArtifactCleanupCallTimeout, stop: make(chan struct{}), done: make(chan struct{})}
}

func (c *studioCleaner) run() {
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

// sweep deletes every Studio object written before now minus the retention.
// sweep 删除所有在「当前时间减保留期」之前写入的 Studio 对象。
func (c *studioCleaner) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), c.interval)
	defer cancel()
	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	cutoff := c.clock.Now().Add(-c.retention)
	deleted, failed := 0, 0
	err := c.storage.List(ctx, studioStoragePrefix, func(e objectstore.ObjectEntry) error {
		if !e.ModTime.Before(cutoff) {
			return nil
		}
		callCtx, callCancel := context.WithTimeout(ctx, c.callTimeout)
		err := c.storage.Delete(callCtx, e.Key)
		callCancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			if failed == 1 {
				c.logger.Warn("deleting an expired studio object failed; the next sweep retries it",
					slog.String("key", e.Key), slog.Any("error", err))
			}
			return nil
		}
		deleted++
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		c.logger.Warn("listing studio objects failed; the sweep ends early and the next one continues",
			slog.Any("error", err))
	}
	if deleted > 0 || failed > 0 {
		c.logger.Info("studio object retention sweep", slog.Int("deleted", deleted), slog.Int("failed", failed))
	}
}

// Stop ends the loop and waits for the sweep in progress, which it cuts
// short.
// Stop 结束循环并等待进行中的扫描（会被提前截断）结束。
func (c *studioCleaner) Stop() {
	close(c.stop)
	<-c.done
}
