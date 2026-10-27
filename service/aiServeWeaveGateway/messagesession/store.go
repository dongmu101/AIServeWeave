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
	"sort"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"github.com/redis/go-redis/v9"
)

// ErrUnavailable rejects absent, consumed, mismatched or concurrently claimed continuations.
// ErrUnavailable 拒绝不存在、已消费、不匹配或被并发认领的续接。
var ErrUnavailable = errors.New("Messages continuation unavailable")

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

// Publish stores no prompt or tool result; each record expires within two minutes.
// Publish 不存储提示词或工具结果；每条记录在两分钟内过期。
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
	n, err := publishScript.Run(ctx, s.client, keys, "ready\n"+string(data), (2 * time.Minute).Milliseconds()).Int()
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

// Claim checks ownership and the complete result set before acquiring a one-shot lease.
// Claim 在取得一次性租约前检查归属与完整结果集合。
func (s *Redis) Claim(ctx context.Context, ids []string, tenant, key, model string) (Record, func(context.Context, bool) error, error) {
	keys, err := keysFor(ids)
	if err != nil {
		return Record{}, nil, err
	}
	value, err := s.client.Get(ctx, keys[0]).Result()
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
		next := value
		if commit {
			next = "used\n" + value[6:]
		}
		n, err := claimScript.Run(ctx, s.client, keys, claimed, next).Int()
		if err != nil || n != 1 {
			return ErrUnavailable
		}
		return nil
	}
	return record, finish, nil
}
