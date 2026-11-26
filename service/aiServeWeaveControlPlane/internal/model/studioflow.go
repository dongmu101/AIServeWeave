package model

import "time"

// StudioFlow is one Studio flow (编排) owned by one tenant user. Version is
// the latest saved version; every save that changes the definition appends a
// StudioFlowVersion and bumps it. Every read and write is scoped by
// (TenantID, UserID), as for conversations.
//
// StudioFlow 是某个租户用户的一个 Studio 编排。Version 是最新保存的版本号；每次改动定义
// 的保存都追加一个 StudioFlowVersion 并递增它。与对话一样，所有读写都以
// (TenantID, UserID) 限定。
type StudioFlow struct {
	ID        string    `gorm:"primaryKey;size:40"`
	TenantID  string    `gorm:"size:40;not null;index:idx_studio_flows_owner,priority:1"`
	UserID    string    `gorm:"size:40;not null;index:idx_studio_flows_owner,priority:2"`
	Name      string    `gorm:"size:200;not null"`
	Version   int64     `gorm:"not null"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null;index:idx_studio_flows_owner,priority:3"`
}

// TableName pins the table name. See Tenant.TableName in model.go.
//
// TableName 钉死表名，理由见 model.go 的 Tenant.TableName。
func (StudioFlow) TableName() string { return "studio_flows" }

// StudioFlowVersion is one immutable saved definition of a flow: the
// common/flowdef JSON exactly as validated. A run records the version it ran,
// so a later edit never changes how an old run reads.
//
// StudioFlowVersion 是编排一次保存后不可变的定义：即通过校验的 common/flowdef JSON。
// 运行记录自己所运行的版本，因此之后的修改不会改变旧运行的含义。
type StudioFlowVersion struct {
	FlowID     string    `gorm:"primaryKey;size:40"`
	Version    int64     `gorm:"primaryKey;autoIncrement:false"`
	Definition string    `gorm:"type:text;not null"`
	CreatedAt  time.Time `gorm:"not null"`
}

// TableName pins the table name. See Tenant.TableName in model.go.
//
// TableName 钉死表名，理由见 model.go 的 Tenant.TableName。
func (StudioFlowVersion) TableName() string { return "studio_flow_versions" }

// Studio flow run statuses. A run is written as running and ends in one of
// the three terminal statuses the Gateway reports. Interrupted is never
// stored: a running run whose lease has passed reads as interrupted, because
// the replica executing it stopped renewing — see store.StudioRunExpired.
//
// Studio 编排运行的状态。运行以 running 写入，以 Gateway 报告的三种终态之一结束。
// Interrupted 从不写入：租约已过的 running 运行读出来就是 interrupted，因为执行它的副本
// 已停止续租——见 store.StudioRunExpired。
const (
	StudioRunRunning     = "running"
	StudioRunSucceeded   = "succeeded"
	StudioRunFailed      = "failed"
	StudioRunCanceled    = "canceled"
	StudioRunInterrupted = "interrupted"
)

// StudioFlowRun is one execution of a flow version. It runs on the Gateway
// replica named by ReplicaID, which holds it by renewing LeaseUntil. Inputs
// is the JSON object of input values as submitted, kept so the run can be
// repeated; Outputs is the JSON object of result fields.
//
// StudioFlowRun 是某个编排版本的一次执行。它在 ReplicaID 指明的 Gateway 副本上运行，该
// 副本通过续租 LeaseUntil 持有它。Inputs 是提交时的输入值 JSON 对象，保留下来以便重跑；
// Outputs 是结果字段的 JSON 对象。
type StudioFlowRun struct {
	ID               string     `gorm:"primaryKey;size:40"`
	FlowID           string     `gorm:"size:40;not null;index:idx_studio_flow_runs_flow,priority:1"`
	TenantID         string     `gorm:"size:40;not null;index:idx_studio_flow_runs_active,priority:1"`
	UserID           string     `gorm:"size:40;not null"`
	FlowVersion      int64      `gorm:"not null"`
	Status           string     `gorm:"size:16;not null;index:idx_studio_flow_runs_active,priority:2"`
	ErrorCode        string     `gorm:"size:64;not null"`
	FailedNode       string     `gorm:"size:64;not null"`
	Inputs           string     `gorm:"type:text;not null"`
	Outputs          string     `gorm:"type:text;not null"`
	PromptTokens     int64      `gorm:"not null"`
	CompletionTokens int64      `gorm:"not null"`
	ReplicaID        string     `gorm:"size:128;not null"`
	LeaseUntil       time.Time  `gorm:"not null;index:idx_studio_flow_runs_active,priority:3"`
	CreatedAt        time.Time  `gorm:"not null;index:idx_studio_flow_runs_flow,priority:2"`
	FinishedAt       *time.Time ``
}

// TableName pins the table name. See Tenant.TableName in model.go.
//
// TableName 钉死表名，理由见 model.go 的 Tenant.TableName。
func (StudioFlowRun) TableName() string { return "studio_flow_runs" }

// StudioStepSkipped is the status of a node a condition branched away from.
// A step's other statuses are the run's terminal ones: succeeded, failed and
// canceled.
//
// StudioStepSkipped 是被条件分支绕过的节点的状态。步骤的其他状态与运行的终态相同：
// succeeded、failed 与 canceled。
const StudioStepSkipped = "skipped"

// StudioFlowRunStep is how one node of a run ended: the engine's step,
// with Inputs and Outputs as JSON objects of text the Gateway has already cut
// to size. A node writes one step, so (RunID, NodeID) identifies it.
//
// StudioFlowRunStep 是一次运行中某个节点的结束情况：即引擎的步骤，Inputs 与 Outputs 是
// Gateway 已截断到上限内的文本 JSON 对象。一个节点只写一个步骤，因此 (RunID, NodeID)
// 唯一标识它。
type StudioFlowRunStep struct {
	RunID            string    `gorm:"primaryKey;size:40"`
	NodeID           string    `gorm:"primaryKey;size:64"`
	TenantID         string    `gorm:"size:40;not null"`
	UserID           string    `gorm:"size:40;not null"`
	NodeType         string    `gorm:"size:32;not null"`
	Status           string    `gorm:"size:16;not null"`
	ErrorCode        string    `gorm:"size:64;not null"`
	Model            string    `gorm:"size:128;not null"`
	Inputs           string    `gorm:"type:text;not null"`
	Outputs          string    `gorm:"type:text;not null"`
	PromptTokens     int64     `gorm:"not null"`
	CompletionTokens int64     `gorm:"not null"`
	StartedAt        time.Time `gorm:"not null"`
	FinishedAt       time.Time `gorm:"not null"`
}

// TableName pins the table name. See Tenant.TableName in model.go.
//
// TableName 钉死表名，理由见 model.go 的 Tenant.TableName。
func (StudioFlowRunStep) TableName() string { return "studio_flow_run_steps" }
