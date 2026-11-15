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

	"github.com/google/uuid"
)

// Len is the length of an id returned by New.
//
// Len 是 New 返回的 id 的长度。
const Len = 32

// New returns a fresh UUIDv7 as 32 hex characters. Within one process the ids
// it returns are strictly increasing, even inside one millisecond. It fails
// only when the system random source does.
//
// New 返回一个新的 UUIDv7，形式为 32 位十六进制。同一进程内返回的 id 严格递增，同一
// 毫秒内也是如此。仅当系统随机源失败时返回错误。
func New() (string, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(u[:]), nil
}

// MustNew is New for callers that cannot serve through a broken random source.
//
// MustNew 是 New 的变体，供无法在随机源损坏时继续服务的调用方使用。
func MustNew() string {
	id, err := New()
	if err != nil {
		panic("idgen: the system random source failed: " + err.Error())
	}
	return id
}
