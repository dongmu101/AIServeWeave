// Package remoteprovider is the contract for hosted provider runtimes the
// control plane provisions on a node from the Console: the bundle shape, the
// rules both sides validate it against, and the HPKE sealing that lets the
// bundle — API keys included — cross a Gateway and the tunnel without the
// Gateway being able to read or alter it.
//
// The control plane seals one Bundle per node to the public key of that node's
// current certificate, after checking the key against the fingerprint the
// Registry recorded for the node. The Agent opens it with its own private key.
// The HPKE info string binds the ciphertext to the node id and version, so a
// bundle cannot be replayed to another node or rolled back on the same one.
//
// remoteprovider 包是控制面经 Console 在节点上配置的托管供应商运行时的契约：打包形状、
// 两侧共同遵守的校验规则，以及让这份打包（含 API Key）穿过 Gateway 与隧道、而 Gateway
// 既读不到也改不了的 HPKE 封装。
//
// 控制面为每个节点把一份 Bundle 封装给该节点当前证书的公钥，封装前先用 Registry 记录的
// 节点指纹核对这把公钥。Agent 用自己的私钥打开。HPKE 的 info 把密文绑定到节点 id 与
// 版本，因此一份打包不能被重放到别的节点，也不能在同一节点上回滚。
package remoteprovider

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hpke"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
)

// Limits on one bundle. They bound what an Agent accepts and what the control
// plane stores per node.
//
// 单份打包的上限。它们约束 Agent 接受的内容，也约束控制面为每个节点保存的内容。
const (
	// IDPrefix starts every provisioned runtime id, keeping it apart from
	// runtimes declared in the Agent's own config.
	// IDPrefix 是每个下发运行时 id 的前缀，使其与 Agent 本地配置声明的运行时区分开。
	IDPrefix = "remote-"
	// MaxProviders is how many runtimes one node may be provisioned with.
	// MaxProviders 是一个节点最多可下发的运行时数。
	MaxProviders = 32
	// MaxIDBytes bounds a runtime id, prefix included.
	// MaxIDBytes 是运行时 id 的长度上限（含前缀）。
	MaxIDBytes = 64
	// MaxKeyBytes bounds one API key.
	// MaxKeyBytes 是单个 API Key 的长度上限。
	MaxKeyBytes = 16 << 10
	// MaxURLBytes bounds one base URL.
	// MaxURLBytes 是单个 base URL 的长度上限。
	MaxURLBytes = 2048
	// MaxModels bounds one runtime's declared model list.
	// MaxModels 是单个运行时声明的模型数上限。
	MaxModels = 256
	// MaxModelBytes bounds one model id.
	// MaxModelBytes 是单个模型 id 的长度上限。
	MaxModelBytes = 256
	// MaxBundleBytes bounds the encoded plaintext of one bundle.
	// MaxBundleBytes 是一份打包编码后明文的大小上限。
	MaxBundleBytes = 256 << 10
)

// infoLabel opens the HPKE info string; the node id and version follow it.
// infoLabel 是 HPKE info 字符串的开头，后接节点 id 与版本。
const infoLabel = "aisw-remote-providers/v1"

// ErrOpen is returned by Open when the ciphertext was not sealed to this key,
// node and version, or was altered on the way.
//
// ErrOpen 在密文不是封装给这把密钥、这个节点与版本，或在途中被改动时由 Open 返回。
var ErrOpen = errors.New("remoteprovider: bundle cannot be opened with this node key")

// Provider is one hosted provider runtime to register on the node. APIKey is
// the credential itself and must never be logged or returned to a browser.
//
// Provider 是要在节点上注册的一个托管供应商运行时。APIKey 就是凭据本身，绝不能写日志或
// 返回给浏览器。
type Provider struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	BaseURL string   `json:"base_url,omitempty"`
	APIKey  string   `json:"api_key"`
	Models  []string `json:"models,omitempty"`
}

// Bundle is the whole provisioned set for one node at one version. An empty
// Providers list removes every provisioned runtime.
//
// Bundle 是一个节点在一个版本下的全部下发内容。Providers 为空表示移除所有下发的运行时。
type Bundle struct {
	NodeID    string     `json:"node_id"`
	Version   uint64     `json:"version"`
	Providers []Provider `json:"providers"`
}

// ValidateProvider checks one provider against the rules both the control
// plane (on input) and the Agent (on receipt) enforce: an id with IDPrefix, a
// hosted kind, an https endpoint on a host that is not loopback, private or
// link-local, a one-line non-empty key and a bounded model list. A host name
// that resolves to a private address is not caught here.
//
// ValidateProvider 按控制面（输入时）与 Agent（接收时）共同执行的规则检查一个 provider：
// id 带 IDPrefix、种类为托管种类、端点为 https 且主机不是回环、私网或链路本地地址、Key
// 为单行非空、模型列表有界。解析到私网地址的主机名在这里查不出来。
func ValidateProvider(p Provider) error {
	if !ValidID(p.ID) {
		return fmt.Errorf("remoteprovider: id must be %q followed by 1-%d letters, digits, '.', '_' or '-'", IDPrefix, MaxIDBytes-len(IDPrefix))
	}
	kind := runtime.Kind(p.Kind)
	if !kind.IsHosted() {
		return fmt.Errorf("remoteprovider: kind %q is not a hosted provider", p.Kind)
	}
	if p.BaseURL == "" {
		if runtime.DefaultBaseURL(kind) == "" {
			return fmt.Errorf("remoteprovider: kind %q needs a base_url", p.Kind)
		}
	} else if err := validateBaseURL(p.BaseURL); err != nil {
		return err
	}
	if strings.TrimSpace(p.APIKey) == "" || strings.ContainsAny(p.APIKey, "\r\n") || p.APIKey != strings.TrimSpace(p.APIKey) {
		return errors.New("remoteprovider: api_key must be one non-empty line without surrounding spaces")
	}
	if len(p.APIKey) > MaxKeyBytes {
		return fmt.Errorf("remoteprovider: api_key exceeds %d bytes", MaxKeyBytes)
	}
	if len(p.Models) > MaxModels {
		return fmt.Errorf("remoteprovider: at most %d models", MaxModels)
	}
	seen := make(map[string]bool, len(p.Models))
	for _, m := range p.Models {
		if m == "" || len(m) > MaxModelBytes || strings.ContainsFunc(m, func(r rune) bool { return r <= ' ' || r == ',' }) {
			return errors.New("remoteprovider: model ids must be non-empty, without spaces or commas")
		}
		if seen[m] {
			return fmt.Errorf("remoteprovider: duplicate model %q", m)
		}
		seen[m] = true
	}
	return nil
}

// Validate checks every provider, the count and that ids are unique.
//
// Validate 检查每个 provider、数量以及 id 是否唯一。
func (b Bundle) Validate() error {
	if b.NodeID == "" {
		return errors.New("remoteprovider: node_id must not be empty")
	}
	if len(b.Providers) > MaxProviders {
		return fmt.Errorf("remoteprovider: at most %d providers per node", MaxProviders)
	}
	seen := make(map[string]bool, len(b.Providers))
	for _, p := range b.Providers {
		if err := ValidateProvider(p); err != nil {
			return err
		}
		if seen[p.ID] {
			return fmt.Errorf("remoteprovider: duplicate id %q", p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

// ValidID reports whether id is a well-formed provisioned runtime id.
//
// ValidID 报告 id 是否为格式正确的下发运行时 id。
func ValidID(id string) bool {
	rest, ok := strings.CutPrefix(id, IDPrefix)
	if !ok || rest == "" || len(id) > MaxIDBytes {
		return false
	}
	for _, r := range rest {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// validateBaseURL requires https, a host, no userinfo, query or fragment, and
// a host that is neither localhost nor a loopback, private, link-local or
// unspecified address literal.
//
// validateBaseURL 要求 https、有主机、不带 userinfo、query 或 fragment，且主机既不是
// localhost，也不是回环、私网、链路本地或未指定的地址字面量。
func validateBaseURL(raw string) error {
	if len(raw) > MaxURLBytes {
		return fmt.Errorf("remoteprovider: base_url exceeds %d bytes", MaxURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("remoteprovider: base_url must be an https URL with a host and no credentials, query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("remoteprovider: base_url must not point at this machine")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return errors.New("remoteprovider: base_url must not point at a loopback, private or link-local address")
		}
	}
	return nil
}

// Seal encrypts b to pub with HPKE DHKEM(P-256, HKDF-SHA256), HKDF-SHA256
// and AES-256-GCM, bound to b.NodeID and b.Version. b is validated first.
//
// Seal 用 HPKE DHKEM(P-256, HKDF-SHA256)、HKDF-SHA256 与 AES-256-GCM 把 b 加密给
// pub，并绑定 b.NodeID 与 b.Version。先校验 b。
func Seal(pub *ecdh.PublicKey, b Bundle) ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	if pub == nil || pub.Curve() != ecdh.P256() {
		return nil, errors.New("remoteprovider: a P-256 node key is required")
	}
	plaintext, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("remoteprovider: encode bundle: %w", err)
	}
	if len(plaintext) > MaxBundleBytes {
		return nil, fmt.Errorf("remoteprovider: bundle exceeds %d bytes", MaxBundleBytes)
	}
	pk, err := hpke.NewDHKEMPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("remoteprovider: node key: %w", err)
	}
	return hpke.Seal(pk, hpke.HKDFSHA256(), hpke.AES256GCM(), info(b.NodeID, b.Version), plaintext)
}

// Open decrypts sealed with priv, expecting it to be addressed to nodeID at
// version, and validates the result. Any failure to decrypt is ErrOpen.
//
// Open 用 priv 解密 sealed，要求它发给 nodeID 且版本为 version，并校验结果。任何解密
// 失败都是 ErrOpen。
func Open(priv *ecdh.PrivateKey, nodeID string, version uint64, sealed []byte) (Bundle, error) {
	if priv == nil || len(sealed) > MaxBundleBytes+1024 {
		return Bundle{}, ErrOpen
	}
	k, err := hpke.NewDHKEMPrivateKey(priv)
	if err != nil {
		return Bundle{}, ErrOpen
	}
	plaintext, err := hpke.Open(k, hpke.HKDFSHA256(), hpke.AES256GCM(), info(nodeID, version), sealed)
	if err != nil {
		return Bundle{}, ErrOpen
	}
	var b Bundle
	if err := json.Unmarshal(plaintext, &b); err != nil {
		return Bundle{}, fmt.Errorf("remoteprovider: decode bundle: %w", err)
	}
	if b.NodeID != nodeID || b.Version != version {
		return Bundle{}, ErrOpen
	}
	if err := b.Validate(); err != nil {
		return Bundle{}, err
	}
	return b, nil
}

// info is the HPKE info string: infoLabel, a zero byte, the node id, a zero
// byte and the big-endian version.
//
// info 是 HPKE info 字符串：infoLabel、一个零字节、节点 id、一个零字节与大端序版本号。
func info(nodeID string, version uint64) []byte {
	out := make([]byte, 0, len(infoLabel)+len(nodeID)+10)
	out = append(out, infoLabel...)
	out = append(out, 0)
	out = append(out, nodeID...)
	out = append(out, 0)
	return binary.BigEndian.AppendUint64(out, version)
}

// ParsePublicKey parses a DER SubjectPublicKeyInfo holding a P-256 ECDSA key,
// the key type every node certificate carries, into its ECDH form.
//
// ParsePublicKey 把一个装有 P-256 ECDSA 密钥的 DER SubjectPublicKeyInfo（每张节点证书
// 携带的密钥类型）解析成 ECDH 形式。
func ParsePublicKey(spki []byte) (*ecdh.PublicKey, error) {
	key, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, fmt.Errorf("remoteprovider: parse node key: %w", err)
	}
	ec, ok := key.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("remoteprovider: node key is not P-256 ECDSA")
	}
	return ec.ECDH()
}

// Fingerprint is the hex SHA-256 of spki, the same value the Registry records
// for a node's key (identitystore.Fingerprint).
//
// Fingerprint 是 spki 的十六进制 SHA-256，与 Registry 为节点密钥记录的值相同
// （identitystore.Fingerprint）。
func Fingerprint(spki []byte) string {
	sum := sha256.Sum256(spki)
	return hex.EncodeToString(sum[:])
}

// PrivateKeyFromPEM parses the node key file (PKCS#8 or SEC 1 "EC PRIVATE
// KEY") into its ECDH form. The error never contains key material.
//
// PrivateKeyFromPEM 把节点密钥文件（PKCS#8 或 SEC 1 "EC PRIVATE KEY"）解析成 ECDH 形式。
// 错误文本从不包含密钥内容。
func PrivateKeyFromPEM(data []byte) (*ecdh.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("remoteprovider: node key file holds no PEM block")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, errors.New("remoteprovider: node key file has an unsupported PEM type")
	}
	if err != nil {
		return nil, errors.New("remoteprovider: node key cannot be parsed")
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("remoteprovider: node key is not P-256 ECDSA")
	}
	return ec.ECDH()
}

// Outcome is the Agent's verdict on the last bundle it received.
//
// Outcome 是 Agent 对最近一次收到的打包的处理结论。
type Outcome int

// Outcome values, mirroring tunnel.v1.RemoteProvidersOutcome.
// Outcome 取值，与 tunnel.v1.RemoteProvidersOutcome 对应。
const (
	OutcomeNone Outcome = iota
	OutcomeApplied
	OutcomeDisabled
	OutcomeOpenFailed
	OutcomeInvalid
	OutcomeStale
	OutcomeStoreFailed
)

// String renders o for APIs and logs.
// String 为 API 与日志渲染 o。
func (o Outcome) String() string {
	switch o {
	case OutcomeApplied:
		return "applied"
	case OutcomeDisabled:
		return "disabled"
	case OutcomeOpenFailed:
		return "open_failed"
	case OutcomeInvalid:
		return "invalid"
	case OutcomeStale:
		return "stale"
	case OutcomeStoreFailed:
		return "store_failed"
	}
	return "none"
}

// RuntimeState is one provisioned runtime's state on the Agent.
//
// RuntimeState 是一个下发运行时在 Agent 上的状态。
type RuntimeState int

// RuntimeState values, mirroring tunnel.v1.RemoteRuntimeState.
// RuntimeState 取值，与 tunnel.v1.RemoteRuntimeState 对应。
const (
	RuntimeUnknown RuntimeState = iota
	RuntimeActive
	RuntimeConflict
	RuntimeFailed
)

// String renders s for APIs and logs.
// String 为 API 与日志渲染 s。
func (s RuntimeState) String() string {
	switch s {
	case RuntimeActive:
		return "active"
	case RuntimeConflict:
		return "conflict"
	case RuntimeFailed:
		return "failed"
	}
	return "unknown"
}

// RuntimeResult is one provisioned runtime's id and state.
//
// RuntimeResult 是一个下发运行时的 id 与状态。
type RuntimeResult struct {
	ID    string
	State RuntimeState
}

// Report is what the Agent tells the Gateway about remote providers. It holds
// no endpoint, key or error text.
//
// Report 是 Agent 告诉 Gateway 的远程供应商情况。它不含端点、Key 或错误文本。
type Report struct {
	AppliedVersion   uint64
	AttemptedVersion uint64
	Outcome          Outcome
	Runtimes         []RuntimeResult
	UpdatedAt        time.Time
}
