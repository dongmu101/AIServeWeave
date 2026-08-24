package logic_test

import (
	"context"
	"os"
	goruntime "runtime"
	"sync"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
)

// TestMain asserts no test in this package leaks a goroutine, per the README
// quality gate.
//
// TestMain 断言本包没有测试泄漏协程，对应 README 的质量门禁。
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

// fakeClock is the injected clock these tests advance by hand, per the
// repository's rule that a test never waits on real time to make time pass.
// Key expiry is measured in days; a test that slept for it would not finish.
//
// fakeClock 是这些测试手动推进的注入时钟，遵循仓库那条规则：测试绝不靠真实等待来
// 让时间流逝。key 的过期以天计；靠 sleep 等它的测试永远跑不完。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	// A fixed, obviously synthetic instant, so a timestamp that leaks into a
	// failure message is recognizable as the test's rather than today's.
	//
	// 一个固定且一望即知是人造的时刻，这样泄漏到失败信息里的时间戳能被认出是测试的，
	// 而不是今天的。
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	// No test in this package waits on a timer; returning a channel that
	// never fires is honest about that, and would deadlock loudly rather than
	// pass quietly if one ever started to.
	//
	// 本包没有测试等待定时器；返回一个永不触发的通道如实反映了这一点，且一旦将来
	// 真有测试开始等待，它会明确地死锁，而不是悄悄通过。
	return make(chan time.Time), func() bool { return true }
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var _ runtime.Clock = (*fakeClock)(nil)

// fixture is one service under test with its store and clock, plus a tenant
// and an owner already created — the state every test here starts from.
//
// fixture 是一个被测服务及其 store 与时钟，外加一个已创建好的租户与 owner——这是
// 本包每个测试的起点状态。
type fixture struct {
	t       *testing.T
	svc     *logic.Service
	store   *memstore.Store
	clock   *fakeClock
	tenant  model.Tenant
	owner   model.User
	ownerAt logic.Actor
}

const testPassword = "correct-horse-battery"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := memstore.New()
	clock := newFakeClock()
	svc := logic.New(st, clock)

	tenant, owner, err := svc.CreateTenant(context.Background(), "Acme", "owner@example.com", testPassword, "10.0.0.1")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	return &fixture{
		t:      t,
		svc:    svc,
		store:  st,
		clock:  clock,
		tenant: tenant,
		owner:  owner,
		ownerAt: logic.Actor{
			UserID:   owner.ID,
			TenantID: tenant.ID,
			Role:     owner.Role,
			IP:       "10.0.0.1",
		},
	}
}

// actorWithRole returns an actor in the fixture's tenant holding role, for
// the permission tests. It does not create a user row: the permission checks
// under test read the actor, not the database.
//
// actorWithRole 返回 fixture 所属租户中持有指定角色的 actor，供权限测试使用。它不
// 创建用户行：被测的权限检查读的是 actor，不是数据库。
func (f *fixture) actorWithRole(role string) logic.Actor {
	return logic.Actor{
		UserID:   model.NewID(model.PrefixUser),
		TenantID: f.tenant.ID,
		Role:     role,
		IP:       "10.0.0.2",
	}
}

// mustCreateKey mints a key or fails the test.
//
// mustCreateKey 铸造一个 key，失败则让测试失败。
func (f *fixture) mustCreateKey(actor logic.Actor, name string) logic.CreatedKey {
	f.t.Helper()
	created, err := f.svc.CreateAPIKey(context.Background(), actor, name, 0)
	if err != nil {
		f.t.Fatalf("CreateAPIKey(%q): %v", name, err)
	}
	return created
}
