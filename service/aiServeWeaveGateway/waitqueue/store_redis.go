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

// Key layout. Every key carries the hash tag {name}, so all of them share one
// Redis Cluster slot and a script may touch any of them.
//
//	<base>leases     ZSET  ticket → lease expiry ms; one for the whole queue,
//	                       so ZCARD is the cluster-wide waiter count
//	<base>seq        INCR  ticket order across every lane
//	<base>t:<lane>   ZSET  ticket → seq; one FIFO per lane (model)
//	<base>p:<lane>   ms of the lane's last queued dispatch
//
// A ticket is "<replica>|<lane>|<random>", so the purge can find the lane
// FIFO of an expired ticket from its member alone.
//
// 键布局。每个键都带 hash tag {name}，因此全部位于同一个 Redis Cluster 槽，脚本可以访问
// 其中任意一个。ticket 的格式是 "<replica>|<lane>|<random>"，清理时只凭成员本身就能找到
// 过期票所在通道的 FIFO。

// The scripts read time from Redis (TIME) rather than taking it from the
// replica, so lease expiry and stall detection do not depend on replica
// clocks agreeing. Redis 7 replicates script effects, which is what makes
// TIME followed by writes safe.
//
// 脚本从 Redis 读取时间（TIME），而不是由副本提供，使租约过期与停滞判断不依赖副本时钟
// 一致。Redis 7 复制脚本效果，这正是「先 TIME 再写入」安全的原因。
//
// KEYS: 1 leases, 2 seq, 3 lane tickets, 4 lane progress. ARGV[1] is <base>.
var scriptPrelude = `
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now, 'LIMIT', 0, ` + strconv.Itoa(purgeBatch) + `)
for i = 1, #expired do
  local lane = string.match(expired[i], '^[^|]*|([^|]*)|')
  if lane then redis.call('ZREM', ARGV[1] .. 't:' .. lane, expired[i]) end
  redis.call('ZREM', KEYS[1], expired[i])
end
`

// ARGV: 1 base, 2 ticket, 3 max waiters, 4 lease ms, 5 key ttl ms.
// Returns 1 joined, 0 full.
var joinScript = redis.NewScript(scriptPrelude + `
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[3]) then return 0 end
if redis.call('ZCARD', KEYS[3]) == 0 then redis.call('SET', KEYS[4], now, 'PX', ARGV[5]) end
local seq = redis.call('INCR', KEYS[2])
redis.call('ZADD', KEYS[3], seq, ARGV[2])
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[4]), ARGV[2])
for i = 1, 3 do redis.call('PEXPIRE', KEYS[i], ARGV[5]) end
return 1
`)

// pollScript renews the ticket's lease and decides whether it may run a
// dispatch pass. An eligible ticket is claimed: removed from the lane and
// the leases, and its seq returned, so it does not hold the head window or
// count as waiting while its pass runs (a non-streaming chat runs the whole
// inference inside that pass). A pass that finds every candidate busy puts
// it back at the same seq (rejoinScript), keeping its place.
//
// ARGV: 1 base, 2 ticket, 3 replica prefix, 4 lease ms, 5 head window,
// 6 stall ms. Returns seq (> 0) when claimed, 0 to keep waiting, -1 lost.
var pollScript = redis.NewScript(scriptPrelude + `
local seq = redis.call('ZSCORE', KEYS[3], ARGV[2])
if not seq then return -1 end
local rank = redis.call('ZRANK', KEYS[3], ARGV[2])
local eligible = rank < tonumber(ARGV[5])
if not eligible then
  local progress = tonumber(redis.call('GET', KEYS[4]) or '0')
  if now - progress > tonumber(ARGV[6]) then
    local prefix = ARGV[3]
    local ahead = redis.call('ZRANGE', KEYS[3], 0, rank)
    for i = 1, #ahead do
      if string.sub(ahead[i], 1, #prefix) == prefix then
        eligible = (ahead[i] == ARGV[2])
        break
      end
    end
  end
end
if not eligible then
  redis.call('ZADD', KEYS[1], now + tonumber(ARGV[4]), ARGV[2])
  return 0
end
redis.call('ZREM', KEYS[3], ARGV[2])
redis.call('ZREM', KEYS[1], ARGV[2])
return tonumber(seq)
`)

// rejoinScript puts a claimed ticket back at its original seq. It skips the
// cap: the request was already admitted, and refusing it here would answer
// it after its place in line was taken by someone who arrived later.
//
// ARGV: 1 base, 2 ticket, 3 seq, 4 lease ms, 5 key ttl ms.
var rejoinScript = redis.NewScript(scriptPrelude + `
redis.call('ZADD', KEYS[3], tonumber(ARGV[3]), ARGV[2])
redis.call('ZADD', KEYS[1], now + tonumber(ARGV[4]), ARGV[2])
redis.call('PEXPIRE', KEYS[1], ARGV[5])
redis.call('PEXPIRE', KEYS[3], ARGV[5])
return 1
`)

// ARGV: 1 ticket. Removes a ticket that is still queued.
var leaveScript = redis.NewScript(`
redis.call('ZREM', KEYS[3], ARGV[1])
redis.call('ZREM', KEYS[1], ARGV[1])
return 1
`)

// ARGV: 1 key ttl ms. Records a queued dispatch on the lane.
var progressScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('SET', KEYS[4], now, 'PX', ARGV[1])
return 1
`)

type redisStore struct {
	client redis.UniversalClient
	base   string
}

func newRedisStore(client redis.UniversalClient, name string) *redisStore {
	return &redisStore{client: client, base: "aisw:wq:{" + name + "}:"}
}

func (s *redisStore) keys(lane string) []string {
	return []string{s.base + "leases", s.base + "seq", s.base + "t:" + lane, s.base + "p:" + lane}
}

func (s *redisStore) join(ctx context.Context, lane, ticket string, maxWaiters int, lease time.Duration) (bool, error) {
	n, err := joinScript.Run(ctx, s.client, s.keys(lane), s.base, ticket, maxWaiters, lease.Milliseconds(), keyTTL.Milliseconds()).Int()
	return n == 1, err
}

func (s *redisStore) poll(ctx context.Context, lane, ticket, prefix string, lease time.Duration, window int, stall time.Duration) (pollResult, int64, error) {
	n, err := pollScript.Run(ctx, s.client, s.keys(lane), s.base, ticket, prefix, lease.Milliseconds(), window, stall.Milliseconds()).Int64()
	switch {
	case err != nil:
		return pollWait, 0, err
	case n > 0:
		return pollEligible, n, nil
	case n < 0:
		return pollLost, 0, nil
	default:
		return pollWait, 0, nil
	}
}

func (s *redisStore) rejoin(ctx context.Context, lane, ticket string, seq int64, lease time.Duration) error {
	return rejoinScript.Run(ctx, s.client, s.keys(lane), s.base, ticket, seq, lease.Milliseconds(), keyTTL.Milliseconds()).Err()
}

func (s *redisStore) leave(ctx context.Context, lane, ticket string) error {
	return leaveScript.Run(ctx, s.client, s.keys(lane), ticket).Err()
}

func (s *redisStore) progress(ctx context.Context, lane string) error {
	return progressScript.Run(ctx, s.client, s.keys(lane), keyTTL.Milliseconds()).Err()
}
