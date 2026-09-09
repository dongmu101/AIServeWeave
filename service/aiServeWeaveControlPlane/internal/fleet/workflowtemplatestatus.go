package fleet

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"AIServeWeave/common/workflowtemplate"
)

// WorkflowTemplateReplica reports one configured endpoint's workflow-template
// bundle status, including failed reads — the same shape RoutingReplica
// carries for routing, generalized from a single revision to a bundle
// digest+count (P03).
//
// WorkflowTemplateReplica 报告一个配置端点的工作流模板整包状态，包括读取失败的
// 端点——与 RoutingReplica 为路由携带的是同一种形状，只是从单一版本号推广成了
// 整包摘要+数量（P03）。
type WorkflowTemplateReplica struct {
	Endpoint      string     `json:"endpoint"`
	ReplicaID     string     `json:"replica_id,omitempty"`
	Mode          string     `json:"mode,omitempty"`
	TemplateCount int        `json:"template_count"`
	BundleDigest  string     `json:"bundle_digest,omitempty"`
	AppliedAt     *time.Time `json:"applied_at,omitempty"`
	CheckedAt     *time.Time `json:"checked_at,omitempty"`
	GeneratedAt   *time.Time `json:"generated_at,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// WorkflowTemplateReport compares the desired bundle with fresh endpoint
// observations. / WorkflowTemplateReport 将期望整包与各端点的新鲜观测比较。
type WorkflowTemplateReport struct {
	DesiredTemplateCount int                       `json:"desired_template_count"`
	DesiredBundleDigest  string                    `json:"desired_bundle_digest"`
	CheckedAt            time.Time                 `json:"checked_at"`
	Complete             bool                      `json:"complete"`
	Replicas             []WorkflowTemplateReplica `json:"replicas"`
}

// WorkflowTemplateStatus reads every configured Gateway with at most eight
// simultaneous calls, the same bound RoutingStatus applies. It cannot declare
// success from an empty fleet, duplicate identities, stale data or file mode.
//
// WorkflowTemplateStatus 最多并发八次调用，读取全部已配置 Gateway，与
// RoutingStatus 相同的上限。空机群、重复身份、过时数据或文件模式均不能被声明为
// 成功。
func (a *Aggregator) WorkflowTemplateStatus(ctx context.Context, desiredCount int, desiredDigest string) WorkflowTemplateReport {
	out := WorkflowTemplateReport{DesiredTemplateCount: desiredCount, DesiredBundleDigest: desiredDigest, CheckedAt: time.Now().UTC(), Replicas: []WorkflowTemplateReplica{}}
	if a == nil {
		return out
	}
	out.CheckedAt = a.clock.Now().UTC()
	out.Replicas = make([]WorkflowTemplateReplica, len(a.gateways))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(8, len(a.gateways)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				out.Replicas[i] = a.workflowTemplateReplica(ctx, a.gateways[i])
			}
		}()
	}
	for i := range a.gateways {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	out.CheckedAt = a.clock.Now().UTC()
	out.Complete = len(out.Replicas) > 0
	seen := make(map[string]bool)
	for i := range out.Replicas {
		r := &out.Replicas[i]
		if r.ReplicaID != "" && seen[r.ReplicaID] {
			r.Error = "duplicate_replica"
		}
		seen[r.ReplicaID] = true
		if r.Error != "" || r.Mode != "controlplane" || r.TemplateCount != desiredCount || r.BundleDigest != desiredDigest {
			out.Complete = false
		}
	}
	return out
}

// workflowTemplateReplica reads a small status document without trusting
// upstream error text, the same discipline routingReplica applies.
//
// workflowTemplateReplica 读取小型状态文档，不采信上游错误文本，与
// routingReplica 相同的克制。
func (a *Aggregator) workflowTemplateReplica(ctx context.Context, endpoint string) WorkflowTemplateReplica {
	out := WorkflowTemplateReplica{Endpoint: endpoint}
	timeout := a.client.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/internal/v1/workflows/status", nil)
	if err != nil {
		out.Error = "unreachable"
		return out
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.client.Do(req)
	if err != nil {
		out.Error = "unreachable"
		if ctx.Err() != nil || isTimeout(err) {
			out.Error = "timeout"
		}
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		out.Error = "unreachable"
		if resp.StatusCode == 404 {
			out.Error = "unsupported"
		}
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			out.Error = "unauthorized"
		}
		return out
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024+1))
	if err != nil || len(body) > 16*1024 {
		out.Error = "malformed"
		return out
	}
	var doc workflowtemplate.ReplicaStatus
	if json.Unmarshal(body, &doc) != nil || doc.ReplicaID == "" || doc.GeneratedAt.IsZero() || doc.TemplateCount < 0 || (doc.Mode != "file" && doc.Mode != "controlplane") {
		out.Error = "malformed"
		return out
	}
	out.ReplicaID = doc.ReplicaID
	out.Mode = doc.Mode
	out.TemplateCount = doc.TemplateCount
	out.BundleDigest = doc.BundleDigest
	out.AppliedAt = &doc.AppliedAt
	out.CheckedAt = &doc.CheckedAt
	out.GeneratedAt = &doc.GeneratedAt
	// Clock skew is reported conservatively, never rounded into an
	// acknowledgement — the same rule routingReplica applies.
	//
	// 时钟偏差按保守方式报告，不将其视作已确认——与 routingReplica 相同的规则。
	skew := a.clock.Now().Sub(doc.GeneratedAt)
	if skew > time.Minute || skew < -time.Minute {
		out.Error = "stale"
	} else if doc.AppliedAt.IsZero() || (doc.Mode == "controlplane" && doc.CheckedAt.IsZero()) {
		out.Error = "unconfirmed"
	} else if doc.Error != "" {
		out.Error = "sync_failed"
	}
	return out
}
