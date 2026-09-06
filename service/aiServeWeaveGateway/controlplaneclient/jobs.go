// jobs.go is the Gateway's side of the control plane's Job persistence API,
// per STATUS.md's J04 and the persistence contract in the ControlPlane
// README's 「Job 持久化契约」.
//
// It is a separate client from Verifier on purpose, even though both speak
// to the same control plane over the same InternalToken. Verifier sits on
// every inference request and must answer from an in-process cache in the
// common case; JobsClient's calls are a side channel to a Gateway's own
// in-memory job table and must never be something an inference response
// waits on. Mixing the two into one type would make it easy to reach for
// JobsClient's blocking HTTP call from a place that can least afford it.
//
// This client does not retry and does not decide what a failure means for
// the job it was reporting on — that policy belongs to STATUS.md's J05
// (the "提交结果未知" resolution) and J02 (the background syncer), both of
// which call this client rather than being part of it. What this file does
// guarantee is the vocabulary a caller reasons in: ErrConflict and
// ErrNotFound are the control plane's own answers, translated; anything
// else — a timeout, a connection failure, an unexpected status — comes back
// wrapped in ErrOutcomeUnknown, which is this package's name for the
// persistence contract's three-state model's third state: the call's
// result, not the run's, is what is unknown.
//
// jobs.go 是 Gateway 一侧的控制面 Job 持久化 API，对应 STATUS.md 的 J04 与
// ControlPlane README「Job 持久化契约」一节所定义的契约。
//
// 它与 Verifier 刻意分成两个客户端，即便两者都经由同一个 InternalToken 与同一个
// 控制面对话。Verifier 坐在每一次推理请求上，常见情况下必须由进程内缓存作答；
// JobsClient 的调用是 Gateway 自己内存 job 表的一条旁路，绝不能变成推理响应要
// 等待的东西。把两者揉进一个类型，会让人很容易从一个最经不起等待的地方，顺手
// 用上 JobsClient 那个会阻塞的 HTTP 调用。
//
// 本客户端不重试，也不替它所报告的那个 job 决定一次失败意味着什么——那份策略
// 属于 STATUS.md 的 J05（"提交结果未知" 的化解）与 J02（后台同步器），两者调用
// 本客户端，而不是本客户端的一部分。本文件保证的是调用方据以推理的词汇：
// ErrConflict 与 ErrNotFound 是控制面自己给出、经过转译的答案；其余情形——超时、
// 连接失败、意料之外的状态码——一律包进 ErrOutcomeUnknown 返回，这是本包为持久化
// 契约三态模型中的第三态起的名字：不确定的是这次调用的结果，不是这次运行本身。
package controlplaneclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultJobsTimeout bounds one Job persistence call. It is short for the
// same reason Verifier's DefaultTimeout is: these calls are meant to be a
// side channel, never something an inference response is held open for.
//
// DefaultJobsTimeout 限制单次 Job 持久化调用。它很短，理由与 Verifier 的
// DefaultTimeout 相同：这些调用本该是一条旁路，绝不能变成推理响应被挂起等待的
// 原因。
const DefaultJobsTimeout = 3 * time.Second

// ErrConflict is the control plane's own conflict, translated: a job id (or
// artifact id) is already recorded under a different tenant than the one
// asserted. It is not raised for a duplicate report of the same tenant's own
// job — that is idempotent on the control plane side and returns success.
//
// ErrConflict 是转译过的控制面自身冲突：一个 job id（或产物 id）已经在一个不同于
// 本次断言租户的名下被记录。对同一租户自己 job 的重复报告不会触发它——那在控制面
// 那一侧是幂等的，会返回成功。
var ErrConflict = errors.New("controlplaneclient: conflict")

// ErrNotFound is the control plane's own not-found, translated: no such job
// (or artifact) under the asserted tenant.
//
// ErrNotFound 是转译过的控制面自身「未找到」：在所断言的租户下没有这个 job
// （或产物）。
var ErrNotFound = errors.New("controlplaneclient: not found")

// ErrOutcomeUnknown wraps any failure that leaves the call's result
// unresolved — a timeout, a connection failure, the control plane's own
// token being refused, or an unrecognized status code. It is this package's
// name for the persistence contract's "提交结果未知" state: the caller
// cannot tell whether the control plane received and processed the request,
// only that no usable answer came back. Retrying blindly risks the same
// double-report the contract calls out; not retrying risks the record never
// existing. Deciding between those is J05's job, not this client's — this
// client's job is only to make the ambiguity impossible to miss.
//
// ErrOutcomeUnknown 包裹一切让这次调用结果悬而未决的失败——超时、连接失败、
// 本 Gateway 自己的 token 被拒绝，或一个无法识别的状态码。这是本包为持久化契约
// 的「提交结果未知」状态起的名字：调用方无法判断控制面是否已经收到并处理了这次
// 请求，只知道没有得到一个可用的答案。盲目重试冒着契约点名过的「重复报告」风险；
// 不重试则冒着「这条记录从未存在」的风险。在两者之间做出选择是 J05 的工作，不是
// 本客户端的——本客户端的工作只是让这份含糊不会被忽略。
var ErrOutcomeUnknown = errors.New("controlplaneclient: outcome unknown")

// ErrInvalidRequest is the control plane's own rejection of a malformed
// request (a missing job id, an empty state) — a definite "no", not the
// ambiguity ErrOutcomeUnknown names. It signals a bug in the caller, not a
// transient condition worth reporting or retrying through J05's machinery.
//
// ErrInvalidRequest 是控制面对一次畸形请求（缺失的 job id、空的 state）给出的
// 明确拒绝——是一个确定的"否"，不是 ErrOutcomeUnknown 所指的那种含糊。它意味着
// 调用方自身有缺陷，而不是一种值得经由 J05 机制上报或重试的临时状况。
var ErrInvalidRequest = errors.New("controlplaneclient: invalid request")

// JobsClientConfig configures a JobsClient.
//
// JobsClientConfig 配置一个 JobsClient。
type JobsClientConfig struct {
	// Endpoint is the control plane's base URL, e.g. http://127.0.0.1:8090.
	//
	// Endpoint 是控制面的基础 URL，例如 http://127.0.0.1:8090。
	Endpoint string
	// Token authenticates this Gateway to the control plane's internal
	// endpoints. It must match the control plane's InternalToken — the same
	// one Verifier uses, since both are the Gateway talking to the control
	// plane about its own callers' business.
	//
	// Token 用于本 Gateway 向控制面的内部端点表明身份。它必须与控制面的
	// InternalToken 一致——与 Verifier 使用的是同一个，因为两者都是 Gateway
	// 就自己调用方的业务在与控制面对话。
	Token string
	// Timeout bounds one call. Zero uses DefaultJobsTimeout.
	//
	// Timeout 限制单次调用。为零时使用 DefaultJobsTimeout。
	Timeout time.Duration
	// HTTPClient is used for the calls. Nil builds one with Timeout.
	//
	// HTTPClient 用于发起这些调用。为 nil 时会用 Timeout 构造一个。
	HTTPClient *http.Client
}

// JobsClient is the Gateway's side of the control plane's Job persistence
// API. See the package doc comment for why it is not part of Verifier.
//
// JobsClient 是 Gateway 一侧的控制面 Job 持久化 API。为什么它不是 Verifier
// 的一部分，见本包的文档注释。
type JobsClient struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewJobsClient returns a JobsClient. It validates what a typo would
// otherwise turn into an outage discovered by the first job submission.
//
// NewJobsClient 返回一个 JobsClient。它校验那些一旦写错、就会变成「由第一次
// job 提交发现的故障」的东西。
func NewJobsClient(cfg JobsClientConfig) (*JobsClient, error) {
	endpoint := strings.TrimSuffix(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, errors.New("controlplaneclient: an endpoint is required")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, errors.New("controlplaneclient: the endpoint must include a scheme, e.g. http://127.0.0.1:8090")
	}
	if cfg.Token == "" {
		return nil, errors.New("controlplaneclient: a token is required; it must match the control plane's InternalToken")
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultJobsTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &JobsClient{endpoint: endpoint, token: cfg.Token, client: client}, nil
}

// Job is the control plane's persisted record of one run, as this client
// reads it back. Unlike any tenant-facing view, it carries the route
// binding (NodeID, RuntimeID, BackendRunID): this internal channel exists
// precisely so a restarted Gateway replica has somewhere to read that back
// from (STATUS.md's J06).
//
// Job 是控制面对一次运行的持久化记录，本客户端读回的就是这份记录。与任何面向
// 租户的视图不同，它携带路由绑定（NodeID、RuntimeID、BackendRunID）：这条内部
// 通道存在的意义正是让一个重启后的 Gateway 副本有地方把它们读回来（STATUS.md
// 的 J06）。
type Job struct {
	JobID           string
	TenantID        string
	WorkflowID      string
	WorkflowVersion string
	NodeID          string
	RuntimeID       string
	BackendRunID    string
	State           string
	ErrorSummary    string
	ObservedSeq     int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
	TerminalAt      *time.Time
}

// CreateJobRequest is what CreateJob reports about a run just submitted.
//
// CreateJobRequest 是 CreateJob 就一次刚提交的运行所报告的内容。
type CreateJobRequest struct {
	JobID           string
	TenantID        string
	WorkflowID      string
	WorkflowVersion string
	NodeID          string
	RuntimeID       string
	BackendRunID    string
	State           string
	ObservedSeq     int64
}

// CreateJob records a run's "已确认" fact — see the ControlPlane README's
// 「Job 持久化契约」. A duplicate JobID for the same tenant is not an error on
// the control plane side and is not one here either: this call returns the
// existing row.
//
// CreateJob 落实一次运行的「已确认」事实——见 ControlPlane README「Job 持久化
// 契约」。同一租户下重复的 JobID 在控制面那一侧不是错误，在这里也不是：这次调用
// 会返回已有的那一行。
func (c *JobsClient) CreateJob(ctx context.Context, req CreateJobRequest) (Job, error) {
	var wire struct {
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
	wire.JobID, wire.TenantID, wire.WorkflowID, wire.WorkflowVersion = req.JobID, req.TenantID, req.WorkflowID, req.WorkflowVersion
	wire.NodeID, wire.RuntimeID, wire.BackendRunID = req.NodeID, req.RuntimeID, req.BackendRunID
	wire.State, wire.ObservedSeq = req.State, req.ObservedSeq

	var job jobWire
	if err := c.call(ctx, http.MethodPost, "/internal/v1/jobs", wire, &job); err != nil {
		return Job{}, err
	}
	return job.toJob(), nil
}

// GetJob reads one job by id, scoped to tenantID.
//
// GetJob 按 id 读取一个 job，限定在 tenantID 范围内。
func (c *JobsClient) GetJob(ctx context.Context, tenantID, jobID string) (Job, error) {
	var job jobWire
	path := "/internal/v1/jobs/" + url.PathEscape(jobID) + "?tenant_id=" + url.QueryEscape(tenantID)
	if err := c.call(ctx, http.MethodGet, path, nil, &job); err != nil {
		return Job{}, err
	}
	return job.toJob(), nil
}

// JobStateUpdate is one status observation to report — from a foreground
// poll, an SSE event, or the background syncer. ObservedSeq must be strictly
// greater than what the control plane already has on record for the update
// to apply; see the ControlPlane README's 「Job 持久化契约」 for why that gate
// exists.
//
// JobStateUpdate 是要报告的一次状态观测——来自一次前台轮询、一次 SSE 事件，或
// 后台同步器。ObservedSeq 必须严格大于控制面已记录的值，这次更新才会生效；这道
// 门槛为何存在，见 ControlPlane README「Job 持久化契约」。
type JobStateUpdate struct {
	State        string
	ErrorSummary string
	ObservedSeq  int64
}

// UpdateJobState reports one status observation. It returns the job's
// current row regardless of whether Applied is true: a caller that lost a
// race to a more recent observation still needs to see what actually
// landed, per store.Jobs' UpdateJobState — Applied being false is an
// expected, silent outcome for a stale or duplicate report, or one that
// arrives after the run is already terminal, never something this method
// treats as a failure.
//
// UpdateJobState 报告一次状态观测。无论 Applied 是否为 true，它都会返回该 job
// 当前的行：一个在竞争中落败于更新观测的调用方，仍然需要看到究竟落地了什么，
// 对应 store.Jobs 的 UpdateJobState——Applied 为 false 对一次陈旧或重复的报告、
// 或一次运行已终态之后才抵达的报告而言，是预期内的无声结果，本方法从不将其当作
// 失败处理。
func (c *JobsClient) UpdateJobState(ctx context.Context, tenantID, jobID string, update JobStateUpdate) (applied bool, job Job, err error) {
	var wire struct {
		TenantID     string `json:"tenant_id"`
		State        string `json:"state"`
		ErrorSummary string `json:"error_summary,omitempty"`
		ObservedSeq  int64  `json:"observed_seq"`
	}
	wire.TenantID, wire.State, wire.ErrorSummary, wire.ObservedSeq = tenantID, update.State, update.ErrorSummary, update.ObservedSeq

	var resp struct {
		Applied bool    `json:"applied"`
		Job     jobWire `json:"job"`
	}
	path := "/internal/v1/jobs/" + url.PathEscape(jobID) + "/state"
	if err := c.call(ctx, http.MethodPatch, path, wire, &resp); err != nil {
		return false, Job{}, err
	}
	return resp.Applied, resp.Job.toJob(), nil
}

// ListActiveJobsForRoute returns every non-terminal job the control plane
// has on record bound to (nodeID, runtimeID), across every tenant. It exists
// for a Gateway replica recovering after a restart (STATUS.md's J06): the
// replica knows which nodes and runtimes are connected to it right now, not
// which tenants submitted the work running on them, so recovery must be
// able to ask "what do I owe this route binding" without a tenant to scope
// by — unlike every other method on this client, which is why this is the
// one call with no tenantID parameter.
//
// ListActiveJobsForRoute 返回控制面对 (nodeID, runtimeID) 记录在案的每一个
// 非终态 job，跨越所有租户。它的存在是为了让一个重启后正在恢复的 Gateway 副本
// （STATUS.md 的 J06）能够发问「我欠这个路由绑定什么」，而无需一个租户来限定
// 范围——因为该副本知道的是此刻连接到自己的是哪些节点与 runtime，而不是哪些
// 租户把工作提交到了它们身上。与本客户端其余每个方法不同，这也是唯一一个没有
// tenantID 参数的调用。
func (c *JobsClient) ListActiveJobsForRoute(ctx context.Context, nodeID, runtimeID string) ([]Job, error) {
	var resp struct {
		Items []jobWire `json:"items"`
	}
	path := "/internal/v1/jobs/active?node_id=" + url.QueryEscape(nodeID) + "&runtime_id=" + url.QueryEscape(runtimeID)
	if err := c.call(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Job, len(resp.Items))
	for i, j := range resp.Items {
		out[i] = j.toJob()
	}
	return out, nil
}

// JobArtifact is one recorded artifact.
//
// JobArtifact 是一条已记录的产物。
type JobArtifact struct {
	ArtifactID string
	JobID      string
	TenantID   string
	Filename   string
	Subfolder  string
	Type       string
	CreatedAt  time.Time
}

// CreateJobArtifactRequest is one artifact to record, with the public id the
// Gateway's own artifact listing already minted for it.
//
// CreateJobArtifactRequest 是要记录的一个产物，携带 Gateway 自己的产物列举已经
// 为它铸造的公开 id。
type CreateJobArtifactRequest struct {
	ArtifactID string
	TenantID   string
	Filename   string
	Subfolder  string
	Type       string
}

// CreateJobArtifact records one artifact a run produced. Like CreateJob, a
// duplicate id for the same job and tenant is not an error.
//
// CreateJobArtifact 记录一次运行产出的一个产物。与 CreateJob 一样，同一 job 与
// 租户下重复的 id 不是错误。
func (c *JobsClient) CreateJobArtifact(ctx context.Context, jobID string, req CreateJobArtifactRequest) (JobArtifact, error) {
	var wire struct {
		ArtifactID string `json:"artifact_id"`
		TenantID   string `json:"tenant_id"`
		Filename   string `json:"filename"`
		Subfolder  string `json:"subfolder,omitempty"`
		Type       string `json:"type,omitempty"`
	}
	wire.ArtifactID, wire.TenantID, wire.Filename, wire.Subfolder, wire.Type =
		req.ArtifactID, req.TenantID, req.Filename, req.Subfolder, req.Type

	var artifact jobArtifactWire
	path := "/internal/v1/jobs/" + url.PathEscape(jobID) + "/artifacts"
	if err := c.call(ctx, http.MethodPost, path, wire, &artifact); err != nil {
		return JobArtifact{}, err
	}
	return artifact.toArtifact(), nil
}

// ListJobArtifacts reads one job's recorded artifacts, scoped to tenantID.
//
// ListJobArtifacts 读取一个 job 已记录的产物，限定在 tenantID 范围内。
func (c *JobsClient) ListJobArtifacts(ctx context.Context, tenantID, jobID string) ([]JobArtifact, error) {
	var resp struct {
		Items []jobArtifactWire `json:"items"`
	}
	path := "/internal/v1/jobs/" + url.PathEscape(jobID) + "/artifacts?tenant_id=" + url.QueryEscape(tenantID)
	if err := c.call(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]JobArtifact, len(resp.Items))
	for i, a := range resp.Items {
		out[i] = a.toArtifact()
	}
	return out, nil
}

// jobWire is the internal API's job shape. It stays unexported: Job is what
// this package hands callers, and keeping the wire struct separate means a
// field rename on either side of the JSON boundary touches one conversion
// function instead of every call site.
//
// jobWire 是内部 API 的 job 形状。它保持未导出：Job 才是本包交给调用方的东西，
// 让两者分开意味着 JSON 边界任一侧的字段改名，只需改一个转换函数，而不是每个
// 调用点。
type jobWire struct {
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

func (j jobWire) toJob() Job {
	return Job{
		JobID: j.JobID, TenantID: j.TenantID, WorkflowID: j.WorkflowID, WorkflowVersion: j.WorkflowVersion,
		NodeID: j.NodeID, RuntimeID: j.RuntimeID, BackendRunID: j.BackendRunID,
		State: j.State, ErrorSummary: j.ErrorSummary, ObservedSeq: j.ObservedSeq,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, TerminalAt: j.TerminalAt,
	}
}

type jobArtifactWire struct {
	ArtifactID string    `json:"artifact_id"`
	JobID      string    `json:"job_id"`
	TenantID   string    `json:"tenant_id"`
	Filename   string    `json:"filename"`
	Subfolder  string    `json:"subfolder,omitempty"`
	Type       string    `json:"type,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func (a jobArtifactWire) toArtifact() JobArtifact {
	return JobArtifact{
		ArtifactID: a.ArtifactID, JobID: a.JobID, TenantID: a.TenantID,
		Filename: a.Filename, Subfolder: a.Subfolder, Type: a.Type, CreatedAt: a.CreatedAt,
	}
}

// call performs one internal API request and decodes its JSON response into
// out, which may be nil for a call whose body is not needed. It is the one
// place a status code becomes an error, so every method above maps errors
// identically.
//
// call 执行一次内部 API 请求，并把其 JSON 响应解码进 out；out 可以为 nil，供不
// 需要响应体的调用使用。它是状态码变成错误的唯一场所，因此上面每个方法的错误
// 映射都一致。
func (c *JobsClient) call(ctx context.Context, method, path string, body, out any) error {
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(req)
	if err != nil {
		// A transport failure — timeout, connection refused, DNS — is exactly
		// the persistence contract's "提交结果未知" window: this call may or
		// may not have reached the control plane.
		//
		// 一次传输失败——超时、连接被拒、DNS——正是持久化契约的「提交结果未知」
		// 窗口：这次调用可能已经、也可能没有抵达控制面。
		return fmt.Errorf("%w: reaching the control plane: %v", ErrOutcomeUnknown, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out == nil {
			return nil
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("%w: decoding the response: %v", ErrOutcomeUnknown, err)
		}
		return nil

	case http.StatusNotFound:
		return ErrNotFound

	case http.StatusConflict:
		return ErrConflict

	case http.StatusBadRequest:
		return ErrInvalidRequest

	case http.StatusUnauthorized, http.StatusForbidden:
		// This Gateway's own token was refused — a deployment fault, not a
		// property of the job being reported on, but the call's result is
		// still unresolved from the caller's point of view.
		//
		// 是本 Gateway 自己的 token 被拒绝了——这是部署故障，不是被报告的那个
		// job 本身的属性，但从调用方的角度看，这次调用的结果依然是未定的。
		return fmt.Errorf("%w: this Gateway's internal token was refused by the control plane", ErrOutcomeUnknown)

	default:
		return fmt.Errorf("%w: the control plane answered %d", ErrOutcomeUnknown, resp.StatusCode)
	}
}
