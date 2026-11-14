package jobqueue

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// Transport delivers queued tasks to workers, at least once. asynq is the
// production implementation; jobqueuetest has an in-memory one.
//
// Transport 把排队任务至少一次地投递给 worker。生产实现是 asynq；jobqueuetest 有一个
// 内存实现。
type Transport interface {
	// Enqueue hands payload to the queue under jobID, giving each delivery
	// up to timeout to run.
	Enqueue(ctx context.Context, jobID string, payload []byte, timeout time.Duration) error
	// Delete drops a task that has not started; best effort.
	Delete(ctx context.Context, jobID string) error
	// Start runs handle for each delivery until Shutdown. A handler error
	// wrapping ErrRequeue redelivers after retryInterval without counting as
	// a failure; any other non-nil error is a failure.
	Start(handle func(ctx context.Context, payload []byte) error) error
	// Shutdown stops taking tasks and waits for running handlers.
	Shutdown()
}

// AsynqConfig configures the asynq transport.
type AsynqConfig struct {
	// Client is a Redis connection pool dedicated to asynq, sized at least
	// Concurrency+10: asynq's workers, heartbeats and forwarders hold
	// connections the rest of the Gateway should not compete with.
	Client redis.UniversalClient
	// Queue is the asynq queue name, the isolation unit between deployments
	// sharing one Redis (asynq's own key prefix is fixed).
	Queue string
	// Concurrency is how many tasks this replica's workers hold at once.
	Concurrency int
	// RetryInterval is the redelivery delay of a requeued task.
	RetryInterval time.Duration
	// ShutdownTimeout bounds how long Shutdown waits for running handlers.
	ShutdownTimeout time.Duration
	Logger          *slog.Logger
}

type asynqTransport struct {
	cfg       AsynqConfig
	client    *asynq.Client
	inspector *asynq.Inspector
	server    *asynq.Server
}

// NewAsynqTransport returns the asynq Transport. It does not close cfg.Client.
func NewAsynqTransport(cfg AsynqConfig) Transport {
	return &asynqTransport{
		cfg:       cfg,
		client:    asynq.NewClientFromRedisClient(cfg.Client),
		inspector: asynq.NewInspectorFromRedisClient(cfg.Client),
	}
}

func (t *asynqTransport) Enqueue(ctx context.Context, jobID string, payload []byte, timeout time.Duration) error {
	// MaxRetry must stay positive: asynq archives a task whose retry count
	// has reached it before asking whether the error was a failure, so a
	// zero would archive the first requeue. Requeues never increment it.
	//
	// MaxRetry 必须为正：asynq 在询问错误是否算失败之前，就会把重试次数已达上限的任务归档，
	// 因此零值会让第一次弹回就被归档。弹回从不增加重试次数。
	_, err := t.client.EnqueueContext(ctx, asynq.NewTask(TaskType, payload),
		asynq.TaskID(jobID), asynq.Queue(t.cfg.Queue), asynq.MaxRetry(20), asynq.Timeout(timeout))
	return err
}

func (t *asynqTransport) Delete(_ context.Context, jobID string) error {
	err := t.inspector.DeleteTask(t.cfg.Queue, jobID)
	if errors.Is(err, asynq.ErrTaskNotFound) || errors.Is(err, asynq.ErrQueueNotFound) {
		return nil
	}
	return err
}

func (t *asynqTransport) Start(handle func(ctx context.Context, payload []byte) error) error {
	retry := t.cfg.RetryInterval
	t.server = asynq.NewServerFromRedisClient(t.cfg.Client, asynq.Config{
		Concurrency:              t.cfg.Concurrency,
		Queues:                   map[string]int{t.cfg.Queue: 1},
		TaskCheckInterval:        min(retry, time.Second),
		DelayedTaskCheckInterval: max(retry, time.Second),
		ShutdownTimeout:          t.cfg.ShutdownTimeout,
		IsFailure:                func(err error) bool { return !errors.Is(err, ErrRequeue) },
		RetryDelayFunc: func(n int, err error, task *asynq.Task) time.Duration {
			if errors.Is(err, ErrRequeue) {
				return retry
			}
			return asynq.DefaultRetryDelayFunc(n, err, task)
		},
		Logger:   asynqLogger{t.cfg.Logger},
		LogLevel: asynq.WarnLevel,
	})
	return t.server.Start(asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		return handle(ctx, task.Payload())
	}))
}

func (t *asynqTransport) Shutdown() {
	// The client and inspector share cfg.Client, which asynq never closes;
	// its owner (main.go) does.
	if t.server != nil {
		t.server.Shutdown()
	}
}

// asynqLogger routes asynq's own log lines into slog. asynq never logs a task
// payload, so the prompt inside it cannot reach the log this way.
type asynqLogger struct{ l *slog.Logger }

func (a asynqLogger) Debug(args ...any) { a.l.Debug("asynq", slog.Any("msg", args)) }
func (a asynqLogger) Info(args ...any)  { a.l.Info("asynq", slog.Any("msg", args)) }
func (a asynqLogger) Warn(args ...any)  { a.l.Warn("asynq", slog.Any("msg", args)) }
func (a asynqLogger) Error(args ...any) { a.l.Error("asynq", slog.Any("msg", args)) }
func (a asynqLogger) Fatal(args ...any) { a.l.Error("asynq fatal", slog.Any("msg", args)) }
