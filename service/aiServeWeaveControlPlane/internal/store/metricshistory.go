package store

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// MetricsHistory persists rolled-up metric samples for Console C27 and
// prunes them past retention.
//
// MetricsHistory 为 Console C27 持久化汇总后的指标样本，并在超出保留期后清理。
type MetricsHistory interface {
	// UpsertRollup writes points, replacing any existing row with the same
	// (metric, labels, bucket_at) — a collector restart mid-bucket must not
	// produce a duplicate row for a bucket it already wrote.
	//
	// UpsertRollup 写入 points，遇到相同 (metric, labels, bucket_at) 的既有行
	// 直接替换——采集器在某个 bucket 中途重启，不应为它已写过的 bucket 产生
	// 重复行。
	UpsertRollup(ctx context.Context, points []model.MetricsHistoryPoint) error
	// ListRollup returns points for any of metrics with bucket_at in
	// [since, until), ordered by bucket_at ascending.
	//
	// ListRollup 返回 metrics 中任意一个、且 bucket_at 落在 [since, until) 的
	// 全部样本，按 bucket_at 升序排列。
	ListRollup(ctx context.Context, metrics []string, since, until time.Time) ([]model.MetricsHistoryPoint, error)
	// DeleteRollupBefore deletes every row with bucket_at < before and
	// reports how many rows it removed.
	//
	// DeleteRollupBefore 删除全部 bucket_at < before 的行，并报告删除行数。
	DeleteRollupBefore(ctx context.Context, before time.Time) (int64, error)
}
