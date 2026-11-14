package jobqueue

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store is the shared record of queued jobs: their state machine, their
// position, and where a dispatched one runs. asynq delivers tasks; the store
// decides whether a delivery may submit, which is what keeps an at-least-once
// delivery from ever submitting a job twice. The Redis implementation is
// below; jobqueuetest has an in-memory one.
//
// Store 是排队 job 的共享记录：状态机、排队名次，以及已派发者在哪里运行。asynq 负责投递
// 任务；store 决定一次投递能否提交——这正是让「至少一次」的投递绝不会把一个 job 提交两次
// 的关键。Redis 实现在下面；jobqueuetest 有一个内存实现。
type Store interface {
	// Admit records rec as queued unless maxLength jobs are already
	// admitted, returning its 1-based position. Admitted entries older than
	// horizon are purged first. ErrQueueFull when full.
	Admit(ctx context.Context, rec Record, maxLength int, horizon, ttl time.Duration) (int, error)
	// Withdraw moves a queued job to cancelled and drops it from the
	// queue, reporting whether it was still queued.
	Withdraw(ctx context.Context, jobID string, now time.Time) (bool, error)
	// Get returns the job's record; false when there is none.
	Get(ctx context.Context, jobID string) (Record, bool, error)
	// Position returns the job's 1-based position, or 0 when not queued.
	Position(ctx context.Context, jobID string) (int, error)
	// Depth returns how many jobs are admitted.
	Depth(ctx context.Context) (int, error)
	// Snapshot returns the job's record and its 1-based position (0 when
	// not queued) in one round trip.
	Snapshot(ctx context.Context, jobID string) (Record, int, bool, error)
	// BeginDispatch moves queued → dispatching with intent. It is
	// idempotent for the same intent, so a retried call whose first reply
	// was lost still succeeds. A job whose cancel was requested is moved to
	// cancelled instead.
	BeginDispatch(ctx context.Context, jobID, intent string, now time.Time) (BeginResult, error)
	// Requeue moves dispatching(intent) → queued, or to cancelled when a
	// cancel was requested meanwhile (reported as true).
	Requeue(ctx context.Context, jobID, intent string, now time.Time) (cancelled bool, err error)
	// Dispatched moves dispatching(intent) → dispatched with b and drops the
	// job from the queue.
	Dispatched(ctx context.Context, jobID, intent string, b Binding, now time.Time) (DispatchResult, error)
	// Fail moves a job in state from to failed with code and drops it from
	// the queue; false when it was not in from.
	Fail(ctx context.Context, jobID string, from State, code string, now time.Time) (bool, error)
	// Cancel cancels a queued job, flags a dispatching one, or reports that
	// it is already dispatched or finished.
	Cancel(ctx context.Context, jobID string, now time.Time) (CancelResult, error)
}

// redisStore keeps one HASH per job and one ZSET of admitted jobs, both under
// one hash tag so they share a Redis Cluster slot. Every transition is one
// Lua script, so a check and its write cannot interleave with another
// replica's.
//
// redisStore 为每个 job 保存一个 HASH，另有一个已准入 job 的 ZSET，二者在同一个 hash tag
// 下，共享一个 Redis Cluster 槽。每次状态转换都是一个 Lua 脚本，检查与写入之间不会插入
// 其他副本的操作。
type redisStore struct {
	client   redis.UniversalClient
	admitted string
	jobKey   string // prefix; + job id
}

// NewRedisStore returns the Redis Store for the queue named name.
func NewRedisStore(client redis.UniversalClient, name string) Store {
	base := "aisw:jq:{" + name + "}:"
	return &redisStore{client: client, admitted: base + "admitted", jobKey: base + "job:"}
}

func (s *redisStore) keys(jobID string) []string { return []string{s.admitted, s.jobKey + jobID} }

func ms(t time.Time) int64 { return t.UnixMilli() }

// ARGV: 1 job id, 2 max length, 3 horizon ms, 4 ttl ms, 5.. field/value pairs.
// Returns position, -1 full, -2 id conflict.
var admitScript = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - tonumber(ARGV[3]))
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[2]) then return -1 end
if redis.call('EXISTS', KEYS[2]) == 1 then return -2 end
for i = 5, #ARGV, 2 do redis.call('HSET', KEYS[2], ARGV[i], ARGV[i + 1]) end
redis.call('PEXPIRE', KEYS[2], ARGV[4])
redis.call('ZADD', KEYS[1], now, ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[4])
return redis.call('ZRANK', KEYS[1], ARGV[1]) + 1
`)

// beginScript: ARGV 1 job id, 2 intent, 3 now ms. Returns -1 missing,
// 0 not queued, 1 begun (or already begun with this intent), 3 cancelled.
var beginScript = redis.NewScript(`
local st = redis.call('HGET', KEYS[2], 'state')
if not st then return -1 end
if st == 'dispatching' and redis.call('HGET', KEYS[2], 'intent') == ARGV[2] then return 1 end
if st ~= 'queued' then return 0 end
if redis.call('HGET', KEYS[2], 'cancel_requested') == '1' then
  redis.call('HSET', KEYS[2], 'state', 'cancelled', 'error_code', 'cancelled', 'updated', ARGV[3])
  redis.call('ZREM', KEYS[1], ARGV[1])
  return 3
end
redis.call('HSET', KEYS[2], 'state', 'dispatching', 'intent', ARGV[2], 'updated', ARGV[3])
return 1
`)

// requeueScript: ARGV 1 job id, 2 intent, 3 now ms. Returns 0 precondition
// failed, 1 requeued, 3 cancelled.
var requeueScript = redis.NewScript(`
if redis.call('HGET', KEYS[2], 'state') ~= 'dispatching' then return 0 end
if redis.call('HGET', KEYS[2], 'intent') ~= ARGV[2] then return 0 end
if redis.call('HGET', KEYS[2], 'cancel_requested') == '1' then
  redis.call('HSET', KEYS[2], 'state', 'cancelled', 'error_code', 'cancelled', 'updated', ARGV[3])
  redis.call('ZREM', KEYS[1], ARGV[1])
  return 3
end
redis.call('HSET', KEYS[2], 'state', 'queued', 'updated', ARGV[3])
return 1
`)

// transitionScript is every other compare-and-set transition.
// ARGV: 1 job id, 2 expected state, 3 expected intent (” = any),
// 4 drop from queue ('1'/'0'), 5.. field/value pairs.
// Returns -1 missing, 0 precondition failed, 1 done, 2 done and a cancel was
// requested.
var transitionScript = redis.NewScript(`
local st = redis.call('HGET', KEYS[2], 'state')
if not st then return -1 end
if st ~= ARGV[2] then return 0 end
if ARGV[3] ~= '' and redis.call('HGET', KEYS[2], 'intent') ~= ARGV[3] then return 0 end
for i = 5, #ARGV, 2 do redis.call('HSET', KEYS[2], ARGV[i], ARGV[i + 1]) end
if ARGV[4] == '1' then redis.call('ZREM', KEYS[1], ARGV[1]) end
if redis.call('HGET', KEYS[2], 'cancel_requested') == '1' then return 2 end
return 1
`)

// ARGV: 1 job id, 2 now ms. Returns -1 missing, else a CancelResult.
var cancelScript = redis.NewScript(`
local st = redis.call('HGET', KEYS[2], 'state')
if not st then return -1 end
if st == 'queued' then
  redis.call('HSET', KEYS[2], 'state', 'cancelled', 'error_code', 'cancelled', 'updated', ARGV[2])
  redis.call('ZREM', KEYS[1], ARGV[1])
  return 1
end
if st == 'dispatching' then
  redis.call('HSET', KEYS[2], 'cancel_requested', '1', 'updated', ARGV[2])
  return 2
end
if st == 'dispatched' then return 3 end
return 4
`)

func (s *redisStore) Admit(ctx context.Context, rec Record, maxLength int, horizon, ttl time.Duration) (int, error) {
	args := []any{rec.JobID, maxLength, horizon.Milliseconds(), ttl.Milliseconds(),
		"state", string(StateQueued), "tenant", rec.TenantID, "workflow_id", rec.WorkflowID,
		"workflow_version", rec.WorkflowVersion, "kind", rec.Kind,
		"created", ms(rec.CreatedAt), "updated", ms(rec.CreatedAt)}
	n, err := admitScript.Run(ctx, s.client, s.keys(rec.JobID), args...).Int()
	switch {
	case err != nil:
		return 0, err
	case n == -1:
		return 0, ErrQueueFull
	case n == -2:
		return 0, ErrIndeterminate
	}
	return n, nil
}

func (s *redisStore) transition(ctx context.Context, jobID string, from State, intent string, drop bool, pairs ...any) (int, error) {
	dropFlag := "0"
	if drop {
		dropFlag = "1"
	}
	args := append([]any{jobID, string(from), intent, dropFlag}, pairs...)
	return transitionScript.Run(ctx, s.client, s.keys(jobID), args...).Int()
}

func (s *redisStore) Withdraw(ctx context.Context, jobID string, now time.Time) (bool, error) {
	n, err := s.transition(ctx, jobID, StateQueued, "", true, "state", string(StateCancelled), "error_code", CodeCancelled, "updated", ms(now))
	return n > 0, err
}

func (s *redisStore) Get(ctx context.Context, jobID string) (Record, bool, error) {
	fields, err := s.client.HGetAll(ctx, s.jobKey+jobID).Result()
	if err != nil || len(fields) == 0 {
		return Record{}, false, err
	}
	return recordFrom(jobID, fields), true, nil
}

func recordFrom(jobID string, fields map[string]string) Record {
	millis := func(name string) time.Time {
		v, _ := strconv.ParseInt(fields[name], 10, 64)
		if v == 0 {
			return time.Time{}
		}
		return time.UnixMilli(v).UTC()
	}
	return Record{
		JobID:           jobID,
		TenantID:        fields["tenant"],
		WorkflowID:      fields["workflow_id"],
		WorkflowVersion: fields["workflow_version"],
		Kind:            fields["kind"],
		State:           State(fields["state"]),
		CancelRequested: fields["cancel_requested"] == "1",
		Binding: Binding{NodeID: fields["node_id"], RuntimeID: fields["runtime_id"],
			Kind: fields["runtime_kind"], RunID: fields["run_id"]},
		ErrorCode: fields["error_code"],
		CreatedAt: millis("created"),
		UpdatedAt: millis("updated"),
	}
}

func (s *redisStore) Position(ctx context.Context, jobID string) (int, error) {
	rank, err := s.client.ZRank(ctx, s.admitted, jobID).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return int(rank) + 1, nil
}

func (s *redisStore) Depth(ctx context.Context) (int, error) {
	n, err := s.client.ZCard(ctx, s.admitted).Result()
	return int(n), err
}

func (s *redisStore) Snapshot(ctx context.Context, jobID string) (Record, int, bool, error) {
	pipe := s.client.Pipeline()
	fields := pipe.HGetAll(ctx, s.jobKey+jobID)
	rank := pipe.ZRank(ctx, s.admitted, jobID)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return Record{}, 0, false, err
	}
	values, err := fields.Result()
	if err != nil || len(values) == 0 {
		return Record{}, 0, false, err
	}
	position := 0
	if r, err := rank.Result(); err == nil {
		position = int(r) + 1
	}
	return recordFrom(jobID, values), position, true, nil
}

func (s *redisStore) BeginDispatch(ctx context.Context, jobID, intent string, now time.Time) (BeginResult, error) {
	n, err := beginScript.Run(ctx, s.client, s.keys(jobID), jobID, intent, ms(now)).Int()
	switch {
	case err != nil:
		return 0, err
	case n == 1:
		return Begun, nil
	case n == 3:
		return BeginCancelled, nil
	default:
		return BeginNotQueued, nil
	}
}

func (s *redisStore) Requeue(ctx context.Context, jobID, intent string, now time.Time) (bool, error) {
	n, err := requeueScript.Run(ctx, s.client, s.keys(jobID), jobID, intent, ms(now)).Int()
	return n == 3, err
}

func (s *redisStore) Dispatched(ctx context.Context, jobID, intent string, b Binding, now time.Time) (DispatchResult, error) {
	n, err := s.transition(ctx, jobID, StateDispatching, intent, true,
		"state", string(StateDispatched), "node_id", b.NodeID, "runtime_id", b.RuntimeID,
		"runtime_kind", b.Kind, "run_id", b.RunID, "updated", ms(now))
	return DispatchResult{Recorded: n > 0, CancelRequested: n == 2}, err
}

func (s *redisStore) Fail(ctx context.Context, jobID string, from State, code string, now time.Time) (bool, error) {
	n, err := s.transition(ctx, jobID, from, "", true, "state", string(StateFailed), "error_code", code, "updated", ms(now))
	return n > 0, err
}

func (s *redisStore) Cancel(ctx context.Context, jobID string, now time.Time) (CancelResult, error) {
	n, err := cancelScript.Run(ctx, s.client, s.keys(jobID), jobID, ms(now)).Int()
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, ErrNotFound
	}
	return CancelResult(n), nil
}
