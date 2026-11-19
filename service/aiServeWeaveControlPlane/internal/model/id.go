package model

import "AIServeWeave/common/idgen"

// ID prefixes, one per entity. A primary key that says what it identifies
// turns a foreign key mix-up from a silent wrong answer into an obvious one:
// a tenant_id reading "usr_..." is wrong at a glance, in a log line, in a
// support ticket and in a WHERE clause.
//
// ID 前缀，每个实体一个。一个能说明自己标识什么的主键，会把外键写错从「悄悄给出错误
// 答案」变成「一眼看出错误」：一个内容为 "usr_..." 的 tenant_id，无论出现在日志行、
// 工单还是 WHERE 子句里，都是一望即知的错。
const (
	PrefixAccountSale      = "acc_"
	PrefixAccountPresale   = "pre_"
	PrefixTenant           = "tnt_"
	PrefixUser             = "usr_"
	PrefixAPIKey           = "key_"
	PrefixAuditLog         = "aud_"
	PrefixPlatformOperator = "plt_"
	PrefixAlertRule        = "alr_"
	PrefixAlertInstance    = "ali_"
	PrefixCreditPackage    = "pkg_"
	PrefixCheckoutOrder    = "ord_"
	PrefixCreditLedger     = "led_"
	PrefixRefundRequest    = "rfd_"
	PrefixConversation     = "cnv_"
	PrefixMessage          = "msg_"
)

// IDSize is the column width, in characters, of every column that holds an
// id from NewID: a four-character prefix and a 32-character idgen UUIDv7 make
// 36, and the rest is headroom for a longer prefix. The size:40 tags in this
// package and the VARCHAR(40) columns in the migrations follow it.
//
// IDSize 是所有保存 NewID 所铸 id 的列的宽度（字符数）：四字符前缀加 32 字符的
// idgen UUIDv7 共 36 字符，余下的是给更长前缀留的余量。本包的 size:40 标签和迁移里的
// VARCHAR(40) 列都按它定。
const IDSize = 40

// NewID mints an identifier with the given prefix: the prefix followed by an
// idgen UUIDv7, so ids of one entity sort in creation order and new rows land
// at the right edge of the primary-key index.
//
// NewID 用给定前缀铸造一个标识符：前缀加一个 idgen UUIDv7，因此同一实体的 id 按创建
// 顺序排序，新行落在主键索引的右端。
func NewID(prefix string) string {
	return prefix + idgen.New()
}
