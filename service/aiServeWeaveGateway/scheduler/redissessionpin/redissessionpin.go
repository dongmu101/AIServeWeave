// Package redissessionpin is the Redis implementation of scheduler.SessionPins:
// one key per session, holding the candidate that last served it and expiring
// with the pin's TTL, so every Gateway replica agrees on where a conversation
// lives.
//
// Only the 64-bit hash of the tenant-scoped session key ever reaches Redis —
// the same hash the scheduler ranks by — never the key itself.
//
// redissessionpin 包是 scheduler.SessionPins 的 Redis 实现：每个会话一个键，保存上一次
// 服务它的候选，随钉选的 TTL 过期，使所有 Gateway 副本对一段对话所在之处达成一致。
//
// 写入 Redis 的只有按租户限定的会话 key 的 64 位哈希——与调度器排序所用的哈希相同——
// 从不包含 key 本身。
package redissessionpin

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

// DefaultPrefix namespaces the pin keys under the Gateway's aisw: namespace.
//
// DefaultPrefix 把钉选键放在 Gateway 的 aisw: 命名空间下。
const DefaultPrefix = "aisw:sched:session-pin:"

// maxValueBytes bounds a stored value that is read back, so a corrupted key
// cannot make a request decode an arbitrary blob.
//
// maxValueBytes 限定读回的存储值大小，使被破坏的键不会让请求去解码任意大的数据。
const maxValueBytes = 4096

// Store is a scheduler.SessionPins backed by Redis.
//
// Store 是由 Redis 支撑的 scheduler.SessionPins。
type Store struct {
	client *redis.Client
	prefix string
}

var _ scheduler.SessionPins = (*Store)(nil)

// New returns a Store on client; an empty prefix uses DefaultPrefix.
//
// New 返回在 client 上的 Store；prefix 为空时使用 DefaultPrefix。
func New(client *redis.Client, prefix string) (*Store, error) {
	if client == nil {
		return nil, errors.New("redissessionpin: New needs a client")
	}
	if prefix == "" {
		prefix = DefaultPrefix
	}
	return &Store{client: client, prefix: prefix}, nil
}

type value struct {
	NodeID    string `json:"n"`
	RuntimeID string `json:"r"`
	Kind      string `json:"k"`
	Model     string `json:"m"`
	At        int64  `json:"t"`
}

func (s *Store) key(hash uint64) string {
	return s.prefix + strconv.FormatUint(hash, 16)
}

// Get implements scheduler.SessionPins. A value that does not decode reads as
// no pin: the session falls back to its hash ranking, and the next success
// overwrites it.
//
// Get 实现 scheduler.SessionPins。无法解码的值按没有钉选处理：会话退回哈希排序，下一次
// 成功会覆盖它。
func (s *Store) Get(ctx context.Context, hash uint64) (scheduler.SessionPin, bool, error) {
	raw, err := s.client.Get(ctx, s.key(hash)).Result()
	if errors.Is(err, redis.Nil) {
		return scheduler.SessionPin{}, false, nil
	}
	if err != nil {
		return scheduler.SessionPin{}, false, fmt.Errorf("redissessionpin: get: %w", err)
	}
	var v value
	if len(raw) > maxValueBytes || json.Unmarshal([]byte(raw), &v) != nil || v.NodeID == "" || v.RuntimeID == "" {
		return scheduler.SessionPin{}, false, nil
	}
	return scheduler.SessionPin{
		Candidate: scheduler.Candidate{NodeID: v.NodeID, RuntimeID: v.RuntimeID, Kind: runtime.Kind(v.Kind), Model: v.Model},
		At:        time.UnixMilli(v.At),
	}, true, nil
}

// Set implements scheduler.SessionPins.
//
// Set 实现 scheduler.SessionPins。
func (s *Store) Set(ctx context.Context, hash uint64, pin scheduler.SessionPin, ttl time.Duration) error {
	data, err := json.Marshal(value{NodeID: pin.Candidate.NodeID, RuntimeID: pin.Candidate.RuntimeID,
		Kind: string(pin.Candidate.Kind), Model: pin.Candidate.Model, At: pin.At.UnixMilli()})
	if err != nil {
		return err
	}
	if err := s.client.Set(ctx, s.key(hash), data, ttl).Err(); err != nil {
		return fmt.Errorf("redissessionpin: set: %w", err)
	}
	return nil
}
