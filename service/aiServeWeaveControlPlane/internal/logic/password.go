package logic

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// longPasswordPrefix distinguishes full-input hashing from legacy bcrypt.
// longPasswordPrefix 区分完整输入哈希与旧版直接 bcrypt。
const longPasswordPrefix = "bcrypt-sha256:"

// hashPassword accepts any password and retains bcrypt's salted slow hashing.
// Inputs beyond bcrypt's 72-byte boundary are first SHA-256 encoded as hex.
// hashPassword 接受任意密码并保留 bcrypt 的加盐慢哈希；超过其 72 字节边界时先取
// SHA-256 的十六进制编码，让密码的全部内容参与校验。
func hashPassword(password string) (string, error) {
	prefix := ""
	if len(password) > 72 {
		password = passwordDigest(password)
		prefix = longPasswordPrefix
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return prefix + string(hash), nil
}

// comparePassword verifies both existing bcrypt hashes and marked long passwords.
// comparePassword 同时校验既有 bcrypt 哈希与带标记的长密码。
func comparePassword(hash, password string) error {
	if strings.HasPrefix(hash, longPasswordPrefix) {
		hash = strings.TrimPrefix(hash, longPasswordPrefix)
		password = passwordDigest(password)
	} else if len(password) > 72 {
		// Keep the same bcrypt work for unknown users and legacy hashes, then
		// reject: a long candidate must never match a truncated legacy value.
		// 对未知用户与旧哈希仍执行等量 bcrypt 运算，再拒绝；长输入绝不能通过截断
		// 匹配旧密码，也不能凭快速失败暴露账号是否存在。
		_ = bcrypt.CompareHashAndPassword([]byte(hash), []byte(passwordDigest(password)))
		return bcrypt.ErrMismatchedHashAndPassword
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// passwordDigest converts the entire UTF-8 input to a bcrypt-compatible value.
// passwordDigest 将完整 UTF-8 输入转换为 bcrypt 可接受的值。
func passwordDigest(password string) string {
	digest := sha256.Sum256([]byte(password))
	return hex.EncodeToString(digest[:])
}
