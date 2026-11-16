package svc

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

// RunDatabaseCommand runs database-only migration operations without Redis or HTTP.
// RunDatabaseCommand 只执行数据库迁移操作，不连接 Redis 或启动 HTTP。
func RunDatabaseCommand(ctx context.Context, cfg config.DatabaseConf, action string, output io.Writer) error {
	if action != "up" && action != "status" && action != "resume" && action != backfillUsageCost {
		return errors.New("migrate must be up, status, resume, or " + backfillUsageCost)
	}
	if cfg.DSN == "" {
		return errors.New("config: Database.DSN is required")
	}
	db, err := openDatabase(cfg)
	if err != nil {
		return errors.New("opening configured database failed")
	}
	pool, err := db.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	st := gormstore.New(db, gormstore.WithMigrationTimeout(cfg.MigrationTimeout))
	if action == backfillUsageCost {
		return runUsageCostBackfill(ctx, st, output)
	}
	if action != "status" {
		if err := st.MigrateAll(ctx, action == "resume"); err != nil {
			return err
		}
	}
	statuses, err := st.MigrationStatus(ctx)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(statuses); err != nil {
		return err
	}
	return st.CheckSchema(ctx)
}

// backfillUsageCost is the -migrate action that copies pre-upgrade costs
// from usage_charges into usage_records (.scratch/token-pricing).
//
// backfillUsageCost 是把升级前的费用从 usage_charges 复制到 usage_records 的
// -migrate 动作（.scratch/token-pricing）。
const backfillUsageCost = "backfill-usage-cost"

// runUsageCostBackfill refuses to run on a schema that is not current, then
// backfills and reports progress as JSON lines.
//
// runUsageCostBackfill 在表结构不是最新时拒绝执行，否则执行回填，并以 JSON 行报告进度。
func runUsageCostBackfill(ctx context.Context, st *gormstore.Store, output io.Writer) error {
	if err := st.CheckSchema(ctx); err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	updated, err := st.BackfillUsageCost(ctx, func(updated int64) {
		_ = encoder.Encode(map[string]any{"backfill": backfillUsageCost, "updated": updated})
	})
	if err != nil {
		return err
	}
	return encoder.Encode(map[string]any{"backfill": backfillUsageCost, "updated": updated, "done": true})
}
