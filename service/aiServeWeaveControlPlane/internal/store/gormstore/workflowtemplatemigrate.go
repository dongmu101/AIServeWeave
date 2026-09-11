package gormstore

import "context"

// MigrateWorkflowTemplates applies versioned workflow_templates SQL under the shared migration lock.
// MigrateWorkflowTemplates 在共享迁移锁下应用工作流模板的版本化 SQL。
func (s *Store) MigrateWorkflowTemplates(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "workflow_templates")
}
