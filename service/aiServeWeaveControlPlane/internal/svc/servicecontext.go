// Package svc assembles the control plane's dependencies once, at startup, and
// hands the result to the handlers.
//
// It is the only place that knows how the pieces fit together: which store
// implementation is in use, whether a cache is configured, and what signs
// session tokens. A handler receives the assembled context and makes no
// construction decisions of its own.
//
// svc 包在启动时一次性装配控制面的依赖，并把结果交给各个 handler。
//
// 它是唯一知道这些部件如何拼在一起的地方：用的是哪个 store 实现、是否配置了缓存、
// 以及由什么来签发会话令牌。handler 拿到装配好的上下文，自己不做任何构造决定。
package svc

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	commonmetrics "AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/cache"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/fleet"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	cpmetrics "AIServeWeave/service/aiServeWeaveControlPlane/internal/metrics"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/metricshistory"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/registryclient"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/requestlogretention"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/revocationoutbox"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/session"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/token"
)

// RevocationSource exposes the durable generation behind API Key cache
// invalidation.
//
// RevocationSource 暴露 API Key 缓存失效背后的持久 generation。
type RevocationSource interface {
	WatchGeneration(ctx context.Context, after int64, wait time.Duration) (int64, error)
}

// ServiceContext is everything the handlers need.
//
// ServiceContext 是各个 handler 所需要的一切。
type ServiceContext struct {
	Config config.Config
	Logic  *logic.Service
	Issuer *token.Issuer
	Cache  *cache.Verifications
	// Revocations supplies the generation watched by Gateway replicas.
	//
	// Revocations 提供 Gateway 副本监听的失效 generation。
	Revocations RevocationSource
	// Sessions is the authoritative server-side half of every Console JWT.
	//
	// Sessions 是每个 Console JWT 在服务端的权威部分。
	Sessions session.Store
	// Fleet aggregates the node inventory across Gateway replicas. It is nil
	// when the deployment did not configure one, and every handler that uses
	// it is mounted only in that case — so a service without an operations
	// console has no fleet endpoint at all, not an endpoint that answers
	// "not configured".
	//
	// Fleet 跨 Gateway 副本聚合节点清单。部署未配置时它为 nil，而使用它的每个 handler
	// 也只在配置了的情况下才挂载——因此一个没有运维控制台的服务，是根本没有机群端点，
	// 而不是有一个回答「未配置」的端点。
	Fleet *fleet.Aggregator

	// RegistryClient calls the Registry's TokenAdmin service on behalf of a
	// platform operator (STATUS.md's P01). It is nil when the deployment did
	// not configure one, and every handler that uses it is mounted only in
	// that case — mirroring Fleet's own rule, for the same reason: a
	// deployment without a platform-operator console has no node write
	// endpoint at all, not one that answers "not configured".
	//
	// RegistryClient 代表一名平台运维调用 Registry 的 TokenAdmin 服务
	// （STATUS.md 的 P01）。部署未配置时它为 nil，使用它的每个 handler 也
	// 只在配置了的情况下才挂载——与 Fleet 自己的规则相同，理由也相同：一个
	// 没有平台运维控制台的部署，是根本没有节点写端点，而不是有一个回答
	// 「未配置」的端点。
	RegistryClient *registryclient.Client

	// MetricsRegistry is this replica's process-wide common/metrics sink.
	// Never nil: main.go always constructs one, whether or not -metrics-addr
	// is configured to export it.
	//
	// MetricsRegistry 是本副本进程级的 common/metrics 汇点。永不为 nil：无论
	// 是否配置 -metrics-addr 来导出它，main.go 都会构造一个。
	MetricsRegistry *commonmetrics.Registry

	db          *gorm.DB
	redisClient *redis.Client
	relayCancel context.CancelFunc
	relayDone   chan struct{}
	lagCancel   context.CancelFunc
	lagDone     chan struct{}

	// metricsHistoryCancel/metricsHistoryDone tear down the collector and
	// retention goroutines started below, only running at all when
	// cfg.MetricsHistory.Enabled() — the same "no config, no goroutine"
	// convention Fleet and Registry already follow for their own optional
	// pieces.
	//
	// metricsHistoryCancel/metricsHistoryDone 关停下面启动的采集器与保留期
	// 清理协程，只在 cfg.MetricsHistory.Enabled() 时才会真的启动——与 Fleet、
	// Registry 自己那些可选部件遵循的"没配置就没有协程"约定相同。
	metricsHistoryCancel context.CancelFunc
	metricsHistoryDone   chan struct{}

	// requestLogRetentionCancel/requestLogRetentionDone tear down the
	// request_logs retention sweeper started below. Unlike
	// metricsHistoryCancel/metricsHistoryDone, this goroutine always starts —
	// there is no "enabled" gate, since the request_logs table always exists
	// once migrated and the internal push API that populates it is mounted
	// unconditionally whenever InternalToken is configured.
	//
	// requestLogRetentionCancel/requestLogRetentionDone 关停下面启动的
	// request_logs 保留期清理协程。与 metricsHistoryCancel/metricsHistoryDone
	// 不同，这个协程总是会启动——不存在"是否启用"的开关，因为 request_logs
	// 表只要迁移过就总是存在，而填充它的内部推送 API 只要配置了 InternalToken
	// 就无条件挂载。
	requestLogRetentionCancel context.CancelFunc
	requestLogRetentionDone   chan struct{}
}

// NewServiceContext connects to the database and Redis, runs the migration when
// configured to, and assembles the service.
//
// It returns an error rather than calling log.Fatal, which go-zero's own
// examples do freely: a startup failure should unwind through main, where the
// already-opened connections can be closed.
//
// NewServiceContext 连接数据库与 Redis，在配置要求时执行迁移，并完成服务装配。
//
// 它返回错误而不是调用 log.Fatal——go-zero 自己的示例大量使用后者：启动失败应当沿着
// main 回溯，好让已经打开的连接被关闭。
func NewServiceContext(ctx context.Context, cfg config.Config) (*ServiceContext, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	db, err := openDatabase(cfg.Database)
	if err != nil {
		return nil, err
	}

	st := gormstore.New(db)
	ready := false
	defer func() {
		if !ready {
			if pool, err := db.DB(); err == nil {
				_ = pool.Close()
			}
		}
	}()
	if cfg.Database.AutoMigrate {
		if err := st.MigrateAll(ctx, false); err != nil {
			return nil, errors.Join(errors.New("running versioned schema migrations"), err)
		}
	} else if err := st.CheckSchema(ctx); err != nil {
		return nil, err
	}

	redisClient := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB, ContextTimeoutEnabled: true})
	defer func() {
		if !ready {
			_ = redisClient.Close()
		}
	}()
	verifications := cache.NewWithClient(redisClient, cfg.Redis.TTL)
	if err := verifications.Ping(ctx); err != nil {
		_ = redisClient.Close()
		return nil, errors.Join(errors.New("reaching the configured Redis"), err)
	}

	clock := runtime.NewSystemClock()
	issuer, err := token.NewIssuer(cfg.Auth.AccessSecret, cfg.Auth.AccessExpire, clock)
	if err != nil {
		return nil, err
	}

	sessions := session.NewRedis(redisClient, clock)
	relay := revocationoutbox.New(st, verifications, clock)
	logicOpts := []logic.Option{logic.WithInvalidator(relay), logic.WithSessions(sessions)}
	var registryClient *registryclient.Client
	if cfg.Registry.Enabled() {
		registryClient, err = registryclient.New(registryclient.Config{
			Addr:       cfg.Registry.Addr,
			CAFile:     cfg.Registry.CACertFile,
			AdminToken: cfg.Registry.AdminToken,
			Timeout:    cfg.Registry.Timeout,
		})
		if err != nil {
			return nil, errors.Join(errors.New("connecting to the configured Registry"), err)
		}
		logicOpts = append(logicOpts, logic.WithRegistryClient(registryClient))
	}

	metricsRegistry := commonmetrics.New(cpmetrics.Descriptions())

	relayCtx, relayCancel := context.WithCancel(ctx)
	relayDone := make(chan struct{})
	go func() { defer close(relayDone); relay.Run(relayCtx) }()

	lagCtx, lagCancel := context.WithCancel(ctx)
	lagDone := make(chan struct{})
	go func() { defer close(lagDone); runOutboxLagGauge(lagCtx, st, metricsRegistry) }()

	var metricsHistoryCancel context.CancelFunc
	var metricsHistoryDone chan struct{}
	if cfg.MetricsHistory.Enabled() {
		collector := metricshistory.New(metricshistory.Config{
			GatewayAddrs: cfg.MetricsHistory.GatewayAddrs,
			RegistryAddr: cfg.MetricsHistory.RegistryAddr,
			Interval:     cfg.MetricsHistory.Interval,
			Store:        st,
		})
		retention := metricshistory.NewRetention(st, cfg.MetricsHistory.Retention, nil, nil)

		mhCtx, mhCancel := context.WithCancel(ctx)
		mhDone := make(chan struct{})
		go func() {
			defer close(mhDone)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); collector.Run(mhCtx) }()
			go func() { defer wg.Done(); retention.Run(mhCtx, metricsHistoryRetentionInterval) }()
			wg.Wait()
		}()
		metricsHistoryCancel = mhCancel
		metricsHistoryDone = mhDone
	}

	requestLogRetention := cfg.RequestLogRetention
	if requestLogRetention <= 0 {
		requestLogRetention = config.DefaultRequestLogRetention
	}
	rlCtx, rlCancel := context.WithCancel(ctx)
	rlDone := make(chan struct{})
	go func() {
		defer close(rlDone)
		requestlogretention.New(st, requestLogRetention, nil, nil).Run(rlCtx, requestLogRetentionInterval)
	}()

	ready = true
	return &ServiceContext{
		Config:      cfg,
		Logic:       logic.New(st, clock, logicOpts...),
		Issuer:      issuer,
		Cache:       verifications,
		Revocations: verifications,
		Sessions:    sessions,
		Fleet: fleet.New(fleet.Config{
			Gateways: cfg.Fleet.Gateways,
			Token:    cfg.Fleet.GatewayToken,
			Timeout:  cfg.Fleet.Timeout,
			Clock:    clock,
		}),
		RegistryClient:            registryClient,
		MetricsRegistry:           metricsRegistry,
		db:                        db,
		redisClient:               redisClient,
		relayCancel:               relayCancel,
		relayDone:                 relayDone,
		lagCancel:                 lagCancel,
		lagDone:                   lagDone,
		metricsHistoryCancel:      metricsHistoryCancel,
		metricsHistoryDone:        metricsHistoryDone,
		requestLogRetentionCancel: rlCancel,
		requestLogRetentionDone:   rlDone,
	}, nil
}

// metricsHistoryRetentionInterval is how often the retention cleanup goroutine
// runs — once a day is plenty for a job that only prunes rows past a
// 90-day-scale window; it does not need to share MetricsHistoryConf.Interval,
// which is the much finer collector cadence.
//
// metricsHistoryRetentionInterval 是保留期清理协程的运行频率——对一个只清理
// 90 天量级窗口之外数据的任务，一天一次足够；它不需要与
// MetricsHistoryConf.Interval(更细的采集节奏)共用同一个值。
const metricsHistoryRetentionInterval = 24 * time.Hour

// requestLogRetentionInterval is how often the request-log retention
// cleanup goroutine runs — daily, the same cadence metricsHistoryRetentionInterval
// already uses, for the same reason: a cleanup task has no reason to run
// more often than once a day.
//
// requestLogRetentionInterval 是 request_logs 保留期清理协程的运行频率——
// 每天一次，与 metricsHistoryRetentionInterval 相同的节奏，理由也相同：
// 一个清理任务没有理由比一天一次更频繁地运行。
const requestLogRetentionInterval = 24 * time.Hour

// outboxLagStore is the read the outbox-lag gauge needs — a subset of
// *gormstore.Store, named here so the poller does not depend on the whole
// concrete store type.
//
// outboxLagStore 是滞后量表所需要的那次读取——*gormstore.Store 的一个子集，在
// 此单独命名，好让这个轮询器不依赖整个具体的 store 类型。
type outboxLagStore interface {
	RevocationOutboxLag(ctx context.Context) (generation, delivered int64, err error)
}

// outboxLagPollInterval is how often runOutboxLagGauge refreshes the gauge. A
// point-in-time gauge does not need to track every single generation bump —
// it only has to be fresh enough that an operator staring at the C27 chart
// sees a stuck relay within a few intervals, not immediately.
//
// outboxLagPollInterval 是 runOutboxLagGauge 刷新量表的频率。一个瞬时量表不需要
// 追踪每一次 generation 的递增——只需要新鲜到让盯着 C27 图表的运维在几个周期内
// 发现一个卡住的中继，而不必立刻发现。
const outboxLagPollInterval = 5 * time.Second

// runOutboxLagGauge sets controlplane_revocation_outbox_lag to
// generation - delivered_generation once per outboxLagPollInterval, until ctx
// is done. A read failure is logged and skipped rather than treated as a
// zero lag — reporting "caught up" on a database error would hide the very
// condition an operator relies on this gauge to catch.
//
// runOutboxLagGauge 每隔一个 outboxLagPollInterval 把
// controlplane_revocation_outbox_lag 设为 generation - delivered_generation，
// 直到 ctx 结束。读取失败只记日志并跳过，而不是当作零滞后处理——把一次数据库
// 错误报告成"已追上"，恰好会掩盖运维依赖这个量表去发现的那种状况。
func runOutboxLagGauge(ctx context.Context, st outboxLagStore, registry *commonmetrics.Registry) {
	ticker := time.NewTicker(outboxLagPollInterval)
	defer ticker.Stop()
	for {
		generation, delivered, err := st.RevocationOutboxLag(ctx)
		if err != nil {
			slog.Warn("outbox lag gauge read failed", slog.Any("error", err))
		} else {
			registry.Gauge(cpmetrics.MetricOutboxLagGeneration, nil).Set(float64(generation - delivered))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Close releases the database and cache connections.
//
// Close 释放数据库与缓存连接。
func (s *ServiceContext) Close() error {
	if s.relayCancel != nil {
		s.relayCancel()
		<-s.relayDone
	}
	if s.lagCancel != nil {
		s.lagCancel()
		<-s.lagDone
	}
	if s.metricsHistoryCancel != nil {
		s.metricsHistoryCancel()
		<-s.metricsHistoryDone
	}
	if s.requestLogRetentionCancel != nil {
		s.requestLogRetentionCancel()
		<-s.requestLogRetentionDone
	}
	var errs []error
	if err := s.Cache.Close(); err != nil {
		errs = append(errs, err)
	}
	if s.redisClient != nil {
		if err := s.redisClient.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.RegistryClient != nil {
		if err := s.RegistryClient.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.db != nil {
		sqlDB, err := s.db.DB()
		if err != nil {
			errs = append(errs, err)
		} else if err := sqlDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CacheEnabled reports whether a verification cache is configured, so startup
// can say so in the log rather than leaving an operator to infer it from
// database load.
//
// CacheEnabled 报告是否配置了校验缓存，好让启动日志把它写出来，而不是留给运维从数据库
// 负载里去推断。
func (s *ServiceContext) CacheEnabled() bool { return s.Cache != nil }

// dialector returns the gorm driver for the configured engine.
//
// An unknown driver is an error rather than a silent fallback to PostgreSQL: a
// typo in this field would otherwise point the service at the wrong engine and
// fail later, with a DSN parse error that names neither the field nor the typo.
//
// dialector 返回所配置引擎对应的 gorm 驱动。
//
// 未知的驱动是错误，而不是悄悄回退到 PostgreSQL：否则这个字段里的一个笔误会让服务
// 指向错误的引擎，并在之后以一个既不提字段名、也不提笔误的 DSN 解析错误失败。
func dialector(cfg config.DatabaseConf) (gorm.Dialector, error) {
	switch cfg.Driver {
	case config.DriverPostgres, "":
		return postgres.Open(cfg.DSN), nil
	case config.DriverMySQL:
		return mysql.Open(cfg.DSN), nil
	default:
		return nil, errors.New("config: unknown Database.Driver " + cfg.Driver + "; want postgres or mysql")
	}
}

// openDatabase opens the connection pool with explicit bounds.
//
// openDatabase 打开连接池，并设置明确的上限。
func openDatabase(cfg config.DatabaseConf) (*gorm.DB, error) {
	driver, err := dialector(cfg)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(driver, &gorm.Config{
		// gorm's default logger prints every statement to stdout at info
		// level. This service logs through go-zero, and a second logger
		// writing SQL — including the parameters of a key lookup — straight
		// to stdout is both noise and a disclosure.
		//
		// gorm 默认的 logger 会以 info 级别把每条语句打到 stdout。本服务通过 go-zero
		// 记日志，而第二个 logger 把 SQL——包括一次 key 查询的参数——直接写到 stdout，
		// 既是噪音也是泄漏。
		Logger: gormlogger.New(log.Default(), gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true,
		}),
		// TranslateError is what turns a driver-specific unique-violation into
		// gorm.ErrDuplicatedKey. Without it, gormstore.translate cannot tell a
		// duplicate email from an unreachable database, and both would surface
		// to the caller as an internal error.
		//
		// TranslateError 负责把驱动特有的唯一约束冲突转换成 gorm.ErrDuplicatedKey。
		// 没有它，gormstore.translate 就分不清「email 重复」与「数据库不可达」，而
		// 两者都会以内部错误的形式呈现给调用方。
		TranslateError: true,
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	return db, nil
}
