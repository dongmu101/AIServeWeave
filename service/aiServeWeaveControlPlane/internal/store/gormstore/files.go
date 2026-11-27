package gormstore

import (
	"context"
	"errors"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// RegisterFile implements store.Files. The table is partitioned, so there is
// no unique key on id to upsert on. It updates the rows with the id, and
// inserts only when there are none. The steps are separate statements, not
// one transaction: a locking read of a missing id takes MySQL gap locks that
// deadlock two concurrent first registrations. Two such registrations at
// different instants may leave two rows with the id, which every method
// handles; at the same instant the second insert conflicts and updates.
//
// RegisterFile 实现 store.Files。表已分区，没有可供 upsert 的 id 唯一键：先更新该 id 的行，
// 没有时才插入。各步是独立的语句而不是一个事务：对不存在的 id 做加锁读会在 MySQL 上加间隙锁，
// 两个并发的首次登记会因此死锁。两次不同时刻的此类登记可能留下同一 id 的两行，每个方法都能
// 处理；同一时刻时第二次插入冲突后改为更新。
func (s *Store) RegisterFile(ctx context.Context, f *model.File) error {
	db := s.db.WithContext(ctx)
	update := func() (bool, error) {
		res := db.Model(&model.File{}).Where("id = ?", f.ID).Updates(map[string]any{
			"tenant_id": f.TenantID, "storage": f.Storage, "object_key": f.ObjectKey,
			"biz_type": f.BizType, "biz_id": f.BizID, "kind": f.Kind, "filename": f.Filename,
			"content_type": f.ContentType, "size_bytes": f.SizeBytes, "sha256": f.SHA256,
			"status": f.Status, "updated_at": f.UpdatedAt,
		})
		if res.Error != nil {
			return false, res.Error
		}
		// MySQL counts changed rows, so an identical update reports none:
		// read the row either way, which also gives the CreatedAt kept.
		// MySQL 统计的是被改变的行，内容相同的更新会报告零行：无论如何都读一次，这也拿到了
		// 保留下来的 CreatedAt。
		var existing []model.File
		if err := db.Select("created_at").Where("id = ?", f.ID).Order("created_at").Limit(1).Find(&existing).Error; err != nil {
			return false, err
		}
		if len(existing) == 0 {
			return false, nil
		}
		f.CreatedAt = existing[0].CreatedAt
		return true, nil
	}
	found, err := update()
	if err != nil || found {
		return translate(err)
	}
	err = translate(db.Create(f).Error)
	if errors.Is(err, store.ErrConflict) {
		_, err = update()
		return translate(err)
	}
	return err
}

// CompleteFile implements store.Files.
//
// CompleteFile 实现 store.Files。
func (s *Store) CompleteFile(ctx context.Context, id string, done store.FileCompletion) error {
	res := s.db.WithContext(ctx).Model(&model.File{}).Where("id = ?", id).Updates(map[string]any{
		"size_bytes": done.SizeBytes, "sha256": done.SHA256, "content_type": done.ContentType,
		"status": model.FileStatusReady, "updated_at": done.UpdatedAt,
	})
	if res.Error != nil {
		return translate(res.Error)
	}
	if res.RowsAffected == 0 {
		// MySQL counts changed rows, not matched ones: a repeated completion
		// with the same values changes nothing but the row is there.
		// MySQL 统计的是被改变的行而不是匹配的行：用相同的值重复完成时什么也没改，但行存在。
		var n int64
		if err := s.db.WithContext(ctx).Model(&model.File{}).Where("id = ?", id).Count(&n).Error; err != nil {
			return translate(err)
		}
		if n == 0 {
			return store.ErrNotFound
		}
	}
	return nil
}

// DeleteFile implements store.Files.
//
// DeleteFile 实现 store.Files。
func (s *Store) DeleteFile(ctx context.Context, id string) error {
	return translate(s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.File{}).Error)
}

// ListFiles implements store.Files.
//
// ListFiles 实现 store.Files。
func (s *Store) ListFiles(ctx context.Context, filter store.FileFilter) ([]model.File, error) {
	limit := filter.Limit
	if limit <= 0 || limit > store.MaxFilePage {
		limit = store.MaxFilePage
	}
	q := s.db.WithContext(ctx).Where("created_at < ?", filter.Before)
	for column, value := range map[string]string{
		"storage": filter.Storage, "tenant_id": filter.TenantID, "biz_type": filter.BizType,
		"kind": filter.Kind, "status": filter.Status,
	} {
		if value != "" {
			q = q.Where(column+" = ?", value)
		}
	}
	out := []model.File{}
	err := q.Order("created_at, id").Limit(limit).Find(&out).Error
	return out, translate(err)
}
