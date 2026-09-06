package gormstore

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// -----------------------------------------------------------------------
// Jobs
// -----------------------------------------------------------------------

// CreateJob inserts one job.
//
// CreateJob 插入一个 job。
func (s *Store) CreateJob(ctx context.Context, job *model.Job) error {
	return translate(s.db.WithContext(ctx).Create(job).Error)
}

// GetJob reads one job by id, scoped to its tenant.
//
// GetJob 按 id 读取一个 job，并限定在其租户范围内。
func (s *Store) GetJob(ctx context.Context, tenantID, id string) (model.Job, error) {
	var out model.Job
	err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ?", id, tenantID).Take(&out).Error
	return out, translate(err)
}

// ListJobs reads one tenant's jobs, newest first.
//
// ListJobs 读取某个租户的 job，最新的在前。
func (s *Store) ListJobs(ctx context.Context, tenantID string, query store.ListQuery, filter store.JobFilter) (store.Page[model.Job], error) {
	db := s.db.WithContext(ctx).Model(&model.Job{}).Where("tenant_id = ?", tenantID)
	if filter.State != "" {
		db = db.Where("state = ?", filter.State)
	}
	if filter.WorkflowID != "" {
		db = db.Where("workflow_id = ?", filter.WorkflowID)
	}
	if !filter.Since.IsZero() {
		db = db.Where("created_at >= ?", filter.Since)
	}
	if !filter.Until.IsZero() {
		db = db.Where("created_at < ?", filter.Until)
	}
	return readPage(db, query, func(j model.Job) (time.Time, string) { return j.CreatedAt, j.ID })
}

// UpdateJobState applies update to one job if update.ObservedSeq is strictly
// greater than the job's stored value and the job is not already terminal.
// Both conditions are expressed in the WHERE clause rather than checked
// after a read, so a concurrent background sync and a foreground poll racing
// on the same job cannot both believe they applied the newer state — the
// database's own row lock during the UPDATE settles it, not a
// read-then-write in this process.
//
// A RowsAffected of zero is ambiguous between "no such job" and "job exists
// but the update did not qualify", which store.Jobs requires this method to
// tell apart: a stale or duplicate update must be a silent, successful
// no-op, never an error indistinguishable from "not found". The follow-up
// existence check below resolves that ambiguity; it costs a second query
// only on the path that was already a no-op, never on the common case where
// the UPDATE itself succeeds.
//
// UpdateJobState 在 update.ObservedSeq 严格大于该 job 已存储的值、且该 job 尚未
// 处于终态时，将 update 应用于它。两个条件都表达在 WHERE 子句里，而不是先读后
// 检查，这样一次后台同步与一次前台轮询在同一个 job 上竞争时，不会都以为自己
// 应用了更新的状态——由这次 UPDATE 本身持有的行锁来裁定，而不是本进程里的一次
// 先读后写。
//
// RowsAffected 为零时，在「没有这个 job」与「job 存在但这次更新不满足条件」之间
// 是含糊的，而 store.Jobs 要求本方法把两者分开：一次陈旧或重复的更新必须是一次
// 无声的、成功的空操作，绝不能是一个与「未找到」无法区分的错误。下面的补充存在性
// 检查解开了这份含糊；它只在本就是空操作的那条路径上多付一次查询的代价，从不
// 出现在 UPDATE 本身成功的常见路径上。
func (s *Store) UpdateJobState(ctx context.Context, tenantID, id string, update store.JobStateUpdate) (bool, error) {
	values := map[string]any{
		"state":         update.State,
		"error_summary": update.ErrorSummary,
		"observed_seq":  update.ObservedSeq,
		"updated_at":    update.At,
	}
	terminal := update.State == model.JobSucceeded || update.State == model.JobFailed || update.State == model.JobCancelled
	if terminal {
		values["terminal_at"] = update.At
	}

	result := s.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ? AND tenant_id = ? AND observed_seq < ? AND state NOT IN (?)",
			id, tenantID, update.ObservedSeq, []string{model.JobSucceeded, model.JobFailed, model.JobCancelled}).
		Updates(values)
	if result.Error != nil {
		return false, translate(result.Error)
	}
	if result.RowsAffected > 0 {
		return true, nil
	}

	// The UPDATE matched nothing: find out why, without letting a stale
	// update masquerade as ErrNotFound.
	//
	// 这次 UPDATE 没有匹配到任何行：查清原因，不能让一次陈旧的更新冒充成
	// ErrNotFound。
	var exists int64
	if err := s.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ? AND tenant_id = ?", id, tenantID).
		Count(&exists).Error; err != nil {
		return false, translate(err)
	}
	if exists == 0 {
		return false, store.ErrNotFound
	}
	return false, nil
}

// -----------------------------------------------------------------------
// Job artifacts
// -----------------------------------------------------------------------

// CreateJobArtifact inserts one artifact record.
//
// CreateJobArtifact 插入一个产物记录。
func (s *Store) CreateJobArtifact(ctx context.Context, artifact *model.JobArtifact) error {
	return translate(s.db.WithContext(ctx).Create(artifact).Error)
}

// ListJobArtifacts reads one job's artifacts, scoped to its tenant.
//
// ListJobArtifacts 读取一个 job 的产物，并限定在其租户范围内。
func (s *Store) ListJobArtifacts(ctx context.Context, tenantID, jobID string) ([]model.JobArtifact, error) {
	var out []model.JobArtifact
	err := s.db.WithContext(ctx).
		Where("job_id = ? AND tenant_id = ?", jobID, tenantID).
		Order("created_at ASC, id ASC").
		Find(&out).Error
	return out, translate(err)
}
