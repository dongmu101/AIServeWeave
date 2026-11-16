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
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"time"

	"AIServeWeave/common/pricing"
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
	// NodeID is the tenant-owned node that served the request, or empty.
	//
	// NodeID 是服务该请求的商户自有节点，没有则为空。
	NodeID string
	// KeyID is the API key that made the request, or empty.
	//
	// KeyID 是发起请求的 API key，没有则为空。
	KeyID string
	// Details and Attributes are the Gateway's meter breakdown and request
	// facts (common/pricing), or nil.
	//
	// Details 与 Attributes 是 Gateway 上报的计量项细分与请求属性（common/pricing），
	// 没有则为 nil。
	Details    map[string]int64
	Attributes map[string]string
}

// Column widths of usage_records (and usage_charges), in bytes.
//
// usage_records（及 usage_charges）的列宽，单位为字节。
const (
	maxUsageRequestID = 64
	maxUsageTenantID  = model.IDSize
	maxUsageModel     = 128
	maxUsageEndpoint  = 24
	maxUsageKeyID     = model.IDSize
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
			len(p.Model) > maxUsageModel || len(p.Endpoint) > maxUsageEndpoint || len(p.KeyID) > maxUsageKeyID {
			slog.Warn("skipping a usage record too long for its columns", slog.String("tenant_id", truncate(p.TenantID, maxUsageTenantID)))
			continue
		}
		record := model.UsageRecord{
			ID:               p.RequestID,
			TenantID:         p.TenantID,
			Model:            p.Model,
			Endpoint:         p.Endpoint,
			PromptTokens:     p.PromptTokens,
			CompletionTokens: p.CompletionTokens,
			TotalTokens:      p.TotalTokens,
			CreatedAt:        p.CreatedAt,
			NodeID:           p.NodeID,
			KeyID:            p.KeyID,
		}
		applyUsageDetails(&record, p.Details, p.Attributes)
		records = append(records, record)
	}
	if len(records) == 0 {
		return 0, nil
	}
	if err := s.attributeUsageToUsers(ctx, records); err != nil {
		return 0, err
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

// applyUsageDetails stores the record's meter breakdown: the details JSON
// and the cache columns summaries add up. Details or attributes that break
// common/pricing's limits are dropped with a warning while the record is
// kept, the same skip-don't-fail rule as an over-long field: the record's
// totals still bill at the base price, and refusing the batch would lose its
// usage, since the Gateway drops a batch the control plane refuses. The
// Gateway already filters by the same rules, so this is a backstop.
//
// applyUsageDetails 保存记录的计量项细分：明细 JSON，以及汇总时求和的缓存列。违反
// common/pricing 限制的明细或属性会被丢弃并记一条警告，记录本身保留，与超长字段同样
// 采取「跳过而不失败」的规则：记录的总数仍按基础价计费，而拒绝整批会丢失其中的用量，
// 因为 Gateway 会丢弃被控制面拒绝的批次。Gateway 已经按同样的规则过滤，这里只是兜底。
func applyUsageDetails(record *model.UsageRecord, details map[string]int64, attributes map[string]string) {
	if err := pricing.ValidateDetails(details); err != nil {
		slog.Warn("dropping invalid usage details", slog.String("tenant_id", record.TenantID), slog.Any("error", err))
		details = nil
	}
	if err := pricing.ValidateAttributes(attributes); err != nil {
		slog.Warn("dropping invalid usage attributes", slog.String("tenant_id", record.TenantID), slog.Any("error", err))
		attributes = nil
	}
	record.ReportedDetails, record.ReportedAttributes = details, attributes
	if len(details) == 0 && len(attributes) == 0 {
		return
	}
	quantities, adjusted := pricing.Quantities(record.PromptTokens, record.CompletionTokens, details)
	record.CacheReadTokens = quantities[pricing.MeterCacheRead]
	record.CacheWriteTokens = quantities[pricing.MeterCacheWrite]
	doc := model.UsageDetails{V: model.UsageDetailsVersion, Meters: make([]model.UsageMeterLine, 0, len(quantities)), Adjusted: adjusted}
	if len(attributes) > 0 {
		doc.Attributes = attributes
	}
	for _, name := range slices.Sorted(maps.Keys(quantities)) {
		doc.Meters = append(doc.Meters, model.UsageMeterLine{K: name, Q: quantities[name]})
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return // a map of strings and integers always encodes / 字符串与整数组成的 map 总能编码
	}
	text := string(encoded)
	record.Details = &text
}

// attributeUsageToUsers fills each record's UserID with its key's creator,
// looking each distinct key up once. A key that no longer exists leaves
// UserID empty rather than failing the batch; any other read error fails it,
// so the Gateway retries instead of writing records without their user.
//
// attributeUsageToUsers 把每条记录的 UserID 填为其 key 的创建者，每个不同的 key 只查
// 一次。key 已不存在时 UserID 留空，而不是让整批失败；其他读取错误则让整批失败，使
// Gateway 重试，而不是写入缺少用户的记录。
func (s *Service) attributeUsageToUsers(ctx context.Context, records []model.UsageRecord) error {
	type keyRef struct{ tenantID, keyID string }
	creators := map[keyRef]string{}
	for i := range records {
		ref := keyRef{records[i].TenantID, records[i].KeyID}
		if ref.keyID == "" {
			continue
		}
		creator, ok := creators[ref]
		if !ok {
			key, err := s.store.GetAPIKey(ctx, ref.tenantID, ref.keyID)
			switch {
			case err == nil:
				creator = key.CreatedBy
			case !errors.Is(err, store.ErrNotFound):
				return err
			}
			creators[ref] = creator
		}
		records[i].UserID = creator
	}
	return nil
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
