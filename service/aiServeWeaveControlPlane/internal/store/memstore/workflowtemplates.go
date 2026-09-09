package memstore

import (
	"context"
	"sort"

	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// currentWorkflowTemplateRevisionLocked scans for the latest appended row for
// templateID. It is always the head: a publish only ever appends one row per
// template with a strictly higher revision than any before it, rollback
// included.
//
// currentWorkflowTemplateRevisionLocked 扫描 templateID 最后一次被追加的行。它永远
// 是头版本：一次发布只会为某个模板追加一行，其版本号严格高于此前的任何一行，
// 回滚也不例外。
func (s *Store) currentWorkflowTemplateRevisionLocked(templateID string) (model.WorkflowTemplateRevision, bool) {
	for i := len(s.workflowTemplates) - 1; i >= 0; i-- {
		if s.workflowTemplates[i].TemplateID == templateID {
			return s.workflowTemplates[i], true
		}
	}
	return model.WorkflowTemplateRevision{}, false
}

// CurrentWorkflowTemplateRevision reads the published row for templateID.
// / CurrentWorkflowTemplateRevision 读取 templateID 已发布的行。
func (s *Store) CurrentWorkflowTemplateRevision(_ context.Context, templateID string) (model.WorkflowTemplateRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.currentWorkflowTemplateRevisionLocked(templateID)
	if !ok {
		return model.WorkflowTemplateRevision{}, store.ErrNotFound
	}
	return row, nil
}

// GetWorkflowTemplateRevision reads an immutable revision.
// / GetWorkflowTemplateRevision 读取一个不可变版本。
func (s *Store) GetWorkflowTemplateRevision(_ context.Context, templateID string, revision int64) (model.WorkflowTemplateRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.workflowTemplates {
		if row.TemplateID == templateID && row.Revision == revision {
			return row, nil
		}
	}
	return model.WorkflowTemplateRevision{}, store.ErrNotFound
}

// ListWorkflowTemplateRevisions reads bounded history, newest first.
// / ListWorkflowTemplateRevisions 有界读取历史，新版本在前。
func (s *Store) ListWorkflowTemplateRevisions(_ context.Context, templateID string, before int64, limit int) ([]model.WorkflowTemplateRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit < 1 || limit > 51 {
		limit = 51
	}
	out := []model.WorkflowTemplateRevision{}
	for i := len(s.workflowTemplates) - 1; i >= 0 && len(out) < limit; i-- {
		row := s.workflowTemplates[i]
		if row.TemplateID != templateID {
			continue
		}
		if before == 0 || row.Revision < before {
			out = append(out, row)
		}
	}
	return out, nil
}

// ListCurrentWorkflowTemplates reads the current head of every distinct
// template, sorted by id.
//
// ListCurrentWorkflowTemplates 读取每个不同模板当前生效的头版本，按 id 排序。
func (s *Store) ListCurrentWorkflowTemplates(_ context.Context) ([]model.WorkflowTemplateRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	heads := map[string]model.WorkflowTemplateRevision{}
	for _, row := range s.workflowTemplates {
		if existing, ok := heads[row.TemplateID]; !ok || row.Revision > existing.Revision {
			heads[row.TemplateID] = row
		}
	}
	out := make([]model.WorkflowTemplateRevision, 0, len(heads))
	for _, row := range heads {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TemplateID < out[j].TemplateID })
	return out, nil
}

// PublishWorkflowTemplateRevision atomically checks templateID's pointer,
// appends history and audits.
//
// PublishWorkflowTemplateRevision 原子检查 templateID 的指针、追加历史并记审计。
func (s *Store) PublishWorkflowTemplateRevision(_ context.Context, templateID string, expected int64, row *model.WorkflowTemplateRevision, audit *model.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.currentWorkflowTemplateRevisionLocked(templateID)
	currentRevision := int64(0)
	if ok {
		currentRevision = current.Revision
	}
	if expected != currentRevision {
		return store.ErrConflict
	}
	if expected >= workflowtemplate.MaxRevisionsPerTemplate {
		return store.ErrWorkflowTemplateCapacity
	}
	row.TemplateID = templateID
	row.Revision = expected + 1
	s.workflowTemplates = append(s.workflowTemplates, *row)
	s.audit = append(s.audit, *audit)
	return nil
}
