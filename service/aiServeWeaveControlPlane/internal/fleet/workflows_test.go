package fleet_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"AIServeWeave/common/workflowview"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/fleet"
)

// catalogueReplica starts a stand-in Gateway serving one workflow catalogue.
//
// catalogueReplica 启动一个替身 Gateway，提供一份工作流目录。
func catalogueReplica(t *testing.T, doc workflowview.TemplateCatalog) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/internal/v1/workflows" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func template(id, description string, inputs ...workflowview.Input) workflowview.Template {
	return workflowview.Template{ID: id, Description: description, Inputs: inputs, Valid: true}
}

// TestACatalogueNamesWhereEachTemplateIsRegistered covers the merge and the
// two ways replicas disagree: a template only some of them have, and a
// template they describe differently. Both mean a caller's request behaves
// differently depending on where it lands, which is exactly what a half-done
// rollout looks like.
//
// TestACatalogueNamesWhereEachTemplateIsRegistered 覆盖合并，以及副本之间不一致的两种
// 形态：只有部分副本拥有的模板，以及它们描述得不同的模板。两者都意味着调用方的请求会
// 因为落在哪里而表现不同，而那正是一次进行到一半的发布的样子。
func TestACatalogueNamesWhereEachTemplateIsRegistered(t *testing.T) {
	first := catalogueReplica(t, workflowview.TemplateCatalog{
		ReplicaID:   "replica-1",
		GeneratedAt: earlier,
		Templates: []workflowview.Template{
			template("portrait", "A portrait workflow",
				workflowview.Input{Name: "prompt", Type: "string", Required: true}),
			template("upscale", "Upscale an image"),
			template("experimental", "Only on one replica"),
		},
	})
	second := catalogueReplica(t, workflowview.TemplateCatalog{
		ReplicaID:   "replica-2",
		GeneratedAt: later,
		Templates: []workflowview.Template{
			template("portrait", "A portrait workflow",
				workflowview.Input{Name: "prompt", Type: "string", Required: true}),
			// The same id, described differently: this replica's copy takes
			// an extra input.
			//
			// 同一个 id，描述不同：这个副本的那一份多接受一个输入。
			template("upscale", "Upscale an image",
				workflowview.Input{Name: "scale", Type: "integer"}),
		},
	})

	catalogue, err := aggregate(t, first, second).Workflows(context.Background())
	if err != nil {
		t.Fatalf("Workflows: %v", err)
	}

	byID := make(map[string]fleet.Workflow, len(catalogue.Templates))
	for _, workflow := range catalogue.Templates {
		byID[workflow.ID] = workflow
	}
	if len(byID) != 3 {
		t.Fatalf("got %d templates, want 3", len(byID))
	}

	if agreed := byID["portrait"]; agreed.Divergent || len(agreed.Replicas) != 2 {
		t.Errorf("portrait = %+v, want it registered on both and not divergent", agreed)
	}
	if differing := byID["upscale"]; !differing.Divergent {
		t.Error("upscale is not marked divergent although the replicas declare different inputs")
	}
	if missing := byID["experimental"]; !missing.Divergent || len(missing.Replicas) != 1 {
		t.Errorf("experimental = %+v, want it divergent and on one replica", missing)
	}
	if catalogue.Partial {
		t.Error("Partial is true although both replicas answered")
	}
	if !catalogue.CollectedAt.Equal(collectedAt) {
		t.Errorf("CollectedAt = %v, want the injected %v", catalogue.CollectedAt, collectedAt)
	}
}

// TestAReplicaWithoutTheEndpointIsNamed asserts a replica that does not serve
// the catalogue is reported as unsupported rather than counted as one holding
// no templates. The two look identical in a merged list, and only one of them
// means somebody should look at a deployment.
//
// TestAReplicaWithoutTheEndpointIsNamed 断言不提供该目录的副本会被报告为 unsupported，
// 而不是被当作「一个模板都没有」。两者在合并后的列表里长得一样，但只有其中一种意味着
// 该有人去看看部署。
func TestAReplicaWithoutTheEndpointIsNamed(t *testing.T) {
	working := catalogueReplica(t, workflowview.TemplateCatalog{
		ReplicaID:   "replica-1",
		GeneratedAt: later,
		Templates:   []workflowview.Template{template("portrait", "")},
	})
	older := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(older.Close)

	catalogue, err := aggregate(t, working, older.URL).Workflows(context.Background())
	if err != nil {
		t.Fatalf("Workflows: %v", err)
	}
	if !catalogue.Partial {
		t.Error("Partial is false although one replica does not serve the catalogue")
	}
	for _, status := range catalogue.Replicas {
		if status.Endpoint == older.URL && status.Error != "unsupported" {
			t.Errorf("the older replica reported %q, want unsupported", status.Error)
		}
	}
	// The one template the working replica has is not marked divergent: only
	// one replica answered, so there is nothing to disagree with.
	//
	// 那个可用副本上的唯一模板不会被标记为不一致：只有一个副本作了答，也就没有可以
	// 与之相左的对象。
	if len(catalogue.Templates) != 1 || catalogue.Templates[0].Divergent {
		t.Errorf("templates = %+v, want one template that is not divergent", catalogue.Templates)
	}
}

// jobReplica starts a stand-in Gateway serving one tenant's job page, and
// records which tenant it was asked about.
//
// jobReplica 启动一个替身 Gateway，提供某一个租户的 job 页，并记录它被问的是哪个租户。
func jobReplica(t *testing.T, replicaID string, jobs map[string][]workflowview.Job, truncated bool) (string, *[]string) {
	t.Helper()
	asked := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/internal/v1/jobs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		tenantID := r.URL.Query().Get("tenant_id")
		if tenantID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		asked = append(asked, tenantID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workflowview.JobPage{
			ReplicaID:   replicaID,
			GeneratedAt: later,
			TenantID:    tenantID,
			Jobs:        jobs[tenantID],
			Truncated:   truncated,
		})
	}))
	t.Cleanup(server.Close)
	return server.URL, &asked
}

// TestJobsAreAskedForByTenant is the isolation assertion: the tenant travels
// to every replica, so the filtering happens in the job table rather than in
// the aggregator.
//
// TestJobsAreAskedForByTenant 是隔离断言：租户会被带到每一个副本，因此过滤发生在 job
// 表里，而不是在聚合器里。
func TestJobsAreAskedForByTenant(t *testing.T) {
	jobs := map[string][]workflowview.Job{
		"tnt_1": {
			{ID: "job-a", WorkflowID: "portrait", State: "running", CreatedAt: earlier, UpdatedAt: earlier},
		},
		"tnt_2": {
			{ID: "job-b", WorkflowID: "portrait", State: "running", CreatedAt: later, UpdatedAt: later},
		},
	}
	first, askedFirst := jobReplica(t, "replica-1", jobs, false)
	second, askedSecond := jobReplica(t, "replica-2", jobs, false)

	view, err := aggregate(t, first, second).Jobs(context.Background(), "tnt_1")
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}

	if len(view.Jobs) != 2 {
		t.Fatalf("got %d jobs, want the one run from each replica", len(view.Jobs))
	}
	for _, job := range view.Jobs {
		if job.ID != "job-a" {
			t.Errorf("job %q leaked from another tenant", job.ID)
		}
	}
	for _, asked := range []*[]string{askedFirst, askedSecond} {
		if len(*asked) != 1 || (*asked)[0] != "tnt_1" {
			t.Errorf("a replica was asked about %v, want exactly [tnt_1]", *asked)
		}
	}
	// Each run names the replica holding it: jobs are not shared between
	// replicas, so "where is it" has one answer and an operator cancelling or
	// debugging needs it.
	//
	// 每次运行都点出持有它的副本：job 不在副本之间共享，因此「它在哪」只有一个答案，
	// 而做取消或排查的运维需要它。
	replicas := map[string]bool{view.Jobs[0].Replica: true, view.Jobs[1].Replica: true}
	if !replicas["replica-1"] || !replicas["replica-2"] {
		t.Errorf("replicas = %v, want both named", replicas)
	}
}

// TestAJobViewSaysWhatItCannotAnswer covers the two flags that keep this from
// being read as a history: an evicting table and a replica that did not
// answer.
//
// TestAJobViewSaysWhatItCannotAnswer 覆盖那两个防止本视图被读成历史的标志：正在逐出的
// 表，以及没有作答的副本。
func TestAJobViewSaysWhatItCannotAnswer(t *testing.T) {
	full, _ := jobReplica(t, "replica-1", map[string][]workflowview.Job{
		"tnt_1": {{ID: "job-a", WorkflowID: "portrait", State: "succeeded", CreatedAt: later}},
	}, true)

	gone := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	goneURL := gone.URL
	gone.Close()

	view, err := aggregate(t, full, goneURL).Jobs(context.Background(), "tnt_1")
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if !view.Truncated {
		t.Error("Truncated is false although a replica reported an evicting table")
	}
	if !view.Partial {
		t.Error("Partial is false although a replica did not answer")
	}
	if len(view.Jobs) != 1 {
		t.Errorf("got %d jobs, want the one the working replica holds", len(view.Jobs))
	}
}

// TestAReplicaAnsweringAboutAnotherTenantIsRefused asserts the answer is
// discarded rather than filtered. Nothing should produce it, and the safe
// reading of an impossible answer is that it cannot be trusted.
//
// TestAReplicaAnsweringAboutAnotherTenantIsRefused 断言这种答案会被丢弃而不是被过滤。
// 不该有任何东西产生它，而对一个不可能出现的答案，安全的理解是它不可信。
func TestAReplicaAnsweringAboutAnotherTenantIsRefused(t *testing.T) {
	confused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workflowview.JobPage{
			ReplicaID:   "replica-confused",
			GeneratedAt: later,
			TenantID:    "tnt_other",
			Jobs:        []workflowview.Job{{ID: "job-x", WorkflowID: "portrait", State: "running"}},
		})
	}))
	t.Cleanup(confused.Close)

	view, err := aggregate(t, confused.URL).Jobs(context.Background(), "tnt_1")
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	if len(view.Jobs) != 0 {
		t.Errorf("got %d jobs, want none from a replica answering about another tenant", len(view.Jobs))
	}
	if !view.Partial || view.Replicas[0].Error != "malformed" {
		t.Errorf("replicas = %+v, want the mismatch reported as malformed", view.Replicas)
	}
}

// TestJobsRefuseAnUnnamedTenant asserts the aggregator will not ask for
// everything when the caller did not say whose runs it wants.
//
// TestJobsRefuseAnUnnamedTenant 断言在调用方没有说清要谁的运行时，聚合器不会去索取全部。
func TestJobsRefuseAnUnnamedTenant(t *testing.T) {
	first, asked := jobReplica(t, "replica-1", nil, false)
	if _, err := aggregate(t, first).Jobs(context.Background(), ""); err == nil {
		t.Error("Jobs(\"\") error = nil, want a refusal")
	}
	if len(*asked) != 0 {
		t.Errorf("a replica was asked %v despite the missing tenant", *asked)
	}
}

// TestWorkflowsAndJobsAreDisabledTogether asserts an unconfigured aggregator
// has no half-working method.
//
// TestWorkflowsAndJobsAreDisabledTogether 断言一个未配置的聚合器没有「半可用」的方法。
func TestWorkflowsAndJobsAreDisabledTogether(t *testing.T) {
	aggregator := fleet.New(fleet.Config{})
	if _, err := aggregator.Workflows(context.Background()); err != fleet.ErrDisabled {
		t.Errorf("Workflows() error = %v, want ErrDisabled", err)
	}
	if _, err := aggregator.Jobs(context.Background(), "tnt_1"); err != fleet.ErrDisabled {
		t.Errorf("Jobs() error = %v, want ErrDisabled", err)
	}
}

// TestACatalogueOrJobPageIsValidatedTheSameWay covers the other two documents
// this package fetches. The check has to be per call site — a new endpoint
// that forgot it would be the same defect again, one document type later.
//
// TestACatalogueOrJobPageIsValidatedTheSameWay 覆盖本包取回的另外两种文档。这项检查
// 必须逐调用点进行——一个忘了它的新端点，就是同一个缺陷换一种文档类型再犯一次。
func TestACatalogueOrJobPageIsValidatedTheSameWay(t *testing.T) {
	bodies := []struct {
		name string
		body string
	}{
		{name: "an empty object", body: `{}`},
		{name: "a JSON null", body: `null`},
		{name: "an error document served with 200", body: `{"error":"unavailable"}`},
	}

	for _, tt := range bodies {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			aggregator := aggregate(t, server.URL)

			catalogue, err := aggregator.Workflows(context.Background())
			if err != nil {
				t.Fatalf("Workflows: %v", err)
			}
			if !catalogue.Partial || catalogue.Replicas[0].Error != "malformed" {
				t.Errorf("catalogue replicas = %+v, want the reply refused", catalogue.Replicas)
			}
			if len(catalogue.Templates) != 0 {
				t.Errorf("got %d templates from a refused reply", len(catalogue.Templates))
			}

			view, err := aggregator.Jobs(context.Background(), "tnt_1")
			if err != nil {
				t.Fatalf("Jobs: %v", err)
			}
			if !view.Partial || view.Replicas[0].Error != "malformed" {
				t.Errorf("job replicas = %+v, want the reply refused", view.Replicas)
			}
			if len(view.Jobs) != 0 {
				t.Errorf("got %d jobs from a refused reply", len(view.Jobs))
			}
		})
	}
}
