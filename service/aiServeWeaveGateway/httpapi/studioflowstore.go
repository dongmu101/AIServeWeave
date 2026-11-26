package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Studio flow refusals, beyond the shared ErrStudioNotFound, ErrStudioLimit
// and ErrStudioInvalid. A definition the control plane refuses comes back as
// a *flowdef.ValidationError carrying its issues.
//
// Studio 编排的拒绝，共享的 ErrStudioNotFound、ErrStudioLimit 与 ErrStudioInvalid 之外的
// 部分。控制面拒绝的定义以携带问题列表的 *flowdef.ValidationError 返回。
var (
	// ErrStudioFlowChanged: the flow was saved elsewhere since the editor's
	// base version.
	// ErrStudioFlowChanged：编排在编辑器的基础版本之后已在别处保存。
	ErrStudioFlowChanged = errors.New("studio: flow changed")
	// ErrStudioRunLimit: the user or tenant has as many runs in progress as
	// allowed.
	// ErrStudioRunLimit：用户或租户进行中的运行数已达上限。
	ErrStudioRunLimit = errors.New("studio: too many runs in progress")
	// ErrStudioRunFinished: this replica no longer holds the run — it ended,
	// its lease passed, or the flow was deleted.
	// ErrStudioRunFinished：本副本已不再持有该运行——运行已结束、租约已过，或编排已删除。
	ErrStudioRunFinished = errors.New("studio: run no longer held")
)

// StudioFlow is one stored flow. Definition is the latest version's
// common/flowdef JSON; lists leave it empty.
//
// StudioFlow 是一个已存储的编排。Definition 是最新版本的 common/flowdef JSON；列表中为空。
type StudioFlow struct {
	ID         string
	Name       string
	Version    int64
	Definition json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// StudioFlowPage is one page of flows; Next is empty on the last page.
// StudioFlowPage 是一页编排；最后一页的 Next 为空。
type StudioFlowPage struct {
	Flows []StudioFlow
	Next  string
}

// StudioFlowPatch renames a flow and/or saves a new definition; nil keeps a
// field. BaseVersion, when positive, is the version the editor started from.
//
// StudioFlowPatch 重命名编排和/或保存新定义；nil 表示保持不变。BaseVersion 为正时是编辑器
// 开始编辑时的版本。
type StudioFlowPatch struct {
	Name        *string
	Definition  json.RawMessage
	BaseVersion int64
}

// StudioFlowVersion is one saved version of a flow.
// StudioFlowVersion 是编排的一个已保存版本。
type StudioFlowVersion struct {
	Version    int64
	Definition json.RawMessage
	CreatedAt  time.Time
}

// StudioRun is one stored run. Inputs, Outputs and Steps are empty in lists.
// StudioRun 是一次已存储的运行。列表中 Inputs、Outputs 与 Steps 为空。
type StudioRun struct {
	ID               string
	FlowID           string
	FlowVersion      int64
	Status           string
	ErrorCode        string
	FailedNode       string
	Inputs           json.RawMessage
	Outputs          map[string]string
	PromptTokens     int64
	CompletionTokens int64
	ReplicaID        string
	CreatedAt        time.Time
	FinishedAt       *time.Time
	Steps            []StudioRunStep
}

// StudioRunPage is one page of a flow's runs; Next is empty on the last page.
// StudioRunPage 是某个编排运行记录的一页；最后一页的 Next 为空。
type StudioRunPage struct {
	Runs []StudioRun
	Next string
}

// StudioRunStep is how one node of a run ended.
// StudioRunStep 是运行中某个节点的结束情况。
type StudioRunStep struct {
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

// StudioRunStart records a run this replica is about to execute.
// StudioRunStart 记录本副本即将执行的一次运行。
type StudioRunStart struct {
	ID          string
	FlowID      string
	FlowVersion int64
	Inputs      json.RawMessage
	ReplicaID   string
	Lease       time.Duration
}

// StudioRunFinish is how a run ended.
// StudioRunFinish 是运行的结束方式。
type StudioRunFinish struct {
	Status           string
	ErrorCode        string
	FailedNode       string
	Outputs          map[string]string
	PromptTokens     int64
	CompletionTokens int64
}

// StudioFlowStore persists Studio flows and their runs; the control plane
// client implements it. Every method is scoped by owner. The run writes name
// the replica holding the run and fail with ErrStudioRunFinished once it no
// longer does.
//
// StudioFlowStore 持久化 Studio 编排及其运行，由控制面客户端实现。每个方法都按 owner
// 限定。写入运行的方法指明持有该运行的副本，一旦它不再持有就以 ErrStudioRunFinished 失败。
type StudioFlowStore interface {
	CreateFlow(ctx context.Context, owner StudioOwner, name string, definition json.RawMessage) (StudioFlow, error)
	ListFlows(ctx context.Context, owner StudioOwner, cursor string, limit int) (StudioFlowPage, error)
	GetFlow(ctx context.Context, owner StudioOwner, id string) (StudioFlow, error)
	UpdateFlow(ctx context.Context, owner StudioOwner, id string, patch StudioFlowPatch) (StudioFlow, error)
	DeleteFlow(ctx context.Context, owner StudioOwner, id string) error
	GetFlowVersion(ctx context.Context, owner StudioOwner, id string, version int64) (StudioFlowVersion, error)
	ListRuns(ctx context.Context, owner StudioOwner, flowID, cursor string, limit int) (StudioRunPage, error)
	StartRun(ctx context.Context, owner StudioOwner, start StudioRunStart) (StudioRun, error)
	GetRun(ctx context.Context, owner StudioOwner, id string) (StudioRun, error)
	RenewRun(ctx context.Context, owner StudioOwner, id, replica string, lease time.Duration) error
	RecordStep(ctx context.Context, owner StudioOwner, runID, replica string, step StudioRunStep) error
	FinishRun(ctx context.Context, owner StudioOwner, id, replica string, finish StudioRunFinish) error
}
