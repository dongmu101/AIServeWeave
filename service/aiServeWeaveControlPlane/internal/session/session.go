// Package session stores the revocable server-side half of Console sessions.
// A signed JWT is accepted only while its matching record remains here.
//
// session 包存储 Console 可吊销会话的服务端部分。一个已签名 JWT 只有在这里仍有
// 与之匹配的记录时才会被接受。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"AIServeWeave/common/runtime"
)

// Errors returned by a Store.
//
// Store 返回的错误。
var (
	// ErrInvalid means a session or mutation gate does not exist, expired, or
	// does not match the identity presented by its caller.
	//
	// ErrInvalid 表示会话或变更门不存在、已过期，或与调用方出示的身份不匹配。
	ErrInvalid = errors.New("session: invalid")
	// ErrUnavailable means the authoritative session backend could not answer.
	// Callers must fail closed instead of accepting the JWT alone.
	//
	// ErrUnavailable 表示权威会话后端无法作答。调用方必须失效关闭，不能只接受 JWT。
	ErrUnavailable = errors.New("session: unavailable")
	// ErrMutationActive means an account lifecycle mutation has closed the
	// subject's login gate.
	//
	// ErrMutationActive 表示账户生命周期变更已关闭该主体的登录门。
	ErrMutationActive = errors.New("session: mutation active")
)

// Subject kinds keep tenant users and platform operators in separate Redis
// namespaces even if their ids could otherwise collide.
//
// 主体种类让租户用户与平台运维位于不同 Redis 命名空间，即使它们的 id 原本可能冲突。
const (
	SubjectTenantUser       = "tenant_user"
	SubjectPlatformOperator = "platform_operator"
)

// MaxSessions is the hard per-subject session bound.
//
// MaxSessions 是每个主体持有会话数的硬上限。
const MaxSessions = 20

const mutationTTL = 30 * time.Second

// Subject identifies the account that owns a session.
//
// Subject 标识拥有某个会话的账户。
type Subject struct {
	Kind string `json:"subject_kind"`
	ID   string `json:"subject_id"`
}

// Record is the non-secret identity half of a session. It never contains the
// signed JWT or another credential.
//
// Record 是会话中不含机密的身份部分。它绝不包含已签名 JWT 或其他凭据。
type Record struct {
	ID        string    `json:"id"`
	Subject   Subject   `json:"subject"`
	TenantID  string    `json:"tenant_id"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Gate is a nonce-protected lifecycle mutation gate. RevokedSessions is the
// number removed when the gate was acquired.
//
// Gate 是由 nonce 保护的生命周期变更门。RevokedSessions 是取得门时移除的会话数。
type Gate struct {
	Subject         Subject
	Nonce           string
	RevokedSessions int
}

// Store is the authoritative online session boundary.
//
// Store 是在线会话的权威边界。
type Store interface {
	Create(ctx context.Context, record Record) error
	Validate(ctx context.Context, id string, subject Subject, tenantID, role string) error
	Revoke(ctx context.Context, subject Subject, id string) (bool, error)
	RevokeAll(ctx context.Context, subject Subject) (int, error)
	BeginMutation(ctx context.Context, subject Subject) (Gate, error)
	EndMutation(ctx context.Context, gate Gate) error
}

// NewID returns a cryptographically random session identifier.
//
// NewID 返回一个密码学随机的会话标识符。
func NewID() string { return "ses_" + randomHex() }

func randomHex() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("controlplane: the system random source failed while minting a session id: " + err.Error())
	}
	return hex.EncodeToString(value[:])
}

func validSubject(subject Subject) bool {
	return subject.ID != "" && (subject.Kind == SubjectTenantUser || subject.Kind == SubjectPlatformOperator)
}

func validRecord(record Record, now time.Time) bool {
	return record.ID != "" && validSubject(record.Subject) && record.TenantID != "" && record.Role != "" && record.ExpiresAt.After(now)
}

func clockOrSystem(clock runtime.Clock) runtime.Clock {
	if clock == nil {
		return runtime.NewSystemClock()
	}
	return clock
}
