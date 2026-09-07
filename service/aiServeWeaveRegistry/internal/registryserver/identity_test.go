package registryserver_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/ca"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/identitystore"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/registryserver"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

// registryFixture is a real Registry NodeIdentity/GatewayDirectory/TokenAdmin
// server, listening on loopback with real mTLS, so tests exercise the exact
// wire path an Agent uses rather than a hand-rolled substitute.
type registryFixture struct {
	addr       string
	ca         *ca.CA
	tokens     *tokenstore.Store
	identities *identitystore.Store
	adminToken string
}

// testAdminToken is the shared secret every fixture's TokenAdmin service is
// guarded by, long enough to look like a real one without a dedicated
// generator that would only ever return this same value in tests.
const testAdminToken = "test-admin-token-0123456789abcdef"

// testGatewayToken is the shared secret startRegistryWithGatewayToken guards
// GatewayDirectory.Join with (STATUS.md's S03).
const testGatewayToken = "test-gateway-token-0123456789abcdef"

func startRegistry(t *testing.T) *registryFixture {
	t.Helper()
	return startRegistryWithGatewayToken(t, "")
}

// startRegistryWithGatewayToken is startRegistry with GatewayDirectory.Join
// guarded by gatewayToken (STATUS.md's S03) instead of left open. Passing ""
// is startRegistry's own default.
func startRegistryWithGatewayToken(t *testing.T, gatewayToken string) *registryFixture {
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
	identities, err := identitystore.Open(filepath.Join(dir, "identities.json"))
	if err != nil {
		t.Fatalf("identitystore.Open() error = %v", err)
	}
	server, err := registryserver.New(registryserver.Config{
		CA: root, Tokens: tokens, Identities: identities,
		AdminToken: testAdminToken, GatewayToken: gatewayToken,
	})
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
	tunnelv1.RegisterTokenAdminServer(grpcServer, server)
	go grpcServer.Serve(lis)
	t.Cleanup(grpcServer.GracefulStop)

	return &registryFixture{addr: lis.Addr().String(), ca: root, tokens: tokens, identities: identities, adminToken: testAdminToken}
}

// approve pre-approves nodeID directly against the fixture's ledger
// (STATUS.md's P01), the way an operator would via TokenAdmin.ApproveNode,
// so a test exercising something other than the approval gate itself does
// not have to detour through the gRPC client to clear it.
func (f *registryFixture) approve(t *testing.T, nodeID string) {
	t.Helper()
	if err := f.identities.Approve(nodeID, time.Now()); err != nil {
		t.Fatalf("identities.Approve(%q) error = %v", nodeID, err)
	}
}

// tokenAdminClient dials the fixture Registry with no client certificate and
// returns a TokenAdmin client plus a context already carrying f's admin
// token, so a test only has to supply the request.
func tokenAdminClient(t *testing.T, f *registryFixture) (tunnelv1.TokenAdminClient, context.Context) {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(f.ca.Bundle()) {
		t.Fatal("AppendCertsFromPEM() found no usable certificate")
	}
	conn, err := grpc.NewClient(f.addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
	})))
	if err != nil {
		t.Fatalf("grpc.NewClient() error = %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+f.adminToken)
	return tunnelv1.NewTokenAdminClient(conn), ctx
}

// newAgentIdentityManager builds a tunnel.IdentityManager wired against the
// fixture Registry exactly the way service/aiServeWeaveAgent/main.go wires
// the real one — this is the actual client code an Agent runs, not a stand-in.
//
// nodeID is required (unlike the pre-P01 tests this helper once served): an
// unbound bootstrap token can no longer self-assign one (STATUS.md's P01),
// so every caller must supply a fixed node_id and, unless the test means to
// exercise the pending-approval gate itself, approve it first via
// registryFixture.approve.
func newAgentIdentityManager(t *testing.T, f *registryFixture, nodeID, bootstrapToken string) *tunnel.IdentityManager {
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
		NodeID:             nodeID,
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
	f.approve(t, "node-a")
	tok, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}

	manager := newAgentIdentityManager(t, f, "node-a", tok)

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
	replay := newAgentIdentityManager(t, f, "node-a", tok)
	if _, err := replay.Ensure(ctx); err == nil {
		t.Fatal("Ensure() with a spent bootstrap token = nil error, want a rejection")
	}
}

func TestRegisterRejectsAnInvalidToken(t *testing.T) {
	f := startRegistry(t)
	manager := newAgentIdentityManager(t, f, "node-a", "not-a-real-token")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := manager.Ensure(ctx); err == nil {
		t.Fatal("Ensure() with an unminted token = nil error, want a rejection")
	}
}

func TestRenewCertificateRotatesAWorkingIdentity(t *testing.T) {
	f := startRegistry(t)
	f.approve(t, "node-a")
	tok, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	manager := newAgentIdentityManager(t, f, "node-a", tok)

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

// rawIdentityClient dials the fixture Registry with no client certificate —
// the bootstrap connection Register itself uses — bypassing
// tunnel.IdentityManager so a test can send a RegisterRequest with an
// explicit node_id and a CSR of its own choosing (STATUS.md's S01 needs to
// exercise combinations IdentityManager's own bootstrap flow does not
// produce, such as two different keys under the same node_id).
func rawIdentityClient(t *testing.T, f *registryFixture) tunnelv1.NodeIdentityClient {
	t.Helper()
	connector, err := tunnel.NewGRPCRegistryConnector(f.addr, f.ca.Bundle())
	if err != nil {
		t.Fatalf("NewGRPCRegistryConnector() error = %v", err)
	}
	client, closer, err := connector.Connect(context.Background(), nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() { closer.Close() })
	return client
}

// newCSR builds a PKCS#10 request signed by a freshly generated key — a
// minimal stand-in for tunnel.createCSR (unexported, and this package cannot
// reach it), sufficient for exercising the Registry's own validation rather
// than the Agent's. It carries no subject or SAN: Register takes node_id
// from RegisterRequest, not from the CSR, so the fingerprint these tests key
// on is entirely a property of the generated key, not of anything in here.
func newCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}, key)
	if err != nil {
		t.Fatalf("CreateCertificateRequest() error = %v", err)
	}
	return der
}

func TestRegisterAllowsARepeatedRegistrationWithTheSameKeyUnderOneNodeID(t *testing.T) {
	f := startRegistry(t)
	f.approve(t, "node-fixed")
	client := rawIdentityClient(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	csr := newCSR(t)

	tok1, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if _, err := client.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-fixed", Csr: csr, BootstrapToken: tok1,
	}); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}

	// A node that lost its issued certificate before persisting it — but
	// kept its private key — presents the very same CSR again under a fresh
	// bootstrap token. This must succeed: it is indistinguishable from a
	// harmless reconnect.
	tok2, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if _, err := client.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-fixed", Csr: csr, BootstrapToken: tok2,
	}); err != nil {
		t.Fatalf("second Register() with the same key error = %v, want success (idempotent reconnect)", err)
	}
}

func TestRegisterRejectsANodeIDReappearingUnderADifferentKey(t *testing.T) {
	f := startRegistry(t)
	f.approve(t, "node-fixed")
	client := rawIdentityClient(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tok1, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if _, err := client.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-fixed", Csr: newCSR(t), BootstrapToken: tok1,
	}); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}

	// A second registration for the same node_id, but with a different key
	// (a distinct CSR), is either an authorized reinstall or an attempt to
	// register under a name that is not the caller's — S01 does not try to
	// tell them apart automatically and refuses both.
	tok2, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	_, err = client.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-fixed", Csr: newCSR(t), BootstrapToken: tok2,
	})
	if err == nil {
		t.Fatal("Register() reappearing under a different key = nil error, want AlreadyExists")
	}
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("Register() reappearing under a different key code = %v, want AlreadyExists", status.Code(err))
	}
}

func TestConcurrentRegistrationForOneNodeIDWithDifferentKeysAllowsExactlyOne(t *testing.T) {
	f := startRegistry(t)
	f.approve(t, "node-fixed")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const attempts = 10
	tokens := make([]string, attempts)
	for i := range attempts {
		tok, err := f.tokens.Mint(15*time.Minute, time.Now())
		if err != nil {
			t.Fatalf("Mint() error = %v", err)
		}
		tokens[i] = tok
	}

	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client := rawIdentityClient(t, f)
			_, err := client.Register(ctx, &tunnelv1.RegisterRequest{
				NodeId: "node-fixed", Csr: newCSR(t), BootstrapToken: tokens[i],
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	succeeded, conflicted := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case status.Code(err) == codes.AlreadyExists:
			conflicted++
		default:
			t.Errorf("Register() unexpected error = %v", err)
		}
	}
	if succeeded != 1 {
		t.Errorf("succeeded registrations = %d, want exactly 1", succeeded)
	}
	if conflicted != attempts-1 {
		t.Errorf("AlreadyExists registrations = %d, want %d", conflicted, attempts-1)
	}
}

func TestMintTokenRejectsAMissingOrWrongAdminToken(t *testing.T) {
	f := startRegistry(t)
	client, ctx := tokenAdminClient(t, f)

	if _, err := client.MintToken(context.Background(), &tunnelv1.MintTokenRequest{Ttl: durationpb.New(15 * time.Minute)}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("MintToken() with no authorization metadata code = %v, want Unauthenticated", status.Code(err))
	}

	wrongCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer wrong-token")
	if _, err := client.MintToken(wrongCtx, &tunnelv1.MintTokenRequest{Ttl: durationpb.New(15 * time.Minute)}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("MintToken() with the wrong admin token code = %v, want PermissionDenied", status.Code(err))
	}

	if _, err := client.MintToken(ctx, &tunnelv1.MintTokenRequest{Ttl: durationpb.New(15 * time.Minute)}); err != nil {
		t.Errorf("MintToken() with the correct admin token error = %v, want nil", err)
	}
}

func TestMintTokenThenRegisterConsumesTheMintedToken(t *testing.T) {
	f := startRegistry(t)
	client, ctx := tokenAdminClient(t, f)

	resp, err := client.MintToken(ctx, &tunnelv1.MintTokenRequest{Ttl: durationpb.New(15 * time.Minute)})
	if err != nil {
		t.Fatalf("MintToken() error = %v", err)
	}
	if resp.GetToken() == "" {
		t.Fatal("MintToken() returned an empty token")
	}

	f.approve(t, "node-a")
	manager := newAgentIdentityManager(t, f, "node-a", resp.GetToken())
	registerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := manager.Ensure(registerCtx); err != nil {
		t.Fatalf("Ensure() with an RPC-minted token error = %v", err)
	}
}

func TestRevokeTokenRejectsAMissingOrWrongAdminToken(t *testing.T) {
	f := startRegistry(t)
	tok, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	client, ctx := tokenAdminClient(t, f)

	if _, err := client.RevokeToken(context.Background(), &tunnelv1.RevokeTokenRequest{Token: tok}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("RevokeToken() with no authorization metadata code = %v, want Unauthenticated", status.Code(err))
	}
	if _, err := client.RevokeToken(ctx, &tunnelv1.RevokeTokenRequest{Token: tok}); err != nil {
		t.Errorf("RevokeToken() with the correct admin token error = %v, want nil", err)
	}
}

func TestRevokeTokenPreventsRegistration(t *testing.T) {
	f := startRegistry(t)
	tok, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	client, ctx := tokenAdminClient(t, f)
	if _, err := client.RevokeToken(ctx, &tunnelv1.RevokeTokenRequest{Token: tok}); err != nil {
		t.Fatalf("RevokeToken() error = %v", err)
	}

	f.approve(t, "node-a")
	manager := newAgentIdentityManager(t, f, "node-a", tok)
	registerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := manager.Ensure(registerCtx); err == nil {
		t.Fatal("Ensure() with a revoked token = nil error, want a rejection")
	}
}

func TestNodeBoundTokenAuthorizesAReinstallWithoutAManualLedgerEdit(t *testing.T) {
	f := startRegistry(t)
	adminClient, adminCtx := tokenAdminClient(t, f)
	identityClient := rawIdentityClient(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tok1, err := adminClient.MintToken(adminCtx, &tunnelv1.MintTokenRequest{Ttl: durationpb.New(15 * time.Minute), NodeId: "node-reinstalled"})
	if err != nil {
		t.Fatalf("MintToken() error = %v", err)
	}
	if _, err := identityClient.Register(ctx, &tunnelv1.RegisterRequest{
		Csr: newCSR(t), BootstrapToken: tok1.GetToken(),
	}); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}

	// A plain, unbound token presenting a different key under the same
	// node_id is refused (TestRegisterRejectsANodeIDReappearingUnderADifferentKey).
	// A node_id-bound token authorizes exactly that, without an operator
	// having to hand-edit the ledger first.
	tok2, err := adminClient.MintToken(adminCtx, &tunnelv1.MintTokenRequest{Ttl: durationpb.New(15 * time.Minute), NodeId: "node-reinstalled"})
	if err != nil {
		t.Fatalf("MintToken() error = %v", err)
	}
	resp, err := identityClient.Register(ctx, &tunnelv1.RegisterRequest{
		Csr: newCSR(t), BootstrapToken: tok2.GetToken(),
	})
	if err != nil {
		t.Fatalf("second Register() with a node-bound token error = %v, want success (authorized reinstall)", err)
	}
	if resp.GetNodeId() != "node-reinstalled" {
		t.Errorf("Register() node_id = %q, want %q", resp.GetNodeId(), "node-reinstalled")
	}
}

func TestRegisterRejectsANodeBoundTokenUsedForADifferentNodeID(t *testing.T) {
	f := startRegistry(t)
	adminClient, adminCtx := tokenAdminClient(t, f)
	identityClient := rawIdentityClient(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tok, err := adminClient.MintToken(adminCtx, &tunnelv1.MintTokenRequest{Ttl: durationpb.New(15 * time.Minute), NodeId: "node-a"})
	if err != nil {
		t.Fatalf("MintToken() error = %v", err)
	}
	_, err = identityClient.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-b", Csr: newCSR(t), BootstrapToken: tok.GetToken(),
	})
	if err == nil {
		t.Fatal("Register() with a node_id that conflicts with the token's binding = nil error, want a rejection")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("Register() with a mismatched node_id code = %v, want PermissionDenied", status.Code(err))
	}
}
