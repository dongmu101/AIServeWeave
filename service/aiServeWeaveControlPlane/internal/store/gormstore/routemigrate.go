package gormstore

import (
	"context"
	"embed"
	"errors"
	"sort"
	"time"

	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// routeMigrationsFS holds independent dual-dialect routing migrations. / routeMigrationsFS 保存独立的双数据库路由迁移。
//
//go:embed migrations/routes/*/*.sql
var routeMigrationsFS embed.FS

type routeMigration struct {
	ID        string `gorm:"primaryKey"`
	AppliedAt time.Time
}

// MigrateRoutes applies idempotent DDL before recording each completed version.
// MySQL implicitly commits DDL; a retry safely replays an unrecorded CREATE.
// MigrateRoutes 先执行幂等 DDL，再记录已完成版本。MySQL 隐式提交 DDL，重试可安全重放未记录的 CREATE。
func (s *Store) MigrateRoutes(ctx context.Context) ([]string, error) {
	dialect := s.db.Name()
	timestamp := "TIMESTAMPTZ"
	suffix := ""
	switch dialect {
	case "postgres":
	case "mysql":
		timestamp = "DATETIME(6)"
		suffix = " ENGINE=InnoDB"
	default:
		return nil, errors.New("gormstore: routing migration requires postgres or mysql")
	}
	db := s.db.WithContext(ctx)
	if err := db.Exec("CREATE TABLE IF NOT EXISTS schema_migrations_routes (id VARCHAR(255) PRIMARY KEY, applied_at " + timestamp + " NOT NULL)" + suffix).Error; err != nil {
		return nil, err
	}
	dir := "migrations/routes/" + dialect
	entries, err := routeMigrationsFS.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var records []routeMigration
	if err := db.Table("schema_migrations_routes").Find(&records).Error; err != nil {
		return nil, err
	}
	applied := map[string]bool{}
	for _, record := range records {
		applied[record.ID] = true
	}
	out := []string{}
	for _, entry := range entries {
		if applied[entry.Name()] {
			continue
		}
		body, err := routeMigrationsFS.ReadFile(dir + "/" + entry.Name())
		if err != nil {
			return out, err
		}
		if err := db.Exec(string(body)).Error; err != nil {
			return out, err
		}
		record := routeMigration{ID: entry.Name(), AppliedAt: time.Now().UTC()}
		if err := db.Table("schema_migrations_routes").Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error; err != nil {
			return out, err
		}
		out = append(out, entry.Name())
	}
	// Seed before serving so first-publication races lock the same existing row. / 服务启动前写入单例，使首次并发发布锁定同一现有行。
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.RouteActive{ID: 1}).Error; err != nil {
		return out, err
	}
	return out, nil
}
