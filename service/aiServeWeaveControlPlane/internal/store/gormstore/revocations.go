package gormstore

import (
	"context"
	"errors"

	"gorm.io/gorm"
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
// confirms it only after publication succeeds. A false result with no error
// means there was no pending work.
//
// The publish runs outside any transaction and without a row lock. Every
// revoking write (key revoke, user disable, balance flips, payments, account
// purchases) bumps this same row, so holding FOR UPDATE across a Redis call
// would queue all of them — each already holding a tenant lock and a pool
// connection — behind Redis whenever it is slow. Correctness does not need
// the lock:
//   - the generation read here was committed, so the publish that follows
//     happens after every revocation it covers;
//   - a revocation committed after the read leaves generation ahead of what
//     is confirmed, so a later flush picks it up;
//   - confirmation only moves forward (delivered_generation < ?), so two
//     racing flushes can never move it back.
//
// The cost is that two flushes racing on the same pending work may both
// publish. A publish only advances the cache generation, so a duplicate
// clears caches once more but cannot resurrect a revoked credential.
//
// FlushRevocations 发布一个合并后的待发送吊销代际，并且仅在发布成功后才确认它。
// 返回 false 且无错误表示没有待发送工作。
//
// 发布在任何事务之外、不持有行锁进行。每一个带吊销效果的写入（吊销 Key、停用用户、
// 余额翻转、支付、账号购买）都会推进同一行，若在 Redis 调用期间持有 FOR UPDATE，
// Redis 一慢，它们全都会排在后面——而它们各自已经持有租户锁和一个连接池连接。
// 正确性并不需要这把锁：
//   - 这里读到的 generation 已经提交，因此随后的发布发生在它覆盖的每一次吊销之后；
//   - 读取之后才提交的吊销会让 generation 领先于已确认值，由下一次发送处理；
//   - 确认只会前进（delivered_generation < ?），两次竞争的发送永远不会让它回退。
//
// 代价是两次针对同一待发送工作的竞争发送可能都会发布。发布只会推进缓存代际，
// 重复发布只是多清一次缓存，不会让已吊销的凭据复活。
func (s *Store) FlushRevocations(ctx context.Context, publish func(context.Context) error) (bool, error) {
	if publish == nil {
		return false, errRevocationOutboxUnavailable
	}
	var outbox revocationOutbox
	if err := s.db.WithContext(ctx).Where("id = ?", revocationOutboxID).Take(&outbox).Error; err != nil {
		return false, translate(err)
	}
	if outbox.Generation <= outbox.DeliveredGeneration {
		return false, nil
	}
	if err := publish(ctx); err != nil {
		return false, err
	}
	result := s.db.WithContext(ctx).Model(&revocationOutbox{}).
		Where("id = ? AND delivered_generation < ?", revocationOutboxID, outbox.Generation).
		UpdateColumn("delivered_generation", outbox.Generation)
	if result.Error != nil {
		return false, translate(result.Error)
	}
	// Zero rows means a concurrent flush already confirmed this generation
	// or a later one; this publish still happened, so report it.
	//
	// 零行表示并发的发送已经确认了这个或更新的代际；本次发布仍然发生了，因此照常报告。
	return true, nil
}

// RevocationOutboxLag reports the outbox's current generation and delivered
// generation, for a caller that wants to expose their difference as a
// point-in-time gauge (P08's controlplane_revocation_outbox_lag) rather than
// drive a delivery attempt the way FlushRevocations does.
//
// RevocationOutboxLag 报告 outbox 当前的 generation 与 delivered_generation，
// 供调用方把两者之差暴露成一个瞬时量表(P08 的
// controlplane_revocation_outbox_lag)，而不是像 FlushRevocations 那样驱动一次
// 发送尝试。
func (s *Store) RevocationOutboxLag(ctx context.Context) (generation, delivered int64, err error) {
	var outbox revocationOutbox
	if err := s.db.WithContext(ctx).Where("id = ?", revocationOutboxID).Take(&outbox).Error; err != nil {
		return 0, 0, translate(err)
	}
	return outbox.Generation, outbox.DeliveredGeneration, nil
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
