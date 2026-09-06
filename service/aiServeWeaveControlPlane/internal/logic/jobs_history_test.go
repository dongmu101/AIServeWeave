package logic_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

func TestListJobsScopesToTenantAndFiltersByState(t *testing.T) {
	f := newFixture(t)
	mustCreateJob(t, f, "job_1")
	mustCreateJob(t, f, "job_2")
	if _, _, err := f.svc.UpdateJobState(context.Background(), f.tenant.ID, "job_2", logic.UpdateJobStateParams{
		State: model.JobSucceeded, ObservedSeq: 1,
	}); err != nil {
		t.Fatalf("UpdateJobState: %v", err)
	}

	other, _, err := f.svc.CreateTenant(context.Background(), "Other", "owner3@example.com", testPassword, "10.0.0.1")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := f.svc.CreateJob(context.Background(), logic.CreateJobParams{
		JobID: "job_other", TenantID: other.ID, WorkflowID: "wf", State: model.JobPending,
	}); err != nil {
		t.Fatalf("CreateJob(other tenant): %v", err)
	}

	page, err := f.svc.ListJobs(context.Background(), f.tenant.ID, store.ListQuery{}, store.JobFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("ListJobs(no filter) returned %d jobs, want 2 (the other tenant's job must not appear)", len(page.Items))
	}

	page, err = f.svc.ListJobs(context.Background(), f.tenant.ID, store.ListQuery{}, store.JobFilter{State: model.JobSucceeded})
	if err != nil {
		t.Fatalf("ListJobs(state filter): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "job_2" {
		t.Fatalf("ListJobs(state=succeeded) = %v, want exactly [job_2]", page.Items)
	}
}

func TestListJobsRejectsAnUnknownStateFilter(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.ListJobs(context.Background(), f.tenant.ID, store.ListQuery{}, store.JobFilter{State: "not-a-real-state"})
	if !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("ListJobs(bogus state) = %v, want ErrInvalidInput — an unknown filter must not silently read as \"no jobs\"", err)
	}
}

func TestListJobsRejectsAnInvertedTimeWindow(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	_, err := f.svc.ListJobs(context.Background(), f.tenant.ID, store.ListQuery{}, store.JobFilter{
		Since: now, Until: now.Add(-time.Hour),
	})
	if !errors.Is(err, logic.ErrInvalidInput) {
		t.Errorf("ListJobs(inverted window) = %v, want ErrInvalidInput", err)
	}
}

func TestGetJobHistoryUsesTheSameTenantScopingAsTheInternalAPI(t *testing.T) {
	f := newFixture(t)
	mustCreateJob(t, f, "job_1")

	if _, err := f.svc.GetJob(context.Background(), f.tenant.ID, "job_1"); err != nil {
		t.Errorf("GetJob(owning tenant) = %v, want nil", err)
	}
	if _, err := f.svc.GetJob(context.Background(), "some-other-tenant", "job_1"); !errors.Is(err, logic.ErrNotFound) {
		t.Errorf("GetJob(other tenant) = %v, want ErrNotFound", err)
	}
}
