// Package rediscooldown is the Redis implementation of scheduler.SharedCooldowns:
// one sorted set every Gateway replica reads and writes, whose members are
// candidates and whose scores are when their rate-limit cooldown ends.
//
// A sorted set fits because the one query that matters — "everything still in
// force" — is a score range, and pruning what has expired is another. Members
// are pruned on every read rather than expired by Redis, so there is no key
// per candidate to scan for; the set stays as small as the number of accounts
// currently cooling down.
//
// Each cooldown's start (Cooldown.Since) lives beside it in a hash keyed by
// the same member, written and pruned together with the set by Lua scripts so
// the two never disagree.
//
// rediscooldown 包是 scheduler.SharedCooldowns 的 Redis 实现：一个所有 Gateway 副本都读写
// 的有序集合，成员是候选，分值是其限流冷却结束的时刻。每条冷却的开始时刻
// （Cooldown.Since）保存在旁边一个以同一成员为字段的哈希里，由 Lua 脚本与集合一起写入、
// 一起清理，两者不会不一致。
//
// 选有序集合，是因为唯一要紧的查询——「所有仍然有效的条目」——是一次分值区间查询，而
// 清理已过期的条目是另一次。成员在每次读取时清理，而不是交给 Redis 过期，因此不存在需要
// 扫描的「每候选一个键」；集合大小始终等于此刻正在冷却的账户数。
package rediscooldown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// DefaultKey is the sorted set's key, under the same aisw: namespace as the
// Gateway's other Redis state. The start-time hash is the key plus sinceSuffix.
//
// DefaultKey 是有序集合的键，与 Gateway 其他 Redis 状态同在 aisw: 命名空间下。开始时刻
// 哈希的键是它加上 sinceSuffix。
const DefaultKey = "aisw:sched:ratelimit-cooldown"

const sinceSuffix = ":since"

// maxActive bounds how many entries one read returns, so a set grown by a
// misbehaving replica cannot make every pull unbounded.
//
// maxActive 限定一次读取返回的条目数，使被行为异常的副本撑大的集合不会让每次拉取都
// 无界。
const maxActive = 4096

// activeScript prunes expired entries from both keys and returns the rest as
// flat (member, until, since) triples, atomically, so a replica never reads an
// entry that another replica's prune is removing.
//
// activeScript 原子地从两个键中清理已过期条目，并把其余条目按 (成员, 结束, 开始) 三元组
// 平铺返回，使副本永远不会读到另一个副本正在清理的条目。
var activeScript = redis.NewScript(`
local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
for i = 1, #expired, 500 do
  redis.call('HDEL', KEYS[2], unpack(expired, i, math.min(i + 499, #expired)))
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
local rows = redis.call('ZRANGEBYSCORE', KEYS[1], '(' .. ARGV[1], '+inf', 'WITHSCORES', 'LIMIT', 0, tonumber(ARGV[2]))
local out = {}
for i = 1, #rows, 2 do
  out[#out + 1] = rows[i]
  out[#out + 1] = rows[i + 1]
  out[#out + 1] = redis.call('HGET', KEYS[2], rows[i]) or ''
end
return out
`)

// markScript writes one entry to both keys.
//
// markScript 把一个条目写入两个键。
var markScript = redis.NewScript(`
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[1])
redis.call('HSET', KEYS[2], ARGV[1], ARGV[3])
return 1
`)

// clearScript removes one entry from both keys.
//
// clearScript 从两个键中删除一个条目。
var clearScript = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('HDEL', KEYS[2], ARGV[1])
return 1
`)

// Store is a scheduler.SharedCooldowns backed by one Redis sorted set.
//
// Store 是由一个 Redis 有序集合支撑的 scheduler.SharedCooldowns。
type Store struct {
	client *redis.Client
	keys   []string
}

var _ scheduler.SharedCooldowns = (*Store)(nil)

// New returns a Store on client using key; an empty key uses DefaultKey.
//
// New 返回在 client 上使用 key 的 Store；key 为空时使用 DefaultKey。
func New(client *redis.Client, key string) (*Store, error) {
	if client == nil {
		return nil, errors.New("rediscooldown: New needs a client")
	}
	if key == "" {
		key = DefaultKey
	}
	return &Store{client: client, keys: []string{key, key + sinceSuffix}}, nil
}

// member is a candidate's encoding in the set. JSON rather than a separator
// because node and runtime IDs are chosen by operators and Agents, and no
// separator is guaranteed absent from them.
//
// member 是候选在集合中的编码。用 JSON 而不是分隔符，因为节点与运行时 ID 由运维和
// Agent 选定，不能保证其中不含任何分隔符。
type member struct {
	NodeID    string `json:"n"`
	RuntimeID string `json:"r"`
	Kind      string `json:"k"`
	Model     string `json:"m"`
}

func encode(c scheduler.Candidate) (string, error) {
	data, err := json.Marshal(member{NodeID: c.NodeID, RuntimeID: c.RuntimeID, Kind: string(c.Kind), Model: c.Model})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decode(raw string) (scheduler.Candidate, bool) {
	var m member
	if json.Unmarshal([]byte(raw), &m) != nil || m.NodeID == "" || m.RuntimeID == "" {
		return scheduler.Candidate{}, false
	}
	return scheduler.Candidate{NodeID: m.NodeID, RuntimeID: m.RuntimeID, Kind: runtime.Kind(m.Kind), Model: m.Model}, true
}

// Mark implements scheduler.SharedCooldowns.
//
// Mark 实现 scheduler.SharedCooldowns。
func (s *Store) Mark(ctx context.Context, c scheduler.Candidate, cd scheduler.Cooldown) error {
	m, err := encode(c)
	if err != nil {
		return err
	}
	if err := markScript.Run(ctx, s.client, s.keys, m, cd.Until.UnixMilli(), cd.Since.UnixMilli()).Err(); err != nil {
		return fmt.Errorf("rediscooldown: mark: %w", err)
	}
	return nil
}

// Clear implements scheduler.SharedCooldowns.
//
// Clear 实现 scheduler.SharedCooldowns。
func (s *Store) Clear(ctx context.Context, c scheduler.Candidate) error {
	m, err := encode(c)
	if err != nil {
		return err
	}
	if err := clearScript.Run(ctx, s.client, s.keys, m).Err(); err != nil {
		return fmt.Errorf("rediscooldown: clear: %w", err)
	}
	return nil
}

// Active implements scheduler.SharedCooldowns. Members that do not decode are
// skipped rather than failing the read: one bad entry must not stop every
// replica from seeing the good ones, and it is pruned once it expires. A
// missing or unreadable start time reads as zero, which only means a quota
// snapshot cannot end that cooldown early.
//
// Active 实现 scheduler.SharedCooldowns。无法解码的成员被跳过而不是让读取失败：一个坏
// 条目不能让所有副本都看不到好的条目，而它过期后就会被清理。缺失或无法解析的开始时刻
// 按零值处理，这只意味着额度快照不能提前结束那条冷却。
func (s *Store) Active(ctx context.Context, now time.Time) (map[scheduler.Candidate]scheduler.Cooldown, error) {
	raw, err := activeScript.Run(ctx, s.client, s.keys, now.UnixMilli(), maxActive).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("rediscooldown: active: %w", err)
	}
	if len(raw)%3 != 0 {
		return nil, fmt.Errorf("rediscooldown: active: reply length %d is not a multiple of 3", len(raw))
	}
	out := make(map[scheduler.Candidate]scheduler.Cooldown, len(raw)/3)
	for i := 0; i < len(raw); i += 3 {
		c, ok := decode(raw[i])
		if !ok {
			continue
		}
		until, err := strconv.ParseFloat(raw[i+1], 64)
		if err != nil {
			continue
		}
		cd := scheduler.Cooldown{Until: time.UnixMilli(int64(until))}
		if since, err := strconv.ParseInt(raw[i+2], 10, 64); err == nil && since > 0 {
			cd.Since = time.UnixMilli(since)
		}
		out[c] = cd
	}
	return out, nil
}
