package svc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
)

// RunDatabaseCommand runs database-only migration operations without Redis or HTTP.
// RunDatabaseCommand 只执行数据库迁移操作，不连接 Redis 或启动 HTTP。
func RunDatabaseCommand(ctx context.Context, cfg config.DatabaseConf, billing config.BillingConf, action string, output io.Writer) error {
	if action != "up" && action != "status" && action != "resume" && action != backfillUsageCost && action != compactUsageLedger {
		return errors.New("migrate must be up, status, resume, " + backfillUsageCost + ", or " + compactUsageLedger)
	}
	if action == compactUsageLedger && cfg.MaintenanceDSN != "" {
		// Compaction deletes ledger entries; a runtime account restricted to
		// INSERT and SELECT on credit_ledger cannot, the maintenance one can.
		// 压缩会删除账目；对 credit_ledger 只有 INSERT 与 SELECT 权限的运行账号做不到，
		// 维护账号可以。
		cfg.DSN = cfg.MaintenanceDSN
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
	if action == compactUsageLedger {
		loc, err := billing.LedgerLocation()
		if err != nil {
			return fmt.Errorf("config: Billing.LedgerTimezone %q is not a known IANA time zone", billing.LedgerTimezone)
		}
		return runUsageLedgerCompaction(ctx, st, loc, output)
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

// compactUsageLedger is the -migrate action that merges pre-upgrade usage
// ledger entries day by day (.scratch/token-pricing「余额账目」). It rewrites
// financial records; back up first.
//
// compactUsageLedger 是按天合并升级前用量账目的 -migrate 动作
// （.scratch/token-pricing「余额账目」）。它会改写财务记录，执行前须先备份。
const compactUsageLedger = "compact-usage-ledger"

// runUsageLedgerCompaction refuses to run on a schema that is not current,
// then compacts and reports progress as JSON lines.
//
// runUsageLedgerCompaction 在表结构不是最新时拒绝执行，否则执行压缩，并以 JSON 行报告
// 进度。
func runUsageLedgerCompaction(ctx context.Context, st *gormstore.Store, loc *time.Location, output io.Writer) error {
	if err := st.CheckSchema(ctx); err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	merged, err := st.CompactUsageLedger(ctx, loc, func(tenants int, merged int64) {
		_ = encoder.Encode(map[string]any{"compact": compactUsageLedger, "tenants": tenants, "merged": merged})
	})
	if err != nil {
		return err
	}
	return encoder.Encode(map[string]any{"compact": compactUsageLedger, "merged": merged, "done": true})
}
