package model

import (
	"crypto/rand"
	"encoding/hex"
)

// ID prefixes, one per entity. A primary key that says what it identifies
// turns a foreign key mix-up from a silent wrong answer into an obvious one:
// a tenant_id reading "usr_..." is wrong at a glance, in a log line, in a
// support ticket and in a WHERE clause.
//
// ID 前缀，每个实体一个。一个能说明自己标识什么的主键，会把外键写错从「悄悄给出错误
// 答案」变成「一眼看出错误」：一个内容为 "usr_..." 的 tenant_id，无论出现在日志行、
// 工单还是 WHERE 子句里，都是一望即知的错。
const (
	PrefixTenant   = "tnt_"
	PrefixUser     = "usr_"
	PrefixAPIKey   = "key_"
	PrefixAuditLog = "aud_"
)

// randomBytes is the entropy in an id. 14 bytes leaves a 32-character id
// after the four-character prefix and hex encoding, which fits the size:32
// columns above with nothing to spare — the two numbers are chosen together
// and neither may move alone.
//
// randomBytes 是一个 id 中的随机字节数。14 字节在四字符前缀与 hex 编码之后正好得到
// 32 字符的 id，不多不少地填满上面那些 size:32 的列——这两个数字是一起定下的，任何
// 一个都不能单独改动。
const randomBytes = 14

// NewID mints an identifier with the given prefix. It panics only if the
// system's random source fails, which is not a condition this service can
// serve through: an id from a degraded source risks colliding with an existing
// row, and a control plane that quietly writes over another tenant's record is
// worse than one that stops.
//
// NewID 用给定前缀铸造一个标识符。仅当系统随机源失败时才 panic，而那不是本服务可以
// 带病继续服务的情况：来自降级随机源的 id 有与既有行碰撞的风险，而一个悄悄覆盖了
// 另一个租户记录的控制面，比一个停下来的控制面更糟。
func NewID(prefix string) string {
	buf := make([]byte, randomBytes)
	if _, err := rand.Read(buf); err != nil {
		panic("controlplane: the system random source failed while minting an id: " + err.Error())
	}
	return prefix + hex.EncodeToString(buf)
}
