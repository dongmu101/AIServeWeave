// Package installcode generates, normalizes and hashes the one-time codes
// that let a node provider install an Agent with a single command.
//
// A code carries 100 bits from crypto/rand, so guessing one inside the
// redemption rate limit is not feasible; only its hash is ever stored.
//
// installcode 包生成、规范化并哈希一次性兑换码，让节点提供者用一行命令安装 Agent。
//
// 码含有来自 crypto/rand 的 100 bit，在兑换限流之内不可能被猜中；库里只存它的哈希。
package installcode

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// alphabet is Crockford Base32: no I, L, O or U, so a code read aloud or off
// a screen has no ambiguous characters.
//
// alphabet 是 Crockford Base32：不含 I、L、O、U，口述或照屏幕抄写的码没有易混字符。
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	// Length is the number of Base32 characters in a code (5 bits each).
	//
	// Length 是一个码的 Base32 字符数（每个 5 bit）。
	Length = 20
	// groupSize is how many characters sit between dashes when displayed.
	//
	// groupSize 是展示时两个连字符之间的字符数。
	groupSize = 4
)

// ErrInvalid reports a string that is not a well-formed code.
//
// ErrInvalid 表示字符串不是格式正确的兑换码。
var ErrInvalid = errors.New("installcode: invalid code")

// New returns a fresh random code in display form, for example
// K7QF-9M2D-X4TB-H8RW-3NPC.
//
// New 返回一个新的随机码，展示形式如 K7QF-9M2D-X4TB-H8RW-3NPC。
func New() (string, error) {
	raw := make([]byte, Length)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	var b strings.Builder
	for i, v := range raw {
		if i > 0 && i%groupSize == 0 {
			b.WriteByte('-')
		}
		// 256 is a multiple of 32, so the modulo adds no bias.
		// 256 是 32 的倍数，取模不引入偏差。
		b.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return b.String(), nil
}

// Normalize removes dashes, upper-cases the code and rejects anything that is
// not exactly Length characters of the alphabet.
//
// Normalize 去掉连字符、转为大写，并拒绝任何不是恰好 Length 个字母表字符的输入。
func Normalize(code string) (string, error) {
	code = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if len(code) != Length {
		return "", ErrInvalid
	}
	for i := 0; i < len(code); i++ {
		if strings.IndexByte(alphabet, code[i]) < 0 {
			return "", ErrInvalid
		}
	}
	return code, nil
}

// Hash returns the hex SHA-256 of the normalized form of code, which is what
// the database stores and looks up.
//
// Hash 返回 code 规范化形式的十六进制 SHA-256，即数据库存储与查找所用的值。
func Hash(code string) (string, error) {
	normalized, err := Normalize(code)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:]), nil
}
