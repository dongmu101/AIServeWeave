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
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/registryclient"
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
		RegistryClient:  registryClient,
		MetricsRegistry: metricsRegistry,
		db:              db,
		redisClient:     redisClient,
		relayCancel:     relayCancel,
		relayDone:       relayDone,
	}, nil
}

// Close releases the database and cache connections.
//
// Close 释放数据库与缓存连接。
func (s *ServiceContext) Close() error {
	if s.relayCancel != nil {
		s.relayCancel()
		<-s.relayDone
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
