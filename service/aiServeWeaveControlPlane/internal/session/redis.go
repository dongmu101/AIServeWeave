package session

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/redis/go-redis/v9"

	"AIServeWeave/common/runtime"
)

const (
	sessionKeyPrefix = "aisw:session:v1:"
	indexKeyPrefix   = "aisw:subject-sessions:v1:"
	gateKeyPrefix    = "aisw:subject-mutation:v1:"
)

var createScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[3]) == 1 then
  return -1
end
if redis.call('EXISTS', KEYS[1]) == 1 then
  return -2
end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[1])
local stored = redis.call('SET', KEYS[1], ARGV[4], 'PXAT', ARGV[2], 'NX')
if not stored then
  return -2
end
redis.call('ZADD', KEYS[2], ARGV[2], ARGV[3])
local overflow = redis.call('ZCARD', KEYS[2]) - tonumber(ARGV[5])
if overflow > 1 then
  redis.call('DEL', KEYS[1])
  redis.call('ZREM', KEYS[2], ARGV[3])
  return -3
end
if overflow > 0 then
  local victims = redis.call('ZRANGE', KEYS[2], 0, overflow - 1)
  for _, id in ipairs(victims) do
    redis.call('DEL', ARGV[6] .. id)
    redis.call('ZREM', KEYS[2], id)
  end
end
local last = redis.call('ZREVRANGE', KEYS[2], 0, 0, 'WITHSCORES')
if #last == 2 then
  redis.call('PEXPIREAT', KEYS[2], last[2])
end
return 1
`)

var validateScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 1 then
  return false
end
return redis.call('GET', KEYS[1])
`)

var revokeScript = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then
  redis.call('ZREM', KEYS[2], ARGV[3])
  return 0
end
local record = cjson.decode(raw)
if record.subject.subject_kind ~= ARGV[1] or record.subject.subject_id ~= ARGV[2] then
  return 0
end
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[3])
if redis.call('ZCARD', KEYS[2]) == 0 then
  redis.call('DEL', KEYS[2])
end
return 1
`)

var revokeAllScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if redis.call('ZCARD', KEYS[1]) > tonumber(ARGV[3]) then
  return -1
end
local ids = redis.call('ZRANGE', KEYS[1], 0, -1)
for _, id in ipairs(ids) do
  redis.call('DEL', ARGV[2] .. id)
end
redis.call('DEL', KEYS[1])
return #ids
`)

var beginMutationScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 1 then
  return -1
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[3])
if redis.call('ZCARD', KEYS[1]) > tonumber(ARGV[5]) then
  return -2
end
redis.call('SET', KEYS[2], ARGV[1], 'PX', ARGV[2])
local ids = redis.call('ZRANGE', KEYS[1], 0, -1)
for _, id in ipairs(ids) do
  redis.call('DEL', ARGV[4] .. id)
end
redis.call('DEL', KEYS[1])
return #ids
`)

var endMutationScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
redis.call('DEL', KEYS[1])
return 1
`)

// Redis is a Lua-backed Store over one Redis client.
//
// Redis 是基于 Lua、使用一个 Redis 客户端的 Store。
type Redis struct {
	client *redis.Client
	clock  runtime.Clock
}

// NewRedis returns a session store using client. The caller owns the client
// lifecycle so it can share one pool with the API Key verification cache.
//
// NewRedis 返回一个使用 client 的会话存储。客户端生命周期由调用方持有，因此它可以
// 与 API Key 校验缓存共用一个连接池。
func NewRedis(client *redis.Client, clock runtime.Clock) *Redis {
	return &Redis{client: client, clock: clockOrSystem(clock)}
}

// Create atomically adds, expires, prunes, and bounds one subject's sessions.
//
// Create 原子地添加、设置过期、清理并限制一个主体的会话。
func (r *Redis) Create(ctx context.Context, record Record) error {
	if r == nil || r.client == nil {
		return ErrUnavailable
	}
	now := r.clock.Now()
	if !validRecord(record, now) {
		return ErrInvalid
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return ErrInvalid
	}
	result, err := createScript.Run(ctx, r.client, []string{
		sessionKey(record.ID), indexKey(record.Subject), gateKey(record.Subject),
	}, now.UnixMilli(), record.ExpiresAt.UnixMilli(), record.ID, payload, MaxSessions, sessionKeyPrefix).Int64()
	if err != nil {
		return unavailable(err)
	}
	switch result {
	case 1:
		return nil
	case -1:
		return ErrMutationActive
	case -3:
		return ErrUnavailable
	default:
		return ErrInvalid
	}
}

// Validate accepts only an exact live record while its login gate is open.
//
// Validate 只在登录门开放时接受身份完全匹配且仍有效的记录。
func (r *Redis) Validate(ctx context.Context, id string, subject Subject, tenantID, role string) error {
	if r == nil || r.client == nil {
		return ErrUnavailable
	}
	if id == "" || !validSubject(subject) || tenantID == "" || role == "" {
		return ErrInvalid
	}
	result, err := validateScript.Run(ctx, r.client, []string{sessionKey(id), gateKey(subject)}).Result()
	if errors.Is(err, redis.Nil) {
		return ErrInvalid
	}
	if err != nil {
		return unavailable(err)
	}
	raw, ok := result.(string)
	if !ok {
		return ErrInvalid
	}
	var record Record
	if json.Unmarshal([]byte(raw), &record) != nil || record.ID != id || record.Subject != subject || record.TenantID != tenantID || record.Role != role || !record.ExpiresAt.After(r.clock.Now()) {
		return ErrInvalid
	}
	return nil
}

// Revoke atomically removes one session when it belongs to subject.
//
// Revoke 在一个会话属于 subject 时原子地移除它。
func (r *Redis) Revoke(ctx context.Context, subject Subject, id string) (bool, error) {
	if r == nil || r.client == nil {
		return false, ErrUnavailable
	}
	if id == "" || !validSubject(subject) {
		return false, ErrInvalid
	}
	result, err := revokeScript.Run(ctx, r.client, []string{sessionKey(id), indexKey(subject)}, subject.Kind, subject.ID, id).Int64()
	if err != nil {
		return false, unavailable(err)
	}
	return result == 1, nil
}

// RevokeAll atomically removes every live session owned by subject.
//
// RevokeAll 原子地移除 subject 拥有的所有有效会话。
func (r *Redis) RevokeAll(ctx context.Context, subject Subject) (int, error) {
	if r == nil || r.client == nil {
		return 0, ErrUnavailable
	}
	if !validSubject(subject) {
		return 0, ErrInvalid
	}
	result, err := revokeAllScript.Run(ctx, r.client, []string{indexKey(subject)}, r.clock.Now().UnixMilli(), sessionKeyPrefix, MaxSessions).Int64()
	if err != nil {
		return 0, unavailable(err)
	}
	if result < 0 {
		return 0, ErrUnavailable
	}
	return int(result), nil
}

// BeginMutation closes a subject's login gate and revokes its sessions in one
// Redis operation.
//
// BeginMutation 在一次 Redis 操作中关闭主体的登录门并吊销其会话。
func (r *Redis) BeginMutation(ctx context.Context, subject Subject) (Gate, error) {
	if r == nil || r.client == nil {
		return Gate{}, ErrUnavailable
	}
	if !validSubject(subject) {
		return Gate{}, ErrInvalid
	}
	nonce := randomHex()
	result, err := beginMutationScript.Run(ctx, r.client, []string{indexKey(subject), gateKey(subject)}, nonce, mutationTTL.Milliseconds(), r.clock.Now().UnixMilli(), sessionKeyPrefix, MaxSessions).Int64()
	if err != nil {
		return Gate{}, unavailable(err)
	}
	if result == -1 {
		return Gate{}, ErrMutationActive
	}
	if result < 0 {
		return Gate{}, ErrUnavailable
	}
	return Gate{Subject: subject, Nonce: nonce, RevokedSessions: int(result)}, nil
}

// EndMutation opens a gate only when its nonce still owns it.
//
// EndMutation 仅在 nonce 仍拥有变更门时重新开放它。
func (r *Redis) EndMutation(ctx context.Context, gate Gate) error {
	if r == nil || r.client == nil {
		return ErrUnavailable
	}
	if !validSubject(gate.Subject) || gate.Nonce == "" {
		return ErrInvalid
	}
	result, err := endMutationScript.Run(ctx, r.client, []string{gateKey(gate.Subject)}, gate.Nonce).Int64()
	if err != nil {
		return unavailable(err)
	}
	if result != 1 {
		return ErrInvalid
	}
	return nil
}

func sessionKey(id string) string { return sessionKeyPrefix + id }

func indexKey(subject Subject) string {
	return indexKeyPrefix + subject.Kind + ":" + subject.ID
}

func gateKey(subject Subject) string {
	return gateKeyPrefix + subject.Kind + ":" + subject.ID
}

func unavailable(err error) error {
	return errors.Join(ErrUnavailable, err)
}

var _ Store = (*Redis)(nil)
