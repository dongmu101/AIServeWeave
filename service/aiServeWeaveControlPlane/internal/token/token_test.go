package token_test

import (
	"errors"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/token"
)

func TestMain(m *testing.M) {
	before := goruntime.NumGoroutine()
	code := m.Run()
	if code == 0 && !goroutineCountSettles(before) {
		os.Stderr.WriteString("leaked goroutines detected after tests completed\n")
		code = 1
	}
	os.Exit(code)
}

func goroutineCountSettles(baseline int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if goruntime.NumGoroutine() <= baseline {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	return make(chan time.Time), func() bool { return true }
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var _ runtime.Clock = (*fakeClock)(nil)

const testSecret = "0123456789abcdef0123456789abcdef"

func newIssuer(t *testing.T, clock runtime.Clock) *token.Issuer {
	t.Helper()
	issuer, err := token.NewIssuer(testSecret, time.Hour, clock)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return issuer
}

func TestIssuedTokenParsesBack(t *testing.T) {
	clock := newFakeClock()
	issuer := newIssuer(t, clock)
	want := token.Claims{UserID: "usr_1", TenantID: "tnt_1", Role: "owner"}

	signed, expiry, err := issuer.Issue(want)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !expiry.Equal(clock.Now().Add(time.Hour)) {
		t.Errorf("expiry = %v, want %v", expiry, clock.Now().Add(time.Hour))
	}

	got, err := issuer.Parse(signed)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got != want {
		t.Errorf("claims = %+v, want %+v", got, want)
	}
}

// TestTokenExpires asserts the lifetime is enforced at the boundary, using the
// injected clock rather than a real wait.
//
// TestTokenExpires 断言生命期在边界处生效，且使用注入的时钟而非真实等待。
func TestTokenExpires(t *testing.T) {
	clock := newFakeClock()
	issuer := newIssuer(t, clock)

	signed, _, err := issuer.Issue(token.Claims{UserID: "usr_1", TenantID: "tnt_1", Role: "owner"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	clock.Advance(time.Hour - time.Second)
	if _, err := issuer.Parse(signed); err != nil {
		t.Errorf("token rejected one second before expiry: %v", err)
	}

	clock.Advance(2 * time.Second)
	if _, err := issuer.Parse(signed); !errors.Is(err, token.ErrInvalid) {
		t.Errorf("expired token accepted: err = %v", err)
	}
}

// TestParseRejectsAlgorithmConfusion is the attack this package pins
// signingMethod for: a token that names "none" as its algorithm, or names a
// different HMAC variant, must not be honored just because it says so.
//
// TestParseRejectsAlgorithmConfusion 正是本包钉死 signingMethod 所要防的攻击：一个
// 自称算法为 "none"、或自称使用其他 HMAC 变体的令牌，不能仅因为它这么说就被认可。
func TestParseRejectsAlgorithmConfusion(t *testing.T) {
	clock := newFakeClock()
	issuer := newIssuer(t, clock)
	claims := jwt.MapClaims{
		"uid":  "usr_evil",
		"tid":  "tnt_victim",
		"role": "owner",
		"exp":  clock.Now().Add(time.Hour).Unix(),
	}

	tests := []struct {
		name  string
		forge func() string
	}{
		{
			name: "alg none",
			forge: func() string {
				tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
				signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
				if err != nil {
					t.Fatalf("forging an alg=none token: %v", err)
				}
				return signed
			},
		},
		{
			name: "a different HMAC variant",
			forge: func() string {
				tok := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
				signed, err := tok.SignedString([]byte(testSecret))
				if err != nil {
					t.Fatalf("forging an HS512 token: %v", err)
				}
				return signed
			},
		},
		{
			name: "signed with another secret",
			forge: func() string {
				tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
				signed, err := tok.SignedString([]byte(strings.Repeat("x", 32)))
				if err != nil {
					t.Fatalf("forging a token with another secret: %v", err)
				}
				return signed
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := issuer.Parse(tt.forge()); !errors.Is(err, token.ErrInvalid) {
				t.Errorf("forged token accepted: err = %v", err)
			}
		})
	}
}

func TestParseRejectsMalformedAndIncompleteTokens(t *testing.T) {
	clock := newFakeClock()
	issuer := newIssuer(t, clock)

	valid, _, err := issuer.Issue(token.Claims{UserID: "usr_1", TenantID: "tnt_1", Role: "owner"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// A token carrying no tenant: the case Parse refuses rather than passing
	// along, because an empty tenant scopes a query to nothing, which is one
	// typo away from everything.
	//
	// 一个不带租户的令牌：Parse 会拒绝而不是放行，因为空租户会把查询限定到「什么都
	// 没有」，而那离「所有东西」只差一个笔误。
	noTenant := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"uid":  "usr_1",
		"role": "owner",
		"exp":  clock.Now().Add(time.Hour).Unix(),
	})
	noTenantSigned, err := noTenant.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("signing a tenantless token: %v", err)
	}

	tests := []struct {
		name  string
		token string
	}{
		{name: "empty", token: ""},
		{name: "not a jwt", token: "hello"},
		{name: "truncated", token: valid[:len(valid)-8]},
		{name: "tampered signature", token: valid[:len(valid)-4] + "AAAA"},
		{name: "no tenant claim", token: noTenantSigned},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := issuer.Parse(tt.token); !errors.Is(err, token.ErrInvalid) {
				t.Errorf("Parse(%q) error = %v, want ErrInvalid", tt.name, err)
			}
		})
	}
}

func TestNewIssuerRefusesWeakConfiguration(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		lifetime time.Duration
	}{
		{name: "short secret", secret: "tooshort", lifetime: time.Hour},
		{name: "empty secret", secret: "", lifetime: time.Hour},
		{name: "zero lifetime", secret: testSecret, lifetime: 0},
		{name: "negative lifetime", secret: testSecret, lifetime: -time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := token.NewIssuer(tt.secret, tt.lifetime, nil); err == nil {
				t.Errorf("NewIssuer accepted a weak configuration")
			}
		})
	}
}
