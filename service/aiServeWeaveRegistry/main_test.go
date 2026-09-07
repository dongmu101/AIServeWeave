package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/ca"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/identitystore"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/registryserver"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

// TestMain asserts no test in this package leaks a goroutine.
//
// TestMain 断言本包没有测试泄漏协程。
func TestMain(m *testing.M) {
	before := goruntime.NumGoroutine()
	code := m.Run()
	if code == 0 && !goroutineCountSettles(before) {
		os.Stderr.WriteString("leaked goroutines detected after tests completed\n")
		code = 1
	}
	os.Exit(code)
}

func goroutineCountSettles(baseline int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if goruntime.NumGoroutine() <= baseline {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRunIssueServerCert covers the operator command the Gateway's tunnel
// listener depends on. What matters is not that a file appeared but what is in
// it: an Agent verifies that listener against the Registry CA and rejects a
// certificate that is not ServerAuth, or that does not name the host it
// dialled.
//
// TestRunIssueServerCert 覆盖 Gateway 隧道监听器所依赖的那条运维命令。要紧的不是
// 「有文件出现了」，而是文件里是什么：Agent 用 Registry CA 验证那个监听器，会拒绝
// 一张不是 ServerAuth 的证书，或者一张没有点名它所拨向主机的证书。
func TestRunIssueServerCert(t *testing.T) {
	dir := t.TempDir()
	root, err := ca.LoadOrCreate(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatalf("ca.LoadOrCreate: %v", err)
	}
	outDir := filepath.Join(dir, "certs")

	if err := runIssueServerCert(root, outDir, []string{"gateway", "127.0.0.1"}); err != nil {
		t.Fatalf("runIssueServerCert: %v", err)
	}

	certPEM, err := os.ReadFile(filepath.Join(outDir, "server-cert.pem"))
	if err != nil {
		t.Fatalf("reading the certificate: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("the certificate file is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing the certificate: %v", err)
	}

	// ServerAuth, not ClientAuth: the node certificates this same CA signs
	// over RPC are ClientAuth, and handing one of those to a listener would
	// fail the handshake in a way that reads like a network problem.
	//
	// 是 ServerAuth 而不是 ClientAuth：同一个 CA 通过 RPC 签发的节点证书是
	// ClientAuth，把那种证书交给监听器，握手失败的样子会像是一次网络问题。
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("ExtKeyUsage = %v, want exactly ServerAuth", cert.ExtKeyUsage)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "gateway" {
		t.Errorf("DNSNames = %v, want [gateway]", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 1 || cert.IPAddresses[0].String() != "127.0.0.1" {
		t.Errorf("IPAddresses = %v, want [127.0.0.1]", cert.IPAddresses)
	}

	// It has to chain to this Registry's own CA, because that is the only root
	// an Agent trusts.
	//
	// 它必须挂在本 Registry 自己的 CA 下，因为那是 Agent 唯一信任的根。
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     root.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("the certificate does not verify against the Registry CA: %v", err)
	}

	// A listener's key is as good as the listener to anyone who reads it.
	//
	// 对任何读到它的人来说，一个监听器的私钥就等同于那个监听器本身。
	info, err := os.Stat(filepath.Join(outDir, "server-key.pem"))
	if err != nil {
		t.Fatalf("stat on the key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key mode = %04o, want 0600", perm)
	}
}

func TestRunIssueServerCertRejects(t *testing.T) {
	dir := t.TempDir()
	root, err := ca.LoadOrCreate(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatalf("ca.LoadOrCreate: %v", err)
	}

	tests := []struct {
		name    string
		outDir  string
		hosts   []string
		wantErr string
	}{
		{name: "no out dir", outDir: "", hosts: []string{"gateway"}, wantErr: "-out-dir"},
		{name: "no hosts", outDir: filepath.Join(dir, "a"), hosts: nil, wantErr: "-tls-host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runIssueServerCert(root, tt.outDir, tt.hosts)
			if err == nil {
				t.Fatalf("runIssueServerCert() error = nil, want one mentioning %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to name %q", err, tt.wantErr)
			}
		})
	}
}

// TestRunTokenAdminClientMintsAndRevokesOverRPC covers the -mint-token/
// -revoke-token CLI paths end to end against a real server: what STATUS.md's
// S02 changed them from (direct, racy access to tokens.json) into (gRPC
// clients of TokenAdmin), so the thing worth testing is that they actually
// reach a running server rather than that some function returns nil.
//
// TestRunTokenAdminClientMintsAndRevokesOverRPC 端到端覆盖 -mint-token/
// -revoke-token 这两条 CLI 路径，针对一个真实的 server：STATUS.md 的 S02 把
// 它们从「直接、有竞态地读写 tokens.json」改成了「TokenAdmin 的 gRPC 客户端」，
// 值得测的是它们确实打到了一个正在运行的 server，而不是某个函数返回了 nil。
func TestRunTokenAdminClientMintsAndRevokesOverRPC(t *testing.T) {
	dir := t.TempDir()
	root, err := ca.LoadOrCreate(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatalf("ca.LoadOrCreate() error = %v", err)
	}
	tokens, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("tokenstore.Open() error = %v", err)
	}
	identities, err := identitystore.Open(filepath.Join(dir, "identities.json"))
	if err != nil {
		t.Fatalf("identitystore.Open() error = %v", err)
	}

	const adminToken = "test-admin-token-0123456789abcdef"
	adminTokenFile := filepath.Join(dir, "admin-token")
	if err := os.WriteFile(adminTokenFile, []byte(adminToken), 0o600); err != nil {
		t.Fatalf("write admin token file: %v", err)
	}

	server, err := registryserver.New(registryserver.Config{
		CA: root, Tokens: tokens, Identities: identities, AdminToken: adminToken,
	})
	if err != nil {
		t.Fatalf("registryserver.New() error = %v", err)
	}
	creds, err := serverCredentials(root, "", "", "127.0.0.1:0", "")
	if err != nil {
		t.Fatalf("serverCredentials() error = %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	grpcServer := grpc.NewServer(grpc.Creds(creds))
	tunnelv1.RegisterTokenAdminServer(grpcServer, server)
	go grpcServer.Serve(lis)
	t.Cleanup(grpcServer.GracefulStop)

	var out bytes.Buffer
	if err := runTokenAdminClient(&out, dir, lis.Addr().String(), "", adminTokenFile, tokenAdminAction{
		mint: true, ttl: 15 * time.Minute,
	}); err != nil {
		t.Fatalf("runTokenAdminClient(mint) error = %v", err)
	}
	token := strings.TrimSpace(out.String())
	if token == "" {
		t.Fatal("runTokenAdminClient(mint) printed an empty token")
	}

	if _, err := tokens.Consume(token, time.Now()); err != nil {
		t.Fatalf("Consume() on the RPC-minted token error = %v, want nil", err)
	}

	// A second token, this time revoked instead of consumed: the running
	// server's own in-memory store must observe the revocation immediately,
	// which is the entire reason revocation goes over RPC and not through a
	// second process editing tokens.json.
	tok2, err := tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	out.Reset()
	if err := runTokenAdminClient(&out, dir, lis.Addr().String(), "", adminTokenFile, tokenAdminAction{
		revoke: tok2,
	}); err != nil {
		t.Fatalf("runTokenAdminClient(revoke) error = %v", err)
	}
	if _, err := tokens.Consume(tok2, time.Now()); !errors.Is(err, tokenstore.ErrInvalidToken) {
		t.Fatalf("Consume() on the RPC-revoked token error = %v, want ErrInvalidToken", err)
	}
}
