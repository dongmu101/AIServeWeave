package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const revocationOutboxID int64 = 1

var errRevocationOutboxUnavailable = errors.New("gormstore: revocation outbox unavailable")

type revocationOutbox struct {
	ID                  int64 `gorm:"primaryKey"`
	Generation          int64
	DeliveredGeneration int64
}

func (revocationOutbox) TableName() string { return "key_revocation_outbox" }

// FlushRevocations publishes one coalesced pending revocation generation and
// confirms it in the same row-lock transaction only after publication
// succeeds. A false result with no error means there was no pending work.
//
// FlushRevocations 发布一个合并后的待发送吊销代际，并且仅在发布成功后，才在持有同一
// 行锁的事务中确认它。返回 false 且无错误表示没有待发送工作。
func (s *Store) FlushRevocations(ctx context.Context, publish func(context.Context) error) (bool, error) {
	if publish == nil {
		return false, errRevocationOutboxUnavailable
	}
	flushed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var outbox revocationOutbox
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", revocationOutboxID).Take(&outbox).Error; err != nil {
			return err
		}
		if outbox.Generation <= outbox.DeliveredGeneration {
			return nil
		}
		if err := publish(ctx); err != nil {
			return err
		}
		result := tx.Model(&revocationOutbox{}).
			Where("id = ?", revocationOutboxID).
			UpdateColumn("delivered_generation", outbox.Generation)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errRevocationOutboxUnavailable
		}
		flushed = true
		return nil
	})
	if err != nil {
		return false, translate(err)
	}
	return flushed, nil
}

func enqueueRevocation(tx *gorm.DB) error {
	result := tx.Model(&revocationOutbox{}).
		Where("id = ?", revocationOutboxID).
		UpdateColumn("generation", gorm.Expr("generation + 1"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errRevocationOutboxUnavailable
	}
	return nil
}
