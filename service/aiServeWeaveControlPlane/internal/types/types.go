// Package types is the Admin API's wire format: the request and response
// shapes the Console and the Gateway exchange with this service.
//
// They are declared by hand rather than generated from a goctl .api file. The
// repository's rule that a contract has exactly one source applies: the .api
// DSL would be a second description of these same structs, kept in sync by
// habit, and goctl's regeneration would overwrite the doc comments this
// repository requires. What goctl buys — the routing boilerplate — is a page
// of code in routes.go.
//
// types 包是 Admin API 的线上格式：Console 与 Gateway 同本服务交换的请求与响应形状。
//
// 它们是手写的，而不是由 goctl 的 .api 文件生成。仓库那条「一份契约只有一个来源」的
// 规则在这里适用：.api DSL 会成为对同一批结构体的第二份描述，靠习惯来保持同步，而
// goctl 的重新生成会覆盖掉本仓库要求的 doc comment。goctl 换来的那部分——路由样板
// ——在 routes.go 里不过一页代码。
package types

import (
	"time"

	"AIServeWeave/common/quota"
)

// LoginRequest is a Console sign-in.
//
// LoginRequest 是一次 Console 登录。
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse carries the session token and who it belongs to. It never
// carries the password digest.
//
// LoginResponse 携带会话令牌及其归属。它绝不携带密码摘要。
type LoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      User      `json:"user"`
}

// User is a person, as rendered to the Console. PasswordHash has no field
// here, and that absence is the point: a response struct that cannot express
// the digest cannot leak it, however the handler is later edited.
//
// User 是渲染给 Console 的一个人。这里没有 PasswordHash 字段，而这个缺席正是要点：
// 一个无法表达该摘要的响应结构体，无论 handler 之后被怎么改动，都泄漏不了它。
type User struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CreateTenantRequest bootstraps a tenant and its first owner.
//
// CreateTenantRequest 引导创建一个租户及其第一个 owner。
type CreateTenantRequest struct {
	Name          string `json:"name"`
	OwnerEmail    string `json:"owner_email"`
	OwnerPassword string `json:"owner_password"`
}

// CreateTenantResponse is the created tenant and owner.
//
// CreateTenantResponse 是已创建的租户与 owner。
type CreateTenantResponse struct {
	Tenant Tenant `json:"tenant"`
	Owner  User   `json:"owner"`
}

// Tenant is an isolation boundary, as rendered to the Console.
//
// Tenant 是渲染给 Console 的一个隔离边界。
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// TenantProfileResponse is the caller's own tenant together with the quota
// that applies to it.
//
// The two are returned by one call because they are read together every time:
// a Console that showed the tenant without its limits, or the limits without
// knowing whose they are, would immediately ask for the other. There is no
// tenant id in the request — the tenant comes from the session, the same rule
// SetLimitsRequest follows, so nobody can aim this at another tenant.
//
// Limits is quota.Limits rather than a shape of this package's own: the
// meaning of these three numbers is a contract shared with the Gateway, and a
// second declaration of them here would be a second place to keep in sync.
// It carries omitempty on every field, so an unconfigured tenant's limits
// encode as `{}` — absent means zero, and zero means unlimited.
//
// TenantProfileResponse 是调用方自己所属的租户，连同适用于它的配额。
//
// 两者由一次调用返回，因为它们每次都是一起读的：一个只显示租户而不显示其限制、或者
// 只显示限制却不知道那是谁的限制的 Console，紧接着就会去要另一半。请求里没有租户 id
// ——租户来自会话，与 SetLimitsRequest 遵循同一条规则，因此没人能把它指向别的租户。
//
// Limits 用的是 quota.Limits 而不是本包自己的形状：这三个数字的含义是与 Gateway 共享的
// 契约，在这里再声明一遍，就是多了一处需要保持同步的地方。它每个字段都带 omitempty，
// 因此一个未配置的租户其 limits 编码为 `{}`——缺席即为零，而零表示不限制。
type TenantProfileResponse struct {
	Tenant Tenant       `json:"tenant"`
	Limits quota.Limits `json:"limits"`
}

// CreateUserRequest adds a user to the caller's tenant.
//
// CreateUserRequest 向调用方所属租户添加一个用户。
type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Role     string `json:"role"`
}

// CreateAPIKeyRequest mints a key.
//
// CreateAPIKeyRequest 铸造一个 key。
type CreateAPIKeyRequest struct {
	Name string `json:"name"`
	// TTLSeconds is the key's lifetime. Zero uses the service default; a
	// negative value means no expiry, which the API keeps expressible so an
	// operator does not resort to passing a hundred years.
	//
	// TTLSeconds 是该 key 的生命期。为零使用服务默认值；负值表示不过期——API 保留
	// 这种表达方式，好让运维不必退而求其次去传一百年。
	TTLSeconds int64 `json:"ttl_seconds"`
}

// CreateAPIKeyResponse is the only response in this API that carries a
// plaintext key. It is returned once, to the call that created it, and no
// read path can produce it again.
//
// CreateAPIKeyResponse 是本 API 中唯一携带明文 key 的响应。它只向创建它的那次调用
// 返回一次，之后没有任何读取路径能再次产生它。
type CreateAPIKeyResponse struct {
	// Key is the plaintext. Show it to the person once and do not store it.
	//
	// Key 是明文。向本人展示一次，不要存储。
	Key    string `json:"key"`
	APIKey APIKey `json:"api_key"`
}

// APIKey is a credential, as rendered to the Console. It carries the display
// form and never the hash: the hash is what a verification looks up by, and
// the Console has no use for it.
//
// APIKey 是渲染给 Console 的一个凭据。它携带 display 形式，绝不携带哈希：哈希是校验
// 时据以查询的值，而 Console 用不着它。
type APIKey struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	Name       string     `json:"name"`
	Display    string     `json:"display"`
	Status     string     `json:"status"`
	CreatedBy  string     `json:"created_by"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// UserListResponse, APIKeyListResponse and AuditListResponse are the three
// list shapes. They are envelopes rather than bare arrays, and that is a
// deliberate break with the shape these endpoints used to return.
//
// A bare array has nowhere to put a cursor, and these lists need one: they
// grow without bound, and a response that returned all of them would be a
// request any caller could use to pull an unbounded result set through this
// process. Adding parallel paginated endpoints instead would leave two ways to
// read one list, which is the kind of duplication this repository treats as a
// contract defect. The Console is the only consumer, and it is updated with
// this change.
//
// NextCursor carries omitempty: its absence is how a response says this is the
// last page. There is no total — see store.Page for why.
//
// UserListResponse、APIKeyListResponse 与 AuditListResponse 是三种列表形状。它们是信封
// 而不是裸数组，这是对这些端点原有形状的一次刻意破坏性变更。
//
// 裸数组没有地方放游标，而这些列表需要游标：它们会无限增长，一个把它们全部返回的响应，
// 会成为任何调用方都能用来经由本进程拉取无界结果集的请求。改为并列增设分页端点，则会
// 留下两种读取同一份列表的方式，而那正是本仓库视为契约缺陷的那类重复。Console 是唯一
// 的消费方，它随本次改动一并更新。
//
// NextCursor 带 omitempty：它的缺席就是响应在说这是最后一页。这里没有总数——原因见
// store.Page。
type UserListResponse struct {
	Items      []User `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// APIKeyListResponse is one page of a tenant's keys.
//
// APIKeyListResponse 是一个租户 key 列表中的一页。
type APIKeyListResponse struct {
	Items      []APIKey `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

// AuditListResponse is one page of a tenant's audit trail.
//
// AuditListResponse 是一个租户审计线索中的一页。
type AuditListResponse struct {
	Items      []AuditEntry `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// AuditEntry is one administrative action, as rendered to the Console.
//
// AuditEntry 是渲染给 Console 的一次管理操作。
type AuditEntry struct {
	ID        string    `json:"id"`
	ActorID   string    `json:"actor_id"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

// VerifyRequest is the Gateway's key verification. It carries the hash, never
// the key: the Gateway hashes the caller's key itself, so a user's credential
// never travels to the control plane and never appears in its request logs.
//
// VerifyRequest 是 Gateway 的 key 校验请求。它携带哈希，绝不携带 key：Gateway 自己
// 对调用方的 key 做哈希，因此用户的凭据从不传到控制面，也从不出现在它的请求日志里。
type VerifyRequest struct {
	Hash string `json:"hash"`
}

// VerifyResponse tells the Gateway which tenant to attribute a request to, and
// what that tenant may consume. It carries nothing else — not the key's name,
// not its creator, not its expiry — because the data plane has no use for any
// of it and every extra field is one more thing to invalidate when it changes.
//
// Limits is the one addition that earns its place: the Gateway enforces the
// quota, and sending it here means enforcement costs no round trip of its own.
// It shares the identity's cache lifetime, so a limit change lands in the same
// window a revocation does.
//
// VerifyResponse 告诉 Gateway 该把一次请求归给哪个租户，以及该租户可以消耗多少。它
// 别无其他内容——不含 key 的名称、创建者或过期时间——因为数据面用不着它们，而每多一个
// 字段，就多一样在它变化时需要失效的东西。
//
// Limits 是唯一配得上位置的新增项：配额由 Gateway 执行，把它放在这里，意味着执行不必
// 自己付出一次往返。它与身份共用缓存生命期，因此一次限制变更的落地窗口与一次吊销相同。
type VerifyResponse struct {
	TenantID string       `json:"tenant_id"`
	KeyID    string       `json:"key_id"`
	Limits   quota.Limits `json:"limits"`
}

// SetLimitsRequest sets the caller's own tenant's quota. There is no tenant id
// in it: the tenant comes from the session, so an admin cannot aim this at
// somebody else's tenant by editing a request body.
//
// SetLimitsRequest 设置调用方自己所属租户的配额。它里面没有租户 id：租户来自会话，
// 因此管理员无法通过改请求体把它指向别人的租户。
type SetLimitsRequest struct {
	RequestsPerMinute int `json:"requests_per_minute"`
	TokensPerMinute   int `json:"tokens_per_minute"`
	MaxConcurrent     int `json:"max_concurrent"`
}

// Limits returns the request in the shared contract's form.
//
// Limits 以共享契约的形式返回该请求。
func (r SetLimitsRequest) Limits() quota.Limits {
	return quota.Limits{
		RequestsPerMinute: r.RequestsPerMinute,
		TokensPerMinute:   r.TokensPerMinute,
		MaxConcurrent:     r.MaxConcurrent,
	}
}

// JobHistoryResponse is one persisted job as the tenant-facing Admin API
// shows it (STATUS.md's J07) — the persisted counterpart to the live
// workflowview.Job the fleet-backed /admin/v1/jobs already renders. Unlike
// JobResponse (the internal API's full record, meant only for a Gateway
// replica reconstructing its own routing), this omits NodeID, RuntimeID and
// BackendRunID: a tenant has no business knowing which node or backend run
// id served their request, the same omission the live view already makes
// for replica identity.
//
// UpdatedAt is when this record was last actually observed to change, not a
// live value — a run may have progressed further on the node without this
// row having caught up yet. See the ControlPlane README's 「Job 持久化契约」
// for why persisted state is a last-observed snapshot, and TerminalAt for
// the one timestamp that, once set, is guaranteed not to move again.
//
// JobHistoryResponse 是租户侧 Admin API 展示的一条持久化 job 记录（STATUS.md
// 的 J07）——是由 fleet 支撑的 `/admin/v1/jobs` 已经渲染的那个实时
// workflowview.Job 的持久化对应物。与 JobResponse（内部 API 的完整记录，
// 只为 Gateway 副本重建自己的路由而存在）不同，这里省去了 NodeID、RuntimeID
// 与 BackendRunID：租户没有理由知道是哪个节点或后端运行 id 服务了自己的请求，
// 这与实时视图已经对副本身份做的省略相同。
//
// UpdatedAt 是这条记录最后一次被真正观测到发生变化的时刻，不是一个实时值——
// 这次运行可能已经在节点上继续推进，只是这一行还没跟上。持久化状态为什么是
// 一份最后观测的快照，见 ControlPlane README「Job 持久化契约」；TerminalAt
// 则是唯一一个一旦被设置就保证不会再移动的时间戳。
type JobHistoryResponse struct {
	JobID           string     `json:"job_id"`
	WorkflowID      string     `json:"workflow_id"`
	WorkflowVersion string     `json:"workflow_version,omitempty"`
	State           string     `json:"state"`
	ErrorSummary    string     `json:"error_summary,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	TerminalAt      *time.Time `json:"terminal_at,omitempty"`
}

// JobHistoryListResponse is one page of a tenant's persisted job history.
//
// JobHistoryListResponse 是一个租户持久化 job 历史中的一页。
type JobHistoryListResponse struct {
	Items      []JobHistoryResponse `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// CreateJobRequest is what a Gateway replica reports about a run it just
// submitted, per STATUS.md's J01/J04 persistence contract. TenantID is what
// the Gateway asserts about its own caller — this endpoint is guarded by the
// internal shared secret, not a tenant session, so there is no session to
// derive it from the way the Admin API's other writes do.
//
// NodeID, RuntimeID and BackendRunID travel over this internal channel even
// though no tenant-facing response ever carries them: a restarted Gateway
// replica recovering a job's route binding (STATUS.md's J06) has nowhere
// else to read them back from.
//
// CreateJobRequest 是一个 Gateway 副本就它刚提交的一次运行所报告的内容，对应
// STATUS.md 的 J01/J04 持久化契约。TenantID 是 Gateway 对自己调用方所做的
// 断言——本端点由内部共享密钥守卫，而不是租户会话，因此没有会话可供像 Admin API
// 其余写操作那样据以推导它。
//
// NodeID、RuntimeID 与 BackendRunID 会经由这条内部通道传输，即便没有任何面向
// 租户的响应会携带它们：一个正在恢复某个 job 路由绑定的重启后 Gateway 副本
// （STATUS.md 的 J06），没有别的地方能把它们读回来。
type CreateJobRequest struct {
	JobID           string `json:"job_id"`
	TenantID        string `json:"tenant_id"`
	WorkflowID      string `json:"workflow_id"`
	WorkflowVersion string `json:"workflow_version,omitempty"`
	NodeID          string `json:"node_id"`
	RuntimeID       string `json:"runtime_id"`
	BackendRunID    string `json:"backend_run_id"`
	State           string `json:"state"`
	ObservedSeq     int64  `json:"observed_seq"`
}

// JobResponse is the internal API's full record of one job — the storage
// layer's view, not the Admin API's. It is never served to a tenant session:
// listJobs (the Admin API's own handler) renders workflowview.Job instead,
// which is where the route binding this struct carries gets stripped.
//
// JobResponse 是内部 API 对一个 job 的完整记录——存储层的视角，不是 Admin API
// 的视角。它绝不提供给租户会话：listJobs（Admin API 自己的 handler）渲染的是
// workflowview.Job，这个结构体携带的路由绑定信息正是在那里被剥离的。
type JobResponse struct {
	JobID           string     `json:"job_id"`
	TenantID        string     `json:"tenant_id"`
	WorkflowID      string     `json:"workflow_id"`
	WorkflowVersion string     `json:"workflow_version,omitempty"`
	NodeID          string     `json:"node_id"`
	RuntimeID       string     `json:"runtime_id"`
	BackendRunID    string     `json:"backend_run_id"`
	State           string     `json:"state"`
	ErrorSummary    string     `json:"error_summary,omitempty"`
	ObservedSeq     int64      `json:"observed_seq"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	TerminalAt      *time.Time `json:"terminal_at,omitempty"`
}

// ListActiveJobsResponse is every non-terminal job a Gateway replica
// recovering after a restart (STATUS.md's J06) owes a route binding —
// across every tenant, since a replica knows which nodes are connected to
// it, not which tenants submitted work to them.
//
// ListActiveJobsResponse 是一个重启后正在恢复的 Gateway 副本（STATUS.md 的
// J06）对某个路由绑定所欠的每一个非终态 job——跨越所有租户，因为一个副本
// 知道的是哪些节点连接到自己，而不是哪些租户把工作提交到了它们身上。
type ListActiveJobsResponse struct {
	Items []JobResponse `json:"items"`
}

// UpdateJobStateRequest is one status observation a Gateway replica reports
// — from a foreground poll, an SSE event, or its background syncer. See
// store.JobStateUpdate for the ObservedSeq gate this is applied under.
//
// UpdateJobStateRequest 是一个 Gateway 副本报告的一次状态观测——来自一次前台
// 轮询、一次 SSE 事件，或它的后台同步器。这次更新据以放行的 ObservedSeq 门槛，
// 见 store.JobStateUpdate。
type UpdateJobStateRequest struct {
	TenantID     string `json:"tenant_id"`
	State        string `json:"state"`
	ErrorSummary string `json:"error_summary,omitempty"`
	ObservedSeq  int64  `json:"observed_seq"`
}

// UpdateJobStateResponse reports whether this call's update was the one
// that applied, alongside the job's current row — which the caller needs to
// read regardless of Applied, since a lost race still leaves something on
// record worth knowing about.
//
// UpdateJobStateResponse 报告这次调用的更新是否是真正生效的那一个，并附上该
// job 当前的行——无论 Applied 为何，调用方都需要读取它，因为一次落败的竞争
// 依然会留下一条值得了解的记录。
type UpdateJobStateResponse struct {
	Applied bool        `json:"applied"`
	Job     JobResponse `json:"job"`
}

// CreateJobArtifactRequest is one artifact a run produced, with the public
// id the Gateway's own artifact listing already minted for it.
//
// CreateJobArtifactRequest 是一次运行产出的一个产物，携带 Gateway 自己的产物
// 列举已经为它铸造的公开 id。
type CreateJobArtifactRequest struct {
	ArtifactID string `json:"artifact_id"`
	TenantID   string `json:"tenant_id"`
	Filename   string `json:"filename"`
	Subfolder  string `json:"subfolder,omitempty"`
	Type       string `json:"type,omitempty"`
}

// JobArtifactResponse is one recorded artifact.
//
// JobArtifactResponse 是一条已记录的产物。
type JobArtifactResponse struct {
	ArtifactID string    `json:"artifact_id"`
	JobID      string    `json:"job_id"`
	TenantID   string    `json:"tenant_id"`
	Filename   string    `json:"filename"`
	Subfolder  string    `json:"subfolder,omitempty"`
	Type       string    `json:"type,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// ListJobArtifactsResponse is one job's recorded artifacts.
//
// ListJobArtifactsResponse 是一个 job 已记录的产物。
type ListJobArtifactsResponse struct {
	Items []JobArtifactResponse `json:"items"`
}

// ErrorResponse is the failure shape every endpoint returns.
//
// ErrorResponse 是每个端点返回失败时的形状。
type ErrorResponse struct {
	Error string `json:"error"`
}
