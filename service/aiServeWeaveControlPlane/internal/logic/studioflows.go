package logic

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"AIServeWeave/common/flowdef"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// The store's Studio flow refusals, passed through unchanged so the handler
// can give each its own code. A definition that fails validation is returned
// as the *flowdef.ValidationError itself, so its issues reach the editor.
//
// 存储层对 Studio 编排的拒绝，原样透传，使 handler 能为它们各给一个错误码。未通过校验
// 的定义直接以 *flowdef.ValidationError 返回，使其问题列表能到达编辑器。
var (
	ErrStudioFlowChanged = store.ErrStudioFlowChanged
	ErrStudioRunLimit    = store.ErrStudioRunLimit
	ErrStudioRunFinished = store.ErrStudioRunFinished
)

// Studio flow limits. As for conversations, the storage limits bound what one
// user can keep; the active-run limits bound how much one user or tenant runs
// at once, on top of the quotas and billing the Gateway applies to every
// model call a run makes.
//
// Studio 编排的上限。与对话一样，存储上限限制一个用户能保存多少；进行中运行的上限限制一个
// 用户或租户同时运行多少，此外运行中的每次模型调用仍受 Gateway 的配额与计费约束。
const (
	MaxStudioFlowsPerUser        = 200
	MaxStudioFlowVersions        = 500
	MaxStudioActiveRunsPerUser   = 3
	MaxStudioActiveRunsPerTenant = 20
	MaxStudioRunInputBytes       = 2 << 20
	MaxStudioRunOutputBytes      = 1 << 20
	MaxStudioStepBytes           = 256 << 10
	MinStudioRunLease            = 10 * time.Second
	MaxStudioRunLease            = 10 * time.Minute
	maxStudioReplicaBytes        = 128
	maxStudioNodeIDBytes         = 64
	maxStudioNodeTypeBytes       = 32
	defaultStudioRunListLimit    = 20
)

// StudioFlowPage is one page of a flow list. Next is empty on the last page.
// / StudioFlowPage 是编排列表的一页。最后一页的 Next 为空。
type StudioFlowPage struct {
	Flows []model.StudioFlow
	Next  string
}

// StudioRunPage is one page of a flow's runs. Next is empty on the last page.
// / StudioRunPage 是某个编排运行记录的一页。最后一页的 Next 为空。
type StudioRunPage struct {
	Runs []model.StudioFlowRun
	Next string
}

// StudioFlowPatch changes a flow. A nil Name or Definition keeps it;
// BaseVersion, when positive, must still be the latest version.
//
// StudioFlowPatch 修改一个编排。Name 或 Definition 为 nil 时保持不变；BaseVersion 为正时
// 必须仍是最新版本。
type StudioFlowPatch struct {
	Name        *string
	Definition  json.RawMessage
	BaseVersion int64
}

// StudioRunParams starts a run. The Gateway mints ID so a retried start is
// recognisable, and asks for a lease the control plane times on its own
// clock.
//
// StudioRunParams 启动一次运行。ID 由 Gateway 铸造，使重试的启动可以被识别；Gateway 请求
// 一段租约，由控制面用自己的时钟计时。
type StudioRunParams struct {
	ID          string
	FlowID      string
	FlowVersion int64
	Inputs      json.RawMessage
	ReplicaID   string
	Lease       time.Duration
}

// StudioStepParams is one node's step as the Gateway reports it, its inputs
// and outputs already cut to size.
//
// StudioStepParams 是 Gateway 报告的一个节点的步骤，其输入与输出已截断到上限内。
type StudioStepParams struct {
	ReplicaID        string
	NodeID           string
	NodeType         string
	Status           string
	ErrorCode        string
	Model            string
	Inputs           map[string]string
	Outputs          map[string]string
	PromptTokens     int64
	CompletionTokens int64
	StartedAt        time.Time
	FinishedAt       time.Time
}

// StudioFinishParams is the end of a run as the Gateway reports it.
// / StudioFinishParams 是 Gateway 报告的运行结束情况。
type StudioFinishParams struct {
	ReplicaID        string
	Status           string
	ErrorCode        string
	FailedNode       string
	Outputs          map[string]string
	PromptTokens     int64
	CompletionTokens int64
}

// canonicalFlow decodes and validates a definition and returns it in the
// form that is stored: re-encoded, so two saves of the same flow compare
// equal however the client spaced or ordered them.
//
// canonicalFlow 解码并校验定义，返回存储所用的形式：重新编码，使同一编排的两次保存无论
// 客户端如何排版或排序字段都能比较相等。
func canonicalFlow(raw json.RawMessage) (string, error) {
	def, err := flowdef.Decode(raw)
	if err != nil {
		return "", err
	}
	if err := flowdef.Validate(def); err != nil {
		return "", err
	}
	body, err := json.Marshal(def)
	if err != nil {
		return "", ErrInvalidInput
	}
	return string(body), nil
}

func validFlowName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= MaxStudioTitleRunes
}

// CreateStudioFlow saves a new flow as its version 1.
// / CreateStudioFlow 把新编排保存为它的版本 1。
func (s *Service) CreateStudioFlow(ctx context.Context, owner store.StudioOwner, name string, definition json.RawMessage) (model.StudioFlow, model.StudioFlowVersion, error) {
	name = strings.TrimSpace(name)
	if !validStudioOwner(owner) || !validFlowName(name) {
		return model.StudioFlow{}, model.StudioFlowVersion{}, ErrInvalidInput
	}
	body, err := canonicalFlow(definition)
	if err != nil {
		return model.StudioFlow{}, model.StudioFlowVersion{}, err
	}
	now := s.clock.Now()
	f := model.StudioFlow{ID: model.NewID(model.PrefixFlow), TenantID: owner.TenantID, UserID: owner.UserID, Name: name, Version: 1, CreatedAt: now, UpdatedAt: now}
	v := model.StudioFlowVersion{FlowID: f.ID, Version: 1, Definition: body, CreatedAt: now}
	if err := s.store.CreateStudioFlow(ctx, &f, &v, MaxStudioFlowsPerUser); err != nil {
		return model.StudioFlow{}, model.StudioFlowVersion{}, translate(err)
	}
	return f, v, nil
}

// GetStudioFlow reads a flow and its latest version.
// / GetStudioFlow 读取一个编排及其最新版本。
func (s *Service) GetStudioFlow(ctx context.Context, owner store.StudioOwner, id string) (model.StudioFlow, model.StudioFlowVersion, error) {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixFlow) {
		return model.StudioFlow{}, model.StudioFlowVersion{}, ErrNotFound
	}
	f, v, err := s.store.GetStudioFlow(ctx, owner, id)
	return f, v, translate(err)
}

// GetStudioFlowVersion reads one saved version of a flow.
// / GetStudioFlowVersion 读取编排的某个已保存版本。
func (s *Service) GetStudioFlowVersion(ctx context.Context, owner store.StudioOwner, id string, version int64) (model.StudioFlowVersion, error) {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixFlow) || version < 1 {
		return model.StudioFlowVersion{}, ErrNotFound
	}
	v, err := s.store.GetStudioFlowVersion(ctx, owner, id, version)
	return v, translate(err)
}

// ListStudioFlows reads one page of owner's flows, most recently updated
// first. cursor is the Next of the previous page.
//
// ListStudioFlows 读取 owner 的一页编排，按最近更新倒序。cursor 是上一页的 Next。
func (s *Service) ListStudioFlows(ctx context.Context, owner store.StudioOwner, cursor string, limit int) (StudioFlowPage, error) {
	if !validStudioOwner(owner) {
		return StudioFlowPage{}, ErrInvalidInput
	}
	limit = studioPageLimit(limit, defaultStudioListLimit)
	filter := store.StudioFlowFilter{Owner: owner, Limit: limit + 1}
	if cursor != "" {
		before, ok := decodeStudioKeyset(cursor, model.PrefixFlow)
		if !ok {
			return StudioFlowPage{}, ErrInvalidInput
		}
		filter.Before = &before
	}
	rows, err := s.store.ListStudioFlows(ctx, filter)
	if err != nil {
		return StudioFlowPage{}, translate(err)
	}
	page := StudioFlowPage{Flows: rows}
	if len(rows) > limit {
		page.Flows = rows[:limit]
		last := rows[limit-1]
		page.Next = encodeStudioKeyset(store.StudioCursor{At: last.UpdatedAt, ID: last.ID})
	}
	return page, nil
}

func studioPageLimit(limit, fallback int) int {
	if limit <= 0 {
		limit = fallback
	}
	return min(limit, maxStudioListLimit)
}

// UpdateStudioFlow renames a flow and/or saves a new definition. Saving a
// definition equal to the latest version creates no version.
//
// UpdateStudioFlow 重命名编排和/或保存新的定义。保存与最新版本相同的定义不会产生新版本。
func (s *Service) UpdateStudioFlow(ctx context.Context, owner store.StudioOwner, id string, p StudioFlowPatch) (model.StudioFlow, model.StudioFlowVersion, error) {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixFlow) {
		return model.StudioFlow{}, model.StudioFlowVersion{}, ErrNotFound
	}
	update := store.StudioFlowUpdate{BaseVersion: p.BaseVersion, MaxVersions: MaxStudioFlowVersions, At: s.clock.Now()}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		if !validFlowName(name) {
			return model.StudioFlow{}, model.StudioFlowVersion{}, ErrInvalidInput
		}
		update.Name = &name
	}
	if p.Definition != nil {
		body, err := canonicalFlow(p.Definition)
		if err != nil {
			return model.StudioFlow{}, model.StudioFlowVersion{}, err
		}
		update.Definition = &body
	}
	if (update.Name == nil && update.Definition == nil) || p.BaseVersion < 0 {
		return model.StudioFlow{}, model.StudioFlowVersion{}, ErrInvalidInput
	}
	f, v, err := s.store.UpdateStudioFlow(ctx, owner, id, update)
	return f, v, translate(err)
}

// DeleteStudioFlow removes a flow with its versions and runs. A run still in
// progress elsewhere learns of it on its next write, which finds nothing.
//
// DeleteStudioFlow 删除一个编排及其版本与运行。仍在别处进行的运行会在下一次写入时得知
// 此事，因为它什么也找不到。
func (s *Service) DeleteStudioFlow(ctx context.Context, owner store.StudioOwner, id string) error {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixFlow) {
		return ErrNotFound
	}
	return translate(s.store.DeleteStudioFlow(ctx, owner, id))
}

// validLease reports whether d is a lease the Gateway may ask for.
func validLease(d time.Duration) bool {
	return d >= MinStudioRunLease && d <= MaxStudioRunLease
}

func validReplica(replica string) bool {
	return replica != "" && len(replica) <= maxStudioReplicaBytes
}

// StartStudioRun records a run the Gateway is about to execute, held by
// replica for lease. The flow version must exist; the definition was already
// validated when it was saved and again by the Gateway before planning.
//
// StartStudioRun 记录 Gateway 即将执行的一次运行，由 replica 持有 lease 时长的租约。编排
// 版本必须存在；定义在保存时已校验过，Gateway 规划运行前也会再校验一次。
func (s *Service) StartStudioRun(ctx context.Context, owner store.StudioOwner, p StudioRunParams) (model.StudioFlowRun, error) {
	if !validStudioOwner(owner) || !validStudioID(p.FlowID, model.PrefixFlow) {
		return model.StudioFlowRun{}, ErrNotFound
	}
	if !validStudioID(p.ID, model.PrefixFlowRun) || p.FlowVersion < 1 || !validReplica(p.ReplicaID) || !validLease(p.Lease) || len(p.Inputs) > MaxStudioRunInputBytes {
		return model.StudioFlowRun{}, ErrInvalidInput
	}
	inputs := map[string]json.RawMessage{}
	if len(p.Inputs) > 0 {
		if err := json.Unmarshal(p.Inputs, &inputs); err != nil || inputs == nil {
			return model.StudioFlowRun{}, ErrInvalidInput
		}
	}
	body, err := json.Marshal(inputs)
	if err != nil {
		return model.StudioFlowRun{}, ErrInvalidInput
	}
	now := s.clock.Now()
	run := model.StudioFlowRun{
		ID: p.ID, FlowID: p.FlowID, TenantID: owner.TenantID, UserID: owner.UserID, FlowVersion: p.FlowVersion,
		Status: model.StudioRunRunning, Inputs: string(body), Outputs: "{}", ReplicaID: p.ReplicaID,
		LeaseUntil: now.Add(p.Lease), CreatedAt: now,
	}
	limits := store.StudioRunLimits{PerUser: MaxStudioActiveRunsPerUser, PerTenant: MaxStudioActiveRunsPerTenant}
	if err := s.store.StartStudioRun(ctx, owner, &run, limits, now); err != nil {
		return model.StudioFlowRun{}, translate(err)
	}
	// A retried start returns the run as first recorded.
	// 重试的启动返回首次记录的运行。
	stored, _, err := s.store.GetStudioRun(ctx, owner, run.ID)
	if err != nil {
		return model.StudioFlowRun{}, translate(err)
	}
	return s.readRun(stored), nil
}

// readRun is how a stored run reads now: one whose replica stopped renewing
// its lease is interrupted, ending when the lease did.
//
// readRun 是已存运行在当下的读法：副本已停止续租的运行为 interrupted，结束时间即租约
// 到期时间。
func (s *Service) readRun(r model.StudioFlowRun) model.StudioFlowRun {
	if store.StudioRunExpired(r, s.clock.Now()) {
		ended := r.LeaseUntil
		r.Status, r.FinishedAt = model.StudioRunInterrupted, &ended
	}
	return r
}

// GetStudioRun reads a run and its steps.
// / GetStudioRun 读取一次运行及其步骤。
func (s *Service) GetStudioRun(ctx context.Context, owner store.StudioOwner, id string) (model.StudioFlowRun, []model.StudioFlowRunStep, error) {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixFlowRun) {
		return model.StudioFlowRun{}, nil, ErrNotFound
	}
	r, steps, err := s.store.GetStudioRun(ctx, owner, id)
	if err != nil {
		return model.StudioFlowRun{}, nil, translate(err)
	}
	return s.readRun(r), steps, nil
}

// ListStudioRuns reads one page of a flow's runs, newest first.
// / ListStudioRuns 读取某个编排运行记录的一页，最新的在前。
func (s *Service) ListStudioRuns(ctx context.Context, owner store.StudioOwner, flowID, cursor string, limit int) (StudioRunPage, error) {
	if !validStudioOwner(owner) || !validStudioID(flowID, model.PrefixFlow) {
		return StudioRunPage{}, ErrNotFound
	}
	limit = studioPageLimit(limit, defaultStudioRunListLimit)
	filter := store.StudioRunFilter{Owner: owner, FlowID: flowID, Limit: limit + 1}
	if cursor != "" {
		before, ok := decodeStudioKeyset(cursor, model.PrefixFlowRun)
		if !ok {
			return StudioRunPage{}, ErrInvalidInput
		}
		filter.Before = &before
	}
	rows, err := s.store.ListStudioRuns(ctx, filter)
	if err != nil {
		return StudioRunPage{}, translate(err)
	}
	var page StudioRunPage
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		page.Next = encodeStudioKeyset(store.StudioCursor{At: last.CreatedAt, ID: last.ID})
	}
	page.Runs = s.readRuns(rows)
	return page, nil
}

func (s *Service) readRuns(rows []model.StudioFlowRun) []model.StudioFlowRun {
	for i := range rows {
		rows[i] = s.readRun(rows[i])
	}
	return rows
}

// RenewStudioRun extends replica's lease on a run by lease from now.
// / RenewStudioRun 把 replica 对运行的租约从现在起延长 lease。
func (s *Service) RenewStudioRun(ctx context.Context, owner store.StudioOwner, id, replica string, lease time.Duration) error {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixFlowRun) {
		return ErrNotFound
	}
	if !validReplica(replica) || !validLease(lease) {
		return ErrInvalidInput
	}
	now := s.clock.Now()
	return translate(s.store.RenewStudioRun(ctx, owner, id, replica, now.Add(lease), now))
}

// studioJSON encodes a step's or a run's text fields, refusing what exceeds
// limit.
func studioJSON(values map[string]string, limit int) (string, bool) {
	if values == nil {
		values = map[string]string{}
	}
	body, err := json.Marshal(values)
	if err != nil || len(body) > limit {
		return "", false
	}
	return string(body), true
}

// RecordStudioRunStep stores how one node of a run ended.
// / RecordStudioRunStep 保存运行中某个节点的结束情况。
func (s *Service) RecordStudioRunStep(ctx context.Context, owner store.StudioOwner, runID string, p StudioStepParams) error {
	if !validStudioOwner(owner) || !validStudioID(runID, model.PrefixFlowRun) {
		return ErrNotFound
	}
	switch p.Status {
	case model.StudioRunSucceeded, model.StudioRunFailed, model.StudioRunCanceled, model.StudioStepSkipped:
	default:
		return ErrInvalidInput
	}
	inputs, okIn := studioJSON(p.Inputs, MaxStudioStepBytes)
	outputs, okOut := studioJSON(p.Outputs, MaxStudioStepBytes)
	if !okIn || !okOut || !validReplica(p.ReplicaID) || p.NodeID == "" || len(p.NodeID) > maxStudioNodeIDBytes ||
		p.NodeType == "" || len(p.NodeType) > maxStudioNodeTypeBytes || len(p.ErrorCode) > maxStudioErrorCodeBytes || len(p.Model) > maxStudioModelBytes ||
		p.PromptTokens < 0 || p.CompletionTokens < 0 || p.StartedAt.IsZero() || p.FinishedAt.Before(p.StartedAt) {
		return ErrInvalidInput
	}
	step := model.StudioFlowRunStep{
		RunID: runID, NodeID: p.NodeID, TenantID: owner.TenantID, UserID: owner.UserID, NodeType: p.NodeType, Status: p.Status,
		ErrorCode: p.ErrorCode, Model: p.Model, Inputs: inputs, Outputs: outputs, PromptTokens: p.PromptTokens, CompletionTokens: p.CompletionTokens,
		StartedAt: p.StartedAt.UTC(), FinishedAt: p.FinishedAt.UTC(),
	}
	return translate(s.store.RecordStudioRunStep(ctx, owner, &step, p.ReplicaID, s.clock.Now()))
}

// FinishStudioRun records the end of a run.
// / FinishStudioRun 记录运行的结束。
func (s *Service) FinishStudioRun(ctx context.Context, owner store.StudioOwner, id string, p StudioFinishParams) error {
	if !validStudioOwner(owner) || !validStudioID(id, model.PrefixFlowRun) {
		return ErrNotFound
	}
	switch p.Status {
	case model.StudioRunSucceeded, model.StudioRunFailed, model.StudioRunCanceled:
	default:
		return ErrInvalidInput
	}
	outputs, ok := studioJSON(p.Outputs, MaxStudioRunOutputBytes)
	if !ok || !validReplica(p.ReplicaID) || len(p.ErrorCode) > maxStudioErrorCodeBytes || len(p.FailedNode) > maxStudioNodeIDBytes ||
		p.PromptTokens < 0 || p.CompletionTokens < 0 {
		return ErrInvalidInput
	}
	finish := store.StudioRunFinish{
		Status: p.Status, ErrorCode: p.ErrorCode, FailedNode: p.FailedNode, Outputs: outputs,
		PromptTokens: p.PromptTokens, CompletionTokens: p.CompletionTokens,
	}
	return translate(s.store.FinishStudioRun(ctx, owner, id, p.ReplicaID, finish, s.clock.Now()))
}
