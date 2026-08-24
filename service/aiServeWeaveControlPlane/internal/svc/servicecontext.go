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

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/cache"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/gormstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/token"
)

// ServiceContext is everything the handlers need.
//
// ServiceContext 是各个 handler 所需要的一切。
type ServiceContext struct {
	Config config.Config
	Logic  *logic.Service
	Issuer *token.Issuer
	Cache  *cache.Verifications

	db *gorm.DB
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
	if cfg.Database.AutoMigrate {
		if err := st.Migrate(ctx); err != nil {
			return nil, errors.Join(errors.New("running the schema migration"), err)
		}
	}

	verifications := cache.New(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB, cfg.Redis.TTL)
	if err := verifications.Ping(ctx); err != nil {
		return nil, errors.Join(errors.New("reaching the configured Redis"), err)
	}

	clock := runtime.NewSystemClock()
	issuer, err := token.NewIssuer(cfg.Auth.AccessSecret, cfg.Auth.AccessExpire, clock)
	if err != nil {
		return nil, err
	}

	return &ServiceContext{
		Config: cfg,
		Logic:  logic.New(st, clock, logic.WithInvalidator(verifications)),
		Issuer: issuer,
		Cache:  verifications,
		db:     db,
	}, nil
}

// Close releases the database and cache connections.
//
// Close 释放数据库与缓存连接。
func (s *ServiceContext) Close() error {
	var errs []error
	if err := s.Cache.Close(); err != nil {
		errs = append(errs, err)
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
