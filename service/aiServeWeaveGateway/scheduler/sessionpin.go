package scheduler

import (
	"container/list"
	"context"
	"log/slog"
	"sync"
	"time"
)

// This file keeps a conversation on the account that last served it.
//
// Session affinity alone ranks candidates by rendezvous hashing of the session
// key, so a conversation always prefers the same first choice. When that
// choice is set aside — its quota ran out, it is cooling down, it is low on
// quota — the conversation moves to the next one and pays once to warm a new
// prompt cache. Without a memory of the move it then moves straight back the
// moment the first choice recovers, and pays again. A pin records the
// candidate that last served the session, and ranking prefers it over the
// hash, so a conversation stays where it is until that place itself fails.
//
// A pin is only a preference inside the same ranking session affinity already
// uses: a pinned candidate with no idle slot, under quota or resource pressure,
// cooling or broken still yields exactly as a first choice would. It does hold
// against another account whose allowance is about to expire, and inside the
// low-quota tier it comes before quota left, so a conversation does not move
// for either (orderRank, compareInTier).
//
// 本文件让一段对话留在上一次服务它的账户上。
//
// 单靠会话亲和，候选按会话 key 的 rendezvous 哈希排序，因此一段对话总是偏好同一个
// 首选。首选被搁置时——额度用完、正在冷却、额度紧张——对话换到下一个，并为预热新的
// 提示缓存付出一次代价。如果不记住这次迁移，首选一恢复，对话就会立刻迁回去，再付一次。
// 钉选记录上一次服务该会话的候选，排序时它优先于哈希，因此对话会留在原处，直到那里
// 自己出问题。
//
// 钉选只是会话亲和已有排序中的一个偏好：被钉选的候选没有空闲槽、额度或资源紧张、
// 正在冷却或已熔断时，与首选一样照常让位。但它不为额度即将作废的其他账户让位，在额度
// 紧张档内也排在剩余额度之前，因此对话不会因这两者迁走（orderRank、compareInTier）。

// SessionPin is the candidate that last served a session, and when that was
// recorded.
//
// SessionPin 是上一次服务某个会话的候选，以及记录它的时刻。
type SessionPin struct {
	Candidate Candidate
	At        time.Time
}

// SessionPins stores one pin per session key hash. Every Gateway replica a
// session's requests may reach should share one store, or each keeps its own
// idea of where the conversation lives.
//
// SessionPins 为每个会话 key 哈希保存一个钉选。一个会话的请求可能到达的所有 Gateway
// 副本应当共享同一个存储，否则每个副本对对话所在之处各有各的看法。
type SessionPins interface {
	// Get returns the pin for key, if any.
	//
	// Get 返回 key 的钉选（如果有）。
	Get(ctx context.Context, key uint64) (SessionPin, bool, error)
	// Set records pin for key, to be forgotten after ttl.
	//
	// Set 为 key 记录 pin，在 ttl 之后遗忘。
	Set(ctx context.Context, key uint64, pin SessionPin, ttl time.Duration) error
}

// DefaultSessionPinTTL is Config.SessionPinTTL's default: about as long as a
// conversation sits idle before its prompt cache is cold anyway.
//
// DefaultSessionPinTTL 是 Config.SessionPinTTL 的默认值：大约是一段对话闲置到其提示缓存
// 反正也已冷掉的时长。
const DefaultSessionPinTTL = time.Hour

// sessionPinTimeout bounds one pin read or write. Both sit on the request
// path; a store that does not answer in time is treated as having no pin.
//
// sessionPinTimeout 限定一次钉选读写。两者都在请求路径上；没有及时应答的存储按没有钉选
// 处理。
const sessionPinTimeout = 250 * time.Millisecond

// withPin adds key's pin to the affinity, when sessions are pinned and the
// request carries a session key. A store error leaves the affinity as it was:
// the conversation falls back to the hash ranking for this request.
//
// withPin 在会话钉选开启、且请求携带会话 key 时，把 key 的钉选加入亲和。存储出错时
// 亲和保持原样：这次请求退回哈希排序。
func (s *Scheduler) withPin(ctx context.Context, key affinity) affinity {
	if !key.session || s.pins == nil {
		return key
	}
	opCtx, cancel := context.WithTimeout(ctx, sessionPinTimeout)
	defer cancel()
	pin, ok, err := s.pins.Get(opCtx, key.hash)
	if err != nil {
		s.logger.Debug("session pin read failed; ranking by session hash", slog.Any("error", err))
		return key
	}
	if ok {
		key.pin, key.pinned, key.pinAt = pin.Candidate, true, pin.At
	}
	return key
}

// notePin records c as the candidate serving key's session. It writes only
// when the pin changes, or when an unchanged pin is a quarter of its TTL old,
// so a conversation that stays put costs a write a few times per TTL, not one
// per request.
//
// notePin 把 c 记录为服务 key 所属会话的候选。只有钉选变化、或未变的钉选已经过了 TTL
// 的四分之一时才写入，因此留在原处的对话每个 TTL 只付出几次写入，而不是每个请求一次。
func (s *Scheduler) notePin(ctx context.Context, key affinity, c Candidate) {
	if !key.session || s.pins == nil {
		return
	}
	now := s.clock.Now()
	if key.pinned && samePinAccount(key.pin, c) && now.Sub(key.pinAt) < s.pinTTL/4 {
		return
	}
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionPinTimeout)
	defer cancel()
	if err := s.pins.Set(opCtx, key.hash, SessionPin{Candidate: c, At: now}, s.pinTTL); err != nil {
		s.logger.Debug("session pin write failed", slog.Any("error", err))
	}
}

// samePinAccount reports whether a and b are the same account: the same
// runtime on the same node. A pin follows the account, not the model, because
// one conversation mixes models (Claude Code sends its small tasks to a
// lighter one) and it is the account whose quota and prompt cache the session
// lives on; a pin that matched the model too would be overwritten by every
// model switch and send the session back to its hash first choice.
//
// samePinAccount 报告 a 与 b 是否为同一账户：同一节点上的同一运行时。钉选跟随账户而
// 不是模型，因为一段对话会混用多个模型（Claude Code 把小任务交给较轻的模型），而会话
// 所依附的额度与提示缓存属于账户；连模型一起匹配的钉选会被每次模型切换覆盖，把会话送回
// 它的哈希首选。
func samePinAccount(a, b Candidate) bool {
	return a.NodeID == b.NodeID && a.RuntimeID == b.RuntimeID
}

// memorySessionPinsMax bounds the in-replica pin store.
//
// memorySessionPinsMax 限定副本内钉选存储的容量。
const memorySessionPinsMax = 65536

// memorySessionPins is the in-replica SessionPins a Scheduler uses when none
// is configured: exact for one replica, and for several it only means each
// keeps its own pins. It forgets the least recently written pin when full
// and expired pins on read.
//
// memorySessionPins 是未配置存储时 Scheduler 使用的副本内 SessionPins：单副本下精确，
// 多副本下只意味着每个副本各自保存钉选。满了时遗忘最久未写入的钉选，读取时遗忘已过期的
// 钉选。
type memorySessionPins struct {
	clock interface{ Now() time.Time }
	max   int

	mu    sync.Mutex
	order *list.List // front: most recently written / 前端：最近写入
	items map[uint64]*list.Element
}

type memoryPin struct {
	key     uint64
	pin     SessionPin
	expires time.Time
}

func newMemorySessionPins(clock interface{ Now() time.Time }, max int) *memorySessionPins {
	return &memorySessionPins{clock: clock, max: max, order: list.New(), items: make(map[uint64]*list.Element)}
}

func (m *memorySessionPins) Get(_ context.Context, key uint64) (SessionPin, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		return SessionPin{}, false, nil
	}
	item := e.Value.(*memoryPin)
	if !m.clock.Now().Before(item.expires) {
		m.order.Remove(e)
		delete(m.items, key)
		return SessionPin{}, false, nil
	}
	return item.pin, true, nil
}

func (m *memorySessionPins) Set(_ context.Context, key uint64, pin SessionPin, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	expires := m.clock.Now().Add(ttl)
	if e, ok := m.items[key]; ok {
		item := e.Value.(*memoryPin)
		item.pin, item.expires = pin, expires
		m.order.MoveToFront(e)
		return nil
	}
	m.items[key] = m.order.PushFront(&memoryPin{key: key, pin: pin, expires: expires})
	for m.order.Len() > m.max {
		oldest := m.order.Back()
		m.order.Remove(oldest)
		delete(m.items, oldest.Value.(*memoryPin).key)
	}
	return nil
}
