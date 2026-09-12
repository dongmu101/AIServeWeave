// requestlog.go holds the persisted record of one authenticated OpenAI
// front-door request, per STATUS.md's P09/C28.
//
// requestlog.go 保存一次已通过鉴权的 OpenAI 前门请求的持久化记录，对应
// STATUS.md 的 P09/C28。
package model

import "time"

// RequestLog is one authenticated chat/responses/embeddings/models request,
// as a Gateway replica reported it. ID is the request-correlation id
// common/reqid minted for it on the Gateway side, not a surrogate key — the
// same choice Job.ID already makes, and for the same reason: the id is
// already globally unique and is what every caller already refers to the
// request by, so a duplicate report (a retried batch push) is naturally
// rejected — here, silently skipped — by the same uniqueness this key
// already provides.
//
// Outcome is a closed classification derived purely from StatusCode by the
// Gateway before this row ever exists; no free-text error message is ever
// stored here.
//
// RequestLog 是一次已通过鉴权的 chat/responses/embeddings/models 请求，由
// Gateway 副本上报。ID 是 Gateway 一侧 common/reqid 为它铸造的请求关联
// id，不是代理键——与 Job.ID 相同的选择，理由也相同：这个 id 本就全局唯一，
// 也是每个调用方已经用来指代这次请求的东西，因此重复上报（一次被重试的批量
// 推送）天然会被这同一个唯一性拒绝——这里的表现是被悄悄跳过。
//
// Outcome 是 Gateway 在这一行存在之前就已从 StatusCode 纯粹推导出的封闭分类；
// 这里从不存储任何自由文本错误信息。
type RequestLog struct {
	ID         string    `gorm:"primaryKey;size:64"`
	TenantID   string    `gorm:"size:32;not null"`
	KeyDisplay string    `gorm:"size:64;not null"`
	Endpoint   string    `gorm:"size:16;not null"`
	StatusCode int       `gorm:"not null"`
	Outcome    string    `gorm:"size:24;not null"`
	DurationMS int64     `gorm:"not null"`
	CreatedAt  time.Time `gorm:"not null"`
}

// TableName returns "request_logs", the table RequestLog persists to.
//
// TableName 返回 "request_logs"，即 RequestLog 持久化到的表名。
func (RequestLog) TableName() string { return "request_logs" }
