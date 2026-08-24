// Package types is the Admin API's wire format: the request and response
// shapes the Console and the Gateway exchange with this service.
//
// They are declared by hand rather than generated from a goctl .api file. The
// repository's rule that a contract has exactly one source applies: the .api
// DSL would be a second description of these same structs, kept in sync by
// habit, and goctl's regeneration would overwrite the doc comments this
// repository requires. What goctl buys — the routing boilerplate — is a page
// of code in routes.go.
//
// types 包是 Admin API 的线上格式：Console 与 Gateway 同本服务交换的请求与响应形状。
//
// 它们是手写的，而不是由 goctl 的 .api 文件生成。仓库那条「一份契约只有一个来源」的
// 规则在这里适用：.api DSL 会成为对同一批结构体的第二份描述，靠习惯来保持同步，而
// goctl 的重新生成会覆盖掉本仓库要求的 doc comment。goctl 换来的那部分——路由样板
// ——在 routes.go 里不过一页代码。
package types

import "time"

// LoginRequest is a Console sign-in.
//
// LoginRequest 是一次 Console 登录。
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse carries the session token and who it belongs to. It never
// carries the password digest.
//
// LoginResponse 携带会话令牌及其归属。它绝不携带密码摘要。
type LoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      User      `json:"user"`
}

// User is a person, as rendered to the Console. PasswordHash has no field
// here, and that absence is the point: a response struct that cannot express
// the digest cannot leak it, however the handler is later edited.
//
// User 是渲染给 Console 的一个人。这里没有 PasswordHash 字段，而这个缺席正是要点：
// 一个无法表达该摘要的响应结构体，无论 handler 之后被怎么改动，都泄漏不了它。
type User struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// CreateTenantRequest bootstraps a tenant and its first owner.
//
// CreateTenantRequest 引导创建一个租户及其第一个 owner。
type CreateTenantRequest struct {
	Name          string `json:"name"`
	OwnerEmail    string `json:"owner_email"`
	OwnerPassword string `json:"owner_password"`
}

// CreateTenantResponse is the created tenant and owner.
//
// CreateTenantResponse 是已创建的租户与 owner。
type CreateTenantResponse struct {
	Tenant Tenant `json:"tenant"`
	Owner  User   `json:"owner"`
}

// Tenant is an isolation boundary, as rendered to the Console.
//
// Tenant 是渲染给 Console 的一个隔离边界。
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateUserRequest adds a user to the caller's tenant.
//
// CreateUserRequest 向调用方所属租户添加一个用户。
type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Role     string `json:"role"`
}

// CreateAPIKeyRequest mints a key.
//
// CreateAPIKeyRequest 铸造一个 key。
type CreateAPIKeyRequest struct {
	Name string `json:"name"`
	// TTLSeconds is the key's lifetime. Zero uses the service default; a
	// negative value means no expiry, which the API keeps expressible so an
	// operator does not resort to passing a hundred years.
	//
	// TTLSeconds 是该 key 的生命期。为零使用服务默认值；负值表示不过期——API 保留
	// 这种表达方式，好让运维不必退而求其次去传一百年。
	TTLSeconds int64 `json:"ttl_seconds"`
}

// CreateAPIKeyResponse is the only response in this API that carries a
// plaintext key. It is returned once, to the call that created it, and no
// read path can produce it again.
//
// CreateAPIKeyResponse 是本 API 中唯一携带明文 key 的响应。它只向创建它的那次调用
// 返回一次，之后没有任何读取路径能再次产生它。
type CreateAPIKeyResponse struct {
	// Key is the plaintext. Show it to the person once and do not store it.
	//
	// Key 是明文。向本人展示一次，不要存储。
	Key    string `json:"key"`
	APIKey APIKey `json:"api_key"`
}

// APIKey is a credential, as rendered to the Console. It carries the display
// form and never the hash: the hash is what a verification looks up by, and
// the Console has no use for it.
//
// APIKey 是渲染给 Console 的一个凭据。它携带 display 形式，绝不携带哈希：哈希是校验
// 时据以查询的值，而 Console 用不着它。
type APIKey struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	Name       string     `json:"name"`
	Display    string     `json:"display"`
	Status     string     `json:"status"`
	CreatedBy  string     `json:"created_by"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// AuditEntry is one administrative action, as rendered to the Console.
//
// AuditEntry 是渲染给 Console 的一次管理操作。
type AuditEntry struct {
	ID        string    `json:"id"`
	ActorID   string    `json:"actor_id"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Detail    string    `json:"detail"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

// VerifyRequest is the Gateway's key verification. It carries the hash, never
// the key: the Gateway hashes the caller's key itself, so a user's credential
// never travels to the control plane and never appears in its request logs.
//
// VerifyRequest 是 Gateway 的 key 校验请求。它携带哈希，绝不携带 key：Gateway 自己
// 对调用方的 key 做哈希，因此用户的凭据从不传到控制面，也从不出现在它的请求日志里。
type VerifyRequest struct {
	Hash string `json:"hash"`
}

// VerifyResponse tells the Gateway which tenant to attribute a request to.
// It carries nothing else — not the key's name, not its creator, not its
// expiry — because the data plane has no use for any of it and every extra
// field is one more thing to invalidate when it changes.
//
// VerifyResponse 告诉 Gateway 该把一次请求归给哪个租户。它别无其他内容——不含 key
// 的名称、创建者或过期时间——因为数据面用不着它们，而每多一个字段，就多一样在它变化
// 时需要失效的东西。
type VerifyResponse struct {
	TenantID string `json:"tenant_id"`
	KeyID    string `json:"key_id"`
}

// ErrorResponse is the failure shape every endpoint returns.
//
// ErrorResponse 是每个端点返回失败时的形状。
type ErrorResponse struct {
	Error string `json:"error"`
}
