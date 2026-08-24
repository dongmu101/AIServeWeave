package apikey_test

import (
	"strings"
	"testing"

	"AIServeWeave/common/apikey"
)

// TestGenerateProducesAUsableKey asserts the three fields of a minted key
// agree with each other: the hash is the hash of the plaintext, and the
// display form is a prefix of it.
//
// TestGenerateProducesAUsableKey 断言一个铸造出的 key 三个字段彼此自洽：哈希是明文的
// 哈希，展示形式是明文的前缀。
func TestGenerateProducesAUsableKey(t *testing.T) {
	got, err := apikey.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if !apikey.WellFormed(got.Plaintext) {
		t.Errorf("Generate produced a key its own WellFormed rejects: %q", got.Plaintext)
	}
	if want := apikey.Hash(got.Plaintext); got.Hash != want {
		t.Errorf("Hash = %q, want %q", got.Hash, want)
	}
	if !apikey.Verify(got.Plaintext, got.Hash) {
		t.Errorf("Verify rejected the key it was just generated with")
	}
	if !strings.HasPrefix(got.Plaintext, got.Display) {
		t.Errorf("Display %q is not a prefix of the plaintext %q", got.Display, got.Plaintext)
	}
}

// TestGeneratedKeysAreDistinct is a smoke test on the randomness: repeated
// mints must not collide. It would not detect a subtly biased source, but it
// does detect the failure that actually happens — a constant, a reused
// buffer, or a seeded PRNG someone swapped in.
//
// TestGeneratedKeysAreDistinct 是对随机性的冒烟测试：反复铸造不得碰撞。它发现不了
// 有细微偏差的随机源，但能发现真正会发生的那种故障——常量、被复用的缓冲区，或者
// 某人换上去的带种子 PRNG。
func TestGeneratedKeysAreDistinct(t *testing.T) {
	const mints = 1000
	seen := make(map[string]struct{}, mints)
	for i := range mints {
		got, err := apikey.Generate()
		if err != nil {
			t.Fatalf("Generate #%d: %v", i, err)
		}
		if _, dup := seen[got.Plaintext]; dup {
			t.Fatalf("Generate returned a duplicate key after %d mints", i)
		}
		seen[got.Plaintext] = struct{}{}
	}
}

// TestDisplayRevealsOnlyTheAgreedPrefix is the executable form of the rule
// that a listing may identify a key but never reconstruct it.
//
// TestDisplayRevealsOnlyTheAgreedPrefix 是那条规则的可执行版本：列表可以标识一个 key，
// 但绝不能重建它。
func TestDisplayRevealsOnlyTheAgreedPrefix(t *testing.T) {
	got, err := apikey.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	want := len(apikey.Prefix) + apikey.DisplayLen
	if len(got.Display) != want {
		t.Errorf("Display length = %d, want %d", len(got.Display), want)
	}
	if len(got.Display) >= len(got.Plaintext) {
		t.Fatalf("Display is not shorter than the key: %q vs %q", got.Display, got.Plaintext)
	}
	// The remaining secret must be substantial: this is the assertion that
	// fails if someone raises DisplayLen far enough to matter.
	//
	// 剩余的密文必须足够多：如果有人把 DisplayLen 调高到有影响的程度，正是这条断言
	// 会失败。
	if remaining := len(got.Plaintext) - len(got.Display); remaining < 30 {
		t.Errorf("only %d characters of the key remain hidden; DisplayLen is too high", remaining)
	}
}

func TestWellFormed(t *testing.T) {
	valid, err := apikey.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "a generated key", key: valid.Plaintext, want: true},
		{name: "empty", key: "", want: false},
		{name: "no prefix", key: strings.TrimPrefix(valid.Plaintext, apikey.Prefix), want: false},
		{name: "prefix only", key: apikey.Prefix, want: false},
		{name: "wrong prefix", key: "sk-" + strings.TrimPrefix(valid.Plaintext, apikey.Prefix), want: false},
		{name: "truncated secret", key: valid.Plaintext[:len(valid.Plaintext)-4], want: false},
		{name: "not base64", key: apikey.Prefix + strings.Repeat("!", 43), want: false},
		{name: "a bearer probe", key: "test", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apikey.WellFormed(tt.key); got != tt.want {
				t.Errorf("WellFormed(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestHashOf(t *testing.T) {
	valid, err := apikey.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{name: "a generated key hashes", key: valid.Plaintext, wantErr: false},
		{name: "a malformed key does not", key: "not-a-key", wantErr: true},
		{name: "empty does not", key: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := apikey.HashOf(tt.key)
			if (err != nil) != tt.wantErr {
				t.Fatalf("HashOf(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != apikey.Hash(tt.key) {
				t.Errorf("HashOf returned %q, want %q", got, apikey.Hash(tt.key))
			}
		})
	}
}

// TestHashIsStableAndIrreversibleInShape pins the stored form: 64 hex
// characters, and the same input always maps to the same output. The stored
// hash is a database key — changing this function silently invalidates every
// key ever issued.
//
// TestHashIsStableAndIrreversibleInShape 钉住存储形式：64 个 hex 字符，且相同输入
// 恒定映射到相同输出。存储的哈希是数据库里的检索键——悄悄改动这个函数会让此前签发的
// 每一个 key 失效。
func TestHashIsStableAndIrreversibleInShape(t *testing.T) {
	const key = apikey.Prefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	first := apikey.Hash(key)
	if len(first) != 64 {
		t.Errorf("Hash length = %d, want 64 hex characters of SHA-256", len(first))
	}
	if second := apikey.Hash(key); first != second {
		t.Errorf("Hash is not deterministic: %q then %q", first, second)
	}
	if strings.Contains(first, strings.TrimPrefix(key, apikey.Prefix)) {
		t.Errorf("the hash contains the secret verbatim")
	}
	if other := apikey.Hash(key + "x"); other == first {
		t.Errorf("two different keys hashed to the same value")
	}
}

func TestVerify(t *testing.T) {
	a, err := apikey.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b, err := apikey.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	tests := []struct {
		name string
		key  string
		hash string
		want bool
	}{
		{name: "matching key and hash", key: a.Plaintext, hash: a.Hash, want: true},
		{name: "another key's hash", key: a.Plaintext, hash: b.Hash, want: false},
		{name: "empty key", key: "", hash: a.Hash, want: false},
		{name: "empty hash", key: a.Plaintext, hash: "", want: false},
		{name: "the display form is not the key", key: a.Display, hash: a.Hash, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apikey.Verify(tt.key, tt.hash); got != tt.want {
				t.Errorf("Verify() = %v, want %v", got, tt.want)
			}
		})
	}
}
