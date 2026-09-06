package logic_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// TestPagingCoversEveryRowExactlyOnce is the property that matters about
// pagination and the one an offset would break: walking the cursors must yield
// every row once, in order, with no gaps and no repeats.
//
// The keys here are created at a single instant, which is the hard case. With
// an offset that would be fine; with a keyset cursor it is only fine because
// the order is total — the id breaks the tie between rows sharing a timestamp.
// A cursor on a non-total order silently skips and repeats rows.
//
// TestPagingCoversEveryRowExactlyOnce 是分页真正要紧的那个性质，也是 offset 会破坏的
// 那个：沿着游标走下去，必须按顺序把每一行恰好取到一次，不漏也不重。
//
// 这里的 key 是在同一个瞬间创建的，那是困难情形。用 offset 无所谓；用 keyset 游标之所以
// 也没问题，仅仅是因为顺序是全序——id 打破了共享同一时间戳的行之间的平手。建立在非全序
// 之上的游标，会悄无声息地跳过并重复一些行。
func TestPagingCoversEveryRowExactlyOnce(t *testing.T) {
	const total = 25
	tests := []struct {
		name string
		size int
	}{
		{name: "one row per page", size: 1},
		{name: "an uneven page size", size: 7},
		{name: "a page larger than the list", size: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			for i := range total {
				f.mustCreateKey(f.ownerAt, fmt.Sprintf("key-%02d", i))
			}

			seen := make([]string, 0, total)
			cursor := ""
			for pages := 0; ; pages++ {
				if pages > total+1 {
					t.Fatalf("paging did not terminate after %d pages", pages)
				}
				page, err := f.svc.ListAPIKeys(context.Background(), f.ownerAt,
					store.ListQuery{Limit: tt.size, Cursor: cursor}, store.APIKeyFilter{})
				if err != nil {
					t.Fatalf("ListAPIKeys(cursor=%q): %v", cursor, err)
				}
				if len(page.Items) > tt.size {
					t.Fatalf("page holds %d rows, want at most %d", len(page.Items), tt.size)
				}
				for _, key := range page.Items {
					seen = append(seen, key.ID)
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}

			if len(seen) != total {
				t.Fatalf("paging yielded %d rows, want %d", len(seen), total)
			}
			unique := make(map[string]bool, len(seen))
			for _, id := range seen {
				if unique[id] {
					t.Errorf("row %q appeared twice", id)
				}
				unique[id] = true
			}
		})
	}
}

// TestPagingIsNotDisturbedByConcurrentWrites asserts what a keyset cursor buys
// over an offset: rows added after a page was read do not shift the rows still
// to come. These lists are written to while they are read — the audit trail
// continuously — and with an offset the reader would skip a row for every one
// inserted ahead of it.
//
// TestPagingIsNotDisturbedByConcurrentWrites 断言 keyset 游标相对 offset 换来的东西：
// 在某一页读过之后新增的行，不会让尚未读到的行发生位移。这些列表正是在被读取的同时被
// 写入的——审计线索更是持续不断——而用 offset，每往前面插入一行，读取者就会漏掉一行。
func TestPagingIsNotDisturbedByConcurrentWrites(t *testing.T) {
	f := newFixture(t)
	for i := range 6 {
		f.clock.Advance(time.Second)
		f.mustCreateKey(f.ownerAt, fmt.Sprintf("original-%d", i))
	}

	first, err := f.svc.ListAPIKeys(context.Background(), f.ownerAt,
		store.ListQuery{Limit: 3}, store.APIKeyFilter{})
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}

	// Three newer keys arrive between the two reads. They sort ahead of
	// everything the first page returned, which is exactly the situation that
	// makes an offset lie.
	//
	// 两次读取之间来了三个更新的 key。它们排在第一页返回的所有内容之前，而这正是让
	// offset 说谎的那种情形。
	for i := range 3 {
		f.clock.Advance(time.Second)
		f.mustCreateKey(f.ownerAt, fmt.Sprintf("inserted-%d", i))
	}

	second, err := f.svc.ListAPIKeys(context.Background(), f.ownerAt,
		store.ListQuery{Limit: 3, Cursor: first.NextCursor}, store.APIKeyFilter{})
	if err != nil {
		t.Fatalf("ListAPIKeys(cursor): %v", err)
	}

	if len(second.Items) != 3 {
		t.Fatalf("second page holds %d rows, want 3", len(second.Items))
	}
	for _, key := range second.Items {
		if key.Name[:8] == "inserted" {
			t.Errorf("the second page returned %q, a row written after the first page was read", key.Name)
		}
	}
	for _, earlier := range first.Items {
		for _, later := range second.Items {
			if earlier.ID == later.ID {
				t.Errorf("row %q appeared on both pages", earlier.ID)
			}
		}
	}
}

// TestPageSizeIsBounded asserts the cap is applied rather than trusted, and
// that an absent limit gets the default. An unbounded list read is how one
// caller pulls the whole audit table into this process's memory.
//
// TestPageSizeIsBounded 断言上限是被施加的而不是被信任的，且未给出 limit 时取默认值。
// 一次无界的列表读取，正是单个调用方把整张审计表拉进本进程内存的方式。
func TestPageSizeIsBounded(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		want    int
		created int
	}{
		{name: "zero asks for the default", limit: 0, created: store.DefaultPageSize + 5, want: store.DefaultPageSize},
		{name: "a negative limit asks for the default", limit: -10, created: store.DefaultPageSize + 5, want: store.DefaultPageSize},
		{name: "above the cap is clamped", limit: 10_000, created: store.MaxPageSize + 5, want: store.MaxPageSize},
		{name: "below the cap is honored", limit: 4, created: 10, want: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			for i := range tt.created {
				f.mustCreateKey(f.ownerAt, fmt.Sprintf("key-%03d", i))
			}
			page, err := f.svc.ListAPIKeys(context.Background(), f.ownerAt,
				store.ListQuery{Limit: tt.limit}, store.APIKeyFilter{})
			if err != nil {
				t.Fatalf("ListAPIKeys: %v", err)
			}
			if len(page.Items) != tt.want {
				t.Errorf("page holds %d rows, want %d", len(page.Items), tt.want)
			}
			if page.NextCursor == "" {
				t.Errorf("NextCursor is empty, but %d rows remain", tt.created-tt.want)
			}
		})
	}
}

// TestInvalidCursorIsReported asserts an edited cursor is refused rather than
// read as "start from the beginning", which would silently re-serve page one.
//
// TestInvalidCursorIsReported 断言被改动过的游标会被拒绝，而不是被读作「从头开始」
// ——那会无声无息地把第一页再发一遍。
func TestInvalidCursorIsReported(t *testing.T) {
	f := newFixture(t)
	f.mustCreateKey(f.ownerAt, "only")

	for _, cursor := range []string{"not-base64!!", "bm90LWEtY3Vyc29y", "fHVzcl8x"} {
		_, err := f.svc.ListAPIKeys(context.Background(), f.ownerAt,
			store.ListQuery{Cursor: cursor}, store.APIKeyFilter{})
		if !errors.Is(err, logic.ErrInvalidInput) {
			t.Errorf("ListAPIKeys(cursor=%q) error = %v, want ErrInvalidInput", cursor, err)
		}
	}
}

// TestFiltersNarrowWithoutLeavingTheTenant asserts each filter selects what it
// names — and that no filter widens the read past the caller's own tenant,
// which is the failure that would matter.
//
// TestFiltersNarrowWithoutLeavingTheTenant 断言每个筛选条件只选出它所点名的内容——并且
// 没有任何筛选条件能把读取范围扩大到调用方自己的租户之外，那才是真正要紧的失败。
func TestFiltersNarrowWithoutLeavingTheTenant(t *testing.T) {
	f := newFixture(t)
	other := newFixture(t)

	mine := f.mustCreateKey(f.ownerAt, "production")
	f.mustCreateKey(f.ownerAt, "staging")
	revoked := f.mustCreateKey(f.ownerAt, "retired")
	if err := f.svc.RevokeAPIKey(context.Background(), f.ownerAt, revoked.Key.ID); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}
	other.mustCreateKey(other.ownerAt, "production")

	tests := []struct {
		name   string
		filter store.APIKeyFilter
		want   []string
	}{
		{name: "no filter returns the tenant's keys", filter: store.APIKeyFilter{}, want: []string{"production", "retired", "staging"}},
		{name: "by name", filter: store.APIKeyFilter{Query: "production"}, want: []string{"production"}},
		{name: "by name, case-insensitively", filter: store.APIKeyFilter{Query: "PRODUCT"}, want: []string{"production"}},
		{name: "by display form", filter: store.APIKeyFilter{Query: mine.Key.Display}, want: []string{"production"}},
		{name: "by status", filter: store.APIKeyFilter{Status: model.StatusRevoked}, want: []string{"retired"}},
		{name: "by status and name together", filter: store.APIKeyFilter{Status: model.StatusActive, Query: "st"}, want: []string{"staging"}},
		{name: "a query matching nothing", filter: store.APIKeyFilter{Query: "nothing-here"}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := f.svc.ListAPIKeys(context.Background(), f.ownerAt, store.ListQuery{}, tt.filter)
			if err != nil {
				t.Fatalf("ListAPIKeys: %v", err)
			}
			got := make([]string, 0, len(page.Items))
			for _, key := range page.Items {
				if key.TenantID != f.tenant.ID {
					t.Fatalf("filter %+v returned tenant %q's key", tt.filter, key.TenantID)
				}
				got = append(got, key.Name)
			}
			if !sameSet(got, tt.want) {
				t.Errorf("names = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestUserFilters covers the role and text filters, and the refusal of a role
// this service does not have: filtering to nothing would read as "no such
// users", an answer that is wrong rather than empty.
//
// TestUserFilters 覆盖角色与文本筛选，以及对本服务并不存在的角色的拒绝：筛出空集会被
// 读作「没有这类用户」，那是一个错误的答案，而不是一个空答案。
func TestUserFilters(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.CreateUser(context.Background(), f.ownerAt, "ada@example.com", testPassword, "Ada Lovelace", model.RoleAdmin); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := f.svc.CreateUser(context.Background(), f.ownerAt, "grace@example.com", testPassword, "Grace Hopper", model.RoleMember); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	tests := []struct {
		name    string
		filter  store.UserFilter
		want    []string
		wantErr error
	}{
		{name: "no filter", filter: store.UserFilter{}, want: []string{"owner@example.com", "ada@example.com", "grace@example.com"}},
		{name: "by role", filter: store.UserFilter{Role: model.RoleAdmin}, want: []string{"ada@example.com"}},
		{name: "by email fragment", filter: store.UserFilter{Query: "grace@"}, want: []string{"grace@example.com"}},
		{name: "by name fragment", filter: store.UserFilter{Query: "lovelace"}, want: []string{"ada@example.com"}},
		{name: "role and text together", filter: store.UserFilter{Role: model.RoleMember, Query: "hopper"}, want: []string{"grace@example.com"}},
		{name: "an unknown role is refused", filter: store.UserFilter{Role: "auditor"}, wantErr: logic.ErrInvalidInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := f.svc.ListUsers(context.Background(), f.ownerAt, store.ListQuery{}, tt.filter)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ListUsers() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListUsers: %v", err)
			}
			got := make([]string, 0, len(page.Items))
			for _, user := range page.Items {
				got = append(got, user.Email)
			}
			if !sameSet(got, tt.want) {
				t.Errorf("emails = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAuditFilters covers the three questions an audit reader actually asks:
// what happened, who did it, and when.
//
// TestAuditFilters 覆盖审计读取者真正会问的三个问题：发生了什么、是谁做的、什么时候。
func TestAuditFilters(t *testing.T) {
	f := newFixture(t)
	start := f.clock.Now()

	f.clock.Advance(time.Hour)
	created := f.mustCreateKey(f.ownerAt, "production")
	middle := f.clock.Now()

	f.clock.Advance(time.Hour)
	if err := f.svc.RevokeAPIKey(context.Background(), f.ownerAt, created.Key.ID); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}
	end := f.clock.Now().Add(time.Second)

	tests := []struct {
		name    string
		filter  store.AuditFilter
		want    []string
		wantErr error
	}{
		{
			name:   "by action",
			filter: store.AuditFilter{Action: model.ActionAPIKeyCreate},
			want:   []string{model.ActionAPIKeyCreate},
		},
		{
			name:   "by actor",
			filter: store.AuditFilter{ActorID: f.owner.ID},
			want:   []string{model.ActionAPIKeyRevoke, model.ActionAPIKeyCreate},
		},
		{
			name: "an actor who did nothing",
			// The tenant bootstrap record names the system, not a user, so
			// filtering by a stranger's id must return nothing rather than it.
			//
			// 租户引导那条记录的行为人是系统而不是某个用户，因此按一个陌生 id 筛选
			// 必须什么都不返回，而不是把它返回出来。
			filter: store.AuditFilter{ActorID: model.NewID(model.PrefixUser)},
			want:   nil,
		},
		{
			name:   "a window that holds only the creation",
			filter: store.AuditFilter{Since: start.Add(time.Minute), Until: middle.Add(time.Second)},
			want:   []string{model.ActionAPIKeyCreate},
		},
		{
			name:   "a window open at its start",
			filter: store.AuditFilter{Until: middle.Add(time.Second)},
			want:   []string{model.ActionAPIKeyCreate, model.ActionTenantCreate},
		},
		{
			name:   "a window open at its end",
			filter: store.AuditFilter{Since: middle.Add(time.Second)},
			want:   []string{model.ActionAPIKeyRevoke},
		},
		{
			name:   "action and window together",
			filter: store.AuditFilter{Action: model.ActionAPIKeyRevoke, Since: start, Until: end},
			want:   []string{model.ActionAPIKeyRevoke},
		},
		{
			name:    "an inverted window is refused",
			filter:  store.AuditFilter{Since: end, Until: start},
			wantErr: logic.ErrInvalidInput,
		},
		{
			name:    "an empty window is refused",
			filter:  store.AuditFilter{Since: middle, Until: middle},
			wantErr: logic.ErrInvalidInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := f.svc.ListAudit(context.Background(), f.ownerAt, store.ListQuery{}, tt.filter)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ListAudit() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListAudit: %v", err)
			}
			got := make([]string, 0, len(page.Items))
			for _, entry := range page.Items {
				got = append(got, entry.Action)
			}
			if !sameSet(got, tt.want) {
				t.Errorf("actions = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestListsAreOrderedNewestFirst asserts the ordering the cursor depends on is
// the ordering callers see.
//
// TestListsAreOrderedNewestFirst 断言游标所依赖的那个顺序，就是调用方看到的顺序。
func TestListsAreOrderedNewestFirst(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		f.clock.Advance(time.Minute)
		f.mustCreateKey(f.ownerAt, fmt.Sprintf("key-%d", i))
	}

	page, err := f.svc.ListAPIKeys(context.Background(), f.ownerAt, store.ListQuery{}, store.APIKeyFilter{})
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	for i := 1; i < len(page.Items); i++ {
		if page.Items[i].CreatedAt.After(page.Items[i-1].CreatedAt) {
			t.Errorf("row %d (%s) is newer than row %d (%s)",
				i, page.Items[i].CreatedAt, i-1, page.Items[i-1].CreatedAt)
		}
	}
}

// sameSet compares two collections ignoring order, so a test states what a
// page holds without also fixing an order it did not mean to assert.
//
// sameSet 忽略顺序地比较两个集合，好让测试在陈述某一页有哪些内容的同时，不会顺带把
// 一个它并不打算断言的顺序也固定下来。
func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[string]int, len(want))
	for _, item := range want {
		counts[item]++
	}
	for _, item := range got {
		counts[item]--
		if counts[item] < 0 {
			return false
		}
	}
	return true
}
