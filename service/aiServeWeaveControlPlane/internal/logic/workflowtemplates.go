package logic

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ErrWorkflowTemplateCapacity means one template's history is full and
// publication was refused. / ErrWorkflowTemplateCapacity 表示某个模板的历史已满，发布被拒绝。
var ErrWorkflowTemplateCapacity = store.ErrWorkflowTemplateCapacity

// ErrWorkflowTemplateLimit means the platform already publishes as many
// distinct templates as workflowtemplate.MaxTemplates allows.
//
// ErrWorkflowTemplateLimit 表示平台已发布的不同模板数已达
// workflowtemplate.MaxTemplates 上限。
var ErrWorkflowTemplateLimit = store.ErrWorkflowTemplateLimit

// WorkflowTemplateSummary is one template's current head without its graph,
// for the catalogue listing — the same "everything but the graph" shape
// common/workflowview.Template carries to a tenant, but here for an
// operator's management view.
//
// WorkflowTemplateSummary 是一个模板当前头版本、不含图的样子，供目录列表使用——
// 与 common/workflowview.Template 面向租户携带的"除了图以外的一切"是同一种形状，
// 只是这里是给运维管理视图看的。
type WorkflowTemplateSummary struct {
	workflowtemplate.RevisionInfo
	Description      string                        `json:"description,omitempty"`
	Inputs           []workflowtemplate.Input      `json:"inputs"`
	Outputs          []workflowtemplate.Output     `json:"outputs,omitempty"`
	Dependencies     workflowtemplate.Dependencies `json:"dependencies,omitempty"`
	VisibleTenantIDs []string                      `json:"visible_tenant_ids,omitempty"`
}

// WorkflowTemplateHistory is a bounded page of one template's publication
// metadata. / WorkflowTemplateHistory 是一个模板发布元数据的有界分页。
type WorkflowTemplateHistory struct {
	Items      []workflowtemplate.RevisionInfo `json:"items"`
	NextBefore int64                           `json:"next_before,omitempty"`
}

// ListWorkflowTemplates returns the current head of every published
// template, graph-free.
//
// ListWorkflowTemplates 返回每个已发布模板当前的头版本，不含图。
func (s *Service) ListWorkflowTemplates(ctx context.Context) ([]WorkflowTemplateSummary, error) {
	rows, err := s.store.ListCurrentWorkflowTemplates(ctx)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]WorkflowTemplateSummary, 0, len(rows))
	for _, row := range rows {
		snap, err := workflowTemplateSnapshot(row)
		if err != nil {
			return nil, err
		}
		out = append(out, WorkflowTemplateSummary{
			RevisionInfo:     snap.RevisionInfo,
			Description:      snap.Description,
			Inputs:           snap.Inputs,
			Outputs:          snap.Outputs,
			Dependencies:     snap.Dependencies,
			VisibleTenantIDs: snap.VisibleTenantIDs,
		})
	}
	return out, nil
}

// WorkflowTemplate reads one template's current full snapshot, graph
// included — for an operator's editor and for the Gateway's internal sync.
// A template id that has never been published is ErrNotFound: unlike routes'
// single always-existing table, a template only exists from its first
// publish onward.
//
// WorkflowTemplate 读取一个模板当前的完整快照，含图——供运维编辑器与 Gateway
// 内部同步使用。一个从未发布过的模板 id 是 ErrNotFound：与路由那张始终存在的
// 单一表不同，一个模板只从它首次发布起才存在。
func (s *Service) WorkflowTemplate(ctx context.Context, templateID string) (workflowtemplate.Snapshot, error) {
	row, err := s.store.CurrentWorkflowTemplateRevision(ctx, templateID)
	if err != nil {
		return workflowtemplate.Snapshot{}, translate(err)
	}
	return workflowTemplateSnapshot(row)
}

// WorkflowTemplateRevision reads one immutable historical snapshot.
// / WorkflowTemplateRevision 读取一个不可变历史快照。
func (s *Service) WorkflowTemplateRevision(ctx context.Context, templateID string, revision int64) (workflowtemplate.Snapshot, error) {
	if revision < 1 {
		return workflowtemplate.Snapshot{}, ErrInvalidInput
	}
	row, err := s.store.GetWorkflowTemplateRevision(ctx, templateID, revision)
	if err != nil {
		return workflowtemplate.Snapshot{}, translate(err)
	}
	return workflowTemplateSnapshot(row)
}

// WorkflowTemplateHistoryPage reads newest-first metadata for one template,
// without its graph or other content.
//
// WorkflowTemplateHistoryPage 按版本倒序读取一个模板的元数据，不包含图或其他内容。
func (s *Service) WorkflowTemplateHistoryPage(ctx context.Context, templateID string, before int64, limit int) (WorkflowTemplateHistory, error) {
	if before < 0 || limit < 1 || limit > 50 {
		return WorkflowTemplateHistory{}, ErrInvalidInput
	}
	rows, err := s.store.ListWorkflowTemplateRevisions(ctx, templateID, before, limit+1)
	if err != nil {
		return WorkflowTemplateHistory{}, translate(err)
	}
	out := WorkflowTemplateHistory{Items: []workflowtemplate.RevisionInfo{}}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextBefore = rows[len(rows)-1].Revision
	}
	for _, row := range rows {
		out.Items = append(out.Items, workflowTemplateInfo(row))
	}
	return out, nil
}

// CurrentWorkflowTemplatesBundle reads every template's current full
// snapshot, for the Gateway's internal sync (P03).
//
// CurrentWorkflowTemplatesBundle 读取每个模板当前的完整快照，供 Gateway 内部同步
// 使用（P03）。
func (s *Service) CurrentWorkflowTemplatesBundle(ctx context.Context) ([]workflowtemplate.Snapshot, error) {
	rows, err := s.store.ListCurrentWorkflowTemplates(ctx)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]workflowtemplate.Snapshot, 0, len(rows))
	for _, row := range rows {
		snap, err := workflowTemplateSnapshot(row)
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, nil
}

// PublishWorkflowTemplate validates and atomically publishes a platform
// operator's template revision. expected 0 against a template id with no
// existing revision creates it — this contract's "create" — and is the one
// case checked against ErrWorkflowTemplateLimit, since only it can add a new
// distinct template id.
//
// PublishWorkflowTemplate 校验并原子发布平台运维的模板版本。expected 为 0 且该
// 模板 id 尚无既有版本时即为创建——本契约里的"创建"——也是唯一会针对
// ErrWorkflowTemplateLimit 检查的情形，因为只有它会新增一个不同的模板 id。
func (s *Service) PublishWorkflowTemplate(ctx context.Context, actor Actor, templateID string, expected int64, content workflowtemplate.Content, visibleTenantIDs []string) (workflowtemplate.Snapshot, error) {
	if !isPlatformActor(actor) || actor.UserID == "" {
		return workflowtemplate.Snapshot{}, ErrForbidden
	}
	if strings.TrimSpace(templateID) == "" || expected < 0 || content.Inputs == nil {
		return workflowtemplate.Snapshot{}, ErrInvalidInput
	}
	if err := workflowtemplate.Validate(templateID, content); err != nil {
		return workflowtemplate.Snapshot{}, ErrInvalidInput
	}
	if len(visibleTenantIDs) > workflowtemplate.MaxVisibleTenants {
		return workflowtemplate.Snapshot{}, ErrInvalidInput
	}
	if expected == 0 {
		heads, err := s.store.ListCurrentWorkflowTemplates(ctx)
		if err != nil {
			return workflowtemplate.Snapshot{}, translate(err)
		}
		exists := false
		for _, row := range heads {
			if row.TemplateID == templateID {
				exists = true
				break
			}
		}
		if !exists && len(heads) >= workflowtemplate.MaxTemplates {
			return workflowtemplate.Snapshot{}, ErrWorkflowTemplateLimit
		}
	}
	digest, err := workflowtemplate.Digest(templateID, content, visibleTenantIDs)
	if err != nil {
		return workflowtemplate.Snapshot{}, ErrInvalidInput
	}
	row, err := encodeWorkflowTemplateRow(content, visibleTenantIDs)
	if err != nil {
		return workflowtemplate.Snapshot{}, ErrInvalidInput
	}
	row.Digest = digest
	return s.publishWorkflowTemplate(ctx, actor, templateID, expected, row, "workflow_templates.publish")
}

// RollbackWorkflowTemplate copies an old revision's content and visibility
// into a new monotonic revision, the same "never decrement" rule
// RollbackRoutes follows.
//
// RollbackWorkflowTemplate 把一个旧版本的内容与可见范围复制到新的单调递增版本，
// 与 RollbackRoutes 相同的"从不递减"规则。
func (s *Service) RollbackWorkflowTemplate(ctx context.Context, actor Actor, templateID string, expected, revision int64) (workflowtemplate.Snapshot, error) {
	if !isPlatformActor(actor) || actor.UserID == "" {
		return workflowtemplate.Snapshot{}, ErrForbidden
	}
	if strings.TrimSpace(templateID) == "" || expected < 0 || revision < 1 {
		return workflowtemplate.Snapshot{}, ErrInvalidInput
	}
	row, err := s.store.GetWorkflowTemplateRevision(ctx, templateID, revision)
	if err != nil {
		return workflowtemplate.Snapshot{}, translate(err)
	}
	if _, err := workflowTemplateSnapshot(row); err != nil {
		return workflowtemplate.Snapshot{}, err
	}
	row.RollbackOf = revision
	return s.publishWorkflowTemplate(ctx, actor, templateID, expected, row, "workflow_templates.rollback")
}

func (s *Service) publishWorkflowTemplate(ctx context.Context, actor Actor, templateID string, expected int64, row model.WorkflowTemplateRevision, action string) (workflowtemplate.Snapshot, error) {
	row.TemplateID = templateID
	row.Revision = expected + 1
	row.CreatedAt = s.clock.Now().UTC()
	row.ActorID = actor.UserID
	audit := model.AuditLog{ID: model.NewID(model.PrefixAuditLog), TenantID: model.PlatformScope, ActorID: actor.UserID, Action: action,
		Target: templateID + ":" + strconv.FormatInt(row.Revision, 10), Detail: "workflow template revision published", IP: actor.IP, CreatedAt: row.CreatedAt}
	if err := s.store.PublishWorkflowTemplateRevision(ctx, templateID, expected, &row, &audit); err != nil {
		return workflowtemplate.Snapshot{}, translate(err)
	}
	return workflowTemplateSnapshot(row)
}

func workflowTemplateInfo(row model.WorkflowTemplateRevision) workflowtemplate.RevisionInfo {
	return workflowtemplate.RevisionInfo{TemplateID: row.TemplateID, Revision: row.Revision, Digest: row.Digest, ActorID: row.ActorID, CreatedAt: row.CreatedAt, RollbackOf: row.RollbackOf}
}

// encodeWorkflowTemplateRow marshals content and its visibility list into the
// row's separate JSON columns. It never touches Digest, Revision or the other
// publication metadata the caller sets.
//
// encodeWorkflowTemplateRow 把内容及其可见范围列表编码进行的各自独立 JSON 列。
// 它从不触碰 Digest、Revision 或调用方设置的其他发布元数据。
func encodeWorkflowTemplateRow(content workflowtemplate.Content, visibleTenantIDs []string) (model.WorkflowTemplateRevision, error) {
	inputs, err := json.Marshal(content.Inputs)
	if err != nil {
		return model.WorkflowTemplateRevision{}, err
	}
	outputs, err := json.Marshal(content.Outputs)
	if err != nil {
		return model.WorkflowTemplateRevision{}, err
	}
	deps, err := json.Marshal(content.Dependencies)
	if err != nil {
		return model.WorkflowTemplateRevision{}, err
	}
	tenants, err := json.Marshal(visibleTenantIDs)
	if err != nil {
		return model.WorkflowTemplateRevision{}, err
	}
	return model.WorkflowTemplateRevision{
		Description:        content.Description,
		InputsJSON:         string(inputs),
		OutputsJSON:        string(outputs),
		DependenciesJSON:   string(deps),
		VisibleTenantsJSON: string(tenants),
		GraphJSON:          string(content.Graph),
	}, nil
}

// workflowTemplateSnapshot decodes a stored row back into a Snapshot,
// recomputing its digest to reject a row that was somehow stored corrupted —
// the same integrity check routeSnapshot applies to RoutesJSON.
//
// workflowTemplateSnapshot 把一个已存储的行解码回 Snapshot，重新计算其摘要以拒绝
// 一份不知何故被损坏存储的行——与 routeSnapshot 对 RoutesJSON 施加的同一种完整性
// 检查。
func workflowTemplateSnapshot(row model.WorkflowTemplateRevision) (workflowtemplate.Snapshot, error) {
	if len(row.GraphJSON) > workflowtemplate.MaxGraphBytes {
		return workflowtemplate.Snapshot{}, errors.New("logic: invalid stored workflow template size")
	}
	content := workflowtemplate.Content{Description: row.Description, Graph: []byte(row.GraphJSON)}
	if err := json.Unmarshal([]byte(row.InputsJSON), &content.Inputs); err != nil {
		return workflowtemplate.Snapshot{}, errors.New("logic: invalid stored workflow template inputs")
	}
	if err := json.Unmarshal([]byte(row.OutputsJSON), &content.Outputs); err != nil {
		return workflowtemplate.Snapshot{}, errors.New("logic: invalid stored workflow template outputs")
	}
	if err := json.Unmarshal([]byte(row.DependenciesJSON), &content.Dependencies); err != nil {
		return workflowtemplate.Snapshot{}, errors.New("logic: invalid stored workflow template dependencies")
	}
	var tenants []string
	if err := json.Unmarshal([]byte(row.VisibleTenantsJSON), &tenants); err != nil {
		return workflowtemplate.Snapshot{}, errors.New("logic: invalid stored workflow template visibility")
	}
	digest, err := workflowtemplate.Digest(row.TemplateID, content, tenants)
	if err != nil || digest != row.Digest {
		return workflowtemplate.Snapshot{}, errors.New("logic: invalid stored workflow template")
	}
	return workflowtemplate.Snapshot{RevisionInfo: workflowTemplateInfo(row), Content: content, VisibleTenantIDs: tenants}, nil
}
