package store

import (
	"context"
	"errors"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// ErrWorkflowTemplateCapacity prevents unbounded immutable history for one
// template, mirroring ErrRouteCapacity.
//
// ErrWorkflowTemplateCapacity 阻止单个模板的不可变历史无限增长，与
// ErrRouteCapacity 同构。
var ErrWorkflowTemplateCapacity = errors.New("store: workflow template history capacity reached")

// ErrWorkflowTemplateLimit prevents the platform from publishing more
// distinct template ids than workflowtemplate.MaxTemplates allows.
//
// ErrWorkflowTemplateLimit 阻止平台发布超过 workflowtemplate.MaxTemplates
// 允许数量的不同模板 id。
var ErrWorkflowTemplateLimit = errors.New("store: too many distinct workflow templates")

// WorkflowTemplates persists immutable per-template revisions with atomic
// publication and audit (P03). Unlike Routes' single platform-wide table,
// every method here is keyed by templateID: many independently versioned
// documents, not one.
//
// WorkflowTemplates 持久化按模板独立版本化的不可变版本，原子更新发布指针与审计
// （P03）。与 Routes 那张平台级单一表不同，这里每个方法都以 templateID 为键：
// 多份各自独立版本化的文档，而不是一份。
type WorkflowTemplates interface {
	// CurrentWorkflowTemplateRevision reads one template's active immutable
	// row. / CurrentWorkflowTemplateRevision 读取一个模板当前生效的不可变行。
	CurrentWorkflowTemplateRevision(ctx context.Context, templateID string) (model.WorkflowTemplateRevision, error)
	// GetWorkflowTemplateRevision reads one immutable historical row.
	// / GetWorkflowTemplateRevision 读取一个不可变历史行。
	GetWorkflowTemplateRevision(ctx context.Context, templateID string, revision int64) (model.WorkflowTemplateRevision, error)
	// ListWorkflowTemplateRevisions reads one template's bounded history,
	// newest first, without loading its graph or other content.
	//
	// ListWorkflowTemplateRevisions 按版本倒序、有界地读取一个模板的历史元数据，
	// 不加载其图或其他内容。
	ListWorkflowTemplateRevisions(ctx context.Context, templateID string, before int64, limit int) ([]model.WorkflowTemplateRevision, error)
	// ListCurrentWorkflowTemplates reads the current head of every distinct
	// template id, for the catalogue and for the Gateway sync bundle.
	//
	// ListCurrentWorkflowTemplates 读取每个不同模板 id 当前生效的头版本，供目录与
	// Gateway 同步整包使用。
	ListCurrentWorkflowTemplates(ctx context.Context) ([]model.WorkflowTemplateRevision, error)
	// PublishWorkflowTemplateRevision commits CAS on templateID's pointer,
	// inserts the immutable history row and the audit row, in one
	// transaction. expected 0 with no existing pointer row creates the
	// template's first revision, which is this contract's "create".
	//
	// PublishWorkflowTemplateRevision 在同一事务内提交对 templateID 指针的 CAS、
	// 插入不可变历史行与审计行。expected 为 0 且尚无既有指针行时，创建该模板的
	// 第一个版本，这正是本契约里的"创建"。
	PublishWorkflowTemplateRevision(ctx context.Context, templateID string, expected int64, row *model.WorkflowTemplateRevision, audit *model.AuditLog) error
}
