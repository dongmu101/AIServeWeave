package session

import (
	"context"
	"sort"
	"sync"
	"time"

	"AIServeWeave/common/runtime"
)

type gateState struct {
	nonce     string
	expiresAt time.Time
}

// Memory is an in-memory Store for deterministic service and HTTP tests.
//
// Memory 是用于确定性服务与 HTTP 测试的内存 Store。
type Memory struct {
	mu       sync.Mutex
	clock    runtime.Clock
	sessions map[string]Record
	subjects map[Subject]map[string]struct{}
	gates    map[Subject]gateState
}

// NewMemory returns an empty bounded in-memory session store.
//
// NewMemory 返回一个空的、有界内存会话存储。
func NewMemory(clock runtime.Clock) *Memory {
	return &Memory{
		clock:    clockOrSystem(clock),
		sessions: make(map[string]Record),
		subjects: make(map[Subject]map[string]struct{}),
		gates:    make(map[Subject]gateState),
	}
}

// Create adds one session unless its subject is being mutated.
//
// Create 添加一个会话，除非其主体正在变更。
func (m *Memory) Create(ctx context.Context, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	if !validRecord(record, now) {
		return ErrInvalid
	}
	if m.gateActive(record.Subject, now) {
		return ErrMutationActive
	}
	if _, exists := m.sessions[record.ID]; exists {
		return ErrInvalid
	}
	m.prune(record.Subject, now)
	ids := m.subjects[record.Subject]
	if ids == nil {
		ids = make(map[string]struct{})
		m.subjects[record.Subject] = ids
	}
	m.sessions[record.ID] = record
	ids[record.ID] = struct{}{}
	m.enforceBound(record.Subject)
	return nil
}

// Validate accepts only an exact, live session outside a mutation gate.
//
// Validate 只接受位于变更门之外、身份完全匹配且仍有效的会话。
func (m *Memory) Validate(ctx context.Context, id string, subject Subject, tenantID, role string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	if m.gateActive(subject, now) {
		return ErrInvalid
	}
	record, ok := m.sessions[id]
	if !ok || !record.ExpiresAt.After(now) || record.Subject != subject || record.TenantID != tenantID || record.Role != role {
		if ok && !record.ExpiresAt.After(now) {
			m.remove(id, record.Subject)
		}
		return ErrInvalid
	}
	return nil
}

// Revoke removes one session only when it belongs to subject.
//
// Revoke 仅在会话属于 subject 时移除它。
func (m *Memory) Revoke(ctx context.Context, subject Subject, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.sessions[id]
	if !ok || record.Subject != subject || !record.ExpiresAt.After(m.clock.Now()) {
		if ok && !record.ExpiresAt.After(m.clock.Now()) {
			m.remove(id, record.Subject)
		}
		return false, nil
	}
	m.remove(id, subject)
	return true, nil
}

// RevokeAll removes every live session owned by subject.
//
// RevokeAll 移除 subject 拥有的所有有效会话。
func (m *Memory) RevokeAll(ctx context.Context, subject Subject) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revokeAll(subject, m.clock.Now()), nil
}

// BeginMutation atomically closes the login gate and revokes existing sessions.
//
// BeginMutation 原子地关闭登录门并吊销既有会话。
func (m *Memory) BeginMutation(ctx context.Context, subject Subject) (Gate, error) {
	if err := ctx.Err(); err != nil {
		return Gate{}, err
	}
	if !validSubject(subject) {
		return Gate{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	if m.gateActive(subject, now) {
		return Gate{}, ErrMutationActive
	}
	nonce := randomHex()
	m.gates[subject] = gateState{nonce: nonce, expiresAt: now.Add(mutationTTL)}
	return Gate{Subject: subject, Nonce: nonce, RevokedSessions: m.revokeAll(subject, now)}, nil
}

// EndMutation opens a gate only for the nonce that acquired it.
//
// EndMutation 只为取得变更门的那个 nonce 重新开放它。
func (m *Memory) EndMutation(ctx context.Context, gate Gate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.gates[gate.Subject]
	if !ok || !state.expiresAt.After(m.clock.Now()) || state.nonce != gate.Nonce {
		return ErrInvalid
	}
	delete(m.gates, gate.Subject)
	return nil
}

func (m *Memory) gateActive(subject Subject, now time.Time) bool {
	gate, ok := m.gates[subject]
	if ok && !gate.expiresAt.After(now) {
		delete(m.gates, subject)
		return false
	}
	return ok
}

func (m *Memory) prune(subject Subject, now time.Time) {
	for id := range m.subjects[subject] {
		if record, ok := m.sessions[id]; !ok || !record.ExpiresAt.After(now) {
			m.remove(id, subject)
		}
	}
}

func (m *Memory) revokeAll(subject Subject, now time.Time) int {
	m.prune(subject, now)
	ids := m.subjects[subject]
	count := len(ids)
	for id := range ids {
		delete(m.sessions, id)
	}
	delete(m.subjects, subject)
	return count
}

func (m *Memory) enforceBound(subject Subject) {
	ids := m.subjects[subject]
	if len(ids) <= MaxSessions {
		return
	}
	ordered := make([]Record, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, m.sessions[id])
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ExpiresAt.Equal(ordered[j].ExpiresAt) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].ExpiresAt.Before(ordered[j].ExpiresAt)
	})
	for i := 0; i < len(ordered)-MaxSessions; i++ {
		m.remove(ordered[i].ID, subject)
	}
}

func (m *Memory) remove(id string, subject Subject) {
	delete(m.sessions, id)
	ids := m.subjects[subject]
	delete(ids, id)
	if len(ids) == 0 {
		delete(m.subjects, subject)
	}
}

var _ Store = (*Memory)(nil)
