// alerting.go holds the persisted records for STATUS.md's P09/C29: a
// threshold rule over metrics_history, and one fired instance of it.
//
// alerting.go 保存 STATUS.md P09/C29 的持久化记录：一条基于 metrics_history
// 的阈值规则，以及它的一次触发实例。
package model

import "time"

// Alert rule metric values. Closed by design — these are NOT raw
// metrics_history.metric column values; success_rate and latency_p95 are
// derived server-side by internal/alertengine from raw counters/histogram
// buckets, per the design doc's「派生指标计算」section.
//
// 告警规则的指标取值。设计上封闭——它们不是 metrics_history.metric 列的原始
// 值；success_rate 与 latency_p95 由 internal/alertengine 从原始计数器/直方图
// 桶派生计算得到，见设计文档「派生指标计算」一节。
const (
	MetricRequestRate = "request_rate"
	MetricSuccessRate = "success_rate"
	MetricLatencyP95  = "latency_p95"
	MetricTokenUsage  = "token_usage"
	MetricCapacity    = "capacity"
)

// Alert rule comparison operators.
//
// 告警规则的比较运算符。
const (
	OperatorLessThan           = "lt"
	OperatorLessThanOrEqual    = "lte"
	OperatorGreaterThan        = "gt"
	OperatorGreaterThanOrEqual = "gte"
)

// Alert instance statuses. Resolved is terminal — a later breach of the same
// rule creates a new instance row rather than reopening this one, the same
// one-way-terminal-state convention Job already uses in this codebase.
//
// 告警实例状态。resolved 是终态——同一规则后续再次触发会创建一条新的实例行，
// 而不是重开这一行，与本仓库 Job 已经采用的终态不可逆约定相同。
const (
	AlertStatusFiring       = "firing"
	AlertStatusAcknowledged = "acknowledged"
	AlertStatusResolved     = "resolved"
)

// Webhook delivery statuses for one alert instance.
//
// 一个告警实例的 Webhook 投递状态。
const (
	NotifyStatusPending = "pending"
	NotifyStatusSent    = "sent"
	NotifyStatusFailed  = "failed"
	NotifyStatusSkipped = "skipped"
)

// AlertRule is one operator-defined threshold rule over a derived
// metrics_history metric (STATUS.md's P09/C29). It is platform-wide, not
// tenant-scoped — the same posture P08/C27 already took for the metrics
// this evaluates.
//
// AlertRule 是运维定义的、基于 metrics_history 派生指标的一条阈值规则
// （STATUS.md 的 P09/C29）。它是平台级的，不分租户——与 P08/C27 对这里
// 要评估的指标已经采取的立场相同。
type AlertRule struct {
	ID                 string `gorm:"primaryKey;size:64"`
	Name               string `gorm:"size:128;not null"`
	Metric             string `gorm:"size:32;not null"`
	Operator           string `gorm:"size:8;not null"`
	Threshold          float64
	ConsecutiveBuckets int    `gorm:"not null"`
	WebhookURL         string `gorm:"size:512;not null;default:''"`
	Enabled            bool   `gorm:"not null;default:true;index"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TableName pins the table name. See Tenant.TableName.
//
// TableName 钉死表名，理由见 Tenant.TableName。
func (AlertRule) TableName() string { return "alert_rules" }

// AlertInstance is one fired instance of an AlertRule. CreatedAt holds the
// time the instance first fired — named CreatedAt rather than FirstFiredAt
// so the shared readPage keyset-pagination helper's hardcoded
// `created_at`/`id` ordering works on this table unmodified, the same
// naming-for-shared-tooling choice P09a's RequestLog.ID already made.
//
// AlertInstance 是一个 AlertRule 的一次触发实例。CreatedAt 保存该实例首次
// 触发的时间——命名为 CreatedAt 而不是 FirstFiredAt，是为了让共享的
// readPage keyset 分页助手那套写死的 `created_at`/`id` 排序无需改动就能
// 用在这张表上，与 P09a 里 RequestLog.ID 已经做过的「为共用工具而命名」的
// 选择相同。
type AlertInstance struct {
	ID              string `gorm:"primaryKey;size:64"`
	RuleID          string `gorm:"size:64;not null;index:idx_alert_instances_rule_status"`
	Status          string `gorm:"size:16;not null;index:idx_alert_instances_rule_status"`
	ValueAtFire     float64
	CreatedAt       time.Time // first fired at; see doc comment above
	LastEvaluatedAt time.Time
	ResolvedAt      *time.Time
	AcknowledgedBy  string `gorm:"size:32;not null;default:''"`
	AcknowledgedAt  *time.Time
	NotifyStatus    string `gorm:"size:16;not null"`
	NotifyAttempts  int    `gorm:"not null;default:0"`
}

// TableName pins the table name. See Tenant.TableName.
//
// TableName 钉死表名，理由见 Tenant.TableName。
func (AlertInstance) TableName() string { return "alert_instances" }
