package fleet

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"AIServeWeave/common/modelroute"
)

// RoutingReplica reports one configured endpoint, including failed reads.
// RoutingReplica 报告一个配置端点，包括读取失败的端点。
type RoutingReplica struct {
	Endpoint    string     `json:"endpoint"`
	ReplicaID   string     `json:"replica_id,omitempty"`
	Mode        string     `json:"mode,omitempty"`
	Revision    int64      `json:"revision"`
	Digest      string     `json:"digest,omitempty"`
	AppliedAt   *time.Time `json:"applied_at,omitempty"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// RoutingReport compares a published revision with fresh endpoint observations.
// RoutingReport 将已发布版本与各端点的新鲜观测比较。
type RoutingReport struct {
	DesiredRevision int64            `json:"desired_revision"`
	DesiredDigest   string           `json:"desired_digest"`
	CheckedAt       time.Time        `json:"checked_at"`
	Complete        bool             `json:"complete"`
	Replicas        []RoutingReplica `json:"replicas"`
}

// RoutingStatus reads every configured Gateway with at most eight simultaneous calls.
// It cannot declare success from an empty fleet, duplicate identities, stale data or file mode.
// RoutingStatus 最多并发八次调用，读取全部已配置 Gateway。
// 空机群、重复身份、过时数据或文件模式均不能被声明为成功。
func (a *Aggregator) RoutingStatus(ctx context.Context, desired modelroute.Snapshot) RoutingReport {
	out := RoutingReport{DesiredRevision: desired.Revision, DesiredDigest: desired.Digest, CheckedAt: time.Now().UTC(), Replicas: []RoutingReplica{}}
	if a == nil {
		return out
	}
	out.CheckedAt = a.clock.Now().UTC()
	out.Replicas = make([]RoutingReplica, len(a.gateways))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(8, len(a.gateways)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				out.Replicas[i] = a.routingReplica(ctx, a.gateways[i])
			}
		}()
	}
	for i := range a.gateways {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	out.CheckedAt = a.clock.Now().UTC()
	out.Complete = desired.Revision > 0 && len(out.Replicas) > 0
	seen := make(map[string]bool)
	for i := range out.Replicas {
		r := &out.Replicas[i]
		if r.ReplicaID != "" && seen[r.ReplicaID] {
			r.Error = "duplicate_replica"
		}
		seen[r.ReplicaID] = true
		if r.Error != "" || r.Mode != "controlplane" || r.Revision != desired.Revision || r.Digest != desired.Digest {
			out.Complete = false
		}
	}
	return out
}

// routingReplica reads a small status document without trusting upstream error text.
// routingReplica 读取小型状态文档，不采信上游错误文本。
func (a *Aggregator) routingReplica(ctx context.Context, endpoint string) RoutingReplica {
	out := RoutingReplica{Endpoint: endpoint}
	timeout := a.client.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/internal/v1/routes", nil)
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
	var doc modelroute.ReplicaStatus
	if json.Unmarshal(body, &doc) != nil || doc.ReplicaID == "" || doc.GeneratedAt.IsZero() || doc.Revision < 0 || (doc.Mode != "file" && doc.Mode != "controlplane") {
		out.Error = "malformed"
		return out
	}
	digest, err := hex.DecodeString(doc.Digest)
	if err != nil || len(digest) != 32 {
		out.Error = "malformed"
		return out
	}
	out.ReplicaID = doc.ReplicaID
	out.Mode = doc.Mode
	out.Revision = doc.Revision
	out.Digest = doc.Digest
	out.AppliedAt = &doc.AppliedAt
	out.CheckedAt = &doc.CheckedAt
	out.GeneratedAt = &doc.GeneratedAt
	// Clock skew is reported conservatively, never rounded into an acknowledgement.
	// 时钟偏差按保守方式报告，不将其视作已确认。
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
