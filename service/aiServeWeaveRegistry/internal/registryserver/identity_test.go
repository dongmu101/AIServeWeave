package registryserver_test

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/ca"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/registryserver"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

// registryFixture is a real Registry NodeIdentity/GatewayDirectory server,
// listening on loopback with real mTLS, so tests exercise the exact wire
// path an Agent uses rather than a hand-rolled substitute.
type registryFixture struct {
	addr   string
	ca     *ca.CA
	tokens *tokenstore.Store
}

func startRegistry(t *testing.T) *registryFixture {
	t.Helper()
	dir := t.TempDir()

	root, err := ca.LoadOrCreate(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatalf("ca.LoadOrCreate() error = %v", err)
	}
	tokens, err := tokenstore.Open(filepath.Join(dir, "tokens.json"))
	if err != nil {
		t.Fatalf("tokenstore.Open() error = %v", err)
	}
	server, err := registryserver.New(registryserver.Config{CA: root, Tokens: tokens})
	if err != nil {
		t.Fatalf("registryserver.New() error = %v", err)
	}

	certPEM, keyPEM, _, err := root.IssueServerCert([]string{"127.0.0.1"}, time.Now())
	if err != nil {
		t.Fatalf("IssueServerCert() error = %v", err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair() error = %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	creds := credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    root.Pool(),
	})
	grpcServer := grpc.NewServer(grpc.Creds(creds))
	tunnelv1.RegisterNodeIdentityServer(grpcServer, server)
	tunnelv1.RegisterGatewayDirectoryServer(grpcServer, server)
	go grpcServer.Serve(lis)
	t.Cleanup(grpcServer.GracefulStop)

	return &registryFixture{addr: lis.Addr().String(), ca: root, tokens: tokens}
}

// newAgentIdentityManager builds a tunnel.IdentityManager wired against the
// fixture Registry exactly the way service/aiServeWeaveAgent/main.go wires
// the real one — this is the actual client code an Agent runs, not a stand-in.
func newAgentIdentityManager(t *testing.T, f *registryFixture, bootstrapToken string) *tunnel.IdentityManager {
	t.Helper()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "bootstrap-token")
	if err := os.WriteFile(tokenFile, []byte(bootstrapToken), 0o600); err != nil {
		t.Fatalf("write bootstrap token file: %v", err)
	}

	connector, err := tunnel.NewGRPCRegistryConnector(f.addr, f.ca.Bundle())
	if err != nil {
		t.Fatalf("NewGRPCRegistryConnector() error = %v", err)
	}
	manager, err := tunnel.NewIdentityManager(tunnel.IdentityConfig{
		RegistryEndpoint:   f.addr,
		CertFile:           filepath.Join(dir, "node.crt"),
		KeyFile:            filepath.Join(dir, "node.key"),
		CAFile:             filepath.Join(dir, "ca.crt"),
		BootstrapTokenFile: tokenFile,
		AgentVersion:       "test",
	}, connector)
	if err != nil {
		t.Fatalf("NewIdentityManager() error = %v", err)
	}
	return manager
}

func TestRegisterIssuesAWorkingIdentityForARealAgentClient(t *testing.T) {
	f := startRegistry(t)
	tok, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}

	manager := newAgentIdentityManager(t, f, tok)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := manager.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if id.NodeID == "" {
		t.Error("Ensure() returned an identity with no node_id")
	}

	// The token is one-time: a second Agent registering with the same value
	// must be rejected even though it still has the token in memory.
	replay := newAgentIdentityManager(t, f, tok)
	if _, err := replay.Ensure(ctx); err == nil {
		t.Fatal("Ensure() with a spent bootstrap token = nil error, want a rejection")
	}
}

func TestRegisterRejectsAnInvalidToken(t *testing.T) {
	f := startRegistry(t)
	manager := newAgentIdentityManager(t, f, "not-a-real-token")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := manager.Ensure(ctx); err == nil {
		t.Fatal("Ensure() with an unminted token = nil error, want a rejection")
	}
}

func TestRenewCertificateRotatesAWorkingIdentity(t *testing.T) {
	f := startRegistry(t)
	tok, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	manager := newAgentIdentityManager(t, f, tok)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := manager.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	renewed, err := manager.Renew(ctx, id)
	if err != nil {
		t.Fatalf("Renew() error = %v", err)
	}
	if renewed.NodeID != id.NodeID {
		t.Errorf("Renew() node_id = %q, want %q", renewed.NodeID, id.NodeID)
	}
}
