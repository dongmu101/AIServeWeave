// Package messagesession coordinates caller-tool continuations across Gateway replicas.
// Package messagesession 协调 Gateway 副本之间的调用方工具续接。
package messagesession

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"github.com/redis/go-redis/v9"
)

// ErrUnavailable rejects absent, consumed, mismatched or concurrently claimed continuations.
// ErrUnavailable 拒绝不存在、已消费、不匹配或被并发认领的续接。
var ErrUnavailable = errors.New("Messages continuation unavailable")

// ErrExpired is the ErrUnavailable case where no record exists at all: the
// round was never continued within continuationTTL, so the caller may resume
// it in a new session. A consumed round keeps its used marker for usedTTL and
// reports plain ErrUnavailable instead.
//
// ErrExpired 是 ErrUnavailable 中完全没有记录的情况：该回合在 continuationTTL 内
// 从未被续接，调用方可以在新会话中恢复它。已消费的回合在 usedTTL 内保留已使用标记，
// 报告的是普通的 ErrUnavailable。
var ErrExpired = fmt.Errorf("%w: lease expired", ErrUnavailable)

// Record binds every tool in a completed round to authenticated ownership and its original node.
// Record 将已完成回合的全部工具绑定到已认证归属与原节点。
type Record struct {
	TenantID, KeyID, Model string
	Candidate              scheduler.Candidate
	IDs                    []string
}

// Store publishes rounds and atomically claims all results in one round.
// Store 发布回合，并原子认领同一回合的全部结果。
type Store interface {
	Publish(context.Context, Record) error
	Claim(context.Context, []string, string, string, string) (Record, func(context.Context, bool) error, error)
}

// Redis provides shared affinity without replica-local fallback.
// Redis 提供共享续接定位，不回退到副本内存。
type Redis struct{ client redis.UniversalClient }

// NewRedis binds the deployment's existing Redis connection.
// NewRedis 绑定部署既有的 Redis 连接。
func NewRedis(client redis.UniversalClient) *Redis { return &Redis{client: client} }

func keysFor(ids []string) ([]string, error) {
	if len(ids) == 0 || len(ids) > 8 {
		return nil, ErrUnavailable
	}
	seen := make(map[string]bool)
	keys := make([]string, len(ids))
	for i, id := range ids {
		if id == "" || len(id) > 256 || seen[id] {
			return nil, ErrUnavailable
		}
		seen[id] = true
		digest := sha256.Sum256([]byte(id))
		keys[i] = "aisw:messages:{sessions}:tool:" + hex.EncodeToString(digest[:])
	}
	return keys, nil
}

var publishScript = redis.NewScript(`
for _,k in ipairs(KEYS) do if redis.call('EXISTS',k)==1 then return 0 end end
for _,k in ipairs(KEYS) do redis.call('SET',k,ARGV[1],'PX',ARGV[2]) end
return 1`)

// continuationTTL bounds how long a caller may take to return tool results;
// it matches the Agent CLI bridge's tool wait, so neither side outlives the other.
//
// continuationTTL 限定调用方送回工具结果的最长时间；与 Agent CLI 桥接的工具等待
// 时限一致，两侧都不会比对方存活更久。
const continuationTTL = time.Hour

// usedTTL keeps a consumed round's used marker, so a replay within it is still
// refused rather than mistaken for an expired round that may resume.
//
// usedTTL 是已消费回合保留已使用标记的时长，期间的重放仍被拒绝，而不会被误认为
// 可以恢复的过期回合。
const usedTTL = 24 * time.Hour

// Publish stores no prompt or tool result; each record expires within continuationTTL.
// Publish 不存储提示词或工具结果；每条记录在 continuationTTL 内过期。
func (s *Redis) Publish(ctx context.Context, record Record) error {
	keys, err := keysFor(record.IDs)
	if err != nil {
		return err
	}
	if record.TenantID == "" || record.KeyID == "" || record.Model == "" || record.Candidate.NodeID == "" || record.Candidate.RuntimeID == "" {
		return ErrUnavailable
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > 16384 {
		return ErrUnavailable
	}
	n, err := publishScript.Run(ctx, s.client, keys, "ready\n"+string(data), continuationTTL.Milliseconds()).Int()
	if err != nil {
		return ErrUnavailable
	}
	if n != 1 {
		return ErrUnavailable
	}
	return nil
}

var claimScript = redis.NewScript(`
for _,k in ipairs(KEYS) do if redis.call('GET',k)~=ARGV[1] then return 0 end end
for _,k in ipairs(KEYS) do redis.call('SET',k,ARGV[2],'KEEPTTL') end
return 1`)

var commitScript = redis.NewScript(`
for _,k in ipairs(KEYS) do if redis.call('GET',k)~=ARGV[1] then return 0 end end
for _,k in ipairs(KEYS) do redis.call('SET',k,ARGV[2],'PX',ARGV[3]) end
return 1`)

// Claim checks ownership and the complete result set before acquiring a
// one-shot lease. It returns ErrExpired when no record exists, and
// ErrUnavailable for every other refusal. Committing the lease keeps a used
// marker for usedTTL; releasing it restores the record as it was.
//
// Claim 在取得一次性租约前检查归属与完整结果集合。完全没有记录时返回 ErrExpired，
// 其余拒绝都返回 ErrUnavailable。提交租约后保留已使用标记 usedTTL；释放租约则原样
// 恢复记录。
func (s *Redis) Claim(ctx context.Context, ids []string, tenant, key, model string) (Record, func(context.Context, bool) error, error) {
	keys, err := keysFor(ids)
	if err != nil {
		return Record{}, nil, err
	}
	value, err := s.client.Get(ctx, keys[0]).Result()
	if errors.Is(err, redis.Nil) {
		return Record{}, nil, ErrExpired
	}
	if err != nil || !strings.HasPrefix(value, "ready\n") || len(value) > 16384 {
		return Record{}, nil, ErrUnavailable
	}
	var record Record
	if json.Unmarshal([]byte(value[6:]), &record) != nil || record.TenantID != tenant || record.KeyID != key || record.Model != model {
		return Record{}, nil, ErrUnavailable
	}
	a, b := append([]string(nil), ids...), append([]string(nil), record.IDs...)
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		return Record{}, nil, ErrUnavailable
	}
	for i := range a {
		if a[i] != b[i] {
			return Record{}, nil, ErrUnavailable
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Record{}, nil, ErrUnavailable
	}
	claimed := hex.EncodeToString(nonce[:]) + "\n" + value[6:]
	n, err := claimScript.Run(ctx, s.client, keys, value, claimed).Int()
	if err != nil || n != 1 {
		return Record{}, nil, ErrUnavailable
	}
	finish := func(ctx context.Context, commit bool) error {
		var n int
		var err error
		if commit {
			n, err = commitScript.Run(ctx, s.client, keys, claimed, "used\n"+value[6:], usedTTL.Milliseconds()).Int()
		} else {
			n, err = claimScript.Run(ctx, s.client, keys, claimed, value).Int()
		}
		if err != nil || n != 1 {
			return ErrUnavailable
		}
		return nil
	}
	return record, finish, nil
}
