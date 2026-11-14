package gormstore

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// -----------------------------------------------------------------------
// ResponseTurns
// -----------------------------------------------------------------------

// CreateResponseTurn inserts one turn, returning store.ErrConflict when the id
// already exists. The table is partitioned by created_at, so its primary key
// is (id, created_at), and created_at is the control plane's clock at the time
// of the call — a Gateway retry carries a new one. The id is therefore looked
// up first, within responseTurnRetryWindow of this call so the lookup touches
// a day or two of partitions instead of all of them: a retry of the same turn
// arrives within seconds, and the ids are random. Only two retries racing each
// other can both insert, leaving two identical turns that read back the same.
//
// CreateResponseTurn 插入一轮；id 已存在时返回 store.ErrConflict。这张表按
// created_at 分区，主键是 (id, created_at)，而 created_at 取的是调用时控制面的
// 时钟——Gateway 重试会带来新的值。因此先按 id 查找，且只查本次调用前
// responseTurnRetryWindow 之内，使查找只触达一两天的分区而不是全部：同一轮的重试在
// 几秒内到达，而 id 是随机的。只有两次相互竞争的重试才可能都插入，留下两行内容相同、
// 读取结果一致的轮次。
func (s *Store) CreateResponseTurn(ctx context.Context, turn *model.ResponseTurn) error {
	var existing int64
	if err := s.db.WithContext(ctx).Model(&model.ResponseTurn{}).
		Where("id = ? AND created_at >= ?", turn.ID, turn.CreatedAt.Add(-responseTurnRetryWindow)).Count(&existing).Error; err != nil {
		return translate(err)
	}
	if existing > 0 {
		return store.ErrConflict
	}
	return translate(s.db.WithContext(ctx).Create(turn).Error)
}

// responseTurnRetryWindow bounds the duplicate lookup in CreateResponseTurn.
//
// responseTurnRetryWindow 限定 CreateResponseTurn 中重复查找的时间范围。
const responseTurnRetryWindow = 24 * time.Hour

// GetResponseTurn reads one turn by id, scoped to its tenant.
//
// GetResponseTurn 按 id 读取一轮，并限定在其租户范围内。
func (s *Store) GetResponseTurn(ctx context.Context, tenantID, id string) (model.ResponseTurn, error) {
	var out model.ResponseTurn
	err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ?", id, tenantID).Take(&out).Error
	return out, translate(err)
}

// DeleteResponseTurnsBefore implements store.ResponseTurns.
//
// DeleteResponseTurnsBefore 实现 store.ResponseTurns。
func (s *Store) DeleteResponseTurnsBefore(ctx context.Context, before time.Time) (store.RetentionResult, error) {
	result, err := s.applyRetention(ctx, "response_turns", before)
	return result, translate(err)
}
