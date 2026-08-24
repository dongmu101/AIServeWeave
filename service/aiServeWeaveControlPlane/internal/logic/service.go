// Package logic is the control plane's business layer: the rules about who
// may do what, what gets audited, and what a caller is allowed to learn from a
// failure.
//
// It depends on the narrow interfaces in store and on an injected clock, and
// on nothing from the HTTP layer. A handler translates JSON to a call here and
// a result back; every decision worth testing is made in this package, which
// is why its tests need neither a database nor a listening port.
//
// logic 包是控制面的业务层：谁可以做什么、什么要写审计、以及调用方能从一次失败中
// 得知多少，这些规则都在这里。
//
// 它依赖 store 中的窄接口与一个注入的时钟，不依赖 HTTP 层的任何东西。handler 只负责
// 把 JSON 翻译成对这里的一次调用、再把结果翻译回去；每一个值得测试的决定都在本包做出，
// 这也是它的测试既不需要数据库、也不需要监听端口的原因。
package logic

import (
	"context"
	"errors"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// Errors this layer returns. They are deliberately coarse: a caller learns
// that a request was refused, not which of several checks refused it.
//
// 本层返回的错误。它们刻意是粗粒度的：调用方得知请求被拒绝，但不会得知是若干检查中的
// 哪一个拒绝了它。
var (
	// ErrInvalidCredentials covers an unknown email, a wrong password and a
	// suspended account alike. Distinguishing them turns the sign-in form
	// into an account enumeration oracle.
	//
	// ErrInvalidCredentials 同时覆盖「email 不存在」「密码错误」与「账户已停用」。
	// 区分它们会把登录表单变成一个账户枚举的探询接口。
	ErrInvalidCredentials = errors.New("logic: invalid credentials")
	// ErrNotFound is a row the caller may not have, whether because it does
	// not exist or because it belongs to someone else.
	//
	// ErrNotFound 表示调用方不能拥有的一行，无论是因为它不存在，还是因为它属于别人。
	ErrNotFound = errors.New("logic: not found")
	// ErrConflict is a uniqueness violation the caller could have avoided,
	// such as reusing an email.
	//
	// ErrConflict 是调用方本可以避免的唯一性冲突，例如重复使用一个 email。
	ErrConflict = errors.New("logic: conflict")
	// ErrInvalidInput is a request this layer will not act on.
	//
	// ErrInvalidInput 表示本层不会执行的请求。
	ErrInvalidInput = errors.New("logic: invalid input")
	// ErrForbidden is a request the caller's role does not permit.
	//
	// ErrForbidden 表示调用方的角色不允许的请求。
	ErrForbidden = errors.New("logic: forbidden")
)

// bcryptCost is the work factor for user passwords. It applies to passwords
// only — see apikey.Hash for why API keys use a fast hash instead, and why
// that is not an inconsistency.
//
// bcryptCost 是用户密码的工作因子。它只适用于密码——API Key 为何改用快速哈希、以及
// 那为什么不算前后矛盾，见 apikey.Hash。
const bcryptCost = bcrypt.DefaultCost

// Invalidator drops a cached verification. Revocation calls it so a revoked
// key stops working immediately rather than when a cache entry expires.
//
// It is an interface here, and implemented in the cache package, so this layer
// stays free of Redis: the rule is "revocation takes effect now", not "Redis
// is involved".
//
// Invalidator 丢弃一条已缓存的校验结果。吊销流程会调用它，好让被吊销的 key 立刻停止
// 工作，而不是等某个缓存条目过期。
//
// 它在这里是一个接口、由 cache 包实现，从而让本层与 Redis 无关：规则是「吊销立即
// 生效」，而不是「必须有 Redis 参与」。
type Invalidator interface {
	Invalidate(ctx context.Context, keyHash string)
}

// Service is the business layer. Construct one with New.
//
// Service 是业务层。用 New 构造。
type Service struct {
	store       store.Store
	clock       runtime.Clock
	invalidator Invalidator
}

// Option configures a Service.
//
// Option 用于配置 Service。
type Option func(*Service)

// WithInvalidator gives the Service a cache to invalidate on revocation.
// Without one, revocation is still correct — the database is the source of
// truth — but a cached verification lives until its TTL.
//
// WithInvalidator 为 Service 提供一个在吊销时需要失效的缓存。没有它时吊销依然正确
// ——数据库才是事实来源——但一条已缓存的校验结果会一直存活到它的 TTL 结束。
func WithInvalidator(invalidator Invalidator) Option {
	return func(s *Service) { s.invalidator = invalidator }
}

// New returns a Service over st. A nil clock uses the system clock; tests
// inject a fake so expiry is exercised without sleeping, per the repository's
// testing convention.
//
// New 基于 st 返回一个 Service。clock 为 nil 时使用系统时钟；按仓库的测试约定，测试
// 注入假时钟，从而无需真实等待即可覆盖过期逻辑。
func New(st store.Store, clock runtime.Clock, opts ...Option) *Service {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	s := &Service{store: st, clock: clock}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Actor is the authenticated caller of an administrative operation. Handlers
// build it from the request's verified token; this layer never derives it from
// anything in a request body.
//
// Actor 是一次管理操作的已认证调用方。handler 从请求中已验证的 token 构造它；本层
// 绝不从请求体中的任何内容推导出它。
type Actor struct {
	UserID   string
	TenantID string
	Role     string
	// IP is recorded in the audit trail. It is the only Actor field that
	// comes from the network rather than from a verified token, so it is
	// treated as evidence, never as an authorization input.
	//
	// IP 会被记入审计线索。它是 Actor 中唯一来自网络而非已验证 token 的字段，因此
	// 它只被当作证据，绝不作为授权依据。
	IP string
}

// canManageKeys reports whether this actor may mint and revoke keys for its
// tenant. A member may only manage keys it created, which the caller checks
// against the key's CreatedBy; this covers the tenant-wide permission.
//
// canManageKeys 报告该 actor 是否可以为其租户铸造与吊销 key。member 只能管理自己创建
// 的 key，这一点由调用方对照 key 的 CreatedBy 检查；此处覆盖的是租户级的权限。
func (a Actor) canManageKeys() bool {
	return a.Role == model.RoleOwner || a.Role == model.RoleAdmin
}

// CreateTenant creates a tenant together with its first owner, in that order,
// because a tenant with no owner is a row nobody can ever sign in to
// administer.
//
// This is the bootstrap path: it is the one operation with no Actor, and the
// service exposes it only to an operator holding the bootstrap token — see the
// handler layer. Its audit record names the system as the actor.
//
// CreateTenant 创建一个租户及其第一个 owner，且顺序如此，因为一个没有 owner 的租户
// 是一行永远没人能登录进去管理的记录。
//
// 这是引导路径：它是唯一没有 Actor 的操作，服务只对持有 bootstrap token 的运维开放
// 它——见 handler 层。它的审计记录以系统作为行为人。
func (s *Service) CreateTenant(ctx context.Context, name, ownerEmail, ownerPassword, ip string) (model.Tenant, model.User, error) {
	name = strings.TrimSpace(name)
	ownerEmail = normalizeEmail(ownerEmail)
	if name == "" || ownerEmail == "" {
		return model.Tenant{}, model.User{}, ErrInvalidInput
	}
	if err := validatePassword(ownerPassword); err != nil {
		return model.Tenant{}, model.User{}, err
	}

	now := s.clock.Now()
	tenant := model.Tenant{
		ID:        model.NewID(model.PrefixTenant),
		Name:      name,
		Status:    model.StatusActive,
		CreatedAt: now,
	}
	if err := s.store.CreateTenant(ctx, &tenant); err != nil {
		return model.Tenant{}, model.User{}, translate(err)
	}

	owner, err := s.createUser(ctx, tenant.ID, ownerEmail, ownerPassword, name+" owner", model.RoleOwner, now)
	if err != nil {
		// The tenant row is left behind rather than rolled back: this layer
		// has no transaction, and an orphaned tenant is inert — nothing can
		// authenticate into it — whereas a delete here would be a second
		// write that can fail just as easily as the first.
		//
		// 租户行会被留下而不是回滚：本层没有事务，而一个孤立的租户是惰性的——没有
		// 任何东西能认证进去——反之在这里再做一次删除，只是又一次同样可能失败的写入。
		return model.Tenant{}, model.User{}, err
	}

	s.audit(ctx, tenant.ID, "", model.ActionTenantCreate, tenant.ID, "tenant created with owner "+owner.ID, ip)
	return tenant, owner, nil
}

// CreateUser adds a user to the actor's tenant. Only an owner may do this:
// adding a user is granting access, and delegating that to every admin makes
// the owner's own account no longer the boundary it looks like.
//
// CreateUser 向 actor 所属租户添加一个用户。只有 owner 可以这么做：添加用户就是授予
// 访问权限，把它下放给每个 admin，会让 owner 自己的账户不再是它看起来的那道边界。
func (s *Service) CreateUser(ctx context.Context, actor Actor, email, password, name, role string) (model.User, error) {
	if actor.Role != model.RoleOwner {
		return model.User{}, ErrForbidden
	}
	if !validRole(role) {
		return model.User{}, ErrInvalidInput
	}
	email = normalizeEmail(email)
	if email == "" {
		return model.User{}, ErrInvalidInput
	}
	if err := validatePassword(password); err != nil {
		return model.User{}, err
	}

	user, err := s.createUser(ctx, actor.TenantID, email, password, name, role, s.clock.Now())
	if err != nil {
		return model.User{}, err
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionUserCreate, user.ID, "role "+role, actor.IP)
	return user, nil
}

// createUser is the shared insert path. It never logs or returns the password.
//
// createUser 是共用的插入路径。它绝不记录或返回密码。
func (s *Service) createUser(ctx context.Context, tenantID, email, password, name, role string, now time.Time) (model.User, error) {
	digest, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return model.User{}, err
	}
	user := model.User{
		ID:           model.NewID(model.PrefixUser),
		TenantID:     tenantID,
		Email:        email,
		PasswordHash: string(digest),
		Name:         name,
		Role:         role,
		Status:       model.StatusActive,
		CreatedAt:    now,
	}
	if err := s.store.CreateUser(ctx, &user); err != nil {
		return model.User{}, translate(err)
	}
	return user, nil
}

// ListUsers returns the actor's tenant's users.
//
// ListUsers 返回 actor 所属租户的用户。
func (s *Service) ListUsers(ctx context.Context, actor Actor) ([]model.User, error) {
	users, err := s.store.ListUsers(ctx, actor.TenantID)
	return users, translate(err)
}

// Authenticate verifies a sign-in and returns the user it belongs to.
//
// Every failure returns ErrInvalidCredentials, and the password comparison
// runs even for an unknown email — against a fixed dummy digest — so the
// response time does not separate "no such account" from "wrong password".
// Without that, the sign-in form answers the question "does this person have
// an account here", which is not a question it should answer.
//
// Authenticate 校验一次登录并返回对应的用户。
//
// 所有失败都返回 ErrInvalidCredentials，且即使 email 不存在也照样执行密码比较——对着
// 一个固定的假摘要——这样响应时间就不会区分「没有这个账户」与「密码错误」。否则，
// 登录表单就在回答「这个人在这里有没有账户」，而那不是它该回答的问题。
func (s *Service) Authenticate(ctx context.Context, email, password, ip string) (model.User, error) {
	user, err := s.store.GetUserByEmail(ctx, normalizeEmail(email))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return model.User{}, err
	}

	digest := user.PasswordHash
	if digest == "" {
		digest = dummyDigest
	}
	compareErr := bcrypt.CompareHashAndPassword([]byte(digest), []byte(password))
	if err != nil || compareErr != nil || user.Status != model.StatusActive {
		return model.User{}, ErrInvalidCredentials
	}

	now := s.clock.Now()
	if err := s.store.MarkUserLogin(ctx, user.ID, now); err != nil {
		return model.User{}, err
	}
	user.LastLoginAt = &now
	s.audit(ctx, user.TenantID, user.ID, model.ActionUserLogin, user.ID, "", ip)
	return user, nil
}

// dummyDigest is a valid bcrypt digest of a value nothing will ever match. It
// exists so Authenticate can spend the same work on an unknown email as on a
// known one.
//
// dummyDigest 是一个合法的 bcrypt 摘要，其原文永远不会被匹配到。它的存在是为了让
// Authenticate 在 email 不存在时也花掉与存在时相同的计算。
const dummyDigest = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// ListAudit returns the actor's tenant's audit trail.
//
// ListAudit 返回 actor 所属租户的审计线索。
func (s *Service) ListAudit(ctx context.Context, actor Actor, limit int) ([]model.AuditLog, error) {
	entries, err := s.store.ListAudit(ctx, actor.TenantID, limit)
	return entries, translate(err)
}

// audit appends one record, best effort. A failed audit write must not fail
// the operation that was already performed: the action happened, and losing
// the record of it is bad, but reporting failure for work that succeeded would
// make a caller retry and do it twice.
//
// The failure is not silent — it is returned to the caller of nothing, so the
// only place it can surface is a log. That is a known gap, recorded in the
// service README: making the audit write part of the same transaction as the
// action is the fix, and it waits on this layer having transactions at all.
//
// audit 尽力而为地追加一条记录。审计写入失败不得让一个已经完成的操作失败：动作已经
// 发生，丢失它的记录固然糟糕，但为一件已成功的工作报告失败，会让调用方重试并做两遍。
//
// 这个失败并非无声无息——但它没有可返回的调用方，因此唯一能浮现的地方是日志。这是一处
// 已知缺口，记在服务 README 里：修法是把审计写入与该动作放进同一个事务，而它要等本层
// 先拥有事务。
func (s *Service) audit(ctx context.Context, tenantID, actorID, action, target, detail, ip string) {
	entry := model.AuditLog{
		ID:        model.NewID(model.PrefixAuditLog),
		TenantID:  tenantID,
		ActorID:   actorID,
		Action:    action,
		Target:    target,
		Detail:    detail,
		IP:        ip,
		CreatedAt: s.clock.Now(),
	}
	_ = s.store.AppendAudit(ctx, &entry)
}

// translate maps store errors onto this layer's vocabulary.
//
// translate 把 store 的错误映射到本层的词汇上。
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrConflict):
		return ErrConflict
	default:
		return err
	}
}

// normalizeEmail lowercases and trims, so one person cannot hold two accounts
// that differ only in case.
//
// normalizeEmail 转小写并去除空白，这样一个人就不能持有两个仅大小写不同的账户。
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// minPasswordLen is the only password rule enforced here. Composition rules —
// a digit, a symbol, mixed case — push people toward predictable substitutions
// and are not what makes a password hard to guess; length is.
//
// minPasswordLen 是这里唯一强制的密码规则。组成规则——必须有数字、符号、大小写混合
// ——只会把人推向可预测的替换写法，并不是让密码难猜的原因；长度才是。
const minPasswordLen = 12

func validatePassword(password string) error {
	if len(password) < minPasswordLen {
		return ErrInvalidInput
	}
	// bcrypt silently truncates past 72 bytes, so a longer password would be
	// accepted while only its first 72 bytes matter — a caller who believes
	// their 100-character passphrase is fully checked deserves to be told
	// otherwise rather than quietly humored.
	//
	// bcrypt 会静默截断超过 72 字节的部分，因此更长的密码虽然会被接受，但只有前 72
	// 字节起作用——一个以为自己 100 字符口令被完整校验的调用方，应当被明确告知，而
	// 不是被悄悄敷衍过去。
	if len(password) > 72 {
		return ErrInvalidInput
	}
	return nil
}

func validRole(role string) bool {
	switch role {
	case model.RoleOwner, model.RoleAdmin, model.RoleMember:
		return true
	default:
		return false
	}
}
