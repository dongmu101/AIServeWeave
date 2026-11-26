package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ownedFlow reads owner's flow id inside tx, locking the row when lock is
// set so concurrent saves of one flow serialize.
//
// ownedFlow 在 tx 内读取 owner 的编排 id；lock 为真时锁住该行，使同一编排的并发保存
// 串行执行。
func ownedFlow(tx *gorm.DB, owner store.StudioOwner, id string, lock bool) (model.StudioFlow, error) {
	q := tx.Where("id = ? AND tenant_id = ? AND user_id = ?", id, owner.TenantID, owner.UserID)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var f model.StudioFlow
	return f, q.Take(&f).Error
}

// ownedRun reads owner's run id inside tx, locking the row when lock is set
// so a lease renewal and a finish of one run serialize.
//
// ownedRun 在 tx 内读取 owner 的运行 id；lock 为真时锁住该行，使同一运行的续租与结束
// 串行执行。
func ownedRun(tx *gorm.DB, owner store.StudioOwner, id string, lock bool) (model.StudioFlowRun, error) {
	q := tx.Where("id = ? AND tenant_id = ? AND user_id = ?", id, owner.TenantID, owner.UserID)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var r model.StudioFlowRun
	return r, q.Take(&r).Error
}

func flowVersion(tx *gorm.DB, id string, version int64) (model.StudioFlowVersion, error) {
	var v model.StudioFlowVersion
	return v, tx.Where("flow_id = ? AND version = ?", id, version).Take(&v).Error
}

// keyset narrows a newest-first query on column to the rows after c.
func keyset(q *gorm.DB, column string, c *store.StudioCursor) *gorm.DB {
	if c == nil {
		return q
	}
	return q.Where(column+" < ? OR ("+column+" = ? AND id < ?)", c.At, c.At, c.ID)
}

// CreateStudioFlow implements store.StudioFlows. As with conversations, the
// count and the insert are not atomic; the limit bounds storage, it is not a
// quota.
//
// CreateStudioFlow 实现 store.StudioFlows。与对话一样，计数与插入不是原子的；这个上限
// 用来约束存储，不是配额。
func (s *Store) CreateStudioFlow(ctx context.Context, f *model.StudioFlow, v *model.StudioFlowVersion, maxPerUser int) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if maxPerUser > 0 {
			var count int64
			if err := tx.Model(&model.StudioFlow{}).Where("tenant_id = ? AND user_id = ?", f.TenantID, f.UserID).Count(&count).Error; err != nil {
				return err
			}
			if count >= int64(maxPerUser) {
				return store.ErrStudioLimit
			}
		}
		if err := tx.Create(f).Error; err != nil {
			return err
		}
		return tx.Create(v).Error
	}))
}

// GetStudioFlow implements store.StudioFlows.
func (s *Store) GetStudioFlow(ctx context.Context, owner store.StudioOwner, id string) (model.StudioFlow, model.StudioFlowVersion, error) {
	db := s.db.WithContext(ctx)
	f, err := ownedFlow(db, owner, id, false)
	if err != nil {
		return model.StudioFlow{}, model.StudioFlowVersion{}, translate(err)
	}
	v, err := flowVersion(db, id, f.Version)
	return f, v, translate(err)
}

// GetStudioFlowVersion implements store.StudioFlows.
func (s *Store) GetStudioFlowVersion(ctx context.Context, owner store.StudioOwner, id string, version int64) (model.StudioFlowVersion, error) {
	db := s.db.WithContext(ctx)
	if _, err := ownedFlow(db, owner, id, false); err != nil {
		return model.StudioFlowVersion{}, translate(err)
	}
	v, err := flowVersion(db, id, version)
	return v, translate(err)
}

// ListStudioFlows implements store.StudioFlows.
func (s *Store) ListStudioFlows(ctx context.Context, filter store.StudioFlowFilter) ([]model.StudioFlow, error) {
	q := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ?", filter.Owner.TenantID, filter.Owner.UserID)
	q = keyset(q, "updated_at", filter.Before)
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}
	out := []model.StudioFlow{}
	return out, translate(q.Order("updated_at DESC, id DESC").Find(&out).Error)
}

// UpdateStudioFlow implements store.StudioFlows, holding the flow row lock so
// two saves cannot both become the same version. Versions are never deleted
// on their own, so the flow's Version is also its version count.
//
// UpdateStudioFlow 实现 store.StudioFlows，持有编排行锁，使两次保存不会成为同一个版本。
// 版本不会单独删除，因此编排的 Version 也就是它的版本数。
func (s *Store) UpdateStudioFlow(ctx context.Context, owner store.StudioOwner, id string, update store.StudioFlowUpdate) (model.StudioFlow, model.StudioFlowVersion, error) {
	var flow model.StudioFlow
	var latest model.StudioFlowVersion
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		f, err := ownedFlow(tx, owner, id, true)
		if err != nil {
			return err
		}
		if update.BaseVersion > 0 && update.BaseVersion != f.Version {
			return store.ErrStudioFlowChanged
		}
		v, err := flowVersion(tx, id, f.Version)
		if err != nil {
			return err
		}
		changed := false
		if update.Name != nil && *update.Name != f.Name {
			f.Name = *update.Name
			changed = true
		}
		if update.Definition != nil && *update.Definition != v.Definition {
			if update.MaxVersions > 0 && f.Version >= int64(update.MaxVersions) {
				return store.ErrStudioLimit
			}
			f.Version++
			v = model.StudioFlowVersion{FlowID: id, Version: f.Version, Definition: *update.Definition, CreatedAt: update.At}
			if err := tx.Create(&v).Error; err != nil {
				return err
			}
			changed = true
		}
		if changed {
			f.UpdatedAt = update.At
			if err := tx.Model(&model.StudioFlow{}).Where("id = ?", id).Updates(map[string]any{
				"name": f.Name, "version": f.Version, "updated_at": f.UpdatedAt,
			}).Error; err != nil {
				return err
			}
		}
		flow, latest = f, v
		return nil
	})
	if err != nil {
		return model.StudioFlow{}, model.StudioFlowVersion{}, translate(err)
	}
	return flow, latest, nil
}

// DeleteStudioFlow implements store.StudioFlows.
func (s *Store) DeleteStudioFlow(ctx context.Context, owner store.StudioOwner, id string) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := ownedFlow(tx, owner, id, true); err != nil {
			return err
		}
		runs := tx.Model(&model.StudioFlowRun{}).Select("id").Where("flow_id = ?", id)
		if err := tx.Where("run_id IN (?)", runs).Delete(&model.StudioFlowRunStep{}).Error; err != nil {
			return err
		}
		if err := tx.Where("flow_id = ?", id).Delete(&model.StudioFlowRun{}).Error; err != nil {
			return err
		}
		if err := tx.Where("flow_id = ?", id).Delete(&model.StudioFlowVersion{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.StudioFlow{}).Error
	}))
}

// StartStudioRun implements store.StudioFlows. It locks the flow so the run
// cannot outlive a concurrent delete of it; as with the per-user flow limit,
// counting active runs and inserting are not atomic across flows.
//
// StartStudioRun 实现 store.StudioFlows。它锁住编排，使运行不会在编排被并发删除后残留；
// 与每用户编排上限一样，跨编排的进行中运行计数与插入不是原子的。
func (s *Store) StartStudioRun(ctx context.Context, owner store.StudioOwner, run *model.StudioFlowRun, limits store.StudioRunLimits, now time.Time) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.StudioFlowRun
		switch err := tx.Select("tenant_id", "user_id").Where("id = ?", run.ID).Take(&existing).Error; {
		case err == nil:
			if existing.TenantID != owner.TenantID || existing.UserID != owner.UserID {
				return store.ErrConflict
			}
			return nil
		case translate(err) != store.ErrNotFound:
			return err
		}
		f, err := ownedFlow(tx, owner, run.FlowID, true)
		if err != nil {
			return err
		}
		if run.FlowVersion < 1 || run.FlowVersion > f.Version {
			return store.ErrNotFound
		}
		active := func(user bool) (int64, error) {
			q := tx.Model(&model.StudioFlowRun{}).Where("tenant_id = ? AND status = ? AND lease_until >= ?", owner.TenantID, model.StudioRunRunning, now)
			if user {
				q = q.Where("user_id = ?", owner.UserID)
			}
			var n int64
			return n, q.Count(&n).Error
		}
		for _, bound := range []struct {
			user bool
			max  int
		}{{true, limits.PerUser}, {false, limits.PerTenant}} {
			if bound.max <= 0 {
				continue
			}
			n, err := active(bound.user)
			if err != nil {
				return err
			}
			if n >= int64(bound.max) {
				return store.ErrStudioRunLimit
			}
		}
		return tx.Create(run).Error
	}))
}

// GetStudioRun implements store.StudioFlows.
func (s *Store) GetStudioRun(ctx context.Context, owner store.StudioOwner, id string) (model.StudioFlowRun, []model.StudioFlowRunStep, error) {
	db := s.db.WithContext(ctx)
	r, err := ownedRun(db, owner, id, false)
	if err != nil {
		return model.StudioFlowRun{}, nil, translate(err)
	}
	steps := []model.StudioFlowRunStep{}
	err = db.Where("run_id = ?", id).Order("finished_at ASC, node_id ASC").Find(&steps).Error
	return r, steps, translate(err)
}

// ListStudioRuns implements store.StudioFlows.
func (s *Store) ListStudioRuns(ctx context.Context, filter store.StudioRunFilter) ([]model.StudioFlowRun, error) {
	db := s.db.WithContext(ctx)
	if _, err := ownedFlow(db, filter.Owner, filter.FlowID, false); err != nil {
		return nil, translate(err)
	}
	q := db.Where("flow_id = ?", filter.FlowID)
	q = keyset(q, "created_at", filter.Before)
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}
	out := []model.StudioFlowRun{}
	return out, translate(q.Order("created_at DESC, id DESC").Find(&out).Error)
}

// RenewStudioRun implements store.StudioFlows.
func (s *Store) RenewStudioRun(ctx context.Context, owner store.StudioOwner, id, replica string, until, now time.Time) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := ownedRun(tx, owner, id, true)
		if err != nil {
			return err
		}
		if err := store.StudioRunHeld(r, replica, now); err != nil {
			return err
		}
		return tx.Model(&model.StudioFlowRun{}).Where("id = ?", id).Update("lease_until", until).Error
	}))
}

// RecordStudioRunStep implements store.StudioFlows.
func (s *Store) RecordStudioRunStep(ctx context.Context, owner store.StudioOwner, step *model.StudioFlowRunStep, replica string, now time.Time) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := ownedRun(tx, owner, step.RunID, true)
		if err != nil {
			return err
		}
		var existing int64
		if err := tx.Model(&model.StudioFlowRunStep{}).Where("run_id = ? AND node_id = ?", step.RunID, step.NodeID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		if err := store.StudioRunHeld(r, replica, now); err != nil {
			return err
		}
		return tx.Create(step).Error
	}))
}

// FinishStudioRun implements store.StudioFlows.
func (s *Store) FinishStudioRun(ctx context.Context, owner store.StudioOwner, id, replica string, finish store.StudioRunFinish, now time.Time) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err := ownedRun(tx, owner, id, true)
		if err != nil {
			return err
		}
		if r.Status == finish.Status && r.ReplicaID == replica {
			return nil
		}
		if err := store.StudioRunHeld(r, replica, now); err != nil {
			return err
		}
		return tx.Model(&model.StudioFlowRun{}).Where("id = ?", id).Updates(map[string]any{
			"status": finish.Status, "error_code": finish.ErrorCode, "failed_node": finish.FailedNode, "outputs": finish.Outputs,
			"prompt_tokens": finish.PromptTokens, "completion_tokens": finish.CompletionTokens, "finished_at": now,
		}).Error
	}))
}

var _ store.StudioFlows = (*Store)(nil)
