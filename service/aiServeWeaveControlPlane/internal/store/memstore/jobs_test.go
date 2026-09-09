package memstore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

func newTestJob(id, tenantID string) *model.Job {
	return &model.Job{
		ID:           id,
		TenantID:     tenantID,
		WorkflowID:   "text-to-image",
		NodeID:       "node-1",
		RuntimeID:    "comfy-1",
		BackendRunID: "prompt-1",
		State:        model.JobPending,
	}
}

func TestCreateJobRejectsADuplicateIDAsConflict(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()

	if err := s.CreateJob(ctx, newTestJob("job_1", "tenant-a")); err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}
	err := s.CreateJob(ctx, newTestJob("job_1", "tenant-a"))
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second CreateJob (duplicate id) = %v, want ErrConflict — the persistence contract treats this as the resubmission window, not a hard failure", err)
	}
}

func TestGetJobIsScopedToItsTenant(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	if err := s.CreateJob(ctx, newTestJob("job_1", "tenant-a")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if _, err := s.GetJob(ctx, "tenant-a", "job_1"); err != nil {
		t.Errorf("GetJob(owning tenant) = %v, want nil", err)
	}
	if _, err := s.GetJob(ctx, "tenant-b", "job_1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetJob(other tenant) = %v, want ErrNotFound — a job belonging to another tenant must read exactly like one that does not exist", err)
	}
}

func TestUpdateJobStateAppliesOnlyWhenTheSequenceAdvances(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	if err := s.CreateJob(ctx, newTestJob("job_1", "tenant-a")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	at1 := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	applied, err := s.UpdateJobState(ctx, "tenant-a", "job_1", store.JobStateUpdate{
		State: model.JobRunning, ObservedSeq: 1, At: at1,
	})
	if err != nil || !applied {
		t.Fatalf("first update: applied=%v err=%v, want true, nil", applied, err)
	}

	// A duplicate or reordered event carrying the same or an older sequence
	// number must be a silent no-op, not an error — this is what makes the
	// background syncer's repeated status calls idempotent.
	//
	// 携带相同或更旧序号的重复或乱序事件，必须是一次无声的空操作，而不是错误——
	// 这正是让后台同步器反复的状态调用保持幂等的原因。
	stale := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)
	applied, err = s.UpdateJobState(ctx, "tenant-a", "job_1", store.JobStateUpdate{
		State: model.JobFailed, ObservedSeq: 1, At: stale,
	})
	if err != nil || applied {
		t.Fatalf("stale-sequence update: applied=%v err=%v, want false, nil", applied, err)
	}
	job, err := s.GetJob(ctx, "tenant-a", "job_1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != model.JobRunning || !job.UpdatedAt.Equal(at1) {
		t.Errorf("job after the stale update = {State: %v, UpdatedAt: %v}, want unchanged from the first update", job.State, job.UpdatedAt)
	}

	at3 := time.Date(2026, 1, 1, 0, 0, 3, 0, time.UTC)
	applied, err = s.UpdateJobState(ctx, "tenant-a", "job_1", store.JobStateUpdate{
		State: model.JobSucceeded, ObservedSeq: 2, At: at3,
	})
	if err != nil || !applied {
		t.Fatalf("second (higher-sequence) update: applied=%v err=%v, want true, nil", applied, err)
	}
	job, err = s.GetJob(ctx, "tenant-a", "job_1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != model.JobSucceeded {
		t.Fatalf("job.State = %v, want %v", job.State, model.JobSucceeded)
	}
	if job.TerminalAt == nil || !job.TerminalAt.Equal(at3) {
		t.Errorf("job.TerminalAt = %v, want %v", job.TerminalAt, at3)
	}
}

func TestUpdateJobStateOnATerminalJobIsANoOp(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	if err := s.CreateJob(ctx, newTestJob("job_1", "tenant-a")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	at1 := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	if _, err := s.UpdateJobState(ctx, "tenant-a", "job_1", store.JobStateUpdate{
		State: model.JobSucceeded, ObservedSeq: 1, At: at1,
	}); err != nil {
		t.Fatalf("reaching a terminal state: %v", err)
	}

	// A higher sequence number arriving after the run is already terminal —
	// a duplicate terminal event delivered twice, say — must not move it. The
	// first result to land wins, regardless of arrival order.
	//
	// 一个在运行早已终态之后才抵达的更高序号——比如一次终态事件被投递了两次——
	// 不得移动它。先落地的结果获胜，与到达顺序无关。
	at2 := time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC)
	applied, err := s.UpdateJobState(ctx, "tenant-a", "job_1", store.JobStateUpdate{
		State: model.JobFailed, ObservedSeq: 99, At: at2,
	})
	if err != nil || applied {
		t.Fatalf("update after terminal: applied=%v err=%v, want false, nil", applied, err)
	}
	job, err := s.GetJob(ctx, "tenant-a", "job_1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != model.JobSucceeded || !job.TerminalAt.Equal(at1) {
		t.Errorf("job after the post-terminal update = {State: %v, TerminalAt: %v}, want unchanged (Succeeded at %v)", job.State, job.TerminalAt, at1)
	}
}

func TestUpdateJobStateOnAMissingJobIsErrNotFound(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	if err := s.CreateJob(ctx, newTestJob("job_1", "tenant-a")); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	tests := []struct {
		name     string
		tenantID string
		jobID    string
	}{
		{"unknown job id", "tenant-a", "job_missing"},
		{"correct job, wrong tenant", "tenant-b", "job_1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.UpdateJobState(ctx, tt.tenantID, tt.jobID, store.JobStateUpdate{
				State: model.JobRunning, ObservedSeq: 1, At: time.Now(),
			})
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("err = %v, want ErrNotFound — this must stay distinguishable from a stale-sequence no-op, which returns nil", err)
			}
		})
	}
}

func TestListJobsFiltersByStateAndScopesToTenant(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	for _, j := range []*model.Job{
		newTestJob("job_1", "tenant-a"),
		newTestJob("job_2", "tenant-a"),
		newTestJob("job_3", "tenant-b"),
	} {
		if err := s.CreateJob(ctx, j); err != nil {
			t.Fatalf("CreateJob(%s): %v", j.ID, err)
		}
	}
	if _, err := s.UpdateJobState(ctx, "tenant-a", "job_2", store.JobStateUpdate{
		State: model.JobSucceeded, ObservedSeq: 1, At: time.Now(),
	}); err != nil {
		t.Fatalf("UpdateJobState: %v", err)
	}

	page, err := s.ListJobs(ctx, "tenant-a", store.ListQuery{}, store.JobFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("ListJobs(tenant-a, no filter) returned %d jobs, want 2 (tenant-b's job must not appear)", len(page.Items))
	}

	page, err = s.ListJobs(ctx, "tenant-a", store.ListQuery{}, store.JobFilter{State: model.JobSucceeded})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "job_2" {
		t.Fatalf("ListJobs(tenant-a, state=succeeded) = %v, want exactly [job_2]", page.Items)
	}
}

func TestCreateJobArtifactRejectsADuplicateIDAsConflict(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	artifact := &model.JobArtifact{ID: "art_1", JobID: "job_1", TenantID: "tenant-a", Filename: "out.png"}
	if err := s.CreateJobArtifact(ctx, artifact); err != nil {
		t.Fatalf("first CreateJobArtifact: %v", err)
	}
	err := s.CreateJobArtifact(ctx, artifact)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second CreateJobArtifact (duplicate id) = %v, want ErrConflict — re-listing after a retry must not create a second row for the same output", err)
	}
}

func TestListJobArtifactsIsScopedToTenantAndJob(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	for _, a := range []*model.JobArtifact{
		{ID: "art_1", JobID: "job_1", TenantID: "tenant-a", Filename: "a.png"},
		{ID: "art_2", JobID: "job_1", TenantID: "tenant-a", Filename: "b.png"},
		{ID: "art_3", JobID: "job_2", TenantID: "tenant-a", Filename: "c.png"},
		{ID: "art_4", JobID: "job_1", TenantID: "tenant-b", Filename: "d.png"},
	} {
		if err := s.CreateJobArtifact(ctx, a); err != nil {
			t.Fatalf("CreateJobArtifact(%s): %v", a.ID, err)
		}
	}

	got, err := s.ListJobArtifacts(ctx, "tenant-a", "job_1")
	if err != nil {
		t.Fatalf("ListJobArtifacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListJobArtifacts(tenant-a, job_1) returned %d artifacts, want 2", len(got))
	}
	for _, a := range got {
		if a.JobID != "job_1" || a.TenantID != "tenant-a" {
			t.Errorf("ListJobArtifacts returned artifact %+v outside its scope", a)
		}
	}
}

func TestListJobArtifactsBeforeFiltersByTypeAgeAndAcrossTenants(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	now := time.Now()
	for _, a := range []*model.JobArtifact{
		{ID: "art_old_output_a", TenantID: "tenant-a", Type: "output", CreatedAt: now.Add(-48 * time.Hour)},
		{ID: "art_old_output_b", TenantID: "tenant-b", Type: "output", CreatedAt: now.Add(-72 * time.Hour)},
		{ID: "art_new_output", TenantID: "tenant-a", Type: "output", CreatedAt: now.Add(-time.Hour)},
		{ID: "art_old_temp", TenantID: "tenant-a", Type: "temp", CreatedAt: now.Add(-48 * time.Hour)},
	} {
		if err := s.CreateJobArtifact(ctx, a); err != nil {
			t.Fatalf("CreateJobArtifact(%s): %v", a.ID, err)
		}
	}

	got, err := s.ListJobArtifactsBefore(ctx, "output", now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("ListJobArtifactsBefore: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListJobArtifactsBefore(output, -24h) returned %d, want 2 (both old outputs, across tenants)", len(got))
	}
	ids := map[string]bool{}
	for _, a := range got {
		ids[a.ID] = true
	}
	if !ids["art_old_output_a"] || !ids["art_old_output_b"] {
		t.Errorf("ListJobArtifactsBefore = %v, want both old output artifacts regardless of tenant", got)
	}
	if ids["art_new_output"] {
		t.Error("ListJobArtifactsBefore returned an artifact newer than the cutoff")
	}
	if ids["art_old_temp"] {
		t.Error("ListJobArtifactsBefore returned an artifact of a different type")
	}
}

func TestListJobArtifactsBeforeOrdersOldestFirstAndCapsAtTheLimit(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	now := time.Now()
	const total = store.MaxExpiredJobArtifacts + 5
	for i := range total {
		a := &model.JobArtifact{
			ID:        fmt.Sprintf("art_%03d", i),
			TenantID:  "tenant-a",
			Type:      "output",
			CreatedAt: now.Add(-time.Duration(total-i) * time.Minute), // ascending: art_000 is oldest
		}
		if err := s.CreateJobArtifact(ctx, a); err != nil {
			t.Fatalf("CreateJobArtifact(%s): %v", a.ID, err)
		}
	}

	got, err := s.ListJobArtifactsBefore(ctx, "output", now)
	if err != nil {
		t.Fatalf("ListJobArtifactsBefore: %v", err)
	}
	if len(got) != store.MaxExpiredJobArtifacts {
		t.Fatalf("ListJobArtifactsBefore returned %d, want exactly the cap %d", len(got), store.MaxExpiredJobArtifacts)
	}
	if got[0].ID != "art_000" {
		t.Errorf("first result = %q, want the oldest artifact art_000", got[0].ID)
	}
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt.Before(got[i-1].CreatedAt) {
			t.Fatalf("result %d (%s, %s) is older than result %d (%s, %s), want oldest-first order",
				i, got[i].ID, got[i].CreatedAt, i-1, got[i-1].ID, got[i-1].CreatedAt)
		}
	}
}

func TestDeleteJobArtifactRemovesTheRowAndIsIdempotent(t *testing.T) {
	s := memstore.New()
	ctx := context.Background()
	if err := s.CreateJobArtifact(ctx, &model.JobArtifact{ID: "art_1", JobID: "job_1", TenantID: "tenant-a"}); err != nil {
		t.Fatalf("CreateJobArtifact: %v", err)
	}

	if err := s.DeleteJobArtifact(ctx, "art_1"); err != nil {
		t.Fatalf("first DeleteJobArtifact: %v", err)
	}
	got, err := s.ListJobArtifacts(ctx, "tenant-a", "job_1")
	if err != nil {
		t.Fatalf("ListJobArtifacts: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListJobArtifacts after delete = %v, want none", got)
	}

	// A missing id is not an error — the cleanup sweep that calls this only
	// wants "this row is gone", and it already is.
	//
	// 不存在的 id 不算错误——调用它的清理扫描想要的只是「这一行不在了」，
	// 而它本就已经不在了。
	if err := s.DeleteJobArtifact(ctx, "art_1"); err != nil {
		t.Fatalf("second DeleteJobArtifact (already gone): %v, want nil", err)
	}
	if err := s.DeleteJobArtifact(ctx, "art_never_existed"); err != nil {
		t.Fatalf("DeleteJobArtifact on an id that never existed: %v, want nil", err)
	}
}
