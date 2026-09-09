package gormstore

import (
	"context"
	"embed"
	"errors"
	"sort"
	"time"

	"gorm.io/gorm/clause"
)

// workflowTemplateMigrationsFS holds independent dual-dialect workflow
// template migrations (P03).
//
// workflowTemplateMigrationsFS 保存独立的双数据库工作流模板迁移（P03）。
//
//go:embed migrations/workflowtemplates/*/*.sql
var workflowTemplateMigrationsFS embed.FS

type workflowTemplateMigration struct {
	ID        string `gorm:"primaryKey"`
	AppliedAt time.Time
}

// MigrateWorkflowTemplates applies idempotent DDL before recording each
// completed version, the same pattern MigrateRoutes follows. Unlike routes'
// single global pointer row, no seed row is written here: each template's
// WorkflowTemplateActive row is created lazily on that template's first
// publish (see gormstore's PublishWorkflowTemplateRevision), because there is
// no one row to race for before any template exists.
//
// MigrateWorkflowTemplates 先执行幂等 DDL，再记录已完成版本，与
// MigrateRoutes 同一种模式。与路由那张单一全局指针行不同，这里不写入种子行：
// 每个模板的 WorkflowTemplateActive 行在该模板首次发布时惰性创建（见 gormstore
// 的 PublishWorkflowTemplateRevision），因为在任何模板存在之前，没有唯一一行
// 可供竞争。
func (s *Store) MigrateWorkflowTemplates(ctx context.Context) ([]string, error) {
	dialect := s.db.Name()
	timestamp := "TIMESTAMPTZ"
	suffix := ""
	switch dialect {
	case "postgres":
	case "mysql":
		timestamp = "DATETIME(6)"
		suffix = " ENGINE=InnoDB"
	default:
		return nil, errors.New("gormstore: workflow template migration requires postgres or mysql")
	}
	db := s.db.WithContext(ctx)
	if err := db.Exec("CREATE TABLE IF NOT EXISTS schema_migrations_workflow_templates (id VARCHAR(255) PRIMARY KEY, applied_at " + timestamp + " NOT NULL)" + suffix).Error; err != nil {
		return nil, err
	}
	dir := "migrations/workflowtemplates/" + dialect
	entries, err := workflowTemplateMigrationsFS.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var records []workflowTemplateMigration
	if err := db.Table("schema_migrations_workflow_templates").Find(&records).Error; err != nil {
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
		body, err := workflowTemplateMigrationsFS.ReadFile(dir + "/" + entry.Name())
		if err != nil {
			return out, err
		}
		if err := db.Exec(string(body)).Error; err != nil {
			return out, err
		}
		record := workflowTemplateMigration{ID: entry.Name(), AppliedAt: time.Now().UTC()}
		if err := db.Table("schema_migrations_workflow_templates").Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error; err != nil {
			return out, err
		}
		out = append(out, entry.Name())
	}
	return out, nil
}
