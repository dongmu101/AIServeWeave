package fleet

import (
	"context"
	"sort"
	"sync"
	"time"

	"AIServeWeave/common/workflowview"
)

// Catalogue is the workflow menu, merged across replicas.
//
// A template can differ between replicas — they load it from their own file
// configuration, and a rollout that has reached two of three replicas is a
// normal state, not an error. So the merge does not pick a winner: a template
// carries the replicas that registered it, and Divergent says whether they
// agree. A console that showed one arbitrary version would hide the one fact
// an operator most needs during a rollout.
//
// Catalogue 是跨副本合并后的工作流菜单。
//
// 同一个模板在不同副本上可能不同——它们各自从自己的文件配置加载，而一次只推到三个副本
// 中两个的发布是正常状态，不是错误。因此合并不选出胜者：每个模板携带注册了它的那些副本，
// 并由 Divergent 说明它们是否一致。一个只展示其中某一个版本的控制台，恰恰藏起了运维在
// 发布过程中最需要的那个事实。
type Catalogue struct {
	CollectedAt time.Time       `json:"collected_at"`
	Replicas    []ReplicaStatus `json:"replicas"`
	Partial     bool            `json:"partial"`
	Templates   []Workflow      `json:"templates"`
}

// Workflow is one template and where it is registered.
//
// Workflow 是一个模板，以及它在哪些地方被注册。
type Workflow struct {
	workflowview.Template
	// Replicas are the replica ids that registered this template, sorted.
	//
	// Replicas 是注册了本模板的副本 id，已排序。
	Replicas []string `json:"replicas"`
	// Divergent reports that the replicas registering this template do not
	// all describe it the same way, or that some replicas do not have it at
	// all. Either way a caller's request may behave differently depending on
	// which replica it lands on, which is a fact and not a detail.
	//
	// Divergent 报告注册了本模板的各副本并未以同一种方式描述它，或者某些副本根本没有
	// 它。无论哪种情况，调用方的请求都可能因为落在哪个副本上而表现不同，这是一个事实，
	// 不是一个细节。
	Divergent bool `json:"divergent"`
}

// Workflows asks every replica for its catalogue and merges them.
//
// Workflows 向每个副本索取它的目录并合并。
func (a *Aggregator) Workflows(ctx context.Context) (Catalogue, error) {
	if a == nil {
		return Catalogue{}, ErrDisabled
	}

	type result struct {
		status  ReplicaStatus
		catalog workflowview.TemplateCatalog
	}
	results := make([]result, len(a.gateways))

	var wg sync.WaitGroup
	for i, endpoint := range a.gateways {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var catalog workflowview.TemplateCatalog
			failure := fetchInto(ctx, a, endpoint, "/internal/v1/workflows", &catalog,
				func(doc workflowview.TemplateCatalog) bool {
					// A replica with no templates registered answers with an
					// empty list, not an absent one — see renderTemplates. So
					// a missing Templates field is a document that did not
					// come from that code path.
					//
					// 一个没有注册任何模板的副本回答的是空列表而不是缺席的列表
					// ——见 renderTemplates。因此 Templates 字段缺失，说明这份文档
					// 并非来自那条代码路径。
					return doc.ReplicaID != "" && !doc.GeneratedAt.IsZero() && doc.Templates != nil
				})
			status := ReplicaStatus{Endpoint: endpoint, Error: failure}
			if failure == "" {
				generated := catalog.GeneratedAt
				status.ReplicaID = catalog.ReplicaID
				status.GeneratedAt = &generated
				status.NodeCount = len(catalog.Templates)
			}
			results[i] = result{status: status, catalog: catalog}
		}()
	}
	wg.Wait()

	out := Catalogue{
		CollectedAt: a.clock.Now().UTC(),
		Replicas:    make([]ReplicaStatus, 0, len(results)),
	}
	merged := make(map[string]*Workflow)
	answered := 0
	for _, item := range results {
		out.Replicas = append(out.Replicas, item.status)
		if item.status.Error != "" {
			out.Partial = true
			continue
		}
		answered++
		for _, template := range item.catalog.Templates {
			existing, seen := merged[template.ID]
			if !seen {
				merged[template.ID] = &Workflow{
					Template: template,
					Replicas: []string{item.catalog.ReplicaID},
				}
				continue
			}
			existing.Replicas = append(existing.Replicas, item.catalog.ReplicaID)
			if !sameTemplate(existing.Template, template) {
				existing.Divergent = true
			}
		}
	}

	out.Templates = make([]Workflow, 0, len(merged))
	for _, workflow := range merged {
		sort.Strings(workflow.Replicas)
		// A template only some replicas have is divergent too: a request that
		// lands on one of the others gets a 404, and that is the same class
		// of surprise as a template whose inputs differ.
		//
		// 只有部分副本拥有的模板同样算不一致：落在其余副本上的请求会得到 404，而那与
		// 「输入声明不同的模板」属于同一类意外。
		if len(workflow.Replicas) != answered {
			workflow.Divergent = true
		}
		out.Templates = append(out.Templates, *workflow)
	}
	sort.Slice(out.Templates, func(i, j int) bool { return out.Templates[i].ID < out.Templates[j].ID })
	return out, nil
}

// sameTemplate reports whether two replicas describe a template identically.
//
// It compares what a caller can observe — the description and the declared
// inputs — because that is what differs when a rollout is half done. The
// graph is not compared for the reason it is not carried: it never leaves the
// Gateway, so two replicas serving different graphs under one id is a
// divergence this view cannot see, and the Gateway README is where that gap
// belongs.
//
// sameTemplate 报告两个副本是否以完全相同的方式描述同一个模板。
//
// 它比较调用方能观察到的部分——描述与已声明的输入——因为发布进行到一半时，不同的正是
// 这些。图不参与比较，理由与它不被携带相同：它从不离开 Gateway，因此「两个副本在同一个
// id 下提供不同的图」是本视图看不见的一种不一致，而那处缺口应记在 Gateway README 里。
func sameTemplate(left, right workflowview.Template) bool {
	if left.Description != right.Description || left.Valid != right.Valid {
		return false
	}
	if len(left.Inputs) != len(right.Inputs) {
		return false
	}
	for i := range left.Inputs {
		if !sameInput(left.Inputs[i], right.Inputs[i]) {
			return false
		}
	}
	return true
}

func sameInput(left, right workflowview.Input) bool {
	if left.Name != right.Name || left.Type != right.Type ||
		left.Required != right.Required || left.MaxLength != right.MaxLength {
		return false
	}
	if !sameFloat(left.Min, right.Min) || !sameFloat(left.Max, right.Max) {
		return false
	}
	return string(left.Default) == string(right.Default)
}

func sameFloat(left, right *float64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
