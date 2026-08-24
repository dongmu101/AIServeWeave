// Package token issues and verifies the session tokens the Console holds
// after signing in.
//
// It uses golang-jwt/jwt/v5 directly rather than go-zero's built-in JWT
// middleware. The middleware is bound to jwt/v4, and it delivers claims by
// putting each one into the request context under its own bare string key —
// two things worth avoiding when the alternative is this file. What is
// produced here is an ordinary HS256 JWT, so nothing about that choice is
// visible to a client, and a later move back to the middleware would not
// invalidate a single issued token.
//
// token 包签发并校验 Console 登录后持有的会话令牌。
//
// 它直接使用 golang-jwt/jwt/v5，而不是 go-zero 内置的 JWT 中间件。那个中间件绑定在
// jwt/v4 上，并且它传递 claims 的方式是把每一个 claim 以裸字符串为键塞进请求 context
// ——在替代方案只是这一个文件的情况下，这两点都值得避开。这里产出的是一个普通的
// HS256 JWT，因此这个选择对客户端完全不可见，将来若改回用中间件，也不会让任何一个
// 已签发的令牌失效。
package token

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"AIServeWeave/common/runtime"
)

// ErrInvalid is returned for every token that will not be accepted, whatever
// the reason — expired, tampered with, signed by someone else, or malformed.
// A caller holding a bad token can do nothing differently for any of them.
//
// ErrInvalid 用于所有不会被接受的令牌，无论原因为何——过期、被篡改、由他人签名，
// 或格式错误。持有一个坏令牌的调用方，对上述任何一种情况都做不出不同的应对。
var ErrInvalid = errors.New("token: invalid token")

// Claims is what a session token asserts. It carries identity and role and
// nothing else: a token is presented on every request, and every field added
// here is a field that goes stale the moment an administrator changes it.
//
// Claims 是一个会话令牌所主张的内容。它只携带身份与角色，别无其他：令牌会在每个请求
// 上出示，而这里每多一个字段，就多一个在管理员改动它的那一刻就开始过期的字段。
type Claims struct {
	UserID   string
	TenantID string
	Role     string
}

// Claim names, fixed here because they are part of the token format: changing
// one invalidates every session in flight.
//
// claim 的名字固定在这里，因为它们是令牌格式的一部分：改动其中任何一个，都会让所有
// 在途会话失效。
const (
	claimUserID   = "uid"
	claimTenantID = "tid"
	claimRole     = "role"
)

// signingMethod is the one algorithm this service issues and accepts.
//
// Pinning it is what closes the algorithm-confusion hole: a parser that
// honors the algorithm named in the token's own header will happily verify a
// token an attacker re-signed with "none", or with HMAC using a public key as
// the secret. The token says what it is; this package decides what it will
// accept.
//
// signingMethod 是本服务签发与接受的唯一算法。
//
// 把它钉死，正是堵上算法混淆漏洞的做法：一个听信令牌自身 header 中所声明算法的解析器，
// 会欣然接受攻击者用 "none" 重新签名的令牌，或者用公钥当作密钥做 HMAC 的令牌。令牌
// 只是自称它是什么；由本包决定它愿意接受什么。
var signingMethod = jwt.SigningMethodHS256

// minSecretLen mirrors config.minSecretLen. It is repeated rather than shared
// because this package must not import config — config imports the service's
// framework, and a token issuer that drags a web framework into its tests is
// one nobody will test.
//
// minSecretLen 与 config.minSecretLen 一致。这里重复而不是共用，是因为本包不能 import
// config——config 会引入本服务的框架，而一个把 web 框架拖进自己测试里的令牌签发器，
// 是没人会去测的。
const minSecretLen = 32

// Issuer signs and verifies session tokens. Construct one with NewIssuer.
//
// Issuer 签发并校验会话令牌。用 NewIssuer 构造。
type Issuer struct {
	secret   []byte
	lifetime time.Duration
	clock    runtime.Clock
}

// NewIssuer returns an Issuer over secret. It fails rather than warns on a
// short secret: an HS256 token is only as strong as the string it is signed
// with, and a service that starts with a weak one issues tokens that look
// exactly like strong ones.
//
// NewIssuer 基于 secret 返回一个 Issuer。密钥过短时它直接失败而不是告警：HS256 令牌
// 的强度不会超过给它签名的那个字符串，而一个用弱密钥启动的服务，签发出的令牌看起来
// 与强密钥签发的一模一样。
func NewIssuer(secret string, lifetime time.Duration, clock runtime.Clock) (*Issuer, error) {
	if len(secret) < minSecretLen {
		return nil, errors.New("token: the signing secret must be at least 32 characters")
	}
	if lifetime <= 0 {
		return nil, errors.New("token: the lifetime must be positive")
	}
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	return &Issuer{secret: []byte(secret), lifetime: lifetime, clock: clock}, nil
}

// Issue returns a signed token for c and the instant it expires.
//
// Issue 返回 c 对应的已签名令牌，以及它过期的时刻。
func (i *Issuer) Issue(c Claims) (string, time.Time, error) {
	now := i.clock.Now()
	expiry := now.Add(i.lifetime)

	tok := jwt.NewWithClaims(signingMethod, jwt.MapClaims{
		claimUserID:   c.UserID,
		claimTenantID: c.TenantID,
		claimRole:     c.Role,
		"iat":         now.Unix(),
		"exp":         expiry.Unix(),
	})
	signed, err := tok.SignedString(i.secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiry, nil
}

// Parse verifies a token and returns what it asserts. Every failure is
// ErrInvalid.
//
// Parse 校验一个令牌并返回它所主张的内容。所有失败都是 ErrInvalid。
func (i *Issuer) Parse(signed string) (Claims, error) {
	parsed, err := jwt.Parse(signed,
		func(*jwt.Token) (any, error) { return i.secret, nil },
		jwt.WithValidMethods([]string{signingMethod.Alg()}),
		// The library's clock is replaced by the injected one, so an expiry
		// test advances a fake clock instead of waiting out a real session.
		//
		// 用注入的时钟替换库自带的时钟，这样过期测试可以推进假时钟，而不必真的等完
		// 一个会话周期。
		jwt.WithTimeFunc(i.clock.Now),
	)
	if err != nil || !parsed.Valid {
		return Claims{}, ErrInvalid
	}

	mapped, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return Claims{}, ErrInvalid
	}
	claims := Claims{
		UserID:   stringClaim(mapped, claimUserID),
		TenantID: stringClaim(mapped, claimTenantID),
		Role:     stringClaim(mapped, claimRole),
	}
	// A token missing an identity is refused rather than passed along as an
	// empty one: an empty TenantID would scope a query to no tenant, and
	// "no tenant" is one typo away from "every tenant".
	//
	// 缺少身份的令牌会被拒绝，而不是当作空身份放行：空的 TenantID 会把查询限定到
	// 「没有租户」，而「没有租户」离「所有租户」只差一个笔误。
	if claims.UserID == "" || claims.TenantID == "" || claims.Role == "" {
		return Claims{}, ErrInvalid
	}
	return claims, nil
}

func stringClaim(claims jwt.MapClaims, name string) string {
	value, _ := claims[name].(string)
	return value
}
