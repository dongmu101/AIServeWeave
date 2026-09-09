package gormstore

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CurrentWorkflowTemplateRevision reads one template's active row in one
// statement, the same join shape CurrentRouteRevision uses.
//
// CurrentWorkflowTemplateRevision 用一条语句读取一个模板当前生效的行，与
// CurrentRouteRevision 相同的 join 形状。
func (s *Store) CurrentWorkflowTemplateRevision(ctx context.Context, templateID string) (model.WorkflowTemplateRevision, error) {
	var row model.WorkflowTemplateRevision
	err := s.db.WithContext(ctx).Table("workflow_template_revisions").Select("workflow_template_revisions.*").
		Joins("JOIN workflow_template_actives ON workflow_template_actives.revision = workflow_template_revisions.revision AND workflow_template_actives.template_id = workflow_template_revisions.template_id AND workflow_template_actives.template_id = ?", templateID).
		Take(&row).Error
	return row, translate(err)
}

// GetWorkflowTemplateRevision reads an immutable historical row.
// / GetWorkflowTemplateRevision 读取一个不可变历史行。
func (s *Store) GetWorkflowTemplateRevision(ctx context.Context, templateID string, revision int64) (model.WorkflowTemplateRevision, error) {
	var row model.WorkflowTemplateRevision
	err := s.db.WithContext(ctx).Where("template_id = ? AND revision = ?", templateID, revision).Take(&row).Error
	return row, translate(err)
}

// ListWorkflowTemplateRevisions reads bounded metadata without loading a
// template's graph. / ListWorkflowTemplateRevisions 有界读取元数据，不加载模板的图。
func (s *Store) ListWorkflowTemplateRevisions(ctx context.Context, templateID string, before int64, limit int) ([]model.WorkflowTemplateRevision, error) {
	if limit < 1 || limit > 51 {
		limit = 51
	}
	q := s.db.WithContext(ctx).Select("template_id, revision, digest, actor_id, created_at, rollback_of").
		Where("template_id = ?", templateID).Order("revision DESC").Limit(limit)
	if before > 0 {
		q = q.Where("revision < ?", before)
	}
	rows := []model.WorkflowTemplateRevision{}
	err := q.Find(&rows).Error
	return rows, translate(err)
}

// ListCurrentWorkflowTemplates reads the current head of every distinct
// template, sorted by id for a deterministic bundle.
//
// ListCurrentWorkflowTemplates 读取每个不同模板当前生效的头版本，按 id 排序以得到
// 确定性的整包。
func (s *Store) ListCurrentWorkflowTemplates(ctx context.Context) ([]model.WorkflowTemplateRevision, error) {
	rows := []model.WorkflowTemplateRevision{}
	err := s.db.WithContext(ctx).Table("workflow_template_revisions").Select("workflow_template_revisions.*").
		Joins("JOIN workflow_template_actives ON workflow_template_actives.revision = workflow_template_revisions.revision AND workflow_template_actives.template_id = workflow_template_revisions.template_id").
		Order("workflow_template_revisions.template_id ASC").Find(&rows).Error
	return rows, translate(err)
}

// PublishWorkflowTemplateRevision commits CAS, immutable history and audit in
// one transaction. Unlike PublishRouteRevision there is no seeded singleton
// row to update: expected 0 first ensures the pointer row exists at revision
// 0, via an upsert rather than a plain INSERT.
//
// That distinction matters under real concurrency, and a live MySQL run
// caught it: many transactions racing a plain INSERT of the same new
// template's pointer row don't merely fail with a duplicate key — under
// InnoDB's default isolation each one first takes a gap lock discovering the
// row absent, then blocks on each other's insert intention, and MySQL
// resolves the resulting cycle by killing one transaction with error 1213
// ("Deadlock found"), not with a duplicate-key error translate() knows how to
// map. An upsert is one statement, so there is no separate "discover absent,
// then insert" step for two transactions to interleave into a cycle.
// Ensuring the row exists is idempotent and safe to run on every first
// publish; the actual CAS decision is still the ordinary conditional UPDATE
// below, unconditionally on every call.
//
// PublishWorkflowTemplateRevision 在同一事务中提交 CAS、不可变历史与审计。与
// PublishRouteRevision 不同，这里没有预先播种的单例行：expected 为 0 时先靠
// upsert 而不是普通 INSERT，确保指针行以版本 0 存在。
//
// 这个区别在真实并发下要紧，一次真实 MySQL 运行就抓到了它：许多事务并发地对
// 同一个新模板的指针行做普通 INSERT，结果不只是简单地败于重复键——在 InnoDB 默认
// 隔离级别下，每个事务先因发现该行不存在而取得一个间隙锁，再互相卡在对方的插入
// 意向锁上，MySQL 用错误 1213（"Deadlock found"）杀掉其中一个事务来打破这个环，
// 而不是 translate() 认识的重复键错误。upsert 是单条语句，因此不存在「先发现不
// 存在、再插入」这两步供两个事务交错成一个环。确保该行存在是幂等的，可以在每次
// 首次发布时安全执行；真正的 CAS 判定仍是下面那条普通的条件 UPDATE，每次调用都
// 无条件执行一次。
func (s *Store) PublishWorkflowTemplateRevision(ctx context.Context, templateID string, expected int64, row *model.WorkflowTemplateRevision, audit *model.AuditLog) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if expected < 0 {
			return store.ErrConflict
		}
		if expected == 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.WorkflowTemplateActive{TemplateID: templateID, Revision: 0}).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&model.WorkflowTemplateActive{}).Where("template_id = ? AND revision = ?", templateID, expected).Update("revision", expected+1)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return store.ErrConflict
		}
		if expected >= workflowtemplate.MaxRevisionsPerTemplate {
			return store.ErrWorkflowTemplateCapacity
		}
		row.TemplateID = templateID
		row.Revision = expected + 1
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		return tx.Create(audit).Error
	}))
}
