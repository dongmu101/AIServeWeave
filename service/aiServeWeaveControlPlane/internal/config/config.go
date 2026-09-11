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

	// Redis stores revocable Console sessions and also caches key verifications
	// for the Gateway. It is required because accepting a JWT without checking
	// its server-side session would silently disable revocation.
	//
	// Redis 存储可吊销的 Console 会话，也为 Gateway 缓存 key 校验结果。它是必需的，
	// 因为只接受 JWT 而不检查服务端会话，会悄悄关闭吊销能力。
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

	// Fleet configures the operator inventory: which Gateway replicas to ask
	// about connected nodes, and the two secrets involved. It is optional,
	// and leaving it out is the ordinary case — a deployment that does not
	// run an operations console has no reason to let this service reach into
	// the data plane at all.
	//
	// Fleet 配置运维清单：向哪些 Gateway 副本询问已连接的节点，以及所涉及的两个密钥。
	// 它是可选的，且省略它才是常态——一个不运行运维控制台的部署，没有任何理由让本服务
	// 去够数据面。
	Fleet FleetConf `json:",optional"`

	// Registry configures this service's client to the Registry's TokenAdmin
	// service (STATUS.md's P01): node approval, disable/enable and
	// maintenance. Like Fleet, it is optional — a deployment with no
	// platform-operator console has no reason to let this service reach the
	// Registry at all — and unlike Fleet it does not gate on Gateway
	// replicas: node identity operations do not need a configured read path
	// into the data plane.
	//
	// Registry 配置本服务对 Registry TokenAdmin 服务（STATUS.md 的 P01）的
	// 客户端：节点审批、禁用/启用与维护。与 Fleet 一样它是可选的——一个没有
	// 平台运维控制台的部署，没有理由让本服务够到 Registry——但与 Fleet 不同，
	// 它不依赖 Gateway 副本：节点身份操作不需要一条通往数据面的已配置读取
	// 路径。
	Registry RegistryConf `json:",optional"`

	// MetricsHistory configures periodic scraping of Gateway/Registry
	// Prometheus endpoints into rollup history for Console C27 (P08). Like
	// Fleet and Registry, it is optional and off by default.
	//
	// MetricsHistory 配置定时抓取 Gateway/Registry 的 Prometheus 端点、为
	// Console C27 落地汇总历史(P08)。与 Fleet、Registry 一样，它是可选的，
	// 默认关闭。
	MetricsHistory MetricsHistoryConf `json:",optional"`
}

// FleetConf configures the fleet inventory.
//
// The node inventory is not tenant data: a node is shared infrastructure that
// any tenant's request may be routed to, and there is no tenant dimension on
// it to filter by. So it is not served on the session-guarded Admin API at
// all — it has its own guard and its own path prefix, and a tenant's session
// cannot reach it however the roles are later changed.
//
// FleetConf 配置机群清单。
//
// 节点清单不是租户数据：节点是共享的基础设施，任何租户的请求都可能被路由到它，而它
// 身上没有可供过滤的租户维度。因此它根本不放在由会话守卫的 Admin API 上——它有自己的
// 守卫与自己的路径前缀，无论将来角色如何调整，租户的会话都够不到它。
type FleetConf struct {
	// Gateways are the base URLs of each Gateway replica's operator
	// listener, e.g. http://gateway-1:8091. A replica that is not listed is
	// simply not asked, and the nodes connected only to it are missing from
	// the answer — which is why the response names the replicas it reached.
	//
	// Gateways 是各 Gateway 副本运维监听器的基础 URL，例如 http://gateway-1:8091。
	// 没有列出的副本不会被询问，只连到它上面的节点也就不会出现在答案里——这正是响应
	// 要点明它联系到了哪些副本的原因。
	Gateways []string `json:",optional"`
	// GatewayToken authenticates this service to those listeners. It must
	// match each Gateway's AISW_GATEWAY_ADMIN_TOKEN.
	//
	// GatewayToken 用于本服务向那些监听器表明身份。它必须与各 Gateway 的
	// AISW_GATEWAY_ADMIN_TOKEN 一致。
	GatewayToken string `json:",optional"`
	// Timeout bounds one call to one replica. A replica that is slow must
	// not hold the whole aggregation, which is why the answer can be partial.
	//
	// Timeout 限制对单个副本的单次调用。一个慢副本不能拖住整次聚合，这正是答案可以是
	// 局部的原因。
	Timeout time.Duration `json:",default=3s"`
}

// Enabled reports whether the fleet inventory is configured. Both parts are
// required together: replicas to ask, and a secret to ask them with. A
// partial configuration is a mistake, and Validate says so rather than
// starting a half-built feature. Reading the inventory back out is
// authorized separately, by a platform operator's session
// (requirePlatformSession) rather than a third secret here — see
// STATUS.md's P01.
//
// Enabled 报告机群清单是否已配置。两部分必须同时具备：可询问的副本，以及用来
// 询问它们的密钥。配置不全属于失误，Validate 会指出这一点，而不是启动一个只搭了
// 一半的功能。读取清单的授权分开处理，由平台运维的会话
// （requirePlatformSession）负责，而不是这里的第三个密钥——见 STATUS.md 的 P01。
func (f FleetConf) Enabled() bool {
	return len(f.Gateways) > 0 || f.GatewayToken != ""
}

// RegistryConf configures the Registry TokenAdmin client (STATUS.md's P01).
//
// RegistryConf 配置 Registry TokenAdmin 客户端（STATUS.md 的 P01）。
type RegistryConf struct {
	// Addr is the Registry's TokenAdmin endpoint, host:port.
	//
	// Addr 是 Registry 的 TokenAdmin 端点，形如 host:port。
	Addr string `json:",optional"`
	// CACertFile verifies the Registry's server certificate. Empty uses the
	// host's root store, which only works when the Registry's certificate
	// chains to a public CA; a self-issued Registry CA needs this set.
	//
	// CACertFile 用于校验 Registry 的服务端证书。留空则使用宿主机的根证书库，
	// 这只在 Registry 的证书链最终指向一个公共 CA 时才有效；自签的 Registry
	// CA 需要设置这一项。
	CACertFile string `json:",optional"`
	// AdminToken authenticates this service to TokenAdmin, matching the
	// Registry's -admin-token-file.
	//
	// AdminToken 用于向 TokenAdmin 表明身份，须与 Registry 的
	// -admin-token-file 一致。
	AdminToken string `json:",optional"`
	// Timeout bounds one call to the Registry.
	//
	// Timeout 限制对 Registry 的单次调用。
	Timeout time.Duration `json:",default=5s"`
}

// Enabled reports whether the Registry client is configured. Both Addr and
// AdminToken are required together, the same reasoning as FleetConf.Enabled.
//
// Enabled 报告 Registry 客户端是否已配置。Addr 与 AdminToken 必须同时具备，
// 理由与 FleetConf.Enabled 相同。
func (r RegistryConf) Enabled() bool {
	return r.Addr != "" || r.AdminToken != ""
}

// MetricsHistoryConf configures periodic scraping of Gateway/Registry
// Prometheus text endpoints into rollup history for Console C27.
// Unconfigured (the zero value), the background collector does not start and
// GET /operator/v1/metrics/history is not mounted — the same "no config, no
// route" convention Fleet and Registry already follow.
//
// MetricsHistoryConf 配置定时抓取 Gateway/Registry 的 Prometheus 文本端点、为
// Console C27 落地汇总历史。未配置时(零值)后台采集器不启动，
// GET /operator/v1/metrics/history 也不挂载——与 Fleet、Registry 现有的
// "没配置就没有这条路由"约定一致。
type MetricsHistoryConf struct {
	// GatewayAddrs are Gateway replicas' -metrics-addr endpoints, e.g.
	// "http://gateway-1:9090". Distinct from Fleet.Gateways, which points at
	// adminapi on a different port.
	//
	// GatewayAddrs 是各 Gateway 副本 -metrics-addr 的端点，例如
	// "http://gateway-1:9090"。与 Fleet.Gateways 不同——后者指向 adminapi，
	// 端口不同。
	GatewayAddrs []string `json:",optional"`
	// RegistryAddr is the Registry's -metrics-addr endpoint.
	//
	// RegistryAddr 是 Registry 的 -metrics-addr 端点。
	RegistryAddr string `json:",optional"`
	// Interval is both the scrape cadence and the rollup bucket width:
	// Gateway/Registry counters and gauges are already cumulative, so a
	// scrape at the end of each window is the natural representative value —
	// scraping more often would not improve accuracy, only cost more
	// requests.
	//
	// Interval 既是抓取周期，也是落库的汇总粒度：Gateway/Registry 的计数器与
	// 量表本就是累计值，每个窗口收尾时抓一次就是自然的代表值——抓得更频繁不会
	// 提高准确度，只会多花请求。
	Interval time.Duration `json:",default=5m"`
	// Retention bounds how long a rollup row is kept before cleanup deletes it.
	//
	// Retention 限制一行汇总数据在被清理任务删除前保留多久。
	Retention time.Duration `json:",default=2160h"`
}

// Enabled reports whether the metrics-history collector is configured.
//
// Enabled 报告指标历史采集器是否已配置。
func (m MetricsHistoryConf) Enabled() bool {
	return len(m.GatewayAddrs) > 0 || m.RegistryAddr != ""
}

// Supported database drivers.
//
// PostgreSQL is the primary target because future analytical tables will use
// JSON query and index features. MySQL is supported because
// deployments have it, and because the five base tables this service owns use
// only scalar columns, which both engines express identically. That equivalence
// is what makes dual support cheap right now — and it stops being true the day
// a JSON column lands, which is the point to re-evaluate rather than quietly
// paper over.
//
// 支持的数据库驱动。
//
// PostgreSQL 是首要目标，因为后续分析表会使用 JSON 查询与索引能力。之所以支持 MySQL，是因为
// 有些部署本来就有它，也因为本服务的五张基础表只使用标量列，而两种引擎对标量列的
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
	// AutoMigrate applies embedded versioned SQL at startup for compatibility
	// with local configurations. False only checks the schema; production runs
	// the database-only migration command before starting a service replica.
	//
	// AutoMigrate 在启动时执行内嵌版本 SQL，兼容既有本地配置。false 仅检查 schema；
	// 生产在启动服务副本前，先运行独立的数据库迁移命令。
	AutoMigrate bool `json:",default=false"`
}

// RedisConf is the verification cache.
//
// RedisConf 是校验缓存的配置。
type RedisConf struct {
	Addr     string `json:",optional"`
	Password string `json:",optional"`
	DB       int    `json:",default=0"`
	// TTL bounds how long a verification result is trusted without asking the
	// database again. Generation notifications clear ordinary revocations;
	// this remains their defense-in-depth expiry while the transactional outbox
	// retries publication across crashes.
	//
	// TTL 限制一次校验结果在不再询问数据库的前提下被信任多久。普通吊销由 generation
	// 通知清除；这里保留纵深防御的过期边界，事务 outbox 负责跨崩溃重试发布。
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
	if c.Redis.Addr == "" {
		return errors.New("config: Redis.Addr is required for revocable sessions")
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
	if c.Fleet.Enabled() {
		if len(c.Fleet.Gateways) == 0 {
			return errors.New("config: Fleet.Gateways is required once the fleet inventory is configured")
		}
		if len(c.Fleet.GatewayToken) < minSecretLen {
			return errors.New("config: Fleet.GatewayToken must be at least 32 characters; generate one with `openssl rand -base64 32`")
		}
	}
	if c.Registry.Enabled() {
		if c.Registry.Addr == "" {
			return errors.New("config: Registry.Addr is required once the Registry client is configured")
		}
		if len(c.Registry.AdminToken) < minSecretLen {
			return errors.New("config: Registry.AdminToken must be at least 32 characters; generate one with `openssl rand -base64 32`")
		}
	}
	if c.MetricsHistory.Enabled() {
		if c.MetricsHistory.Interval <= 0 {
			return errors.New("config: MetricsHistory.Interval must be positive once metrics history is configured")
		}
		if c.MetricsHistory.Retention <= 0 {
			return errors.New("config: MetricsHistory.Retention must be positive once metrics history is configured")
		}
	}
	return nil
}
