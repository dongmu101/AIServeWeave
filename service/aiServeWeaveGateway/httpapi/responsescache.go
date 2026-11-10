package httpapi

import (
	"container/list"
	"encoding/json"
	"sync"
	"time"
)

// Bounds of the replica-local response-turn cache. The byte budget counts the
// stored turns themselves (each at most the control plane's
// maxResponseTurnBodyBytes, 1 MiB), so memory stays bounded however long the
// chains are or however many tenants continue conversations. The TTL is the
// upper bound on how long this replica can keep serving a turn the control
// plane no longer holds.
//
// 副本本地 response-turn 缓存的边界。字节预算统计的是已存储的轮次本身（每个至多
// 为控制面 maxResponseTurnBodyBytes，即 1 MiB），因此无论链多长、有多少租户在续接
// 会话，内存都有界。TTL 是本副本在控制面已不再持有某一轮之后，仍可能继续提供它的
// 时间上限。
const (
	responseTurnCacheBytes = 32 << 20
	responseTurnCacheTTL   = 10 * time.Minute
	// responseTurnCacheEntryOverhead approximates the per-entry cost of the
	// map, the list element and the key strings beyond the payload itself.
	//
	// responseTurnCacheEntryOverhead 近似估计每个条目在载荷之外的开销（map、链表元素
	// 与键字符串）。
	responseTurnCacheEntryOverhead = 256
)

type responseTurnKey struct {
	tenantID, responseID string
}

type responseTurnEntry struct {
	key      responseTurnKey
	previous string
	messages json.RawMessage
	size     int
	expires  time.Time
}

// responseTurnCache is a byte-bounded LRU of stored response turns, keyed by
// (tenant, response id). A turn is immutable once the control plane has it:
// the control plane exposes only create and get for response_turns, a
// duplicate create returns the existing row without overwriting it, and no
// path updates or deletes a row (STATUS.md lists retention cleanup as a known
// gap). So a cached turn can only become stale by being removed someday; the
// TTL bounds that window, and only found turns are ever cached, so a turn
// that is not there yet is never remembered as absent. The key includes the
// tenant, so one tenant can never hit another tenant's entry. The cached
// messages slice is shared with callers, who must treat it as read-only.
//
// responseTurnCache 是按字节数设上限的已存储 response turn 的 LRU，键为
// (租户, response id)。一轮一旦进入控制面就是不可变的：控制面对 response_turns 只
// 提供创建与读取，重复创建返回已有的行而不覆盖，也没有任何路径更新或删除行
// （STATUS.md 把保留期清理列为已知缺口）。因此缓存的一轮只可能在将来某天被移除而
// 变陈旧；TTL 限定这个窗口，并且只缓存「找到」的轮次，所以尚不存在的一轮绝不会被记
// 成不存在。键里带租户，一个租户永远命中不到另一个租户的条目。缓存的 messages 切片
// 与调用方共享，调用方必须把它当只读。
type responseTurnCache struct {
	mu       sync.Mutex
	maxBytes int
	ttl      time.Duration
	now      func() time.Time
	used     int
	order    *list.List // front = most recently used / 队首 = 最近使用
	entries  map[responseTurnKey]*list.Element
}

func newResponseTurnCache(maxBytes int, ttl time.Duration) *responseTurnCache {
	return &responseTurnCache{
		maxBytes: maxBytes,
		ttl:      ttl,
		now:      time.Now,
		order:    list.New(),
		entries:  make(map[responseTurnKey]*list.Element),
	}
}

func (c *responseTurnCache) get(tenantID, responseID string) (previous string, messages json.RawMessage, ok bool) {
	if c == nil || tenantID == "" || responseID == "" {
		return "", nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, found := c.entries[responseTurnKey{tenantID, responseID}]
	if !found {
		return "", nil, false
	}
	entry := el.Value.(*responseTurnEntry)
	if !c.now().Before(entry.expires) {
		c.remove(el)
		return "", nil, false
	}
	c.order.MoveToFront(el)
	return entry.previous, entry.messages, true
}

func (c *responseTurnCache) put(tenantID, responseID, previous string, messages json.RawMessage) {
	if c == nil || tenantID == "" || responseID == "" {
		return
	}
	size := responseTurnCacheEntryOverhead + len(tenantID) + 2*len(responseID) + len(previous) + len(messages)
	// A turn that would take a large share of the budget would only evict
	// many useful small ones; it is simply not cached.
	//
	// 占用预算很大一部分的一轮只会挤掉许多有用的小条目，所以干脆不缓存。
	if size > c.maxBytes/4 {
		return
	}
	key := responseTurnKey{tenantID, responseID}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, found := c.entries[key]; found {
		c.remove(el)
	}
	c.entries[key] = c.order.PushFront(&responseTurnEntry{key: key, previous: previous, messages: messages, size: size, expires: c.now().Add(c.ttl)})
	c.used += size
	for c.used > c.maxBytes {
		c.remove(c.order.Back())
	}
}

// remove drops one element; c.mu must be held.
//
// remove 移除一个元素；调用时必须持有 c.mu。
func (c *responseTurnCache) remove(el *list.Element) {
	entry := c.order.Remove(el).(*responseTurnEntry)
	delete(c.entries, entry.key)
	c.used -= entry.size
}
