package gormstore

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/apidocs"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// apiDocListColumns is every api_docs column except content_json, so a
// navigation list never loads page bodies.
//
// apiDocListColumns 是 api_docs 中除 content_json 以外的全部列，使导航列表从不加载页面正文。
var apiDocListColumns = []string{"slug", "category", "category_order", "sort_order", "title", "method", "path",
	"summary", "status", "source", "seed_revision", "version", "updated_by", "updated_at"}

// MigrateAPIDocs applies the api_docs versioned SQL under the shared
// migration lock — a narrow entry point for live tests; production goes
// through MigrateAll.
//
// MigrateAPIDocs 在共享迁移锁下应用 api_docs 的版本化 SQL——供独立测试使用的窄入口；
// 生产走 MigrateAll。
func (s *Store) MigrateAPIDocs(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "api_docs")
}

// ListAPIDocs implements store.APIDocs. / ListAPIDocs 实现 store.APIDocs。
func (s *Store) ListAPIDocs(ctx context.Context, publishedOnly bool) ([]model.APIDoc, error) {
	db := s.db.WithContext(ctx).Model(&model.APIDoc{}).Select(apiDocListColumns)
	if publishedOnly {
		db = db.Where("status = ?", apidocs.StatusPublished)
	}
	var out []model.APIDoc
	err := db.Order("category_order, sort_order, slug").Find(&out).Error
	return out, translate(err)
}

// GetAPIDoc implements store.APIDocs. / GetAPIDoc 实现 store.APIDocs。
func (s *Store) GetAPIDoc(ctx context.Context, slug string) (model.APIDoc, error) {
	var out model.APIDoc
	err := s.db.WithContext(ctx).Where("slug = ?", slug).Take(&out).Error
	return out, translate(err)
}

// CreateAPIDoc implements store.APIDocs. / CreateAPIDoc 实现 store.APIDocs。
func (s *Store) CreateAPIDoc(ctx context.Context, doc *model.APIDoc, audit *model.AuditLog) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(doc).Error; err != nil {
			return err
		}
		return tx.Create(audit).Error
	}))
}

// UpdateAPIDoc implements store.APIDocs. / UpdateAPIDoc 实现 store.APIDocs。
func (s *Store) UpdateAPIDoc(ctx context.Context, doc *model.APIDoc, expected int64, audit *model.AuditLog) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.APIDoc{}).Where("slug = ? AND version = ?", doc.Slug, expected).Updates(map[string]any{
			"category": doc.Category, "category_order": doc.CategoryOrder, "sort_order": doc.SortOrder,
			"title": doc.Title, "method": doc.Method, "path": doc.Path, "summary": doc.Summary,
			"content_json": doc.ContentJSON, "status": doc.Status, "source": doc.Source,
			"seed_revision": doc.SeedRevision, "version": expected + 1,
			"updated_by": doc.UpdatedBy, "updated_at": doc.UpdatedAt,
		})
		if err := casOutcome(tx, result, doc.Slug); err != nil {
			return err
		}
		doc.Version = expected + 1
		return tx.Create(audit).Error
	}))
}

// DeleteAPIDoc implements store.APIDocs. / DeleteAPIDoc 实现 store.APIDocs。
func (s *Store) DeleteAPIDoc(ctx context.Context, slug string, expected int64, audit *model.AuditLog) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Where("slug = ? AND version = ?", slug, expected).Delete(&model.APIDoc{})
		if err := casOutcome(tx, result, slug); err != nil {
			return err
		}
		return tx.Create(audit).Error
	}))
}

// casOutcome turns a conditional write's zero-row result into ErrNotFound
// when the slug is gone and ErrConflict when only its version moved.
//
// casOutcome 把条件写入影响 0 行的结果区分为：slug 已不存在时 ErrNotFound，仅版本
// 变化时 ErrConflict。
func casOutcome(tx *gorm.DB, result *gorm.DB, slug string) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var count int64
	if err := tx.Model(&model.APIDoc{}).Where("slug = ?", slug).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return store.ErrNotFound
	}
	return store.ErrConflict
}

// SeedAPIDoc implements store.APIDocs. Both statements are single,
// idempotent and conditional, so replicas starting together cannot
// interleave them into a lost operator edit: the refresh only matches a row
// that is still seed-sourced.
//
// SeedAPIDoc 实现 store.APIDocs。两条语句都是单条、幂等且带条件的，因此同时启动的
// 多个副本无法把它们交错成一次丢失的运维编辑：刷新只会命中仍来源于种子的行。
func (s *Store) SeedAPIDoc(ctx context.Context, doc *model.APIDoc) (bool, error) {
	db := s.db.WithContext(ctx)
	inserted := db.Clauses(clause.OnConflict{DoNothing: true}).Create(doc)
	if inserted.Error != nil {
		return false, translate(inserted.Error)
	}
	if inserted.RowsAffected == 1 {
		return true, nil
	}
	refreshed := db.Model(&model.APIDoc{}).
		Where("slug = ? AND source = ? AND seed_revision < ?", doc.Slug, apidocs.SourceSeed, doc.SeedRevision).
		Updates(map[string]any{
			"category": doc.Category, "category_order": doc.CategoryOrder, "sort_order": doc.SortOrder,
			"title": doc.Title, "method": doc.Method, "path": doc.Path, "summary": doc.Summary,
			"content_json": doc.ContentJSON, "seed_revision": doc.SeedRevision,
			"version": gorm.Expr("version + 1"), "updated_by": "", "updated_at": doc.UpdatedAt,
		})
	if refreshed.Error != nil {
		return false, translate(refreshed.Error)
	}
	return refreshed.RowsAffected == 1, nil
}
