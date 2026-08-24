// Package apikey mints and verifies the API keys callers present to the
// Gateway. It is deliberately free of storage, transport and framework: the
// rules about what a key is made of, what may be persisted and what may be
// shown are the ones that must not vary by call site, so they live in one
// package with no dependencies to hide behind.
//
// The top-level README's 安全设计 states the rule this package exists to keep:
// 用户 API Key 只保存不可逆哈希. Nothing here ever returns a plaintext key
// except Generate, and Generate returns it exactly once — the caller shows it
// to the person who asked for it and then has no way to recover it.
//
// apikey 包负责铸造与校验调用方向 Gateway 出示的 API Key。它刻意不涉及存储、传输
// 与框架：一个 key 由什么构成、什么可以落库、什么可以展示，这些规则不允许因调用点
// 而异，因此把它们放在一个没有任何依赖可供推诿的包里。
//
// 顶层 README「安全设计」里的那条规则正是本包存在的理由：用户 API Key 只保存不可逆
// 哈希。这里除 Generate 外没有任何函数会返回明文 key，而 Generate 只返回一次——
// 调用方把它展示给索取的人之后，就再也无从找回。
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	// Prefix marks a string as one of this system's API keys. It is not a
	// secret and carries no entropy; it exists so a key found in a log, a
	// pasted snippet or a public repository is recognizable as ours — which
	// is what lets a secret scanner report it and an operator know what to
	// revoke.
	//
	// Prefix 用于标记一个字符串是本系统的 API Key。它不是秘密，也不携带任何熵；
	// 它存在的意义是让出现在日志、粘贴片段或公开仓库里的 key 能被认出是我们的
	// ——这正是密钥扫描器得以上报、运维得以知道该吊销什么的前提。
	Prefix = "aisw-"

	// secretBytes is the entropy behind one key. 256 bits is far past any
	// brute-force concern and is what makes the fast-hash choice in Hash
	// sound.
	//
	// secretBytes 是一个 key 背后的熵。256 位远超任何暴力破解的顾虑，这也正是
	// Hash 中选择快速哈希这一决定得以成立的前提。
	secretBytes = 32

	// DisplayLen is how many characters of the secret a listing may show
	// alongside the prefix, so a person with several keys can tell which is
	// which. Eight base64 characters is 48 bits — enough to distinguish keys
	// in one account, nowhere near enough to reconstruct one.
	//
	// DisplayLen 是列表中可以随前缀一并展示的密文字符数，好让持有多个 key 的人
	// 分得清哪个是哪个。8 个 base64 字符是 48 位——足以区分一个账户下的若干 key，
	// 却远不足以重建出其中任何一个。
	DisplayLen = 8
)

// ErrMalformed is returned for a string that cannot be one of our keys. It is
// deliberately the same error for every way a key can be wrong: telling a
// caller which part failed tells an attacker which part to keep.
//
// ErrMalformed 用于表示一个字符串不可能是我们的 key。它对所有出错方式刻意返回同一个
// 错误：告诉调用方哪一部分不对，等于告诉攻击者哪一部分可以留着。
var ErrMalformed = errors.New("apikey: malformed key")

// Generated is one freshly minted key. Plaintext is the only copy that will
// ever exist; Hash is what the caller stores; Display is what a listing may
// show later.
//
// Generated 是一个刚铸造出的 key。Plaintext 是将来唯一存在过的副本；Hash 是调用方
// 要保存的东西；Display 是之后列表里可以展示的内容。
type Generated struct {
	// Plaintext is shown to the person who requested the key, once. Storing
	// it, logging it or returning it from any later read is the failure this
	// package is built to prevent.
	//
	// Plaintext 只向索取该 key 的人展示一次。把它存下来、写进日志，或在之后任何一次
	// 读取中返回，都是本包为之而建的那种失败。
	Plaintext string
	// Hash is the irreversible form that goes to the database.
	//
	// Hash 是进入数据库的不可逆形式。
	Hash string
	// Display is the non-secret identifier of this key, safe in a listing,
	// a log line and an audit record.
	//
	// Display 是该 key 的非机密标识，可安全地出现在列表、日志行与审计记录中。
	Display string
}

// Generate mints a new key from the system's cryptographic random source. It
// returns an error only when that source fails, which is not a condition a
// caller can retry its way out of: a key minted from a degraded source is
// worse than no key at all, so this never falls back to a weaker source.
//
// Generate 从系统的密码学随机源铸造一个新 key。只有当该随机源失败时才返回错误，而
// 那不是调用方可以靠重试绕过的情况：用一个已降级的随机源铸造出的 key 比没有 key 更
// 糟，因此这里绝不退化到更弱的随机源。
func Generate() (Generated, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return Generated{}, err
	}
	plaintext := Prefix + base64.RawURLEncoding.EncodeToString(buf)
	return Generated{
		Plaintext: plaintext,
		Hash:      Hash(plaintext),
		Display:   Display(plaintext),
	}, nil
}

// Hash returns the stored form of a key: SHA-256 over the whole string,
// hex encoded.
//
// A fast hash is the right choice here, and the reasoning matters because the
// instinct says otherwise. bcrypt and argon2 exist to make an offline
// dictionary attack expensive against secrets people choose. A key from
// Generate is 256 uniformly random bits — there is no dictionary, and no
// amount of hashing cost changes an attacker's odds against it. What a slow
// hash would change is the Gateway: verification happens on every inference
// request, and a per-request bcrypt would put tens of milliseconds in front of
// every token. The security of this scheme rests on the entropy in
// secretBytes, not on the cost of the hash — which is precisely why
// secretBytes may never be lowered.
//
// Hash 返回一个 key 的存储形式：对整个字符串做 SHA-256，再以 hex 编码。
//
// 这里选快速哈希是对的，而且理由值得写下来，因为直觉会给出相反的答案。bcrypt 与
// argon2 的存在意义，是让针对「人自己选的秘密」的离线字典攻击变得昂贵。而 Generate
// 产出的 key 是 256 位均匀随机比特——不存在字典，再高的哈希代价也不会改变攻击者的
// 胜算。慢哈希真正会改变的是 Gateway：校验发生在每一次推理请求上，逐请求做一次
// bcrypt 等于在每个 token 前面垫上几十毫秒。本方案的安全性系于 secretBytes 的熵，
// 而不是哈希的代价——这也正是 secretBytes 绝不允许被调低的原因。
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Display returns the non-secret identifier of a key: its prefix plus the
// first DisplayLen characters of the secret. A malformed key yields the empty
// string rather than an error, because every caller of this function is
// rendering something for a person to read and none of them can act on a
// failure.
//
// Display 返回一个 key 的非机密标识：前缀加上密文的前 DisplayLen 个字符。格式不对的
// key 得到空字符串而不是错误，因为本函数的每一个调用方都是在渲染给人看的东西，它们
// 都无法对一个失败做出任何处置。
func Display(key string) string {
	secret, ok := strings.CutPrefix(key, Prefix)
	if !ok || len(secret) < DisplayLen {
		return ""
	}
	return Prefix + secret[:DisplayLen]
}

// WellFormed reports whether key could be one of ours, by shape alone. It is
// the cheap check the Gateway runs before spending a database lookup or a
// control-plane round trip on a string that cannot possibly be a key — a
// scanner probing with "Bearer test" should cost nothing.
//
// It says nothing about whether the key exists or is still valid. Only a
// lookup answers that.
//
// WellFormed 仅从形状上报告 key 是否可能是我们的。它是 Gateway 在为一个根本不可能是
// key 的字符串花费一次数据库查询或一次控制面往返之前所做的廉价检查——用
// "Bearer test" 试探的扫描器应当一分钱都花不掉。
//
// 它不说明该 key 是否存在、是否仍然有效。只有查表才能回答那件事。
func WellFormed(key string) bool {
	secret, ok := strings.CutPrefix(key, Prefix)
	if !ok {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	return err == nil && len(decoded) == secretBytes
}

// Verify reports whether key hashes to storedHash. The comparison is constant
// time, which is not about the hash lookup — an attacker cannot steer a
// database index without already knowing the hash — but about the last step,
// where a naive string compare would leak a prefix match one byte at a time.
//
// Verify 报告 key 的哈希是否等于 storedHash。比较是常数时间的，这一点并不是为了哈希
// 查表——攻击者在不知道哈希的前提下无法操纵数据库索引——而是为了最后这一步：朴素的
// 字符串比较会一个字节一个字节地泄漏前缀匹配的进度。
func Verify(key, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(Hash(key)), []byte(storedHash)) == 1
}

// HashOf validates the shape of key and returns its stored form, so a caller
// that only needs the lookup value does not repeat the two steps and risk
// getting their order wrong.
//
// HashOf 校验 key 的形状并返回其存储形式，这样只需要查询值的调用方就不必重复这两步，
// 也就不会把它们的顺序弄反。
func HashOf(key string) (string, error) {
	if !WellFormed(key) {
		return "", ErrMalformed
	}
	return Hash(key), nil
}
