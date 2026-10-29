// usageledger.go implements the logic layer for STATUS.md's P2 usage
// ledger: turning a Gateway's batch push into validated model.UsageRecord
// rows, and serving the tenant/operator settlement queries against them.
//
// usageledger.go 实现 STATUS.md P2 用量账本的逻辑层：把 Gateway 的一次批量
// 推送转换为经过校验的 model.UsageRecord 行，并服务针对它们的租户/运维
// 结算查询。
package logic

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateUsageRecordParams is one record a Gateway replica reports.
//
// CreateUsageRecordParams 是一条由 Gateway 副本上报的记录。
type CreateUsageRecordParams struct {
	RequestID        string
	TenantID         string
	Model            string
	Endpoint         string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CreatedAt        time.Time
}

// Column widths of usage_records (and usage_charges), in bytes.
//
// usage_records（及 usage_charges）的列宽，单位为字节。
const (
	maxUsageRequestID = 64
	maxUsageTenantID  = 32
	maxUsageModel     = 128
	maxUsageEndpoint  = 24
)

// CreateUsageRecords persists a batch of records, skipping — not failing on
// — any entry missing a required field or too long for its column, mirroring
// CreateRequestLogs: a partially malformed batch from a Gateway replica
// should not cost the well-formed entries in it their place in the ledger.
// Skipping an over-long entry matters more than it looks: the Gateway
// retries a failed batch and holds newer batches behind it, so a batch the
// database rejects every time would stall every tenant's usage.
//
// It performs no tenant-scoped authorization: the caller is the Gateway,
// authenticated by the internal shared secret at the handler layer — the
// same trust boundary CreateRequestLogs already crosses.
//
// CreateUsageRecords 持久化一批记录，对任何缺失必填字段或超出列宽的条目是跳过
// 而不是让整批失败，与 CreateRequestLogs 对应：一个 Gateway 副本发来的部分
// 畸形批次，不应该让其中格式良好的条目失去入账的机会。跳过超长条目比看上去更
// 重要：Gateway 会重试失败的批次并让更新的批次排在其后，因此一个每次都被数据库
// 拒绝的批次会卡住所有租户的用量。
//
// 它不做任何按租户的授权检查：调用方是 Gateway，在 handler 层已由内部共享
// 密钥认证——与 CreateRequestLogs 已经跨过的是同一条信任边界。
func (s *Service) CreateUsageRecords(ctx context.Context, batch []CreateUsageRecordParams) (accepted int, err error) {
	records := make([]model.UsageRecord, 0, len(batch))
	for _, p := range batch {
		if p.RequestID == "" || p.TenantID == "" || p.Model == "" || p.Endpoint == "" {
			continue
		}
		if len(p.RequestID) > maxUsageRequestID || len(p.TenantID) > maxUsageTenantID ||
			len(p.Model) > maxUsageModel || len(p.Endpoint) > maxUsageEndpoint {
			slog.Warn("skipping a usage record too long for its columns", slog.String("tenant_id", truncate(p.TenantID, maxUsageTenantID)))
			continue
		}
		records = append(records, model.UsageRecord{
			ID:               p.RequestID,
			TenantID:         p.TenantID,
			Model:            p.Model,
			Endpoint:         p.Endpoint,
			PromptTokens:     p.PromptTokens,
			CompletionTokens: p.CompletionTokens,
			TotalTokens:      p.TotalTokens,
			CreatedAt:        p.CreatedAt,
		})
	}
	if len(records) == 0 {
		return 0, nil
	}
	// With billing on, the records and their charges commit together, so a
	// record is never in the ledger uncharged nor charged twice.
	//
	// 开启计费时，记录与其费用一同提交，因此一条记录既不会入账却未扣费，也不会被扣费
	// 两次。
	if s.billingEnabled() {
		if err := s.chargeUsage(ctx, records); err != nil {
			return 0, err
		}
		return len(records), nil
	}
	if err := s.store.CreateUsageRecords(ctx, records); err != nil {
		return 0, err
	}
	return len(records), nil
}

// SummarizeUsage reads the settlement aggregation over persisted usage
// records. Tenant scoping (or its deliberate absence, for the operator
// cross-tenant settlement view) is entirely the caller's responsibility via
// filter.TenantID — this method applies no authorization of its own,
// matching ListRequestLogs.
//
// SummarizeUsage 读取已持久化用量记录的结算聚合。租户限定(或运维跨租户
// 结算视角时刻意不限定)完全是调用方通过 filter.TenantID 承担的责任——本
// 方法不做任何自己的授权判断，与 ListRequestLogs 一致。
func (s *Service) SummarizeUsage(ctx context.Context, filter store.UsageRecordFilter) ([]store.UsageSummary, error) {
	return s.store.SummarizeUsage(ctx, filter)
}
