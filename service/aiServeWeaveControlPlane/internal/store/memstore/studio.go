package memstore

import (
	"context"
	"sort"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ownedConversation reads a conversation of owner. Callers hold s.mu.
// ownedConversation 读取 owner 的一个对话。调用方持有 s.mu。
func (s *Store) ownedConversation(owner store.StudioOwner, id string) (model.StudioConversation, bool) {
	c, ok := s.studioConversations[id]
	if !ok || c.TenantID != owner.TenantID || c.UserID != owner.UserID {
		return model.StudioConversation{}, false
	}
	return c, true
}

// CreateStudioConversation implements store.StudioConversations.
func (s *Store) CreateStudioConversation(_ context.Context, c *model.StudioConversation, maxPerUser int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.studioConversations[c.ID]; exists {
		return store.ErrConflict
	}
	count := 0
	for _, existing := range s.studioConversations {
		if existing.TenantID == c.TenantID && existing.UserID == c.UserID {
			count++
		}
	}
	if maxPerUser > 0 && count >= maxPerUser {
		return store.ErrStudioLimit
	}
	s.studioConversations[c.ID] = *c
	return nil
}

// GetStudioConversation implements store.StudioConversations.
func (s *Store) GetStudioConversation(_ context.Context, owner store.StudioOwner, id string) (model.StudioConversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ownedConversation(owner, id)
	if !ok {
		return model.StudioConversation{}, store.ErrNotFound
	}
	return c, nil
}

// ListStudioConversations implements store.StudioConversations.
func (s *Store) ListStudioConversations(_ context.Context, filter store.StudioConversationFilter) ([]model.StudioConversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	query := strings.ToLower(filter.Query)
	out := []model.StudioConversation{}
	for _, c := range s.studioConversations {
		if c.TenantID != filter.Owner.TenantID || c.UserID != filter.Owner.UserID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(c.Title), query) {
			continue
		}
		if b := filter.Before; b != nil && !(c.UpdatedAt.Before(b.UpdatedAt) || (c.UpdatedAt.Equal(b.UpdatedAt) && c.ID < b.ID)) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// UpdateStudioConversation implements store.StudioConversations.
func (s *Store) UpdateStudioConversation(_ context.Context, owner store.StudioOwner, id string, patch store.StudioConversationPatch, at time.Time) (model.StudioConversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ownedConversation(owner, id)
	if !ok {
		return model.StudioConversation{}, store.ErrNotFound
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
	s.studioConversations[id] = c
	return c, nil
}

// DeleteStudioConversation implements store.StudioConversations.
func (s *Store) DeleteStudioConversation(_ context.Context, owner store.StudioOwner, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ownedConversation(owner, id); !ok {
		return store.ErrNotFound
	}
	delete(s.studioConversations, id)
	delete(s.studioMessages, id)
	return nil
}

// ListStudioMessages implements store.StudioConversations.
func (s *Store) ListStudioMessages(_ context.Context, owner store.StudioOwner, conversationID string, afterSeq int64, limit int) ([]model.StudioMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ownedConversation(owner, conversationID); !ok {
		return nil, store.ErrNotFound
	}
	out := []model.StudioMessage{}
	for _, message := range s.studioMessages[conversationID] {
		if message.Seq > afterSeq {
			out = append(out, message)
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// BeginStudioTurn implements store.StudioConversations.
func (s *Store) BeginStudioTurn(_ context.Context, owner store.StudioOwner, conversationID string, turn store.StudioTurn) (model.StudioConversation, []model.StudioMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ownedConversation(owner, conversationID)
	if !ok {
		return model.StudioConversation{}, nil, store.ErrNotFound
	}
	_, kept, err := store.PlanStudioTurn(s.studioMessages[conversationID], turn)
	if err != nil {
		return model.StudioConversation{}, nil, err
	}
	s.studioMessages[conversationID] = append([]model.StudioMessage(nil), kept...)
	if c.Title == "" {
		c.Title = turn.Title
	}
	if turn.Model != "" {
		c.Model = turn.Model
	}
	c.UpdatedAt = turn.At
	s.studioConversations[conversationID] = c
	return c, append([]model.StudioMessage(nil), kept...), nil
}

// AppendStudioReply implements store.StudioConversations.
func (s *Store) AppendStudioReply(_ context.Context, owner store.StudioOwner, conversationID string, replyTo int64, reply *model.StudioMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.ownedConversation(owner, conversationID)
	if !ok {
		return store.ErrNotFound
	}
	messages := s.studioMessages[conversationID]
	for _, message := range messages {
		if message.ID == reply.ID {
			return nil
		}
	}
	if len(messages) == 0 || messages[len(messages)-1].Seq != replyTo {
		return store.ErrStudioStaleTurn
	}
	reply.Seq = replyTo + 1
	s.studioMessages[conversationID] = append(messages, *reply)
	c.UpdatedAt = reply.CreatedAt
	s.studioConversations[conversationID] = c
	return nil
}
