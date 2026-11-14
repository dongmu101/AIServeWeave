// requestlog.go holds the persisted record of one authenticated OpenAI
// front-door request, per STATUS.md's P09/C28.
//
// requestlog.go 保存一次已通过鉴权的 OpenAI 前门请求的持久化记录，对应
// STATUS.md 的 P09/C28。
package model

import "time"

// RequestLog is one authenticated inference front-door request (OpenAI,
// Anthropic Messages, Ollama, audio, rerank or model listing; Endpoint holds
// the Gateway's closed short name for it, at most 16 characters), as a
// Gateway replica reported it. ID is the request-correlation id
// common/reqid minted for it on the Gateway side, not a surrogate key — the
// same choice Job.ID already makes, and for the same reason: the id is
// already globally unique and is what every caller already refers to the
// request by, so a duplicate report (a retried batch push) is naturally
// rejected — here, silently skipped — by the same uniqueness this key
// already provides. The table is range-partitioned by day on CreatedAt, so
// the primary key is (id, created_at): the dedup holds because the Gateway's
// outbox resends a record with the CreatedAt it was first stamped with.
//
// Outcome is a closed classification derived purely from StatusCode by the
// Gateway before this row ever exists. Model, ErrorCode, ErrorMessage and
// ErrorDetail are filled only for a failed request (status 400 and up), for
// troubleshooting, and stay empty for a successful one; ErrorDetail is the
// server-side cause the client never saw and is shown on the operator search
// only.
//
// RequestLog 是一次已通过鉴权的推理前门请求(OpenAI、Anthropic Messages、
// Ollama、音频、rerank 或模型列表；Endpoint 存 Gateway 为它定的封闭短名，最长
// 16 个字符)，由 Gateway 副本上报。ID 是 Gateway 一侧 common/reqid 为它铸造的请求关联
// id，不是代理键——与 Job.ID 相同的选择，理由也相同：这个 id 本就全局唯一，
// 也是每个调用方已经用来指代这次请求的东西，因此重复上报（一次被重试的批量
// 推送）天然会被这同一个唯一性拒绝——这里的表现是被悄悄跳过。这张表按
// CreatedAt 按天做 RANGE 分区，因此主键是 (id, created_at)：去重依然成立，
// 因为 Gateway 的 outbox 重发记录时沿用其首次生成时的 CreatedAt。
//
// Outcome 是 Gateway 在这一行存在之前就已从 StatusCode 纯粹推导出的封闭分类。
// Model、ErrorCode、ErrorMessage 与 ErrorDetail 只为失败请求（状态码 400 及以上）
// 填写以便排错，成功请求留空；ErrorDetail 是客户端看不到的服务端原因，只在运维
// 检索中显示。
type RequestLog struct {
	ID         string    `gorm:"primaryKey;size:64"`
	TenantID   string    `gorm:"size:32;not null"`
	KeyDisplay string    `gorm:"size:64;not null"`
	Endpoint   string    `gorm:"size:16;not null"`
	StatusCode int       `gorm:"not null"`
	Outcome    string    `gorm:"size:24;not null"`
	DurationMS int64     `gorm:"not null"`
	CreatedAt  time.Time `gorm:"primaryKey;not null"`

	Model        string `gorm:"size:128;not null;default:''"`
	ErrorCode    string `gorm:"size:64;not null;default:''"`
	ErrorMessage string `gorm:"size:512;not null;default:''"`
	ErrorDetail  string `gorm:"size:1024;not null;default:''"`
}

// Length limits of the failure columns. CreateRequestLogs cuts longer values
// on a rune boundary instead of letting the database reject the batch.
//
// 失败字段列的长度上限。CreateRequestLogs 在 rune 边界截断更长的值，而不是让数据库
// 拒绝整批写入。
const (
	RequestLogModelMax        = 128
	RequestLogErrorCodeMax    = 64
	RequestLogErrorMessageMax = 512
	RequestLogErrorDetailMax  = 1024
)

// TableName returns "request_logs", the table RequestLog persists to.
//
// TableName 返回 "request_logs"，即 RequestLog 持久化到的表名。
func (RequestLog) TableName() string { return "request_logs" }
