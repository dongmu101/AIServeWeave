// Package model's responseturn.go holds the persisted record of one
// Responses API turn a Gateway replica asked this service to remember, per
// STATUS.md's P2 "Responses 持久会话" and the design in
// docs/superpowers/specs/2026-09-16-p2-images-responses-multimodal-boundary-design.md
// §三. It exists only for callers that opted in with `store: true` — a
// Responses request that never sets it never reaches this table.
//
// model 的 responseturn.go 保存一次 Responses API 轮次的持久化记录——一个
// Gateway 副本请求本服务代为记住的内容，对应 STATUS.md 的 P2「Responses
// 持久会话」与
// docs/superpowers/specs/2026-09-16-p2-images-responses-multimodal-boundary-design.md
// 第三节的设计。它只为显式选择了 `store: true` 的调用方存在——一次从未设置
// 该字段的 Responses 请求永远不会进入这张表。
package model

import "time"

// ResponseTurn is one turn of a Responses API conversation, keyed by the
// public response id the Gateway replica minted for it.
//
// Messages is opaque JSON this service never parses: it is the Gateway's own
// canonical message list (runtime.ChatMessage, encoded) contributed by this
// one turn — the system instructions and user input that went in, plus the
// assistant's reply that came out. Reconstructing a longer conversation is
// the Gateway's job, not this service's: it walks PreviousResponseID one hop
// at a time via GetResponseTurn and concatenates each turn's Messages in
// order, the same division of labor model.Job already draws between "this
// service stores the row" and "the Gateway knows what the bytes mean".
//
// PreviousResponseID is a plain string column, not a foreign key: the turn
// it names may not exist (an unknown or expired id, which the Gateway must
// answer as a client error, not a server one) or may belong to another
// tenant (GetResponseTurn's tenant scoping already refuses to reveal that
// distinction, the same ErrNotFound shape store.ErrNotFound uses
// everywhere else in this package).
//
// ResponseTurn 是一次 Responses API 会话中的一轮，以 Gateway 副本为它铸造的
// 公开 response id 为键。
//
// Messages 是本服务从不解析的不透明 JSON：它是 Gateway 自己的 canonical
// 消息列表（runtime.ChatMessage 编码后的形式），由这一轮贡献——送进去的
// system 指示与用户输入，加上出来的 assistant 回复。重建一段更长的对话是
// Gateway 的职责，不是本服务的：它经由 GetResponseTurn 逐跳沿
// PreviousResponseID 向前走，并按顺序拼接每一轮的 Messages——与
// model.Job 已经划开的「本服务只存这一行，Gateway 才知道字节的含义」是
// 同一种分工。
//
// PreviousResponseID 是一个普通的字符串列，不是外键：它指名的那一轮可能不
// 存在（一个未知或已过期的 id，Gateway 必须把这当作客户端错误而不是服务端
// 错误来回答），也可能属于另一个租户（GetResponseTurn 的租户限定已经拒绝
// 揭示这种区别——与本包别处 store.ErrNotFound 采用的是同一种不可区分）。
type ResponseTurn struct {
	ID                 string    `gorm:"primaryKey;size:64"`
	TenantID           string    `gorm:"size:32;not null;index:idx_response_turns_tenant"`
	PreviousResponseID string    `gorm:"size:64;not null;default:''"`
	Model              string    `gorm:"size:128;not null;default:''"`
	Messages           string    `gorm:"type:text;not null"`
	CreatedAt          time.Time `gorm:"not null;index:idx_response_turns_tenant"`
}

// TableName pins the table name. See Tenant.TableName in model.go.
//
// TableName 钉死表名，理由见 model.go 的 Tenant.TableName。
func (ResponseTurn) TableName() string { return "response_turns" }
