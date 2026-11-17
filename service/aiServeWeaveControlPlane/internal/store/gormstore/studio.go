package gormstore

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// MigrateStudio applies the studio versioned SQL under the shared migration
// lock, for live tests; production goes through MigrateAll.
//
// MigrateStudio 在共享迁移锁下应用 studio 的版本化 SQL，供独立测试使用；生产走 MigrateAll。
func (s *Store) MigrateStudio(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "studio")
}

// ownedConversation reads owner's conversation id inside tx, locking the row
// when lock is set so concurrent turns of one conversation serialize.
//
// ownedConversation 在 tx 内读取 owner 的对话 id；lock 为真时锁住该行，使同一对话的
// 并发轮次串行执行。
func ownedConversation(tx *gorm.DB, owner store.StudioOwner, id string, lock bool) (model.StudioConversation, error) {
	q := tx.Where("id = ? AND tenant_id = ? AND user_id = ?", id, owner.TenantID, owner.UserID)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var c model.StudioConversation
	return c, q.Take(&c).Error
}

// CreateStudioConversation implements store.StudioConversations. The count
// and the insert are not atomic, so racing creates can pass the limit by the
// number of racers; the limit bounds storage, it is not a quota.
//
// CreateStudioConversation 实现 store.StudioConversations。计数与插入不是原子的，并发
// 创建最多可超出上限并发数个；这个上限用来约束存储，不是配额。
func (s *Store) CreateStudioConversation(ctx context.Context, c *model.StudioConversation, maxPerUser int) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if maxPerUser > 0 {
			var count int64
			if err := tx.Model(&model.StudioConversation{}).Where("tenant_id = ? AND user_id = ?", c.TenantID, c.UserID).Count(&count).Error; err != nil {
				return err
			}
			if count >= int64(maxPerUser) {
				return store.ErrStudioLimit
			}
		}
		return tx.Create(c).Error
	}))
}

// GetStudioConversation implements store.StudioConversations.
func (s *Store) GetStudioConversation(ctx context.Context, owner store.StudioOwner, id string) (model.StudioConversation, error) {
	c, err := ownedConversation(s.db.WithContext(ctx), owner, id, false)
	return c, translate(err)
}

// ListStudioConversations implements store.StudioConversations.
func (s *Store) ListStudioConversations(ctx context.Context, filter store.StudioConversationFilter) ([]model.StudioConversation, error) {
	q := s.db.WithContext(ctx).Where("tenant_id = ? AND user_id = ?", filter.Owner.TenantID, filter.Owner.UserID)
	if filter.Query != "" {
		q = q.Where("LOWER(title) LIKE ? ESCAPE '!'", "%"+escapeLike(strings.ToLower(filter.Query))+"%")
	}
	if b := filter.Before; b != nil {
		q = q.Where("updated_at < ? OR (updated_at = ? AND id < ?)", b.UpdatedAt, b.UpdatedAt, b.ID)
	}
	if filter.Limit > 0 {
		q = q.Limit(filter.Limit)
	}
	out := []model.StudioConversation{}
	err := q.Order("updated_at DESC, id DESC").Find(&out).Error
	return out, translate(err)
}

// escapeLike escapes LIKE wildcards with '!' so a search for "50%" matches
// the text, not every title.
//
// escapeLike 用 '!' 转义 LIKE 通配符，使搜索 "50%" 匹配字面文本而不是所有标题。
func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}

// UpdateStudioConversation implements store.StudioConversations.
func (s *Store) UpdateStudioConversation(ctx context.Context, owner store.StudioOwner, id string, patch store.StudioConversationPatch, at time.Time) (model.StudioConversation, error) {
	var out model.StudioConversation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		c, err := ownedConversation(tx, owner, id, true)
		if err != nil {
			return err
		}
		if patch.Title != nil {
			c.Title = *patch.Title
		}
		if patch.Model != nil {
			c.Model = *patch.Model
		}
		if patch.SystemPrompt != nil {
			c.SystemPrompt = *patch.SystemPrompt
		}
		c.UpdatedAt = at
		out = c
		return tx.Model(&model.StudioConversation{}).Where("id = ?", id).Updates(map[string]any{
			"title": c.Title, "model": c.Model, "system_prompt": c.SystemPrompt, "updated_at": at,
		}).Error
	})
	return out, translate(err)
}

// DeleteStudioConversation implements store.StudioConversations.
func (s *Store) DeleteStudioConversation(ctx context.Context, owner store.StudioOwner, id string) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := ownedConversation(tx, owner, id, true); err != nil {
			return err
		}
		if err := tx.Where("conversation_id = ?", id).Delete(&model.StudioMessage{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.StudioConversation{}).Error
	}))
}

// ListStudioMessages implements store.StudioConversations.
func (s *Store) ListStudioMessages(ctx context.Context, owner store.StudioOwner, conversationID string, afterSeq int64, limit int) ([]model.StudioMessage, error) {
	db := s.db.WithContext(ctx)
	if _, err := ownedConversation(db, owner, conversationID, false); err != nil {
		return nil, translate(err)
	}
	q := db.Where("conversation_id = ? AND seq > ?", conversationID, afterSeq).Order("seq ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	out := []model.StudioMessage{}
	return out, translate(q.Find(&out).Error)
}

// BeginStudioTurn implements store.StudioConversations, holding the
// conversation row lock for the whole turn write.
//
// BeginStudioTurn 实现 store.StudioConversations，整个轮次写入期间持有对话行锁。
func (s *Store) BeginStudioTurn(ctx context.Context, owner store.StudioOwner, conversationID string, turn store.StudioTurn) (model.StudioConversation, []model.StudioMessage, error) {
	var conversation model.StudioConversation
	var kept []model.StudioMessage
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		c, err := ownedConversation(tx, owner, conversationID, true)
		if err != nil {
			return err
		}
		history := []model.StudioMessage{}
		if err := tx.Where("conversation_id = ?", conversationID).Order("seq ASC").Find(&history).Error; err != nil {
			return err
		}
		from, remaining, err := store.PlanStudioTurn(history, turn)
		if err != nil {
			return err
		}
		if from > 0 {
			if err := tx.Where("conversation_id = ? AND seq >= ?", conversationID, from).Delete(&model.StudioMessage{}).Error; err != nil {
				return err
			}
		}
		if turn.User != nil {
			if err := tx.Create(turn.User).Error; err != nil {
				return err
			}
		}
		if c.Title == "" {
			c.Title = turn.Title
		}
		if turn.Model != "" {
			c.Model = turn.Model
		}
		c.UpdatedAt = turn.At
		if err := tx.Model(&model.StudioConversation{}).Where("id = ?", conversationID).Updates(map[string]any{
			"title": c.Title, "model": c.Model, "updated_at": c.UpdatedAt,
		}).Error; err != nil {
			return err
		}
		conversation, kept = c, remaining
		return nil
	})
	if err != nil {
		return model.StudioConversation{}, nil, translate(err)
	}
	return conversation, kept, nil
}

// AppendStudioReply implements store.StudioConversations.
func (s *Store) AppendStudioReply(ctx context.Context, owner store.StudioOwner, conversationID string, replyTo int64, reply *model.StudioMessage) error {
	return translate(s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := ownedConversation(tx, owner, conversationID, true); err != nil {
			return err
		}
		var existing int64
		if err := tx.Model(&model.StudioMessage{}).Where("id = ? AND conversation_id = ?", reply.ID, conversationID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		var last model.StudioMessage
		if err := tx.Select("seq").Where("conversation_id = ?", conversationID).Order("seq DESC").Take(&last).Error; err != nil {
			if translate(err) == store.ErrNotFound {
				return store.ErrStudioStaleTurn
			}
			return err
		}
		if last.Seq != replyTo {
			return store.ErrStudioStaleTurn
		}
		reply.Seq = replyTo + 1
		if err := tx.Create(reply).Error; err != nil {
			return err
		}
		return tx.Model(&model.StudioConversation{}).Where("id = ?", conversationID).Update("updated_at", reply.CreatedAt).Error
	}))
}

var _ store.StudioConversations = (*Store)(nil)
