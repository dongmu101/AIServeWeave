// Package agentenrollment prepares bounded, node-specific enrollment downloads.
//
// Package agentenrollment 生成大小有界、专属节点的接入下载材料。
package agentenrollment

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
)

// ErrInvalidInput indicates an invalid address or expiration interval.
//
// ErrInvalidInput 表示地址或有效期无效。
var ErrInvalidInput = errors.New("agentenrollment: invalid input")

// Issuer issues credentials without exposing the Registry administrator token.
//
// Issuer 签发凭据而不暴露 Registry 管理令牌。
type Issuer interface {
	MintNodeToken(context.Context, string, time.Duration) (string, time.Time, error)
}

// Service creates enrollment archives using only the configured public CA file.
//
// Service 仅使用配置指定的公开 CA 文件生成接入压缩包。
type Service struct {
	issuer Issuer
	caFile string
	clock  runtime.Clock
}

// New constructs a download service; it never reads the CA private key.
//
// New 创建下载服务；它永远不读取 CA 私钥。
func New(issuer Issuer, caFile string, clock runtime.Clock) *Service {
	return &Service{issuer: issuer, caFile: caFile, clock: clock}
}

// Certificate contains normalized public certificates and their file checksum.
//
// Certificate 包含规范化公开证书及其文件校验和。
type Certificate struct {
	PEM    string
	SHA256 string
}

// CA reads at most 64 KiB, rejects non-certificate PEM blocks, and validates CAs.
//
// CA 最多读取 64 KiB，拒绝非证书 PEM 块，并校验 CA 证书。
func (s *Service) CA() (Certificate, error) {
	f, err := os.Open(s.caFile)
	if err != nil {
		return Certificate{}, errors.New("agentenrollment: CA unavailable")
	}
	defer f.Close()
	const limit = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) > limit {
		return Certificate{}, errors.New("agentenrollment: invalid CA file")
	}
	var normalized bytes.Buffer
	now := s.clock.Now()
	for len(bytes.TrimSpace(raw)) > 0 {
		raw = bytes.TrimSpace(raw)
		if !bytes.HasPrefix(raw, []byte("-----BEGIN CERTIFICATE-----")) {
			return Certificate{}, errors.New("agentenrollment: certificate-only CA required")
		}
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return Certificate{}, errors.New("agentenrollment: invalid CA PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
			return Certificate{}, errors.New("agentenrollment: invalid CA certificate")
		}
		_ = pem.Encode(&normalized, block)
		raw = rest
	}
	if normalized.Len() == 0 {
		return Certificate{}, errors.New("agentenrollment: empty CA")
	}
	digest := sha256.Sum256(normalized.Bytes())
	return Certificate{PEM: normalized.String(), SHA256: hex.EncodeToString(digest[:])}, nil
}

// Bundle is an archive returned once after a successful mint operation.
//
// Bundle 是成功签发后仅返回一次的接入压缩包。
type Bundle struct {
	NodeID    string
	ExpiresAt time.Time
	Filename  string
	Archive   []byte
}

// Create allocates a random new node ID before issuing its bound token.
// Addresses are download content, never server-side network destinations.
//
// Create 分配随机新节点 ID 后签发绑定令牌。地址仅写入下载内容，绝不作为服务端网络目标。
func (s *Service) Create(ctx context.Context, registry, gateway string, ttl time.Duration) (Bundle, error) {
	if !validAddress(registry) || !validAddress(gateway) || ttl < 15*time.Minute || ttl > 24*time.Hour {
		return Bundle{}, ErrInvalidInput
	}
	ca, err := s.CA()
	if err != nil {
		return Bundle{}, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Bundle{}, errors.New("agentenrollment: identity generation failed")
	}
	nodeID := "node_" + hex.EncodeToString(id[:])
	token, expiry, err := s.issuer.MintNodeToken(ctx, nodeID, ttl)
	if err != nil {
		return Bundle{}, errors.New("agentenrollment: token issuance unavailable")
	}
	if token == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") || !expiry.After(s.clock.Now()) {
		return Bundle{}, errors.New("agentenrollment: invalid issued token")
	}
	connection, _ := json.MarshalIndent(struct {
		NodeID    string    `json:"node_id"`
		Registry  string    `json:"registry_addr"`
		Gateway   string    `json:"gateway_addr"`
		ExpiresAt time.Time `json:"expires_at"`
		CASHA256  string    `json:"ca_sha256"`
	}{nodeID, registry, gateway, expiry, ca.SHA256}, "", "  ")
	files := []struct {
		name, data string
		mode       os.FileMode
	}{
		{"ca.pem", ca.PEM, 0644},
		{"bootstrap-token", token + "\n", 0600},
		{"connection.json", string(connection) + "\n", 0644},
		{"README.txt", instructions(nodeID, registry, gateway, expiry, ca.SHA256), 0644},
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		header.SetMode(file.mode)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return Bundle{}, errors.New("agentenrollment: archive creation failed")
		}
		if _, err := io.WriteString(entry, file.data); err != nil {
			return Bundle{}, errors.New("agentenrollment: archive creation failed")
		}
	}
	if err := writer.Close(); err != nil {
		return Bundle{}, errors.New("agentenrollment: archive creation failed")
	}
	return Bundle{NodeID: nodeID, ExpiresAt: expiry, Filename: "aiserveweave-enrollment-" + nodeID + ".zip", Archive: archive.Bytes()}, nil
}

func validAddress(address string) bool {
	if len(address) > 253 {
		return false
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, char := range host {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '.') {
			return false
		}
	}
	return !strings.HasPrefix(host, ".") && !strings.HasSuffix(host, ".") && !strings.Contains(host, "..")
}

func instructions(node, registry, gateway string, expiry time.Time, checksum string) string {
	return fmt.Sprintf(`AIServeWeave Agent 接入包 / Agent enrollment

节点 / Node: %s
注册截止时间 / Register before (UTC): %s
CA 文件 SHA256 / CA file SHA256: %s

本包只适用于一个新节点。令牌只用一次，请通过可信渠道交付；不要上传到仓库或公开链接。
This bundle authorizes one new node. Deliver privately; never publish the token.
过期或已使用后须向管理员申请新包。节点私钥由 Agent 在本机生成。
Request a new bundle if expired or spent. Agent generates its private key locally.

1. 从平台管理员指定的可信发布地址下载与 Registry/Gateway 同一版本的 Agent 包，
   选择本机系统和架构；解压后将本包四个文件放入该目录。
   Obtain the matching platform release for your OS/architecture from your administrator,
   extract it and copy these four files into its directory. Binary and installers are separate.
2. 确认到 %s 和 %s 的出站连接可达，无需开放节点入站端口。
   Ensure outbound connectivity to these endpoints; no inbound Agent port is needed.
3. 将下方 VERSION、USER、BINARY 换成实际值，校验 SHA256SUMS.txt 后安装。
   Replace VERSION, USER and BINARY below with the actual release, user and binary filename.

Ubuntu（管理员安装，指定普通用户运行 / sudo install, ordinary service user）：
  chmod 600 bootstrap-token
  sudo bash agent.sh install --version VERSION --user USER --file ./BINARY --checksums ./SHA256SUMS.txt --node-id %s --registry %s --gateway %s --ca-file ./ca.pem --token-file ./bootstrap-token

macOS（Apple Silicon，当前用户，无 sudo / current user, no sudo）：
  chmod 600 bootstrap-token
  bash agent.sh install --version VERSION --file ./BINARY --checksums ./SHA256SUMS.txt --node-id %s --registry %s --gateway %s --ca-file ./ca.pem --token-file ./bootstrap-token

Windows（原生 amd64/arm64，当前普通用户 / native, current ordinary user）：
  .\agent.ps1 install -Version VERSION -File .\BINARY -Checksums .\SHA256SUMS.txt -NodeId %s -Registry %s -Gateway %s -CAFile .\ca.pem -TokenFile .\bootstrap-token

4. 本机打开 http://127.0.0.1:8899 配置推理后端或 CLI。Ubuntu 远程操作时先执行：
   Open the local settings page to configure backends/CLI. For a remote Ubuntu host:
     ssh -L 8899:127.0.0.1:8899 USER@AGENT_HOST
   CLI 须以运行 Agent 的同一用户登录。安装服务不等于模型已接入，须由管理员验收请求。
   Sign into CLI as the Agent user. Service startup does not prove inference is ready.
5. 注册成功后删除本目录 bootstrap-token 和原 ZIP，保护保留的节点身份与 CLI 登录。
   After registration, delete the extracted token and original ZIP; protect node identity and CLI login.

支持目标与实机验收以发行包内 README/acceptance 为准；不得关闭 TLS 校验。
Use the release documentation for platform acceptance status. Never disable TLS verification.
`, node, expiry.UTC().Format(time.RFC3339), checksum, registry, gateway,
		node, registry, gateway, node, registry, gateway, node, registry, gateway)
}
