package store

import (
	"context"
	"errors"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// ErrStudioFlowChanged is returned when an update names a base version that
// is no longer the flow's latest: someone saved the flow in between.
//
// ErrStudioFlowChanged 表示更新指定的基础版本已不是编排的最新版本：期间有人保存过它。
var ErrStudioFlowChanged = errors.New("store: studio flow changed since the base version")

// ErrStudioRunLimit is returned when starting a run would take a user or a
// tenant past the number of runs allowed to be active at once.
//
// ErrStudioRunLimit 表示启动这次运行会让用户或租户同时进行的运行数超出上限。
var ErrStudioRunLimit = errors.New("store: too many studio runs in progress")

// ErrStudioRunFinished is returned when a replica writes to a run it no
// longer holds: the run already ended, its lease passed, or another replica
// holds it. The replica must stop executing it.
//
// ErrStudioRunFinished 表示副本写入了一个它已不再持有的运行：运行已结束、租约已过，或由
// 其他副本持有。该副本必须停止执行它。
var ErrStudioRunFinished = errors.New("store: studio run is no longer held by this replica")

// StudioCursor is the keyset position of a Studio list page: the (At, ID) of
// the last row returned, At being the column the list is ordered by.
//
// StudioCursor 是 Studio 列表分页的键集位置：上一页最后一行的 (At, ID)，At 是列表排序
// 所依据的列。
type StudioCursor struct {
	At time.Time
	ID string
}

// StudioFlowFilter selects one page of a user's flows, most recently updated
// first.
//
// StudioFlowFilter 选取用户编排的一页，按最近更新倒序。
type StudioFlowFilter struct {
	Owner  StudioOwner
	Before *StudioCursor
	Limit  int
}

// StudioFlowUpdate changes a flow. A non-nil Definition that differs from the
// latest version's appends a new version; one equal to it changes nothing.
// BaseVersion, when positive, must be the latest version (else
// ErrStudioFlowChanged). MaxVersions bounds the versions a flow may keep
// (ErrStudioLimit). UpdatedAt moves to At only when something changed.
//
// StudioFlowUpdate 修改一个编排。非 nil 且与最新版本不同的 Definition 会追加一个新版本；
// 与之相同则不作改动。BaseVersion 为正时必须等于最新版本（否则 ErrStudioFlowChanged）。
// MaxVersions 限制编排可保留的版本数（ErrStudioLimit）。只有确有改动时 UpdatedAt 才更新
// 为 At。
type StudioFlowUpdate struct {
	Name        *string
	Definition  *string
	BaseVersion int64
	MaxVersions int
	At          time.Time
}

// StudioRunFilter selects one page of a flow's runs, newest first.
//
// StudioRunFilter 选取某个编排运行记录的一页，最新的在前。
type StudioRunFilter struct {
	Owner  StudioOwner
	FlowID string
	Before *StudioCursor
	Limit  int
}

// StudioRunLimits bounds how many runs may be active at once. A run is
// active while it is running and its lease has not passed. Zero means no
// bound.
//
// StudioRunLimits 限制同时进行的运行数。运行处于 running 且租约未过时视为进行中。零表示
// 不限制。
type StudioRunLimits struct {
	PerUser   int
	PerTenant int
}

// StudioRunFinish is the end of a run as its replica reports it.
//
// StudioRunFinish 是副本报告的运行结束情况。
type StudioRunFinish struct {
	Status           string
	ErrorCode        string
	FailedNode       string
	Outputs          string
	PromptTokens     int64
	CompletionTokens int64
}

// StudioFlows persists Studio flows, their versions, and their runs with each
// run's steps. Every method is scoped by owner; a row of another owner is
// ErrNotFound. Methods that write to a run take the replica writing and the
// current time, and refuse with ErrStudioRunFinished unless that replica
// still holds the run — see StudioRunHeld.
//
// StudioFlows 持久化 Studio 编排、编排版本，以及编排运行与每次运行的步骤。每个方法都按
// owner 限定，属于其他 owner 的行一律返回 ErrNotFound。写入运行的方法接收写入的副本与
// 当前时间，除非该副本仍持有该运行，否则以 ErrStudioRunFinished 拒绝——见 StudioRunHeld。
type StudioFlows interface {
	// CreateStudioFlow inserts f with its first version v, unless f's owner
	// already has maxPerUser flows (ErrStudioLimit).
	// CreateStudioFlow 插入 f 及其第一个版本 v；所有者已有 maxPerUser 个编排时返回
	// ErrStudioLimit。
	CreateStudioFlow(ctx context.Context, f *model.StudioFlow, v *model.StudioFlowVersion, maxPerUser int) error
	// GetStudioFlow reads a flow and its latest version.
	// GetStudioFlow 读取一个编排及其最新版本。
	GetStudioFlow(ctx context.Context, owner StudioOwner, id string) (model.StudioFlow, model.StudioFlowVersion, error)
	// GetStudioFlowVersion reads one version of a flow.
	// GetStudioFlowVersion 读取编排的某个版本。
	GetStudioFlowVersion(ctx context.Context, owner StudioOwner, id string, version int64) (model.StudioFlowVersion, error)
	// ListStudioFlows reads one page of flows, without definitions.
	// ListStudioFlows 读取一页编排，不含定义。
	ListStudioFlows(ctx context.Context, filter StudioFlowFilter) ([]model.StudioFlow, error)
	// UpdateStudioFlow applies update and returns the flow and its latest
	// version afterwards.
	// UpdateStudioFlow 应用 update，返回之后的编排及其最新版本。
	UpdateStudioFlow(ctx context.Context, owner StudioOwner, id string, update StudioFlowUpdate) (model.StudioFlow, model.StudioFlowVersion, error)
	// DeleteStudioFlow removes a flow with its versions, runs and steps.
	// DeleteStudioFlow 删除一个编排及其版本、运行与步骤。
	DeleteStudioFlow(ctx context.Context, owner StudioOwner, id string) error

	// StartStudioRun inserts run, which must name a version of a flow of the
	// same owner (else ErrNotFound), unless that would pass limits
	// (ErrStudioRunLimit). A retry of an existing run id of the same owner
	// succeeds without writing.
	// StartStudioRun 插入 run；run 必须指向同一所有者某个编排的某个版本（否则
	// ErrNotFound），超出 limits 时返回 ErrStudioRunLimit。同一所有者已存在的运行 id
	// 重试时直接成功、不再写入。
	StartStudioRun(ctx context.Context, owner StudioOwner, run *model.StudioFlowRun, limits StudioRunLimits, now time.Time) error
	// GetStudioRun reads a run and its steps in the order they finished.
	// GetStudioRun 读取一次运行及其步骤，按结束顺序排列。
	GetStudioRun(ctx context.Context, owner StudioOwner, id string) (model.StudioFlowRun, []model.StudioFlowRunStep, error)
	// ListStudioRuns reads one page of a flow's runs, without steps. A flow
	// that is not the owner's is ErrNotFound.
	// ListStudioRuns 读取某个编排的一页运行，不含步骤。不属于该所有者的编排返回
	// ErrNotFound。
	ListStudioRuns(ctx context.Context, filter StudioRunFilter) ([]model.StudioFlowRun, error)
	// RenewStudioRun moves the lease of a run replica holds to until.
	// RenewStudioRun 把 replica 持有的运行的租约延长到 until。
	RenewStudioRun(ctx context.Context, owner StudioOwner, id, replica string, until, now time.Time) error
	// RecordStudioRunStep stores a step of a run replica holds. A retry of the
	// same node succeeds without writing.
	// RecordStudioRunStep 保存 replica 持有的运行的一个步骤。同一节点的重试直接成功、
	// 不再写入。
	RecordStudioRunStep(ctx context.Context, owner StudioOwner, step *model.StudioFlowRunStep, replica string, now time.Time) error
	// FinishStudioRun ends a run replica holds. Repeating the finish of a run
	// that already ended with the same status succeeds without writing.
	// FinishStudioRun 结束 replica 持有的运行。对已以相同状态结束的运行重复结束时直接
	// 成功、不再写入。
	FinishStudioRun(ctx context.Context, owner StudioOwner, id, replica string, finish StudioRunFinish, now time.Time) error
}

// StudioRunExpired reports whether a run reads as interrupted at now: it is
// still marked running, but its replica stopped renewing the lease.
//
// StudioRunExpired 报告运行在 now 时是否读作 interrupted：它仍标记为 running，但其副本
// 已停止续租。
func StudioRunExpired(run model.StudioFlowRun, now time.Time) bool {
	return run.Status == model.StudioRunRunning && run.LeaseUntil.Before(now)
}

// StudioRunHeld reports, as nil or ErrStudioRunFinished, whether replica may
// still write to run at now. Both store implementations share it, so the
// fencing rule lives in one place.
//
// StudioRunHeld 以 nil 或 ErrStudioRunFinished 报告 replica 在 now 时是否仍可写入 run。
// 两种存储实现共用它，使隔离规则只有一处。
func StudioRunHeld(run model.StudioFlowRun, replica string, now time.Time) error {
	if run.Status != model.StudioRunRunning || run.ReplicaID != replica || StudioRunExpired(run, now) {
		return ErrStudioRunFinished
	}
	return nil
}
