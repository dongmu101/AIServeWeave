// requestlogs.go implements the logic layer for STATUS.md's P09/C28: turning
// a Gateway's batch push into validated model.RequestLog rows, and serving
// the tenant/operator search queries against them.
//
// requestlogs.go 实现 STATUS.md P09/C28 的逻辑层：把 Gateway 的一次批量推送
// 转换为经过校验的 model.RequestLog 行，并服务针对它们的租户/运维检索查询。
package logic

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateRequestLogParams is one record a Gateway replica reports.
//
// CreateRequestLogParams 是一条由 Gateway 副本上报的记录。
type CreateRequestLogParams struct {
	RequestID  string
	TenantID   string
	KeyDisplay string
	Endpoint   string
	StatusCode int
	Outcome    string
	DurationMS int64
	CreatedAt  time.Time
}

// CreateRequestLogs persists a batch of records, skipping — not failing on
// — any entry missing a required field. A partially malformed batch from a
// Gateway replica should not cost the well-formed entries in it their
// chance to be searchable; a bug in one caller's payload construction is
// visible in the accepted count, not by losing everything else in the
// batch.
//
// It performs no tenant-scoped authorization: the caller is the Gateway,
// authenticated by the internal shared secret at the handler layer — the
// same trust boundary CreateJob already crosses.
//
// CreateRequestLogs 持久化一批记录，对任何缺失必填字段的条目是跳过而不是
// 让整批失败。一个 Gateway 副本发来的部分畸形批次，不应该让其中格式良好的
// 条目失去被检索到的机会；某个调用方载荷构造上的缺陷，体现在被接受的计数
// 里，而不是拖累批次里的其余一切。
//
// 它不做任何按租户的授权检查：调用方是 Gateway，在 handler 层已由内部共享
// 密钥认证——与 CreateJob 已经跨过的是同一条信任边界。
func (s *Service) CreateRequestLogs(ctx context.Context, batch []CreateRequestLogParams) (accepted int, err error) {
	records := make([]model.RequestLog, 0, len(batch))
	for _, p := range batch {
		if p.RequestID == "" || p.TenantID == "" || p.Endpoint == "" {
			continue
		}
		records = append(records, model.RequestLog{
			ID:         p.RequestID,
			TenantID:   p.TenantID,
			KeyDisplay: p.KeyDisplay,
			Endpoint:   p.Endpoint,
			StatusCode: p.StatusCode,
			Outcome:    p.Outcome,
			DurationMS: p.DurationMS,
			CreatedAt:  p.CreatedAt,
		})
	}
	if len(records) == 0 {
		return 0, nil
	}
	if err := s.store.CreateRequestLogs(ctx, records); err != nil {
		return 0, err
	}
	return len(records), nil
}

// ListRequestLogs reads one page of persisted request records. Tenant
// scoping (or its deliberate absence, for the operator cross-tenant search)
// is entirely the caller's responsibility via filter.TenantID — this method
// applies no authorization of its own, matching ListJobs.
//
// ListRequestLogs 读取一页已持久化的请求记录。租户限定(或运维跨租户检索时
// 刻意不限定)完全是调用方通过 filter.TenantID 承担的责任——本方法不做任何
// 自己的授权判断，与 ListJobs 一致。
func (s *Service) ListRequestLogs(ctx context.Context, query store.ListQuery, filter store.RequestLogFilter) (store.Page[model.RequestLog], error) {
	return s.store.ListRequestLogs(ctx, query, filter)
}
