package agentenrollment

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

type fixedClock struct {
	runtime.Clock
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

type fakeIssuer struct {
	nodes  []string
	ttl    time.Duration
	expiry time.Time
	err    error
}

func (i *fakeIssuer) MintNodeToken(_ context.Context, node string, ttl time.Duration) (string, time.Time, error) {
	i.nodes = append(i.nodes, node)
	i.ttl = ttl
	return "test-private-bootstrap-token", i.expiry, i.err
}

func fixture(t *testing.T) (*Service, *fakeIssuer, []byte) {
	t.Helper()
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, ca, 0600); err != nil {
		t.Fatal(err)
	}
	issuer := &fakeIssuer{expiry: now.Add(time.Hour)}
	return New(issuer, path, fixedClock{now: now}), issuer, ca
}

// TestCreateBindsFreshNodeAndPackagesOnlyEnrollmentFiles checks node isolation and archive contents.
//
// TestCreateBindsFreshNodeAndPackagesOnlyEnrollmentFiles 检查节点隔离和压缩包内容。
func TestCreateBindsFreshNodeAndPackagesOnlyEnrollmentFiles(t *testing.T) {
	s, issuer, ca := fixture(t)
	seen := make(map[string]bool)
	for range 2 {
		bundle, err := s.Create(t.Context(), "registry.example.com:9443", "agent.example.com:8443", time.Hour)
		if err != nil {
			t.Fatalf("Create error = %v, want nil", err)
		}
		if len(bundle.NodeID) != 37 || seen[bundle.NodeID] || issuer.nodes[len(issuer.nodes)-1] != bundle.NodeID || issuer.ttl != time.Hour {
			t.Fatalf("node binding = %q / %v, ttl = %v, want distinct new bound node and 1h", bundle.NodeID, issuer.nodes, issuer.ttl)
		}
		seen[bundle.NodeID] = true
		archive, err := zip.NewReader(bytes.NewReader(bundle.Archive), int64(len(bundle.Archive)))
		if err != nil {
			t.Fatal(err)
		}
		files := make(map[string][]byte)
		for _, file := range archive.File {
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(reader)
			_ = reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			files[file.Name] = content
			if file.Name == "bootstrap-token" && file.Mode().Perm() != 0600 {
				t.Errorf("token mode = %v, want 0600", file.Mode())
			}
		}
		if len(files) != 4 || !bytes.Equal(files["ca.pem"], ca) || string(files["bootstrap-token"]) != "test-private-bootstrap-token\n" || !bytes.Contains(files["README.txt"], []byte(bundle.NodeID)) || bytes.Contains(files["README.txt"], []byte("test-private-bootstrap-token")) {
			t.Fatalf("archive filenames/content do not match the four enrollment files")
		}
		var connection map[string]any
		if err := json.Unmarshal(files["connection.json"], &connection); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(ca)
		if connection["node_id"] != bundle.NodeID || connection["registry_addr"] != "registry.example.com:9443" || connection["gateway_addr"] != "agent.example.com:8443" || connection["ca_sha256"] != hex.EncodeToString(digest[:]) {
			t.Errorf("connection = %v, want bound ID, external endpoints and CA digest", connection)
		}
	}
}

// TestCARejectsUnsafeFilesBeforeMint rejects unsafe certificate sources before issuing credentials.
//
// TestCARejectsUnsafeFilesBeforeMint 在签发凭据前拒绝不安全的证书来源。
func TestCARejectsUnsafeFilesBeforeMint(t *testing.T) {
	tests := []struct {
		name      string
		transform func([]byte) []byte
	}{
		{"private key appended", func(ca []byte) []byte {
			return append(ca, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret")})...)
		}},
		{"private key first", func(ca []byte) []byte {
			return append(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret")}), ca...)
		}},
		{"oversized", func([]byte) []byte { return bytes.Repeat([]byte("x"), 65537) }},
		{"empty", func([]byte) []byte { return nil }},
		{"garbage prefix", func(ca []byte) []byte { return append([]byte("secret\n"), ca...) }},
		{"invalid DER", func([]byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("bad")}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, issuer, ca := fixture(t)
			if err := os.WriteFile(s.caFile, tt.transform(ca), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Create(t.Context(), "registry.example.com:9443", "agent.example.com:8443", time.Hour); err == nil {
				t.Fatal("Create error = nil, want unsafe CA refusal")
			}
			if len(issuer.nodes) != 0 {
				t.Fatalf("mint calls = %d, want 0", len(issuer.nodes))
			}
		})
	}
	t.Run("expired", func(t *testing.T) {
		s, issuer, _ := fixture(t)
		s.clock = fixedClock{now: issuer.expiry.Add(48 * time.Hour)}
		if _, err := s.CA(); err == nil {
			t.Fatal("CA error = nil, want expired refusal")
		}
	})
}

// TestCreateValidatesAddressesAndTTLBeforeMint checks address and lifetime boundaries.
//
// TestCreateValidatesAddressesAndTTLBeforeMint 检查地址与有效期边界。
func TestCreateValidatesAddressesAndTTLBeforeMint(t *testing.T) {
	tests := []struct {
		name, address string
		ttl           time.Duration
	}{
		{"URL", "https://example.com:9443", time.Hour},
		{"missing port", "example.com", time.Hour},
		{"shell expansion", "$(id):9443", time.Hour},
		{"newline", "example.com\n:9443", time.Hour},
		{"invalid port", "example.com:65536", time.Hour},
		{"zero port", "example.com:0", time.Hour},
		{"short TTL", "example.com:9443", 14 * time.Minute},
		{"long TTL", "example.com:9443", 25 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, issuer, _ := fixture(t)
			_, err := s.Create(t.Context(), tt.address, "agent.example.com:8443", tt.ttl)
			if !errors.Is(err, ErrInvalidInput) || len(issuer.nodes) != 0 {
				t.Fatalf("error=%v calls=%d, want invalid input and zero mints", err, len(issuer.nodes))
			}
		})
	}
	for _, address := range []string{"example.com:9443", "127.0.0.1:9443", "[2001:db8::1]:9443"} {
		if !validAddress(address) {
			t.Errorf("validAddress(%q)=false, want true", address)
		}
	}
}

// TestMintFailureDoesNotExposeUpstreamCredentials checks issuer error sanitization.
//
// TestMintFailureDoesNotExposeUpstreamCredentials 检查签发错误脱敏。
func TestMintFailureDoesNotExposeUpstreamCredentials(t *testing.T) {
	s, issuer, _ := fixture(t)
	issuer.err = errors.New("upstream-secret-detail")
	bundle, err := s.Create(t.Context(), "registry.example.com:9443", "agent.example.com:8443", time.Hour)
	if err == nil || strings.Contains(err.Error(), "upstream-secret-detail") || len(bundle.Archive) != 0 {
		t.Fatalf("Create error = %v, want sanitized failure and no archive", err)
	}
}
