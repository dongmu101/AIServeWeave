package memstore

import (
	"context"
	"sort"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ownedFlow reads a flow of owner. Callers hold s.mu.
// ownedFlow 读取 owner 的一个编排。调用方持有 s.mu。
func (s *Store) ownedFlow(owner store.StudioOwner, id string) (model.StudioFlow, bool) {
	f, ok := s.studioFlows[id]
	if !ok || f.TenantID != owner.TenantID || f.UserID != owner.UserID {
		return model.StudioFlow{}, false
	}
	return f, true
}

// ownedRun reads a run of owner. Callers hold s.mu.
// ownedRun 读取 owner 的一次运行。调用方持有 s.mu。
func (s *Store) ownedRun(owner store.StudioOwner, id string) (model.StudioFlowRun, bool) {
	r, ok := s.studioRuns[id]
	if !ok || r.TenantID != owner.TenantID || r.UserID != owner.UserID {
		return model.StudioFlowRun{}, false
	}
	return r, true
}

// latestVersion returns a flow's newest version. Callers hold s.mu.
func (s *Store) latestVersion(id string) model.StudioFlowVersion {
	versions := s.studioFlowVersions[id]
	return versions[len(versions)-1]
}

// CreateStudioFlow implements store.StudioFlows.
func (s *Store) CreateStudioFlow(_ context.Context, f *model.StudioFlow, v *model.StudioFlowVersion, maxPerUser int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.studioFlows[f.ID]; exists {
		return store.ErrConflict
	}
	count := 0
	for _, existing := range s.studioFlows {
		if existing.TenantID == f.TenantID && existing.UserID == f.UserID {
			count++
		}
	}
	if maxPerUser > 0 && count >= maxPerUser {
		return store.ErrStudioLimit
	}
	s.studioFlows[f.ID] = *f
	s.studioFlowVersions[f.ID] = []model.StudioFlowVersion{*v}
	return nil
}

// GetStudioFlow implements store.StudioFlows.
func (s *Store) GetStudioFlow(_ context.Context, owner store.StudioOwner, id string) (model.StudioFlow, model.StudioFlowVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.ownedFlow(owner, id)
	if !ok {
		return model.StudioFlow{}, model.StudioFlowVersion{}, store.ErrNotFound
	}
	return f, s.latestVersion(id), nil
}

// GetStudioFlowVersion implements store.StudioFlows.
func (s *Store) GetStudioFlowVersion(_ context.Context, owner store.StudioOwner, id string, version int64) (model.StudioFlowVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ownedFlow(owner, id); !ok {
		return model.StudioFlowVersion{}, store.ErrNotFound
	}
	for _, v := range s.studioFlowVersions[id] {
		if v.Version == version {
			return v, nil
		}
	}
	return model.StudioFlowVersion{}, store.ErrNotFound
}

// ListStudioFlows implements store.StudioFlows.
func (s *Store) ListStudioFlows(_ context.Context, filter store.StudioFlowFilter) ([]model.StudioFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.StudioFlow{}
	for _, f := range s.studioFlows {
		if f.TenantID != filter.Owner.TenantID || f.UserID != filter.Owner.UserID || !beforeCursor(f.UpdatedAt, f.ID, filter.Before) {
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return newerFirst(out[i].UpdatedAt, out[i].ID, out[j].UpdatedAt, out[j].ID) })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// beforeCursor reports whether (at, id) comes after the cursor in a
// newest-first list; a nil cursor admits everything.
func beforeCursor(at time.Time, id string, c *store.StudioCursor) bool {
	return c == nil || at.Before(c.At) || (at.Equal(c.At) && id < c.ID)
}

func newerFirst(at1 time.Time, id1 string, at2 time.Time, id2 string) bool {
	if !at1.Equal(at2) {
		return at1.After(at2)
	}
	return id1 > id2
}

// UpdateStudioFlow implements store.StudioFlows.
func (s *Store) UpdateStudioFlow(_ context.Context, owner store.StudioOwner, id string, update store.StudioFlowUpdate) (model.StudioFlow, model.StudioFlowVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.ownedFlow(owner, id)
	if !ok {
		return model.StudioFlow{}, model.StudioFlowVersion{}, store.ErrNotFound
	}
	if update.BaseVersion > 0 && update.BaseVersion != f.Version {
		return model.StudioFlow{}, model.StudioFlowVersion{}, store.ErrStudioFlowChanged
	}
	latest := s.latestVersion(id)
	changed := false
	if update.Name != nil && *update.Name != f.Name {
		f.Name = *update.Name
		changed = true
	}
	if update.Definition != nil && *update.Definition != latest.Definition {
		if update.MaxVersions > 0 && len(s.studioFlowVersions[id]) >= update.MaxVersions {
			return model.StudioFlow{}, model.StudioFlowVersion{}, store.ErrStudioLimit
		}
		f.Version++
		latest = model.StudioFlowVersion{FlowID: id, Version: f.Version, Definition: *update.Definition, CreatedAt: update.At}
		s.studioFlowVersions[id] = append(s.studioFlowVersions[id], latest)
		changed = true
	}
	if changed {
		f.UpdatedAt = update.At
		s.studioFlows[id] = f
	}
	return f, latest, nil
}

// DeleteStudioFlow implements store.StudioFlows.
func (s *Store) DeleteStudioFlow(_ context.Context, owner store.StudioOwner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ownedFlow(owner, id); !ok {
		return store.ErrNotFound
	}
	for runID, r := range s.studioRuns {
		if r.FlowID == id {
			delete(s.studioRuns, runID)
			delete(s.studioRunSteps, runID)
		}
	}
	delete(s.studioFlowVersions, id)
	delete(s.studioFlows, id)
	return nil
}

// StartStudioRun implements store.StudioFlows.
func (s *Store) StartStudioRun(_ context.Context, owner store.StudioOwner, run *model.StudioFlowRun, limits store.StudioRunLimits, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.studioRuns[run.ID]; ok {
		if existing.TenantID != owner.TenantID || existing.UserID != owner.UserID {
			return store.ErrConflict
		}
		return nil
	}
	f, ok := s.ownedFlow(owner, run.FlowID)
	if !ok || run.FlowVersion < 1 || run.FlowVersion > f.Version {
		return store.ErrNotFound
	}
	user, tenant := 0, 0
	for _, r := range s.studioRuns {
		if r.TenantID != owner.TenantID || r.Status != model.StudioRunRunning || store.StudioRunExpired(r, now) {
			continue
		}
		tenant++
		if r.UserID == owner.UserID {
			user++
		}
	}
	if (limits.PerUser > 0 && user >= limits.PerUser) || (limits.PerTenant > 0 && tenant >= limits.PerTenant) {
		return store.ErrStudioRunLimit
	}
	s.studioRuns[run.ID] = *run
	return nil
}

// GetStudioRun implements store.StudioFlows.
func (s *Store) GetStudioRun(_ context.Context, owner store.StudioOwner, id string) (model.StudioFlowRun, []model.StudioFlowRunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.ownedRun(owner, id)
	if !ok {
		return model.StudioFlowRun{}, nil, store.ErrNotFound
	}
	steps := append([]model.StudioFlowRunStep{}, s.studioRunSteps[id]...)
	sort.SliceStable(steps, func(i, j int) bool {
		if !steps[i].FinishedAt.Equal(steps[j].FinishedAt) {
			return steps[i].FinishedAt.Before(steps[j].FinishedAt)
		}
		return steps[i].NodeID < steps[j].NodeID
	})
	return r, steps, nil
}

// ListStudioRuns implements store.StudioFlows.
func (s *Store) ListStudioRuns(_ context.Context, filter store.StudioRunFilter) ([]model.StudioFlowRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ownedFlow(filter.Owner, filter.FlowID); !ok {
		return nil, store.ErrNotFound
	}
	out := []model.StudioFlowRun{}
	for _, r := range s.studioRuns {
		if r.FlowID == filter.FlowID && beforeCursor(r.CreatedAt, r.ID, filter.Before) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return newerFirst(out[i].CreatedAt, out[i].ID, out[j].CreatedAt, out[j].ID) })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// heldRun reads a run replica may write to. Callers hold s.mu.
func (s *Store) heldRun(owner store.StudioOwner, id, replica string, now time.Time) (model.StudioFlowRun, error) {
	r, ok := s.ownedRun(owner, id)
	if !ok {
		return model.StudioFlowRun{}, store.ErrNotFound
	}
	return r, store.StudioRunHeld(r, replica, now)
}

// RenewStudioRun implements store.StudioFlows.
func (s *Store) RenewStudioRun(_ context.Context, owner store.StudioOwner, id, replica string, until, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.heldRun(owner, id, replica, now)
	if err != nil {
		return err
	}
	r.LeaseUntil = until
	s.studioRuns[id] = r
	return nil
}

// RecordStudioRunStep implements store.StudioFlows.
func (s *Store) RecordStudioRunStep(_ context.Context, owner store.StudioOwner, step *model.StudioFlowRunStep, replica string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ownedRun(owner, step.RunID); !ok {
		return store.ErrNotFound
	}
	for _, existing := range s.studioRunSteps[step.RunID] {
		if existing.NodeID == step.NodeID {
			return nil
		}
	}
	if _, err := s.heldRun(owner, step.RunID, replica, now); err != nil {
		return err
	}
	s.studioRunSteps[step.RunID] = append(s.studioRunSteps[step.RunID], *step)
	return nil
}

// FinishStudioRun implements store.StudioFlows.
func (s *Store) FinishStudioRun(_ context.Context, owner store.StudioOwner, id, replica string, finish store.StudioRunFinish, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.ownedRun(owner, id)
	if !ok {
		return store.ErrNotFound
	}
	if r.Status == finish.Status && r.ReplicaID == replica {
		return nil
	}
	if err := store.StudioRunHeld(r, replica, now); err != nil {
		return err
	}
	r.Status, r.ErrorCode, r.FailedNode, r.Outputs = finish.Status, finish.ErrorCode, finish.FailedNode, finish.Outputs
	r.PromptTokens, r.CompletionTokens = finish.PromptTokens, finish.CompletionTokens
	r.FinishedAt = &now
	s.studioRuns[id] = r
	return nil
}

var _ store.StudioFlows = (*Store)(nil)
