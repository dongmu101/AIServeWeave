// Package idgen mints the repository's identifiers: a UUIDv7 written as 32
// lowercase hex characters, with no dashes. The leading 48 bits are the Unix
// millisecond timestamp, so ids minted later sort later. A B-tree primary key
// then takes new rows at its right edge instead of splitting pages at random,
// and the hex form keeps that order under plain string comparison.
//
// An id is not a secret: 74 of its bits are random and its timestamp is
// readable. API keys, session ids, tokens and nonces keep using crypto/rand
// directly.
//
// idgen 包铸造本仓库的标识符：UUIDv7，写成 32 位小写十六进制、不带连字符。前 48 位是
// Unix 毫秒时间戳，越晚铸造的 id 排序越靠后。B-tree 主键因此把新行追加在右端，而不是
// 在随机位置分裂页；十六进制形式在普通字符串比较下也保持这个顺序。
//
// id 不是密钥：其中只有 74 位随机，时间戳可以读出。API key、会话 id、令牌和 nonce
// 仍然直接使用 crypto/rand。
package idgen

import (
	"encoding/hex"
	"uuid"
)

// Len is the length of an id returned by New.
//
// Len 是 New 返回的 id 的长度。
const Len = 32

// New returns a fresh UUIDv7 from the standard library uuid package as 32 hex
// characters. The ids it returns sort in mint order unless the system clock
// moves backwards.
//
// New 返回一个由标准库 uuid 包铸造的新 UUIDv7，形式为 32 位十六进制。除非系统时钟
// 回拨，返回的 id 按铸造顺序排序。
func New() string {
	u := uuid.NewV7()
	return hex.EncodeToString(u[:])
}
