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
	if action != "up" && action != "status" && action != "resume" {
		return errors.New("migrate must be up, status, or resume")
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
	st := gormstore.New(db)
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
