package logic_test

import (
	"context"
	"errors"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

func mustCreateJob(t *testing.T, f *fixture, jobID string) model.Job {
	t.Helper()
	job, err := f.svc.CreateJob(context.Background(), logic.CreateJobParams{
		JobID:        jobID,
		TenantID:     f.tenant.ID,
		WorkflowID:   "text-to-image",
		NodeID:       "node-1",
		RuntimeID:    "comfy-1",
		BackendRunID: "prompt-1",
		State:        model.JobPending,
	})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return job
}

func TestCreateJobIsIdempotentOnADuplicateID(t *testing.T) {
	f := newFixture(t)
	first := mustCreateJob(t, f, "job_1")

	// A retried create — the persistence contract's "提交结果未知" window
	// resolving into a second report of the same run — must come back as a
	// successful read of the existing row, not a conflict the Gateway has to
	// interpret.
	//
	// 一次重试的创建——持久化契约的「提交结果未知」窗口最终演变成对同一次运行的
	// 第二次报告——必须以对既有行的一次成功读取收场，而不是一个还要 Gateway 自己
	// 去解读的冲突。
	second, err := f.svc.CreateJob(context.Background(), logic.CreateJobParams{
		JobID:        "job_1",
		TenantID:     f.tenant.ID,
		WorkflowID:   "text-to-image",
		NodeID:       "node-1",
		RuntimeID:    "comfy-1",
		BackendRunID: "prompt-1",
		State:        model.JobPending,
	})
	if err != nil {
		t.Fatalf("CreateJob (duplicate) = %v, want a nil error (idempotent)", err)
	}
	if second.ID != first.ID || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("CreateJob (duplicate) = %+v, want the existing row %+v", second, first)
	}
}

func TestCreateJobDuplicateIDInAnotherTenantIsAConflict(t *testing.T) {
	f := newFixture(t)
	mustCreateJob(t, f, "job_1")

	otherTenant, _, err := f.svc.CreateTenant(context.Background(), "Other", "owner2@example.com", testPassword, "10.0.0.1")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	_, err = f.svc.CreateJob(context.Background(), logic.CreateJobParams{
		JobID:      "job_1",
		TenantID:   otherTenant.ID,
		WorkflowID: "text-to-image",
		State:      model.JobPending,
	})
	if !errors.Is(err, logic.ErrConflict) {
		t.Fatalf("CreateJob(same id, other tenant) = %v, want ErrConflict — the id is genuinely taken by a job this tenant may not read", err)
	}
}

func TestGetJobIsScopedToItsTenant(t *testing.T) {
	f := newFixture(t)
	mustCreateJob(t, f, "job_1")

	if _, err := f.svc.GetJob(context.Background(), f.tenant.ID, "job_1"); err != nil {
		t.Errorf("GetJob(owning tenant) = %v, want nil", err)
	}
	if _, err := f.svc.GetJob(context.Background(), "tenant-other", "job_1"); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("GetJob(other tenant) = %v, want ErrNotFound", err)
	}
}

func TestUpdateJobStateReturnsTheCurrentRowEvenWhenItDidNotApply(t *testing.T) {
	f := newFixture(t)
	mustCreateJob(t, f, "job_1")

	applied, job, err := f.svc.UpdateJobState(context.Background(), f.tenant.ID, "job_1", logic.UpdateJobStateParams{
		State: model.JobSucceeded, ObservedSeq: 1,
	})
	if err != nil || !applied || job.State != model.JobSucceeded {
		t.Fatalf("first update: applied=%v state=%v err=%v, want true, %v, nil", applied, job.State, err, model.JobSucceeded)
	}

	// A stale update after the run is already terminal: applied is false,
	// there is no error, and the returned row is the terminal one, not a
	// zero value — the caller lost the race but still needs the truth.
	//
	// 一次运行早已终态之后的陈旧更新：applied 为 false，没有错误，返回的行是
	// 终态的那一行，而不是零值——调用方在竞争中落败，但仍然需要知道真相。
	applied, job, err = f.svc.UpdateJobState(context.Background(), f.tenant.ID, "job_1", logic.UpdateJobStateParams{
		State: model.JobFailed, ObservedSeq: 2,
	})
	if err != nil {
		t.Fatalf("post-terminal update returned an error: %v, want nil (a silent no-op)", err)
	}
	if applied {
		t.Error("post-terminal update reported applied=true, want false")
	}
	if job.State != model.JobSucceeded {
		t.Errorf("post-terminal update's returned job.State = %v, want unchanged %v", job.State, model.JobSucceeded)
	}
}

func TestUpdateJobStateOnAMissingJobIsErrNotFound(t *testing.T) {
	f := newFixture(t)
	_, _, err := f.svc.UpdateJobState(context.Background(), f.tenant.ID, "job_missing", logic.UpdateJobStateParams{
		State: model.JobRunning, ObservedSeq: 1,
	})
	if !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestCreateJobArtifactIsIdempotentOnADuplicateID(t *testing.T) {
	f := newFixture(t)
	mustCreateJob(t, f, "job_1")

	params := logic.CreateJobArtifactParams{
		ArtifactID: "art_1", JobID: "job_1", TenantID: f.tenant.ID, Filename: "out.png",
	}
	first, err := f.svc.CreateJobArtifact(context.Background(), params)
	if err != nil {
		t.Fatalf("first CreateJobArtifact: %v", err)
	}
	second, err := f.svc.CreateJobArtifact(context.Background(), params)
	if err != nil {
		t.Fatalf("CreateJobArtifact (duplicate) = %v, want a nil error (idempotent)", err)
	}
	if second.ID != first.ID {
		t.Errorf("CreateJobArtifact (duplicate).ID = %q, want %q", second.ID, first.ID)
	}

	artifacts, err := f.svc.ListJobArtifacts(context.Background(), f.tenant.ID, "job_1")
	if err != nil {
		t.Fatalf("ListJobArtifacts: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("ListJobArtifacts returned %d rows, want 1 — a retried create must not add a second row for the same output", len(artifacts))
	}
}
