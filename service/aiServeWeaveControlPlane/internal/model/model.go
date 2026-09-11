// Package model holds the control plane's persisted entities and the gorm
// mapping for them. Its five base tables are tenants, users,
// platform_operators, api_keys, and audit_logs; feature-specific models land
// only with the features that need them.
//
// One rule shapes every struct here: a field that holds a credential holds
// only its irreversible form. There is no column anywhere in this package that
// a leaked database dump could be replayed from.
//
// model 包保存控制面的持久化实体及其 gorm 映射。五张基础表是 tenants、users、
// platform_operators、api_keys 与 audit_logs；功能专用模型只随真正需要它们的功能落地。
//
// 有一条规则塑造了这里的每一个结构体：凡是保存凭据的字段，只保存其不可逆形式。本包
// 中不存在任何一列，能让泄漏的数据库转储被重放利用。
package model

import (
	"time"

	"AIServeWeave/common/quota"
)

// Status values shared by tenants, users and API keys. They are strings
// rather than integers because these rows are read by people during an
// incident, and a status column that reads "revoked" needs no lookup table.
//
// tenants、users 与 API key 共用的状态取值。用字符串而不是整数，是因为这些行会在故障
// 处置时被人直接阅读，而一个写着 "revoked" 的状态列不需要再去查对照表。
const (
	// StatusActive is the only status that may serve traffic.
	//
	// StatusActive 是唯一可以承载流量的状态。
	StatusActive = "active"
	// StatusSuspended is an administrative hold: reversible, and applied to a
	// tenant or user rather than to one credential.
	//
	// StatusSuspended 是管理性的暂停：可逆，且作用于租户或用户，而不是某一个凭据。
	StatusSuspended = "suspended"
	// StatusRevoked is terminal. A revoked key is never reactivated — the
	// answer to needing it again is a new key, because the old one may be in
	// somebody else's hands and that is why it was revoked.
	//
	// StatusRevoked 是终态。被吊销的 key 永不重新启用——再次需要它的答案是签发一个
	// 新 key，因为旧的那个可能已在他人手中，而这正是它被吊销的原因。
	StatusRevoked = "revoked"
)

// Roles a user may hold within one tenant.
//
// 一个用户在某个租户内可以持有的角色。
const (
	// RoleOwner may do everything, including managing other users.
	//
	// RoleOwner 可以做任何事，包括管理其他用户。
	RoleOwner = "owner"
	// RoleAdmin may manage API keys and read everything.
	//
	// RoleAdmin 可以管理 API Key 并读取一切。
	RoleAdmin = "admin"
	// RoleMember may read, and may manage only the keys it created.
	//
	// RoleMember 可以读取，且只能管理自己创建的 key。
	RoleMember = "member"
)

// PlatformScope is the sentinel value logic.Actor.TenantID and AuditLog.TenantID
// carry for a platform operator (STATUS.md's P01), rather than a real tenant
// id. It is safe to use as a sentinel because every real tenant id comes from
// NewID(PrefixTenant) and therefore always starts with "tnt_" — "platform"
// can never collide with one, so this needs no reserved column, no nullable
// TenantID, and no schema change to either of the two dual-engine tables it
// appears in.
//
// PlatformScope 是平台运维身份（STATUS.md 的 P01）在 logic.Actor.TenantID 与
// AuditLog.TenantID 里携带的哨兵值，而不是一个真实租户 id。用它作哨兵是安全的：
// 每个真实租户 id 都来自 NewID(PrefixTenant)，因此必定以 "tnt_" 开头——"platform"
// 永远不会与之相撞，所以它出现的那两张双引擎表都不需要为此保留列、不需要把
// TenantID 改成可空，也不需要任何 schema 变更。
const PlatformScope = "platform"

// RolePlatformOperator is the logic.Actor.Role a platform operator's session
// carries. It is deliberately not one of the tenant Role* constants above:
// platform operators are authorized separately from tenant roles (STATUS.md's
// P01), and giving their session the same Role vocabulary as a tenant admin
// would make the two easy to confuse in a permission check.
//
// RolePlatformOperator 是平台运维会话所携带的 logic.Actor.Role。它刻意不是上面
// 租户 Role* 常量中的一个：平台运维身份与租户角色分开授权（STATUS.md 的 P01），
// 若让两者的会话共用同一套 Role 词汇，权限检查时就容易把二者混淆。
const RolePlatformOperator = "platform_operator"

// Tenant is one isolation boundary. Every other row in this package belongs to
// exactly one tenant, and every query the API layer issues is scoped by it —
// that scoping is what tenancy is, and it is enforced in the store layer
// rather than left to each handler to remember.
//
// Tenant 是一个隔离边界。本包中其他每一行都恰好属于一个租户，API 层发出的每一次查询
// 都以它限定范围——这个限定就是多租户本身，且它由 store 层强制执行，而不是留给每个
// handler 自己记得。
type Tenant struct {
	ID     string `gorm:"primaryKey;size:32"`
	Name   string `gorm:"size:128;not null"`
	Status string `gorm:"size:16;not null;index"`
	// The three limit columns are the tenant's quota, stored here rather than
	// in a table of their own: every tenant has exactly one set, and a
	// one-to-one table would add a join to the one query that sits on the
	// inference request path.
	//
	// Zero means unlimited, which is what every existing row gets when this
	// column is added. A deployment upgrading into this feature must not
	// suddenly start rejecting the traffic it accepted yesterday.
	//
	// 这三个限制列是租户的配额，存在这里而不是单独一张表：每个租户恰好有一组，而
	// 一对一的表会给那条位于推理请求路径上的查询平添一次 join。
	//
	// 零表示不限制，这也是本列加上时所有既有行得到的值。一个升级到本功能的部署，
	// 绝不能突然开始拒绝它昨天还接受的流量。
	RequestsPerMinute int `gorm:"not null;default:0"`
	TokensPerMinute   int `gorm:"not null;default:0"`
	MaxConcurrent     int `gorm:"not null;default:0"`
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Limits renders the tenant's quota in the form the Gateway enforces. It is a
// method rather than an embedded struct so the columns stay flat: a schema is
// read by people writing SQL during an incident, and gorm's embedded prefixes
// are one more thing they would have to know.
//
// Limits 把租户的配额渲染成 Gateway 所执行的那个形式。它是一个方法而不是内嵌结构体，
// 好让这些列保持扁平：schema 会被故障处置中写 SQL 的人直接阅读，而 gorm 的内嵌前缀
// 是他们又要多知道的一件事。
func (t Tenant) Limits() quota.Limits {
	return quota.Limits{
		RequestsPerMinute: t.RequestsPerMinute,
		TokensPerMinute:   t.TokensPerMinute,
		MaxConcurrent:     t.MaxConcurrent,
	}
}

// TableName pins the table name to the README's 数据模型 listing, rather than
// letting gorm's pluralizer decide it. The schema is a documented contract;
// it must not change because a library changed its inflection rules.
//
// TableName 把表名钉死在 README「数据模型」所列的名字上，而不是交给 gorm 的复数化
// 规则去决定。schema 是有文档的契约；它不能因为某个库改了词形变化规则而改变。
func (Tenant) TableName() string { return "tenants" }

// User is a person who signs in to the Console. Its PasswordHash is the one
// place in this system where a slow hash is the right answer: unlike an API
// key, a password is chosen by a person and therefore lives in a dictionary
// somebody already has.
//
// User 是一个登录 Console 的人。它的 PasswordHash 是本系统中唯一适合使用慢哈希的
// 地方：与 API Key 不同，密码是人自己选的，因此它一定活在某人已经拥有的字典里。
type User struct {
	ID       string `gorm:"primaryKey;size:32"`
	TenantID string `gorm:"size:32;not null;index"`
	// Email is unique across the whole deployment, not per tenant: it is the
	// sign-in identifier, and an identifier that needs a tenant alongside it
	// to be unique is one a person cannot type into a login form.
	//
	// Email 在整个部署内唯一，而不是每租户内唯一：它是登录标识，而一个必须再带上
	// 租户才能唯一的标识，是人无法填进登录表单的。
	Email string `gorm:"size:255;not null;uniqueIndex"`
	// PasswordHash is a bcrypt digest. The plaintext never reaches this
	// struct: the logic layer hashes before constructing one.
	//
	// PasswordHash 是 bcrypt 摘要。明文从不进入这个结构体：logic 层在构造它之前就
	// 已完成哈希。
	PasswordHash string `gorm:"size:120;not null"`
	Name         string `gorm:"size:128"`
	Role         string `gorm:"size:16;not null"`
	Status       string `gorm:"size:16;not null;index"`
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName pins the table name. See Tenant.TableName.
//
// TableName 钉死表名，理由见 Tenant.TableName。
func (User) TableName() string { return "users" }

// PlatformOperator is a person who signs in to manage the fleet — node
// approval, disable and maintenance (STATUS.md's P01) — rather than a
// tenant. It is a separate table from User, not a User with an empty
// TenantID: User.TenantID is `not null` precisely because every tenant user
// belongs to exactly one tenant, and a platform operator belongs to none.
// Sharing one table would mean weakening that constraint for every row to
// accommodate a kind of account that is a small minority of them.
//
// Its Email is unique across this table only, not against users.Email: a
// platform operator and a tenant user are different login surfaces
// (different endpoints, different session scope — see PlatformScope), so
// the same person holding both is not the identity collision an email
// uniqueness constraint exists to prevent.
//
// PlatformOperator 是登录以管理机群的人——节点审批、禁用与维护
// （STATUS.md 的 P01）——而不是某个租户的人。它是与 User 分开的一张表，而不是
// 一个 TenantID 留空的 User：User.TenantID 是 `not null`，正是因为每个租户用户
// 都恰好属于一个租户，而平台运维不属于任何租户。共用一张表，就意味着要为了
// 容纳这一小部分账户，削弱对每一行都成立的这条约束。
//
// 它的 Email 只在本表内唯一，不与 users.Email 比对：平台运维与租户用户是两个
// 不同的登录入口（不同端点、不同会话范围——见 PlatformScope），因此同一个人
// 同时持有两者，并不是 email 唯一性约束想要防止的那种身份冲突。
type PlatformOperator struct {
	ID           string `gorm:"primaryKey;size:32"`
	Email        string `gorm:"size:255;not null;uniqueIndex"`
	PasswordHash string `gorm:"size:120;not null"`
	Name         string `gorm:"size:128"`
	Status       string `gorm:"size:16;not null;index"`
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName pins the table name. See Tenant.TableName.
//
// TableName 钉死表名，理由见 Tenant.TableName。
func (PlatformOperator) TableName() string { return "platform_operators" }

// APIKey is one credential a caller presents to the Gateway. The plaintext
// exists only in the response to the call that created it; what lives here is
// the hash it is looked up by and the display form a listing may show.
//
// APIKey 是调用方向 Gateway 出示的一个凭据。明文只存在于创建它那次调用的响应里；
// 留在这里的是用于检索它的哈希，以及列表可以展示的那个显示形式。
type APIKey struct {
	ID       string `gorm:"primaryKey;size:32"`
	TenantID string `gorm:"size:32;not null;index"`
	// CreatedBy is the user who minted the key. It survives that user's
	// deletion, because an audit trail that loses its actor when the actor
	// leaves is not an audit trail.
	//
	// CreatedBy 是铸造该 key 的用户。它在该用户被删除后依然保留，因为一条会随行为人
	// 离开而丢失行为人的审计线索，不算审计线索。
	CreatedBy string `gorm:"size:32;not null"`
	Name      string `gorm:"size:128;not null"`
	// Hash is the SHA-256 of the plaintext, and the column every verification
	// looks up by. It is unique: two rows with one hash would mean one key
	// authenticating as two tenants.
	//
	// Hash 是明文的 SHA-256，也是每次校验据以检索的那一列。它是唯一的：两行共用一个
	// 哈希，意味着一个 key 能以两个租户的身份通过认证。
	Hash string `gorm:"size:64;not null;uniqueIndex"`
	// Display is the non-secret identifier, safe to show and to log.
	//
	// Display 是非机密标识，可安全展示与记录日志。
	Display string `gorm:"size:32;not null"`
	Status  string `gorm:"size:16;not null;index"`
	// ExpiresAt is optional. A key with no expiry is a key that will still be
	// valid the day it is finally found in an old repository, so the API
	// defaults to setting one.
	//
	// ExpiresAt 是可选的。没有过期时间的 key，在它最终被人从某个旧仓库里翻出来的
	// 那天依然有效，因此 API 默认会给它设置一个。
	ExpiresAt *time.Time `gorm:"index"`
	// LastUsedAt is written by the Gateway's verification path, coarsely: see
	// the store's UpdateLastUsed for why it is not updated on every request.
	//
	// LastUsedAt 由 Gateway 的校验路径粗粒度写入：不在每次请求都更新的原因见 store
	// 的 UpdateLastUsed。
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// TableName pins the table name. See Tenant.TableName.
//
// TableName 钉死表名，理由见 Tenant.TableName。
func (APIKey) TableName() string { return "api_keys" }

// Usable reports whether this key may authenticate a request right now. It is
// the single definition of "valid", so the Gateway's cache, the admin listing
// and the verification path cannot drift into three different answers.
//
// Usable 报告该 key 此刻是否可以为一次请求提供认证。它是「有效」的唯一定义，因此
// Gateway 的缓存、管理端列表与校验路径不会漂移成三个不同的答案。
func (k APIKey) Usable(now time.Time) bool {
	if k.Status != StatusActive {
		return false
	}
	return k.ExpiresAt == nil || now.Before(*k.ExpiresAt)
}

// Audit action names. The set is closed and every write path uses one of
// these, so an audit query can be written against a known vocabulary instead
// of a guess at what past code happened to pass.
//
// 审计动作名。集合是封闭的，每条写路径都使用其中之一，这样审计查询就能针对一套已知
// 词汇来写，而不是去猜过去的代码碰巧传了什么。
const (
	ActionTenantCreate       = "tenant.create"
	ActionUserCreate         = "user.create"
	ActionUserLogin          = "user.login"
	ActionUserUpdate         = "user.update"
	ActionUserPasswordChange = "user.password.change"
	ActionUserPasswordReset  = "user.password.reset"
	ActionUserRoleChange     = "user.role.change"
	ActionUserDisable        = "user.disable"
	ActionUserEnable         = "user.enable"
	ActionUserSessionsRevoke = "user.sessions.revoke"
	ActionSessionLogout      = "session.logout"
	ActionAPIKeyCreate       = "apikey.create"
	ActionAPIKeyRevoke       = "apikey.revoke"
	ActionTenantLimits       = "tenant.limits"

	// Platform actions (STATUS.md's P01) are recorded with TenantID set to
	// PlatformScope rather than a real tenant, since a node has no tenant.
	ActionPlatformOperatorCreate         = "platform_operator.create"
	ActionPlatformOperatorLogin          = "platform_operator.login"
	ActionPlatformOperatorPasswordChange = "platform_operator.password.change"
	ActionPlatformOperatorPasswordReset  = "platform_operator.password.reset"
	ActionPlatformOperatorDisable        = "platform_operator.disable"
	ActionPlatformOperatorEnable         = "platform_operator.enable"
	ActionPlatformOperatorSessionsRevoke = "platform_operator.sessions.revoke"
	ActionPlatformSessionLogout          = "platform_session.logout"
	ActionNodeApprove                    = "node.approve"
	ActionNodeDisable                    = "node.disable"
	ActionNodeEnable                     = "node.enable"
	ActionNodeMaintenanceEnter           = "node.maintenance.enter"
	ActionNodeMaintenanceExit            = "node.maintenance.exit"
)

// AuditLog is one administrative action, recorded for the README's
// 所有管理操作写入审计日志. It is append-only: nothing in this service updates
// or deletes a row of this table, which is what makes it evidence rather than
// state.
//
// AuditLog 是一次管理操作的记录，对应 README 的「所有管理操作写入审计日志」。它是
// 只追加的：本服务不会更新或删除该表的任何一行，这正是它成为证据而非状态的原因。
type AuditLog struct {
	ID       string `gorm:"primaryKey;size:32"`
	TenantID string `gorm:"size:32;not null;index"`
	// ActorID is the user who performed the action, or empty for an action
	// performed by the system itself.
	//
	// ActorID 是执行该动作的用户；由系统自身执行的动作则为空。
	ActorID string `gorm:"size:32;index"`
	Action  string `gorm:"size:64;not null;index"`
	// Target names what was acted on — an id, never the object's contents.
	//
	// Target 指出被操作的对象——一个 id，绝不是该对象的内容。
	Target string `gorm:"size:64"`
	// Detail is a short human-readable summary. It must never carry a
	// credential, a prompt or a request body: this table is read by more
	// people than any other in the schema, and it is retained longer.
	//
	// Detail 是一段简短的、给人读的摘要。它绝不能携带凭据、prompt 或请求体：这张表
	// 比 schema 中任何一张都有更多人读，保留时间也更长。
	Detail string `gorm:"size:512"`
	// IP is the client address the action came from.
	//
	// IP 是该动作来源的客户端地址。
	IP        string    `gorm:"size:64"`
	CreatedAt time.Time `gorm:"index"`
}

// TableName pins the table name. See Tenant.TableName.
//
// TableName 钉死表名，理由见 Tenant.TableName。
func (AuditLog) TableName() string { return "audit_logs" }
