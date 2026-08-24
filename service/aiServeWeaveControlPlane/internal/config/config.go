// Package config is the control plane's configuration, loaded from a YAML
// file by go-zero's conf loader.
//
// This service is the first in the repository to take a configuration file
// rather than flags. That is deliberate and does not set a precedent for the
// data plane: the control plane carries a database DSN, a Redis address and
// two secrets, and flags for those mean credentials in a process listing.
// Agent and Gateway keep their flags — they have few knobs and no secrets
// beyond file paths.
//
// config 包是控制面的配置，由 go-zero 的 conf 加载器从 YAML 文件读入。
//
// 本服务是仓库中第一个使用配置文件而非 flag 的服务。这是有意为之，且不构成数据面的
// 先例：控制面要携带数据库 DSN、Redis 地址与两个密钥，把这些放进 flag 就意味着凭据
// 出现在进程列表里。Agent 与 Gateway 保持用 flag——它们旋钮很少，除文件路径外也没有
// 秘密。
package config

import (
	"errors"
	"time"

	"github.com/zeromicro/go-zero/rest"
)

// Config is the whole of this service's configuration.
//
// Config 是本服务配置的全部。
type Config struct {
	rest.RestConf

	// Database is the control-plane database.
	//
	// Database 是控制面数据库。
	Database DatabaseConf

	// Redis caches key verifications for the Gateway. It is optional: with no
	// address configured the service still works, every verification goes to
	// the database, and the operator is warned at startup. A cache that must
	// be present to serve traffic is not a cache.
	//
	// Redis 为 Gateway 缓存 key 校验结果。它是可选的：未配置地址时服务照常工作，
	// 每次校验都落到数据库，且启动时会有告警。一个必须存在才能承载流量的缓存
	// 不叫缓存。
	Redis RedisConf `json:",optional"`

	// Auth configures the Console's session tokens.
	//
	// Auth 配置 Console 的会话令牌。
	Auth AuthConf

	// InternalToken authenticates the Gateway to this service's internal
	// verification endpoint. It is a shared secret rather than mTLS because
	// the Gateway already dials this service over the cluster's own network;
	// the service README records what would justify upgrading it.
	//
	// InternalToken 用于 Gateway 向本服务的内部校验端点表明身份。之所以用共享密钥
	// 而不是 mTLS，是因为 Gateway 本就在集群自有网络内访问本服务；服务 README 记录了
	// 什么情况下值得把它升级。
	InternalToken string

	// BootstrapToken authorizes tenant creation, which is the one operation
	// with no signed-in user behind it. It stands in for the Console's
	// eventual sign-up flow, the way the Registry's -mint-token stands in for
	// node onboarding.
	//
	// BootstrapToken 授权创建租户，那是唯一一个背后没有已登录用户的操作。它是
	// Console 将来注册流程的临时替身，正如 Registry 的 -mint-token 是节点纳管的
	// 临时替身。
	BootstrapToken string
}

// Supported database drivers.
//
// PostgreSQL is the primary target: the twenty tables still to come include
// several JSON columns (workflow templates, deployment revisions, job events),
// and JSONB's indexing is a real advantage there. MySQL is supported because
// deployments have it, and because the four tables this service owns today use
// only scalar columns, which both engines express identically. That equivalence
// is what makes dual support cheap right now — and it stops being true the day
// a JSON column lands, which is the point to re-evaluate rather than quietly
// paper over.
//
// 支持的数据库驱动。
//
// PostgreSQL 是首要目标：尚未落地的那二十张表里有若干 JSON 列（工作流模板、部署
// revision、job 事件），JSONB 的索引能力在那里是实打实的优势。之所以支持 MySQL，是因为
// 有些部署本来就有它，也因为本服务今天拥有的这四张表只使用标量列，而两种引擎对标量列的
// 表达完全一致。正是这种等价性让「同时支持两者」在当下代价很低——而它会在某个 JSON 列
// 落地的那天不再成立，那时应当重新评估，而不是悄悄糊过去。
const (
	DriverPostgres = "postgres"
	DriverMySQL    = "mysql"
)

// DatabaseConf is the database connection.
//
// DatabaseConf 是数据库连接配置。
type DatabaseConf struct {
	// Driver selects the engine: DriverPostgres or DriverMySQL.
	//
	// Driver 选择引擎：DriverPostgres 或 DriverMySQL。
	Driver string `json:",default=postgres,options=postgres|mysql"`
	// DSN is the full connection string. Its syntax is the driver's, not
	// ours:
	//
	//   postgres://user:pass@host:5432/aiserveweave?sslmode=require
	//   user:pass@tcp(host:3306)/aiserveweave?charset=utf8mb4&parseTime=True&loc=Local
	//
	// The MySQL form needs parseTime=True: without it the driver returns
	// timestamps as []byte and every time.Time column in this service fails
	// to scan.
	//
	// DSN 是完整连接串。它的语法属于驱动，不属于我们：
	//
	//   postgres://user:pass@host:5432/aiserveweave?sslmode=require
	//   user:pass@tcp(host:3306)/aiserveweave?charset=utf8mb4&parseTime=True&loc=Local
	//
	// MySQL 那种写法必须带 parseTime=True：否则驱动会把时间戳作为 []byte 返回，
	// 本服务中每一个 time.Time 列都会扫描失败。
	DSN string
	// MaxOpenConns bounds the pool. It is set explicitly because gorm's
	// default is unbounded, and an unbounded pool turns a slow query into a
	// connection exhaustion incident on the database rather than a queue here.
	//
	// MaxOpenConns 限制连接池大小。之所以显式设置，是因为 gorm 的默认值是无上限，
	// 而无上限的池会把一个慢查询变成数据库侧的连接耗尽事故，而不是这里的一个队列。
	MaxOpenConns int `json:",default=20"`
	MaxIdleConns int `json:",default=5"`
	// ConnMaxLifetime bounds how long one connection is reused, so a rolling
	// database failover is picked up without restarting this service.
	//
	// ConnMaxLifetime 限制单个连接被复用的时长，好让数据库滚动切换无需重启本服务
	// 即可被感知。
	ConnMaxLifetime time.Duration `json:",default=30m"`
	// AutoMigrate runs the schema migration at startup. It defaults to false
	// so a production deployment does not silently alter its own schema on a
	// rollout; local setups turn it on.
	//
	// AutoMigrate 在启动时执行 schema 迁移。默认为 false，好让生产部署不会在一次
	// 发布中悄悄改动自己的 schema；本地环境可以打开它。
	AutoMigrate bool `json:",default=false"`
}

// RedisConf is the verification cache.
//
// RedisConf 是校验缓存的配置。
type RedisConf struct {
	Addr     string `json:",optional"`
	Password string `json:",optional"`
	DB       int    `json:",default=0"`
	// TTL bounds how long a verification result is trusted without asking
	// the database again. It is the window a revoked key can still be served
	// within, so it is short by default: revocation is an incident response
	// action, and a minute of exposure after one is already a long time.
	//
	// TTL 限制一次校验结果在不再询问数据库的前提下被信任多久。它就是一个已被
	// 吊销的 key 仍可能被放行的窗口，因此默认值很短：吊销是应急响应动作，事后再暴露
	// 一分钟已经算久了。
	TTL time.Duration `json:",default=30s"`
}

// AuthConf configures the Console's session tokens.
//
// AuthConf 配置 Console 的会话令牌。
type AuthConf struct {
	// AccessSecret signs session tokens. go-zero's rest layer verifies them
	// with the same value, so the two must never diverge.
	//
	// AccessSecret 用于签发会话令牌。go-zero 的 rest 层用同一个值校验它们，因此两者
	// 绝不能出现分歧。
	AccessSecret string
	// AccessExpire is a session's lifetime.
	//
	// AccessExpire 是一个会话的生命期。
	AccessExpire time.Duration `json:",default=12h"`
}

// minSecretLen is the shortest secret this service will start with. A short
// shared secret in a config file is the kind of thing that survives from a
// local experiment into production, so it is refused at startup rather than
// warned about.
//
// minSecretLen 是本服务愿意启动所需的最短密钥长度。配置文件里一个过短的共享密钥，
// 正是那种会从本地试验一路活到生产的东西，因此这里是启动时拒绝，而不是发个告警。
const minSecretLen = 32

// Validate refuses a configuration that would start an insecure service. It
// checks what a YAML schema cannot: that the secrets are actually secrets.
//
// Validate 拒绝会启动出一个不安全服务的配置。它检查 YAML schema 无法检查的东西：
// 那些密钥是否真的算得上密钥。
func (c Config) Validate() error {
	if c.Database.DSN == "" {
		return errors.New("config: Database.DSN is required")
	}
	for name, secret := range map[string]string{
		"Auth.AccessSecret": c.Auth.AccessSecret,
		"InternalToken":     c.InternalToken,
		"BootstrapToken":    c.BootstrapToken,
	} {
		if len(secret) < minSecretLen {
			return errors.New("config: " + name + " must be at least 32 characters; generate one with `openssl rand -base64 32`")
		}
	}
	return nil
}
