package logic

import (
	"context"
	"errors"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateJobParams is what a Gateway replica reports about a run it just
// submitted. JobID is minted by that replica, not by this service — unlike
// CreateTenant or CreateUser, this layer never calls model.NewID for a job:
// the id already exists by the time this call happens, since it is what the
// caller who already answered the tenant's HTTP request is using to refer
// to the run.
//
// CreateJobParams 是一个 Gateway 副本就它刚提交的一次运行所报告的内容。JobID 由
// 那个副本铸造，不是本服务铸造——与 CreateTenant 或 CreateUser 不同，本层在这里
// 从不调用 model.NewID：这次调用发生时该 id 已经存在，因为它正是已经答复过租户
// HTTP 请求的调用方，用来指代这次运行的那个 id。
type CreateJobParams struct {
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

// CreateJob records a run's persistence contract's "已确认" fact: the
// control plane now has a durable row for it. A duplicate JobID — the
// persistence contract's "提交结果未知" window resolving into a second
// report of the same run — is not an error: the existing row is fetched and
// returned instead, so a Gateway retrying a create it could not confirm the
// result of gets back a successful, idempotent answer rather than a
// conflict it has to interpret.
//
// It performs no tenant-scoped authorization: the caller is the Gateway,
// authenticated by the internal shared secret at the handler layer, not a
// signed-in user acting within one tenant's session. TenantID here is what
// the Gateway asserts about its own caller, the same trust boundary
// VerifyKeyHash's caller already crosses on every inference request.
//
// CreateJob 落实持久化契约的「已确认」事实：控制面现在为它持有一行持久化记录。
// 重复的 JobID——持久化契约的「提交结果未知」窗口最终演变成对同一次运行的第二次
// 报告——不是错误：会改为读取并返回已有的那一行，这样一个不确定此前一次创建是否
// 成功而重试的 Gateway，得到的是一个成功的、幂等的答复，而不是一个还要自己去
// 解读的冲突。
//
// 它不做任何按租户的授权检查：调用方是 Gateway，在 handler 层已由内部共享密钥
// 认证，而不是在某个租户会话内行动的已登录用户。这里的 TenantID 是 Gateway 对
// 自己调用方所做的断言，与 VerifyKeyHash 的调用方在每一次推理请求上已经跨过的
// 是同一条信任边界。
func (s *Service) CreateJob(ctx context.Context, p CreateJobParams) (model.Job, error) {
	if p.JobID == "" || p.TenantID == "" || p.WorkflowID == "" {
		return model.Job{}, ErrInvalidInput
	}
	now := s.clock.Now()
	job := model.Job{
		ID:              p.JobID,
		TenantID:        p.TenantID,
		WorkflowID:      p.WorkflowID,
		WorkflowVersion: p.WorkflowVersion,
		NodeID:          p.NodeID,
		RuntimeID:       p.RuntimeID,
		BackendRunID:    p.BackendRunID,
		State:           p.State,
		ObservedSeq:     p.ObservedSeq,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	err := s.store.CreateJob(ctx, &job)
	if err == nil {
		return job, nil
	}
	if errors.Is(err, store.ErrConflict) {
		existing, getErr := s.store.GetJob(ctx, p.TenantID, p.JobID)
		if getErr != nil {
			// The conflict was real but the row is not visible under this
			// tenant: something else's job holds this id, which the caller
			// must learn about as a genuine conflict, not an idempotent
			// success it is not entitled to.
			//
			// 冲突是真实的，但这一行在该租户下不可见：这个 id 被别的东西的
			// job 占用了，调用方必须得知这是一次真实的冲突，而不是一次它
			// 无权获得的幂等成功。
			return model.Job{}, ErrConflict
		}
		return existing, nil
	}
	return model.Job{}, translate(err)
}

// GetJob reads one job, scoped to the tenant the Gateway asserts.
//
// GetJob 读取一个 job，限定在 Gateway 所断言的租户范围内。
func (s *Service) GetJob(ctx context.Context, tenantID, jobID string) (model.Job, error) {
	job, err := s.store.GetJob(ctx, tenantID, jobID)
	return job, translate(err)
}

// UpdateJobStateParams is one status observation a Gateway replica reports,
// from a foreground poll, an SSE event, or the background syncer — this
// layer does not distinguish which, because store.UpdateJobState's
// ObservedSeq gate already makes all three safe to call from concurrently
// without coordination.
//
// UpdateJobStateParams 是一个 Gateway 副本报告的一次状态观测，来自一次前台轮询、
// 一次 SSE 事件，或后台同步器——本层不区分是哪一种，因为 store.UpdateJobState
// 的 ObservedSeq 门槛已经让三者可以互不协调地并发调用而不出问题。
type UpdateJobStateParams struct {
	State        string
	ErrorSummary string
	ObservedSeq  int64
}

// UpdateJobState applies one status observation and returns the job's
// current row regardless of whether this call's update was the one that
// applied — a caller that lost a race to a more recent update still needs
// to see what actually landed. applied reports which happened, matching
// store.Jobs' UpdateJobState: false means either a stale/duplicate update
// (a silent, expected no-op) or the job was already terminal, never an
// error.
//
// UpdateJobState 应用一次状态观测，并返回该 job 当前的行，无论这次调用的更新是否
// 是真正生效的那一个——一个在竞争中落败于更新观测的调用方，仍然需要看到究竟落地了
// 什么。applied 报告发生了哪一种情况，与 store.Jobs 的 UpdateJobState 一致：
// false 意味着一次陈旧/重复的更新（一次无声的、预期内的空操作）或该 job 已处于
// 终态，而不是错误。
func (s *Service) UpdateJobState(ctx context.Context, tenantID, jobID string, p UpdateJobStateParams) (applied bool, job model.Job, err error) {
	if p.State == "" {
		return false, model.Job{}, ErrInvalidInput
	}
	applied, err = s.store.UpdateJobState(ctx, tenantID, jobID, store.JobStateUpdate{
		State:        p.State,
		ErrorSummary: p.ErrorSummary,
		ObservedSeq:  p.ObservedSeq,
		At:           s.clock.Now(),
	})
	if err != nil {
		return false, model.Job{}, translate(err)
	}
	job, err = s.store.GetJob(ctx, tenantID, jobID)
	if err != nil {
		return false, model.Job{}, translate(err)
	}
	return applied, job, nil
}

// CreateJobArtifactParams is one artifact a run produced, as the Gateway's
// artifact listing minted a public id for it.
//
// CreateJobArtifactParams 是一次运行产出的一个产物，其公开 id 由 Gateway 的产物
// 列举铸造。
type CreateJobArtifactParams struct {
	ArtifactID string
	JobID      string
	TenantID   string
	Filename   string
	Subfolder  string
	Type       string
}

// CreateJobArtifact records one artifact. Like CreateJob, a duplicate id is
// treated as an idempotent success rather than an error: the caller already
// has the id and the fields it would send again describe the same output,
// so there is nothing to reconcile — unlike CreateJob, the existing row is
// not re-fetched, since a caller retrying an artifact record already knows
// what it submitted.
//
// CreateJobArtifact 记录一个产物。与 CreateJob 一样，重复的 id 被当作幂等成功
// 而不是错误处理：调用方已经持有这个 id，且它会再次发送的字段描述的是同一份
// 输出，因此没有什么需要协调——与 CreateJob 不同，这里不会重新读取已有的行，
// 因为重试一次产物记录的调用方本就知道自己提交过什么。
func (s *Service) CreateJobArtifact(ctx context.Context, p CreateJobArtifactParams) (model.JobArtifact, error) {
	if p.ArtifactID == "" || p.JobID == "" || p.TenantID == "" || p.Filename == "" {
		return model.JobArtifact{}, ErrInvalidInput
	}
	artifact := model.JobArtifact{
		ID:        p.ArtifactID,
		JobID:     p.JobID,
		TenantID:  p.TenantID,
		Filename:  p.Filename,
		Subfolder: p.Subfolder,
		Type:      p.Type,
		CreatedAt: s.clock.Now(),
	}
	err := s.store.CreateJobArtifact(ctx, &artifact)
	if err != nil && !errors.Is(err, store.ErrConflict) {
		return model.JobArtifact{}, translate(err)
	}
	return artifact, nil
}

// ListJobArtifacts returns one job's artifacts, scoped to the tenant the
// Gateway asserts.
//
// ListJobArtifacts 返回一个 job 的产物，限定在 Gateway 所断言的租户范围内。
func (s *Service) ListJobArtifacts(ctx context.Context, tenantID, jobID string) ([]model.JobArtifact, error) {
	artifacts, err := s.store.ListJobArtifacts(ctx, tenantID, jobID)
	return artifacts, translate(err)
}
