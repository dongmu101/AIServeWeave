package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	usageOutboxStream    = "aisw:{usage-ledger}:pending"
	usageOutboxRejected  = "aisw:{usage-ledger}:rejected"
	usageOutboxGroup     = "controlplane"
	usageOutboxCapacity  = 100_000
	usageRejectedLimit   = 5_000
	usageOutboxWriteWait = 250 * time.Millisecond
)

// Cap the stream without trimming usage that has not been acknowledged.
var stageUsageRecord = redis.NewScript(`
if redis.call('XLEN', KEYS[1]) >= tonumber(ARGV[1]) then return 0 end
redis.call('XADD', KEYS[1], '*', 'record', ARGV[2])
return 1
`)

var quarantineUsageRecord = redis.NewScript(`
if #redis.call('XRANGE', KEYS[1], ARGV[2], ARGV[2]) == 0 then return 0 end
redis.call('XADD', KEYS[2], 'MAXLEN', ARGV[3], '*', 'record', ARGV[4], 'reason', ARGV[5])
redis.call('XACK', KEYS[1], ARGV[1], ARGV[2])
return redis.call('XDEL', KEYS[1], ARGV[2])
`)

// usageLedgerOutbox stores finished requests in Redis before handing them to
// a worker. Any replica can reclaim previously delivered, unacknowledged rows.
// The control plane deduplicates on the original request ID and timestamp.
type usageLedgerOutbox struct {
	redis      *redis.Client
	push       UsageLedgerClient
	logger     *slog.Logger
	metrics    *recorder
	fallback   usageLedgerSink
	stream     string
	rejected   string
	consumer   string
	batchSize  int64
	pollEvery  time.Duration
	claimAfter time.Duration
	cancel     context.CancelFunc
	done       chan struct{}
}

func newUsageLedgerOutbox(client *redis.Client, consumer string, pusher *usageLedgerPusher) *usageLedgerOutbox {
	return &usageLedgerOutbox{
		redis: client, push: pusher.client, logger: pusher.logger, metrics: pusher.metrics,
		fallback: pusher, stream: usageOutboxStream, rejected: usageOutboxRejected,
		consumer: consumer, batchSize: int64(pusher.cfg.BatchSize),
		pollEvery:  min(time.Second, pusher.cfg.FlushInterval),
		claimAfter: time.Minute, done: make(chan struct{}),
	}
}

func (o *usageLedgerOutbox) enqueue(record UsageRecord) bool {
	raw, err := json.Marshal(record)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), usageOutboxWriteWait)
		defer cancel()
		var added int
		added, err = stageUsageRecord.Run(ctx, o.redis, []string{o.stream}, usageOutboxCapacity, raw).Int()
		if err == nil && added == 1 {
			return true
		}
	}
	// Keep serving requests if Redis is unavailable or the stream fills.
	// A lost Redis reply can cause a duplicate push, which billing already deduplicates.
	o.metrics.UsageOutboxStageFailed()
	return o.fallback.enqueue(record)
}

func (o *usageLedgerOutbox) start() {
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	go o.run(ctx)
}

func (o *usageLedgerOutbox) stop() {
	o.cancel()
	<-o.done
}

func (o *usageLedgerOutbox) run(ctx context.Context) {
	defer close(o.done)
	var messages []redis.XMessage
	initialized := false
	cursor := "0-0"
	backoff := o.pollEvery
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		var err error
		if !initialized {
			err = o.redis.XGroupCreateMkStream(ctx, o.stream, usageOutboxGroup, "0").Err()
			if redis.HasErrorPrefix(err, "BUSYGROUP") {
				err = nil
			}
			initialized = err == nil
		}
		if err == nil && len(messages) == 0 {
			messages, cursor, err = o.read(ctx, cursor)
		}
		if err == nil && len(messages) != 0 {
			if err = o.deliver(ctx, messages); err == nil {
				messages = nil
				backoff = o.pollEvery
				timer.Reset(0)
				continue
			}
		}
		if ctx.Err() != nil {
			return
		}
		if redis.HasErrorPrefix(err, "NOGROUP") {
			initialized = false
			cursor = "0-0"
			messages = nil
		}
		if err != nil {
			o.logger.Warn("usage outbox drain failed", slog.Any("error", err))
			timer.Reset(backoff)
			backoff = min(backoff*2, MaxUsageLedgerRetryBackoff)
			continue
		}
		backoff = o.pollEvery
		timer.Reset(o.pollEvery)
	}
}

func (o *usageLedgerOutbox) read(ctx context.Context, cursor string) ([]redis.XMessage, string, error) {
	messages, next, err := o.redis.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream: o.stream, Group: usageOutboxGroup, Consumer: o.consumer, Start: cursor,
		MinIdle: o.claimAfter, Count: o.batchSize,
	}).Result()
	if err != nil {
		return messages, cursor, err
	}
	if len(messages) != 0 {
		return messages, next, nil
	}
	streams, err := o.redis.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: usageOutboxGroup, Consumer: o.consumer, Streams: []string{o.stream, ">"},
		Count: o.batchSize, Block: -1,
	}).Result()
	if err == nil && len(streams) != 0 {
		messages = streams[0].Messages
	}
	if err == redis.Nil {
		err = nil
	}
	return messages, next, err
}

func (o *usageLedgerOutbox) deliver(ctx context.Context, messages []redis.XMessage) error {
	records := make([]UsageRecord, 0, len(messages))
	valid := make([]redis.XMessage, 0, len(messages))
	for _, message := range messages {
		raw, _ := message.Values["record"].(string)
		var record UsageRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil || record.RequestID == "" {
			if err := o.quarantine(ctx, message.ID, raw, "invalid_record"); err != nil {
				return err
			}
			continue
		}
		records = append(records, record)
		valid = append(valid, message)
	}
	return o.pushBatch(ctx, valid, records)
}

func (o *usageLedgerOutbox) pushBatch(ctx context.Context, messages []redis.XMessage, records []UsageRecord) error {
	if len(records) == 0 {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, DefaultUsageLedgerCallTimeout)
	err := o.push.PushUsageRecords(callCtx, records)
	cancel()
	if err != nil {
		o.metrics.UsageLedgerPushFailed()
		if !errors.Is(err, ErrUsageBatchRejected) {
			return err
		}
		if len(records) == 1 {
			raw, _ := messages[0].Values["record"].(string)
			return o.quarantine(ctx, messages[0].ID, raw, "controlplane_rejected")
		}
		middle := len(records) / 2
		if err := o.pushBatch(ctx, messages[:middle], records[:middle]); err != nil {
			return err
		}
		return o.pushBatch(ctx, messages[middle:], records[middle:])
	}
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	_, err = o.redis.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.XAck(ctx, o.stream, usageOutboxGroup, ids...)
		pipe.XDel(ctx, o.stream, ids...)
		return nil
	})
	return err
}

func (o *usageLedgerOutbox) quarantine(ctx context.Context, id, raw, reason string) error {
	moved, err := quarantineUsageRecord.Run(ctx, o.redis, []string{o.stream, o.rejected},
		usageOutboxGroup, id, usageRejectedLimit, raw, reason).Int()
	if err == nil && moved != 0 {
		o.metrics.UsageLedgerDropped()
		o.logger.Error("usage record quarantined without charge", slog.String("reason", reason))
	}
	return err
}
