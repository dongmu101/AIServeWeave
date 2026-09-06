// Package model's job.go holds the persisted record of one workflow run,
// per STATUS.md's J03 and the persistence contract in the ControlPlane
// README's 「Job 持久化契约」 section. It is the storage-layer half of that
// contract: the fields a Gateway replica needs restored after it restarts,
// which is a superset of what any external view of a job is allowed to show.
//
// model 的 job.go 保存一次工作流运行的持久化记录，对应 STATUS.md 的 J03 与
// ControlPlane README「Job 持久化契约」一节。它是该契约在存储层的那一半：
// Gateway 副本重启后需要恢复的字段，是任何对外 job 视图允许展示内容的超集。
package model

import "time"

// Job states. They are this table's own vocabulary, deliberately not
// runtime.WorkflowState's values — the same separation Gateway's
// httpapi.publicStatus already draws between the backend's own states and an
// API's public ones, applied here between the backend's states and this
// table's persisted ones.
//
// Job 状态。这是本表自己的词汇，刻意不是 runtime.WorkflowState 的取值——与
// Gateway 的 httpapi.publicStatus 在后端自身状态与某个 API 的公开状态之间已经
// 划开的那道界线相同，这里把它用在后端状态与本表持久化状态之间。
const (
	JobPending   = "pending"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// Job is one workflow run as the control plane's jobs table records it.
//
// Two fields matter for the same reason README already states for Gateway's
// own in-memory job: NodeID/RuntimeID/BackendRunID are the route binding a
// restarted Gateway replica needs to keep answering cancel and artifact
// requests, and they must never be rendered into an external job view —
// storage need and external visibility are separate decisions, and this
// struct only settles the first one.
//
// ObservedSeq is not a row version for optimistic locking on writes this
// table itself originates — it is the Gateway-assigned sequence number a
// background sync or an SSE event carried, and UpdateJobState is gated on it
// being strictly greater than the stored value. That is what makes a
// duplicate or reordered status update an idempotent no-op instead of a
// write that can undo a more recent one; see the store package's
// UpdateJobState.
//
// Job 是控制面 jobs 表所记录的一次工作流运行。
//
// 有两点与 README 已经为 Gateway 自己的内存 job 陈述的理由相同：NodeID、
// RuntimeID、BackendRunID 是一个重启后的 Gateway 副本要继续回答取消与产物请求
// 所需的路由绑定，且它们绝不能被渲染进任何对外 job 视图——存储层需要什么，与对外
// 展示什么，是两个独立的决定，这个结构体只落实第一个。
//
// ObservedSeq 不是本表自己发起写入时用于乐观锁的行版本号——它是后台同步或一次
// SSE 事件携带的、由 Gateway 赋予的序号，UpdateJobState 以它严格大于已存储的值
// 为条件放行。这正是让一次重复或乱序的状态更新变成幂等空操作、而不是一次能够
// 撤销更新近写入的写入的原因；见 store 包的 UpdateJobState。
type Job struct {
	// ID is the public job id the Gateway replica that accepted the run
	// minted (e.g. "job_..."). It is the primary key rather than a
	// surrogate one: the id already is globally unique and is what every
	// later write about this job is keyed by, so a duplicate create is
	// naturally rejected by the same uniqueness this key already provides.
	//
	// ID 是接受该次运行的 Gateway 副本铸造的公开 job id（如 "job_..."）。它是
	// 主键而不是另设一个代理键：这个 id 本就全局唯一，也是此后关于这个 job 的
	// 每一次写入据以定位的键，因此重复的创建天然会被这同一个唯一性拒绝。
	ID         string `gorm:"primaryKey;size:64"`
	TenantID   string `gorm:"size:32;not null;index:idx_jobs_tenant_created;index:idx_jobs_tenant_status"`
	WorkflowID string `gorm:"size:128;not null"`
	// WorkflowVersion is an opaque, template-author-chosen string. Workflow
	// template versioning (STATUS.md's P03) does not exist yet, so this
	// column accepts an empty string until it does — a job's record is not
	// blocked on a feature it merely wants to reference later.
	//
	// WorkflowVersion 是一个不透明的、由模板作者选择的字符串。工作流模板版本
	// 管理（STATUS.md 的 P03）尚不存在，因此这一列在它出现之前接受空字符串——
	// 一条 job 记录不该被它只是想在将来引用的功能卡住。
	WorkflowVersion string `gorm:"size:64;not null;default:''"`

	// NodeID, RuntimeID and BackendRunID are the route binding: which node,
	// which runtime instance on it, and the backend's own run identifier
	// (a ComfyUI prompt_id, unique only within that one instance). See the
	// package doc comment for why these are stored but never surfaced.
	//
	// NodeID、RuntimeID 与 BackendRunID 是路由绑定：哪个节点、节点上的哪个
	// runtime 实例，以及后端自己的运行标识（一个 ComfyUI prompt_id，只在那一个
	// 实例内部唯一）。为什么它们只存储、绝不对外展示，见本包的文档注释。
	NodeID       string `gorm:"size:128;not null"`
	RuntimeID    string `gorm:"size:128;not null"`
	BackendRunID string `gorm:"size:256;not null"`

	State        string `gorm:"size:16;not null;index:idx_jobs_tenant_status"`
	ErrorSummary string `gorm:"size:512"`
	// ObservedSeq gates UpdateJobState; see the type doc comment.
	//
	// ObservedSeq 是 UpdateJobState 的放行条件；见类型的文档注释。
	ObservedSeq int64 `gorm:"not null;default:0"`

	CreatedAt time.Time `gorm:"not null;index:idx_jobs_tenant_created"`
	UpdatedAt time.Time `gorm:"not null"`
	// TerminalAt is set once, the first time State becomes terminal, and
	// UpdateJobState's terminal-state immutability means it is never moved
	// afterwards. Nil means still in flight.
	//
	// TerminalAt 只被设置一次，即 State 第一次变为终态的那一刻；UpdateJobState
	// 的终态不可变规则意味着此后它不会再被移动。nil 表示仍在进行中。
	TerminalAt *time.Time
}

// TableName pins the table name. See Tenant.TableName in model.go.
//
// TableName 钉死表名，理由见 model.go 的 Tenant.TableName。
func (Job) TableName() string { return "jobs" }

// Terminal reports whether the run has reached one of the states
// UpdateJobState will no longer move it away from.
//
// Terminal 报告该次运行是否已到达 UpdateJobState 不会再使其离开的状态之一。
func (j Job) Terminal() bool {
	switch j.State {
	case JobSucceeded, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

// JobArtifact is one output a run produced, keyed by the public artifact id
// a Gateway replica minted for it — mirroring how the Gateway's own
// in-memory jobStore mints and resolves artifact ids, so the same id keeps
// meaning the same thing whether it is answered from memory or restored
// from this table.
//
// Filename, Subfolder and Type are the backend's own three-part locator
// (runtime.ArtifactRef without its RunID, which is redundant with JobID
// here). Like a job's route binding, this locator is stored for recovery but
// is not the identifier handed to a caller — the public ID is.
//
// JobArtifact 是一次运行产出的一个产物，以 Gateway 副本为它铸造的公开产物 id
// 为键——与 Gateway 自己内存版 jobStore 铸造与解析产物 id 的方式相同，这样
// 同一个 id 无论是由内存作答还是从本表恢复，含义都保持一致。
//
// Filename、Subfolder 与 Type 是后端自己的三段式定位信息（即 runtime.ArtifactRef
// 去掉在这里与 JobID 重复的 RunID）。与 job 的路由绑定一样，这份定位信息只为恢复
// 而存储，不是交给调用方的标识符——公开 ID 才是。
type JobArtifact struct {
	ID        string    `gorm:"primaryKey;size:64"`
	JobID     string    `gorm:"size:64;not null;index:idx_job_artifacts_job"`
	TenantID  string    `gorm:"size:32;not null;index:idx_job_artifacts_tenant"`
	Filename  string    `gorm:"size:512;not null"`
	Subfolder string    `gorm:"size:512;not null;default:''"`
	Type      string    `gorm:"size:32;not null;default:''"`
	CreatedAt time.Time `gorm:"not null"`
}

// TableName pins the table name. See Tenant.TableName in model.go.
//
// TableName 钉死表名，理由见 model.go 的 Tenant.TableName。
func (JobArtifact) TableName() string { return "job_artifacts" }
