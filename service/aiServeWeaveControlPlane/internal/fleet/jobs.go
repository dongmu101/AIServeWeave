package fleet

import (
	"context"
	"net/url"
	"sort"
	"sync"
	"time"

	"AIServeWeave/common/workflowview"
)

// JobView is one tenant's runs, merged across replicas — and it is a live
// view, not a history.
//
// Every field here exists to keep that distinction visible. The Gateway's job
// table is in process memory, bounded, and per replica: a run disappears when
// its replica restarts, when the table's bound pushes it out, and it was never
// visible from any other replica in the first place. So this view can answer
// "what is running now" and cannot answer "what ran last week". Truncated and
// Partial are what stop a short list from being read as the second answer.
//
// The persistent job history STATUS asks for is a different feature: it needs
// a jobs table in this service and a write path from the Gateway, and that
// design is deferred rather than approximated here.
//
// JobView 是某一个租户跨副本合并后的运行——而它是实时视图，不是历史。
//
// 这里的每个字段都是为了让这一区别保持可见。Gateway 的 job 表位于进程内存、有上限、
// 且每副本各自持有：一次运行会在它所在副本重启时消失、会被表的上限挤出去，而且它从一
// 开始就不曾在别的副本上可见。因此本视图能回答「现在在跑什么」，回答不了「上周跑过什么」。
// Truncated 与 Partial 正是阻止一份短列表被读成后一个答案的东西。
//
// STATUS 所要求的持久化 Job 历史是另一项功能：它需要本服务里的一张 jobs 表和一条来自
// Gateway 的写路径，那份设计被推迟，而不是在这里被近似。
type JobView struct {
	CollectedAt time.Time       `json:"collected_at"`
	Replicas    []ReplicaStatus `json:"replicas"`
	Partial     bool            `json:"partial"`
	// Truncated is true when any replica's job table has evicted runs to stay
	// within its bound. The table is shared across tenants, so a busy
	// neighbour can push this tenant's older runs out.
	//
	// Truncated 在任何副本的 job 表曾为守住上限而逐出运行时为 true。该表由各租户共享，
	// 因此一个繁忙的邻居可以把本租户较早的运行挤出去。
	Truncated bool  `json:"truncated"`
	Jobs      []Job `json:"jobs"`
}

// Job is one run, and which replica is holding it.
//
// Job 是一次运行，以及哪个副本正持有它。
type Job struct {
	workflowview.Job
	// Replica is the replica whose job table this run came from. A run lives
	// on exactly one — jobs are not shared between replicas, which is the
	// same tunnel constraint that makes the node inventory partial.
	//
	// Replica 是这次运行所来自的那个副本的 job 表。一次运行恰好只存在于一个副本上
	// ——job 不在副本之间共享，这与让节点清单成为局部视图的是同一条隧道约束。
	// It carries omitempty so a tenant response, where it is cleared, has no
	// such field at all rather than an empty one that suggests the answer was
	// simply unknown.
	//
	// 它带 omitempty，这样在被清空的租户响应里，这个字段是根本不存在，而不是一个空值
	// ——空值会让人以为「只是不知道」。
	Replica string `json:"replica,omitempty"`
}

// Jobs asks every replica for one tenant's runs.
//
// The tenant is a parameter and it is passed to each replica, so the filtering
// happens where the table is rather than here. A bug in this function can then
// lose runs, but it cannot show one tenant another's.
//
// Jobs 向每个副本索取某一个租户的运行。
//
// 租户是参数，并且会被传给每个副本，因此过滤发生在表所在的地方而不是这里。这样一来，
// 本函数中的缺陷可能丢失运行，但无法把一个租户的运行展示给另一个租户。
func (a *Aggregator) Jobs(ctx context.Context, tenantID string) (JobView, error) {
	if a == nil {
		return JobView{}, ErrDisabled
	}
	if tenantID == "" {
		return JobView{}, ErrDisabled
	}

	type result struct {
		status ReplicaStatus
		page   workflowview.JobPage
	}
	results := make([]result, len(a.gateways))
	query := "?tenant_id=" + url.QueryEscape(tenantID)

	var wg sync.WaitGroup
	for i, endpoint := range a.gateways {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var page workflowview.JobPage
			failure := fetchInto(ctx, a, endpoint, "/internal/v1/jobs"+query, &page,
				func(doc workflowview.JobPage) bool {
					return doc.ReplicaID != "" && !doc.GeneratedAt.IsZero() &&
						doc.TenantID != "" && doc.Jobs != nil
				})
			if failure == "" && page.TenantID != tenantID {
				// The replica answered about a different tenant than the one
				// asked for. Nothing should produce this, and the safe
				// reading is that the answer cannot be trusted rather than
				// that it can be filtered.
				//
				// 副本回答的租户与被问的不是同一个。不该有任何东西产生这种情况，而
				// 安全的理解是「这个答案不可信」，而不是「这个答案可以过滤一下」。
				failure = "malformed"
			}
			status := ReplicaStatus{Endpoint: endpoint, Error: failure}
			if failure == "" {
				generated := page.GeneratedAt
				status.ReplicaID = page.ReplicaID
				status.GeneratedAt = &generated
				status.NodeCount = len(page.Jobs)
			}
			results[i] = result{status: status, page: page}
		}()
	}
	wg.Wait()

	view := JobView{
		CollectedAt: a.clock.Now().UTC(),
		Replicas:    make([]ReplicaStatus, 0, len(results)),
		Jobs:        make([]Job, 0),
	}
	for _, item := range results {
		view.Replicas = append(view.Replicas, item.status)
		if item.status.Error != "" {
			view.Partial = true
			continue
		}
		if item.page.Truncated {
			view.Truncated = true
		}
		for _, job := range item.page.Jobs {
			view.Jobs = append(view.Jobs, Job{Job: job, Replica: item.page.ReplicaID})
		}
	}

	// Newest first, with the id breaking ties so the order is total: two runs
	// created in the same instant on different replicas would otherwise swap
	// places between reads.
	//
	// 最新的在前，id 用于打破平手，从而让顺序成为全序：否则在不同副本上于同一瞬间创建的
	// 两次运行，会在多次读取之间互换位置。
	sort.Slice(view.Jobs, func(i, j int) bool {
		if view.Jobs[i].CreatedAt.Equal(view.Jobs[j].CreatedAt) {
			return view.Jobs[i].ID > view.Jobs[j].ID
		}
		return view.Jobs[i].CreatedAt.After(view.Jobs[j].CreatedAt)
	})
	return view, nil
}
