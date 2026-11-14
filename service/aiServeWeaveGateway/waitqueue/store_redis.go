package waitqueue

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyTTL expires an idle queue's keys; every join refreshes it. A queue that
// is waited on is never idle for this long, since MaxWait is far shorter.
const keyTTL = 24 * time.Hour

// purgeBatch bounds how many expired tickets one script removes, so a script
// never runs for long even after a mass expiry; the next call removes more.
const purgeBatch = 256

// The scripts read time from Redis (TIME) rather than taking it from the
// replica, so lease expiry and stall detection do not depend on replica
// clocks agreeing. Redis 7 replicates script effects, which is what makes
// TIME followed by writes safe.
//
// KEYS: 1 tickets (ZSET seq → ticket), 2 leases (ZSET expiry ms → ticket),
// 3 seq (INCR counter), 4 progress (ms of the last queued dispatch). All four
// share one hash tag so they live in one Redis Cluster slot.
//
// 脚本从 Redis 读取时间（TIME），而不是由副本提供，使租约过期与停滞判断不依赖副本时钟
// 一致。Redis 7 复制脚本效果，这正是「先 TIME 再写入」安全的原因。
var scriptPrelude = `
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now, 'LIMIT', 0, ` + strconv.Itoa(purgeBatch) + `)
for i = 1, #expired do
  redis.call('ZREM', KEYS[1], expired[i])
  redis.call('ZREM', KEYS[2], expired[i])
end
`

// ARGV: 1 ticket, 2 max waiters, 3 lease ms, 4 key ttl ms.
var joinScript = redis.NewScript(scriptPrelude + `
local n = redis.call('ZCARD', KEYS[1])
if n >= tonumber(ARGV[2]) then return 0 end
if n == 0 then redis.call('SET', KEYS[4], now, 'PX', ARGV[4]) end
local seq = redis.call('INCR', KEYS[3])
redis.call('ZADD', KEYS[1], seq, ARGV[1])
redis.call('ZADD', KEYS[2], now + tonumber(ARGV[3]), ARGV[1])
for i = 1, 3 do redis.call('PEXPIRE', KEYS[i], ARGV[4]) end
return 1
`)

// ARGV: 1 ticket, 2 replica prefix, 3 lease ms, 4 head window, 5 stall ms.
// Returns 1 eligible, 0 wait, -1 lost.
var pollScript = redis.NewScript(scriptPrelude + `
if not redis.call('ZSCORE', KEYS[1], ARGV[1]) then return -1 end
redis.call('ZADD', KEYS[2], now + tonumber(ARGV[3]), ARGV[1])
local rank = redis.call('ZRANK', KEYS[1], ARGV[1])
if rank < tonumber(ARGV[4]) then return 1 end
local progress = tonumber(redis.call('GET', KEYS[4]) or '0')
if now - progress <= tonumber(ARGV[5]) then return 0 end
local prefix = ARGV[2]
local ahead = redis.call('ZRANGE', KEYS[1], 0, rank)
for i = 1, #ahead do
  if string.sub(ahead[i], 1, #prefix) == prefix then
    if ahead[i] == ARGV[1] then return 1 end
    return 0
  end
end
return 0
`)

// ARGV: 1 ticket, 2 dispatched ("1" or "0"), 3 key ttl ms.
var leaveScript = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('ZREM', KEYS[2], ARGV[1])
if ARGV[2] == '1' then
  local t = redis.call('TIME')
  local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
  redis.call('SET', KEYS[4], now, 'PX', ARGV[3])
end
return 1
`)

type redisStore struct {
	client redis.UniversalClient
	keys   []string
}

func newRedisStore(client redis.UniversalClient, name string) *redisStore {
	base := "aisw:wq:{" + name + "}:"
	return &redisStore{client: client, keys: []string{base + "tickets", base + "leases", base + "seq", base + "progress"}}
}

func (s *redisStore) join(ctx context.Context, ticket string, maxWaiters int, lease time.Duration) (bool, error) {
	n, err := joinScript.Run(ctx, s.client, s.keys, ticket, maxWaiters, lease.Milliseconds(), keyTTL.Milliseconds()).Int()
	return n == 1, err
}

func (s *redisStore) poll(ctx context.Context, ticket, prefix string, lease time.Duration, window int, stall time.Duration) (pollResult, error) {
	n, err := pollScript.Run(ctx, s.client, s.keys, ticket, prefix, lease.Milliseconds(), window, stall.Milliseconds()).Int()
	if err != nil {
		return pollWait, err
	}
	switch n {
	case 1:
		return pollEligible, nil
	case -1:
		return pollLost, nil
	default:
		return pollWait, nil
	}
}

func (s *redisStore) leave(ctx context.Context, ticket string, dispatched bool) error {
	flag := "0"
	if dispatched {
		flag = "1"
	}
	return leaveScript.Run(ctx, s.client, s.keys, ticket, flag, keyTTL.Milliseconds()).Err()
}
