package logic_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// fakeCatalogSource serves a settable document and counts fetches.
type fakeCatalogSource struct {
	mu      sync.Mutex
	data    []byte
	err     error
	fetches int
}

func (f *fakeCatalogSource) set(data string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data, f.err = []byte(data), err
}

func (f *fakeCatalogSource) Fetch(context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	return f.data, f.err
}

func (f *fakeCatalogSource) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches
}

func (f *fakeCatalogSource) Label() string { return "https://prices.example/list.json" }

const embeddedCatalog = `{
  "model-a": {"mode": "chat", "input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05},
  "model-b": {"mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06}
}`

const remoteCatalog = `{
  "model-a": {"mode": "chat", "input_cost_per_token": 3.3e-06, "output_cost_per_token": 1.5e-05},
  "model-b": {"mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 6e-06},
  "model-c": {"mode": "chat", "input_cost_per_token": 1e-07, "output_cost_per_token": 1e-07}
}`

// catalogBilling turns billing on; it needs a payment provider, so it uses
// the package's fake Alipay client.
func catalogBilling() logic.Option {
	return logic.WithBilling(logic.BillingConfig{
		Alipay: &logic.AlipayConfig{Client: newFakeAlipay("point"), CreditsPerUnitMicro: alipayRate, ReturnURL: "https://console.example/console/billing"},
	})
}

var catalogOperator = logic.Actor{UserID: "op_1", TenantID: model.PlatformScope, Role: model.RolePlatformOperator, IP: "10.0.0.9"}

func catalogFixture(t *testing.T, embedded string, refresh time.Duration) (*fixture, *fakeCatalogSource) {
	t.Helper()
	f := newFixture(t)
	remote := &fakeCatalogSource{}
	remote.set(remoteCatalog, nil)
	f.svc = logic.New(f.store, f.clock, catalogBilling(), logic.WithPriceCatalog(logic.PriceCatalogConfig{
		Remote: remote, Embedded: []byte(embedded), RefreshInterval: refresh, MaxChangePercent: 30,
	}))
	return f, remote
}

func catalogAudits(t *testing.T, f *fixture, action string) []model.AuditLog {
	t.Helper()
	page, err := f.store.ListAudit(context.Background(), model.PlatformScope, store.ListQuery{Limit: 200}, store.AuditFilter{Action: action})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func TestInitPriceCatalogImportsTheSnapshotOnce(t *testing.T) {
	f, _ := catalogFixture(t, embeddedCatalog, 24*time.Hour)
	ctx := context.Background()
	if err := f.svc.InitPriceCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := f.store.CatalogSyncState(ctx)
	snap, _ := pricecatalog.Parse([]byte(embeddedCatalog))
	if state.ContentHash != snap.ContentHash || state.EmbeddedHash != snap.ContentHash || state.Source != pricecatalog.EmbeddedLabel || state.Version != 1 {
		t.Fatalf("state after import = %+v", state)
	}
	audits := catalogAudits(t, f, model.ActionBillingCatalogSync)
	if len(audits) != 1 || audits[0].ActorID != "" || audits[0].Detail != "added=2 applied=0 review=0 removed=0 suppressed=0 version=1" {
		t.Fatalf("sync audit = %+v", audits)
	}
	if err := f.svc.InitPriceCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if len(catalogAudits(t, f, model.ActionBillingCatalogSync)) != 1 {
		t.Fatal("a second start must not import again")
	}

	// With the periodic fetch on, a newer binary's snapshot is not applied:
	// the remote list is the source.
	newer := logic.New(f.store, f.clock, catalogBilling(), logic.WithPriceCatalog(logic.PriceCatalogConfig{
		Embedded: []byte(remoteCatalog), RefreshInterval: time.Hour, MaxChangePercent: 30,
	}))
	if err := newer.InitPriceCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if state2, _ := f.store.CatalogSyncState(ctx); state2.ContentHash != snap.ContentHash {
		t.Fatalf("a periodic deployment must ignore a newer snapshot: %+v", state2)
	}
}

func TestInitPriceCatalogAppliesANewerSnapshotWithoutRemote(t *testing.T) {
	f, _ := catalogFixture(t, embeddedCatalog, 0)
	ctx := context.Background()
	if err := f.svc.InitPriceCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	upgraded := logic.New(f.store, f.clock, catalogBilling(), logic.WithPriceCatalog(logic.PriceCatalogConfig{
		Embedded: []byte(remoteCatalog), MaxChangePercent: 30,
	}))
	if err := upgraded.InitPriceCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := upgraded.ListCatalogPending(ctx, catalogOperator, store.ListQuery{})
	if err != nil || len(pending.Items) != 1 || pending.Items[0].Pending.Model != "model-b" {
		t.Fatalf("the tripled output of model-b must wait: %+v, %v", pending, err)
	}
	set, _ := f.store.CurrentCatalogPrices(ctx)
	if len(set.Prices) != 3 {
		t.Fatalf("model-c is added and model-a's 10%% change applied: %+v", set)
	}
}

func TestSyncPriceCatalogAndReview(t *testing.T) {
	f, remote := catalogFixture(t, embeddedCatalog, 24*time.Hour)
	ctx := context.Background()
	if err := f.svc.InitPriceCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	result, err := f.svc.SyncPriceCatalog(ctx, &catalogOperator)
	if err != nil || len(result.Added) != 1 || len(result.Applied) != 1 || len(result.Reviewed) != 1 {
		t.Fatalf("sync = %+v, %v", result, err)
	}
	if remote.count() != 1 {
		t.Fatalf("fetches = %d", remote.count())
	}
	auto := catalogAudits(t, f, model.ActionBillingCatalogAutoApply)
	if len(auto) != 1 || auto[0].Target != "model-a" || auto[0].ActorID != "op_1" || auto[0].Detail != "max_change=10.00% version=2 model=model-a" {
		t.Fatalf("auto-apply audit = %+v", auto)
	}
	if again, err := f.svc.SyncPriceCatalog(ctx, &catalogOperator); err != nil || !again.Skipped {
		t.Fatalf("unchanged content = %+v, %v", again, err)
	}

	page, state, err := f.svc.ListPriceCatalog(ctx, catalogOperator, store.ListQuery{}, store.CatalogFilter{Prefix: "model-"})
	if err != nil || len(page.Items) != 3 || state.Source != remote.Label() || state.Version != 2 {
		t.Fatalf("list = %+v, %+v, %v", page, state, err)
	}
	if _, _, err := f.svc.ListPriceCatalog(ctx, catalogOperator, store.ListQuery{}, store.CatalogFilter{Status: "bogus"}); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatalf("bad status filter: %v", err)
	}

	decision, err := f.svc.ApproveCatalogPending(ctx, catalogOperator, "model-b")
	if err != nil || decision.Version != 3 {
		t.Fatalf("approve = %+v, %v", decision, err)
	}
	approve := catalogAudits(t, f, model.ActionBillingCatalogApprove)
	if len(approve) != 1 || approve[0].Detail != "kind=change input=1000000→1000000 output=2000000→6000000 version=3 model=model-b" {
		t.Fatalf("approve audit = %+v", approve)
	}
	if _, err := f.svc.ApproveCatalogPending(ctx, catalogOperator, "model-b"); !errors.Is(err, logic.ErrNotFound) {
		t.Fatalf("nothing waits: %v", err)
	}
	if _, err := f.svc.RejectCatalogPending(ctx, catalogOperator, ""); !errors.Is(err, logic.ErrInvalidInput) {
		t.Fatalf("empty name: %v", err)
	}

	// model-c disappears upstream; rejecting the removal keeps its prices.
	remote.set(`{
	  "model-a": {"mode": "chat", "input_cost_per_token": 3.3e-06, "output_cost_per_token": 1.5e-05},
	  "model-b": {"mode": "chat", "input_cost_per_token": 1e-06, "output_cost_per_token": 6e-06}
	}`, nil)
	if result, err := f.svc.SyncPriceCatalog(ctx, nil); err != nil || len(result.Removed) != 1 {
		t.Fatalf("background sync = %+v, %v", result, err)
	}
	if _, err := f.svc.RejectCatalogPending(ctx, catalogOperator, "model-c"); err != nil {
		t.Fatal(err)
	}
	if reject := catalogAudits(t, f, model.ActionBillingCatalogReject); len(reject) != 1 || reject[0].Detail != "kind=remove model=model-c" {
		t.Fatalf("reject audit = %+v", reject)
	}
	if rows, err := f.store.CatalogPricesAt(ctx, "model-c", f.clock.Now()); err != nil || len(rows) != 2 {
		t.Fatalf("model-c keeps its prices: %+v, %v", rows, err)
	}
}

func TestSyncPriceCatalogFailureIsRecorded(t *testing.T) {
	f, remote := catalogFixture(t, embeddedCatalog, 24*time.Hour)
	ctx := context.Background()
	if err := f.svc.InitPriceCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	remote.set("", errors.New("HTTP 503"))
	if _, err := f.svc.SyncPriceCatalog(ctx, &catalogOperator); !errors.Is(err, logic.ErrUnavailable) {
		t.Fatalf("fetch failure: %v", err)
	}
	state, _ := f.store.CatalogSyncState(ctx)
	if state.LastError != remote.Label()+": HTTP 503" || state.Version != 1 || state.Source != pricecatalog.EmbeddedLabel {
		t.Fatalf("state = %+v", state)
	}
	remote.set("<html>rate limited</html>", nil)
	if _, err := f.svc.SyncPriceCatalog(ctx, &catalogOperator); !errors.Is(err, logic.ErrUnavailable) {
		t.Fatalf("parse failure: %v", err)
	}
	if state, _ := f.store.CatalogSyncState(ctx); !strings.HasPrefix(state.LastError, remote.Label()+": pricecatalog:") {
		t.Fatalf("a parse failure is recorded too: %+v", state)
	}
	remote.set(remoteCatalog, nil)
	if _, err := f.svc.SyncPriceCatalog(ctx, &catalogOperator); err != nil {
		t.Fatal(err)
	}
	if state, _ := f.store.CatalogSyncState(ctx); state.LastError != "" || state.LastErrorAt != nil {
		t.Fatalf("a successful sync clears the error: %+v", state)
	}
}

func TestPriceCatalogAccess(t *testing.T) {
	f, _ := catalogFixture(t, embeddedCatalog, 24*time.Hour)
	ctx := context.Background()
	if _, err := f.svc.SyncPriceCatalog(ctx, &f.ownerAt); !errors.Is(err, logic.ErrForbidden) {
		t.Fatalf("a tenant owner must be refused: %v", err)
	}
	if _, _, err := f.svc.ListPriceCatalog(ctx, f.ownerAt, store.ListQuery{}, store.CatalogFilter{}); !errors.Is(err, logic.ErrForbidden) {
		t.Fatalf("a tenant owner must not list: %v", err)
	}
	off := logic.New(f.store, f.clock, logic.WithPriceCatalog(logic.PriceCatalogConfig{Embedded: []byte(embeddedCatalog)}))
	if _, _, err := off.ListPriceCatalog(ctx, catalogOperator, store.ListQuery{}, store.CatalogFilter{}); !errors.Is(err, logic.ErrNotFound) {
		t.Fatalf("billing off: %v", err)
	}
	if err := off.InitPriceCatalog(ctx); err != nil {
		t.Fatalf("billing off: Init is a no-op: %v", err)
	}
	if state, _ := f.store.CatalogSyncState(ctx); state.ContentHash != "" {
		t.Fatal("billing off must not import")
	}
}

func TestRunPriceCatalogSyncImportsThenFetches(t *testing.T) {
	f, remote := catalogFixture(t, embeddedCatalog, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.svc.RunPriceCatalogSync(ctx)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for remote.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	state, _ := f.store.CatalogSyncState(context.Background())
	snap, _ := pricecatalog.Parse([]byte(remoteCatalog))
	embedded, _ := pricecatalog.Parse([]byte(embeddedCatalog))
	if remote.count() != 1 || state.ContentHash != snap.ContentHash || state.EmbeddedHash != embedded.ContentHash {
		t.Fatalf("fetches = %d, state = %+v", remote.count(), state)
	}
}
