package logic

import (
	"context"
	"errors"
	"strings"
	"time"

	"AIServeWeave/common/apikey"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// DefaultKeyLifetime is the expiry given to a key whose creator did not ask
// for one. A key that never expires is a key that is still valid the day it
// surfaces in an old repository or a departed employee's laptop, so the
// default is a bounded lifetime and an unbounded one has to be asked for
// explicitly.
//
// DefaultKeyLifetime 是创建者未指定时给一个 key 的过期时间。永不过期的 key，在它某天
// 出现在旧仓库或离职员工的笔记本里时依然有效，因此默认值是一个有界的生命期，无界的
// 那种必须显式索取。
const DefaultKeyLifetime = 90 * 24 * time.Hour

// MaxKeyLifetime bounds what a caller may ask for. An explicit "no expiry" is
// still expressible — see CreateAPIKey — but it is a separate, visible choice
// rather than the effect of passing a very large number.
//
// MaxKeyLifetime 限制调用方可以索取的上限。显式的「不过期」仍然可以表达——见
// CreateAPIKey——但那是一个独立且可见的选择，而不是传入一个很大的数字所产生的副作用。
const MaxKeyLifetime = 365 * 24 * time.Hour

// CreatedKey is the result of minting a key. Plaintext is present here and
// nowhere else in this service: no read path ever reconstructs it.
//
// CreatedKey 是铸造一个 key 的结果。Plaintext 只在此处出现，本服务的其他任何地方都
// 没有它：没有任何读取路径能重建它。
type CreatedKey struct {
	Key model.APIKey
	// Plaintext must be shown to the requester and then discarded by every
	// layer above. It must not be logged, cached or written to a response
	// that is stored.
	//
	// Plaintext 必须展示给索取者，然后被上面每一层丢弃。它不得被记录日志、缓存，
	// 或写进一个会被存储的响应里。
	Plaintext string
}

// CreateAPIKey mints a key for the actor's tenant.
//
// lifetime of zero uses DefaultKeyLifetime; a negative lifetime means no
// expiry, which is the explicit, deliberate form of that choice.
//
// CreateAPIKey 为 actor 所属租户铸造一个 key。
//
// lifetime 为零时使用 DefaultKeyLifetime；为负数表示不过期，这是该选择显式而审慎的
// 表达形式。
func (s *Service) CreateAPIKey(ctx context.Context, actor Actor, name string, lifetime time.Duration) (CreatedKey, error) {
	if !actor.canManageKeys() {
		return CreatedKey{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return CreatedKey{}, ErrInvalidInput
	}
	if lifetime > MaxKeyLifetime {
		return CreatedKey{}, ErrInvalidInput
	}

	generated, err := apikey.Generate()
	if err != nil {
		return CreatedKey{}, err
	}

	now := s.clock.Now()
	key := model.APIKey{
		ID:        model.NewID(model.PrefixAPIKey),
		TenantID:  actor.TenantID,
		CreatedBy: actor.UserID,
		Name:      name,
		Hash:      generated.Hash,
		Display:   generated.Display,
		Status:    model.StatusActive,
		CreatedAt: now,
	}
	switch {
	case lifetime == 0:
		expiry := now.Add(DefaultKeyLifetime)
		key.ExpiresAt = &expiry
	case lifetime > 0:
		expiry := now.Add(lifetime)
		key.ExpiresAt = &expiry
	}

	if err := s.store.CreateAPIKey(ctx, &key); err != nil {
		return CreatedKey{}, translate(err)
	}
	// The audit detail names the key by its display form, never its
	// plaintext: this table is read by more people than any other.
	//
	// 审计详情用 display 形式指代该 key，绝不用明文：这张表比其他任何一张都有更多
	// 人在读。
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionAPIKeyCreate, key.ID, "created "+key.Display, actor.IP)

	return CreatedKey{Key: key, Plaintext: generated.Plaintext}, nil
}

// ListAPIKeys returns the actor's tenant's keys. The stored hash is cleared
// before returning: it is not a secret in the sense the plaintext is, but it
// is the exact value a verification looks up by, and nothing above this layer
// has a use for it.
//
// ListAPIKeys 返回 actor 所属租户的 key。返回前会清空存储的哈希：它不像明文那样是
// 秘密，但它正是校验时据以查询的那个值，而本层之上没有任何地方用得着它。
func (s *Service) ListAPIKeys(ctx context.Context, actor Actor) ([]model.APIKey, error) {
	keys, err := s.store.ListAPIKeys(ctx, actor.TenantID)
	if err != nil {
		return nil, translate(err)
	}
	for i := range keys {
		keys[i].Hash = ""
	}
	return keys, nil
}

// RevokeAPIKey revokes one key belonging to the actor's tenant. A member may
// revoke only a key it created; an owner or admin may revoke any of the
// tenant's keys.
//
// RevokeAPIKey 吊销 actor 所属租户的一个 key。member 只能吊销自己创建的 key；owner
// 与 admin 可以吊销该租户的任何 key。
func (s *Service) RevokeAPIKey(ctx context.Context, actor Actor, keyID string) error {
	keys, err := s.store.ListAPIKeys(ctx, actor.TenantID)
	if err != nil {
		return translate(err)
	}
	var target model.APIKey
	for _, key := range keys {
		if key.ID == keyID {
			target = key
			break
		}
	}
	if target.ID == "" {
		return ErrNotFound
	}
	if !actor.canManageKeys() && target.CreatedBy != actor.UserID {
		// Reported as not-found rather than forbidden: a member who can tell
		// "exists but not yours" from "does not exist" can enumerate the
		// tenant's keys.
		//
		// 报为 not-found 而不是 forbidden：能分辨「存在但不属于你」与「不存在」的
		// member，可以据此枚举出该租户的全部 key。
		return ErrNotFound
	}

	if err := s.store.RevokeAPIKey(ctx, actor.TenantID, keyID, s.clock.Now()); err != nil {
		return translate(err)
	}
	// The cache is dropped after the database write, not before: invalidating
	// first leaves a window in which a concurrent verification re-populates
	// the cache from a row that is still active, and the key would then stay
	// usable for a full TTL after a revocation that had already succeeded.
	//
	// 缓存在数据库写入之后才清除，而不是之前：先失效会留下一个窗口，期间一次并发校验
	// 会用一行仍然 active 的记录把缓存重新填上，于是在一次已经成功的吊销之后，该 key
	// 还会继续可用整整一个 TTL。
	if s.invalidator != nil {
		s.invalidator.Invalidate(ctx, target.Hash)
	}
	s.audit(ctx, actor.TenantID, actor.UserID, model.ActionAPIKeyRevoke, keyID, "revoked "+target.Display, actor.IP)
	return nil
}

// Verification is what the Gateway learns about a key it presented. It carries
// no name, no creator and no timestamps: the data plane needs to know which
// tenant to attribute the request to, and nothing more.
//
// Verification 是 Gateway 就它出示的 key 所能得知的内容。它不带名称、不带创建者、不带
// 时间戳：数据面只需要知道把这次请求归给哪个租户，别无其他。
type Verification struct {
	TenantID string
	KeyID    string
}

// VerifyKeyHash resolves a key hash to the tenant it authenticates, or
// ErrNotFound when the key is unknown, revoked or expired.
//
// It takes the hash rather than the key, and that is a deliberate boundary:
// the Gateway hashes the plaintext itself, so a caller's key never travels to
// this service, never appears in its request logs, and is never held in its
// memory. Everything this method needs — a lookup and a validity check — works
// just as well on the hash.
//
// The three failure modes collapse into one error on purpose. A caller
// probing with a stolen, expired key learns only that it does not work.
//
// VerifyKeyHash 把一个 key 哈希解析为它所认证的租户；key 未知、已吊销或已过期时返回
// ErrNotFound。
//
// 它接收哈希而不是 key，这是一条刻意划下的边界：Gateway 自己对明文做哈希，因此调用方
// 的 key 从不传到本服务、从不出现在它的请求日志里、也从不驻留在它的内存中。本方法所需
// 的一切——一次查询与一次有效性检查——在哈希上同样成立。
//
// 三种失败模式被刻意收敛成同一个错误。拿着一个被盗且已过期的 key 试探的调用方，只能
// 得知它不管用。
func (s *Service) VerifyKeyHash(ctx context.Context, hash string) (Verification, error) {
	if len(hash) != 64 {
		return Verification{}, ErrNotFound
	}
	key, err := s.store.GetAPIKeyByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Verification{}, ErrNotFound
		}
		return Verification{}, err
	}
	if !key.Usable(s.clock.Now()) {
		return Verification{}, ErrNotFound
	}
	return Verification{TenantID: key.TenantID, KeyID: key.ID}, nil
}

// TouchAPIKey records that a key was used. The Gateway calls it at most once
// per key per interval, not once per request — see the store method's note.
//
// TouchAPIKey 记录一个 key 被使用过。Gateway 对每个 key 每个间隔最多调用一次，而不是
// 每次请求一次——见 store 侧方法的说明。
func (s *Service) TouchAPIKey(ctx context.Context, keyID string) error {
	return translate(s.store.MarkAPIKeyUsed(ctx, keyID, s.clock.Now()))
}
