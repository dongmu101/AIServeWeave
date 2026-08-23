package tunnel_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/tunnel"
)

// identityNow is the wall-clock instant every identity test starts from. It is
// fixed so certificate validity windows and renewal thresholds are exact
// rather than dependent on when the suite runs.
var identityNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// certLifetime is the validity window the fake Registry issues, matching the
// 30d the README specifies for a node certificate.
const certLifetime = 30 * 24 * time.Hour

// -----------------------------------------------------------------------
// Fakes
// -----------------------------------------------------------------------

// fixedClock is a runtime.Clock whose time only moves when a test moves it.
// Identity code only reads Now(), so no timer plumbing is needed here.
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFixedClock(now time.Time) *fixedClock { return &fixedClock{now: now} }

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	ch := make(chan time.Time, 1)
	return ch, func() bool { return true }
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// testCA is a throwaway certificate authority standing in for the Registry's
// CA. It never leaves the test process.
type testCA struct {
	key    *ecdsa.PrivateKey
	cert   *x509.Certificate
	pem    []byte
	serial int64
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "aiserveweave-test-ca"},
		NotBefore:             identityNow.Add(-time.Hour),
		NotAfter:              identityNow.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("self-sign CA: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	return &testCA{
		key:    key,
		cert:   cert,
		pem:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		serial: 1,
	}
}

// issueNode signs csrDER into a node certificate naming nodeID through the
// canonical URI SAN, exactly as the Registry is specified to.
func (ca *testCA) issueNode(t *testing.T, csrDER []byte, nodeID string, notBefore, notAfter time.Time) []byte {
	t.Helper()

	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatalf("CSR signature invalid: %v", err)
	}
	return ca.sign(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: nodeID},
		URIs:        []*url.URL{tunnel.NodeURI(nodeID)},
		NotBefore:   notBefore,
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}, csr.PublicKey)
}

// issueServer mints a server certificate for host, used by the mTLS handshake
// tests that stand in for two different Gateway replicas.
func (ca *testCA) issueServer(t *testing.T, host string) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	certPEM := ca.sign(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: host},
		DNSNames:    []string{host},
		NotBefore:   identityNow.Add(-time.Hour),
		NotAfter:    identityNow.Add(certLifetime),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, &key.PublicKey)

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("encode server key: %v", err)
	}
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatalf("build server key pair: %v", err)
	}
	return pair
}

func (ca *testCA) sign(t *testing.T, tmpl *x509.Certificate, pub any) []byte {
	t.Helper()

	ca.serial++
	tmpl.SerialNumber = big.NewInt(ca.serial)
	tmpl.BasicConstraintsValid = true
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	if err != nil {
		t.Fatalf("sign certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// fakeRegistry is a scriptable NodeIdentity service. It records every request
// so tests can assert what the Agent sent — in particular that a CSR, and
// never a private key, crosses the wire.
type fakeRegistry struct {
	t     *testing.T
	ca    *testCA
	clock *fixedClock

	nodeID       string    // identity the Registry assigns; "" echoes the request
	notAfterLie  time.Time // when set, reported in the response instead of the truth
	omitCABundle bool      // when set, the response omits the CA bundle

	registerErr error
	renewErr    error

	registerReqs []*tunnelv1.RegisterRequest
	renewReqs    []*tunnelv1.RenewRequest
}

func (r *fakeRegistry) Register(_ context.Context, in *tunnelv1.RegisterRequest, _ ...grpc.CallOption) (*tunnelv1.RegisterResponse, error) {
	r.registerReqs = append(r.registerReqs, in)
	if r.registerErr != nil {
		return nil, r.registerErr
	}
	nodeID := r.nodeID
	if nodeID == "" {
		nodeID = in.GetNodeId()
	}
	certPEM, notAfter := r.issue(in.GetCsr(), nodeID)
	resp := &tunnelv1.RegisterResponse{
		NodeId:         nodeID,
		CertificatePem: certPEM,
		NotAfter:       timestamppb.New(notAfter),
	}
	if !r.omitCABundle {
		resp.CaBundlePem = r.ca.pem
	}
	if !r.notAfterLie.IsZero() {
		resp.NotAfter = timestamppb.New(r.notAfterLie)
	}
	return resp, nil
}

func (r *fakeRegistry) RenewCertificate(_ context.Context, in *tunnelv1.RenewRequest, _ ...grpc.CallOption) (*tunnelv1.RenewResponse, error) {
	r.renewReqs = append(r.renewReqs, in)
	if r.renewErr != nil {
		return nil, r.renewErr
	}
	certPEM, notAfter := r.issue(in.GetCsr(), in.GetNodeId())
	resp := &tunnelv1.RenewResponse{
		CertificatePem: certPEM,
		NotAfter:       timestamppb.New(notAfter),
	}
	if !r.omitCABundle {
		resp.CaBundlePem = r.ca.pem
	}
	return resp, nil
}

func (r *fakeRegistry) issue(csr []byte, nodeID string) ([]byte, time.Time) {
	notBefore := r.clock.Now().Add(-time.Minute)
	notAfter := r.clock.Now().Add(certLifetime)
	return r.ca.issueNode(r.t, csr, nodeID, notBefore, notAfter), notAfter
}

// fakeConnector hands out the fake Registry and records whether each call was
// made with a client certificate, which is how the tests assert that Register
// is the only unauthenticated method.
type fakeConnector struct {
	registry   *fakeRegistry
	connectErr error

	presented []*tunnel.Identity // one entry per Connect; nil means bootstrap
	closes    int
}

func (c *fakeConnector) Connect(_ context.Context, id *tunnel.Identity) (tunnelv1.NodeIdentityClient, io.Closer, error) {
	if c.connectErr != nil {
		return nil, nil, c.connectErr
	}
	c.presented = append(c.presented, id)
	return c.registry, closerFunc(func() error { c.closes++; return nil }), nil
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// -----------------------------------------------------------------------
// Fixture
// -----------------------------------------------------------------------

// identityFixture is one temp-directory installation of the Agent identity.
type identityFixture struct {
	t         *testing.T
	dir       string
	ca        *testCA
	clock     *fixedClock
	registry  *fakeRegistry
	connector *fakeConnector
	cfg       tunnel.IdentityConfig
}

func newIdentityFixture(t *testing.T, nodeID string) *identityFixture {
	t.Helper()

	dir := t.TempDir()
	ca := newTestCA(t)
	clock := newFixedClock(identityNow)
	registry := &fakeRegistry{t: t, ca: ca, clock: clock}
	connector := &fakeConnector{registry: registry}

	return &identityFixture{
		t:         t,
		dir:       dir,
		ca:        ca,
		clock:     clock,
		registry:  registry,
		connector: connector,
		cfg: tunnel.IdentityConfig{
			NodeID:             nodeID,
			RegistryEndpoint:   "registry.example.com:8444",
			CertFile:           filepath.Join(dir, "node.crt"),
			KeyFile:            filepath.Join(dir, "node.key"),
			CAFile:             filepath.Join(dir, "ca.crt"),
			BootstrapTokenFile: filepath.Join(dir, "bootstrap.token"),
			AgentVersion:       "test",
			Clock:              clock,
			Logger:             slog.New(slog.DiscardHandler),
		},
	}
}

func (f *identityFixture) writeToken(token string) {
	f.t.Helper()
	if err := os.WriteFile(f.cfg.BootstrapTokenFile, []byte(token+"\n"), 0o600); err != nil {
		f.t.Fatalf("write bootstrap token: %v", err)
	}
}

func (f *identityFixture) manager() *tunnel.IdentityManager {
	f.t.Helper()
	m, err := tunnel.NewIdentityManager(f.cfg, f.connector)
	if err != nil {
		f.t.Fatalf("NewIdentityManager: %v", err)
	}
	return m
}

// bootstrapped runs a successful registration and returns the resulting
// manager and identity, which most tests need as their starting point.
func (f *identityFixture) bootstrapped() (*tunnel.IdentityManager, *tunnel.Identity) {
	f.t.Helper()
	f.writeToken("one-time-token")
	m := f.manager()
	id, err := m.Ensure(context.Background())
	if err != nil {
		f.t.Fatalf("Ensure (bootstrap): %v", err)
	}
	return m, id
}

func (f *identityFixture) mode(path string) os.FileMode {
	f.t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		f.t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func (f *identityFixture) read(path string) []byte {
	f.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// -----------------------------------------------------------------------
// Bootstrap
// -----------------------------------------------------------------------

func TestIdentityBootstrapStoresCertificateAndConsumesToken(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	_, id := f.bootstrapped()

	if id.NodeID != "mac-mini-01" {
		t.Errorf("node id = %q, want %q", id.NodeID, "mac-mini-01")
	}
	wantNotAfter := identityNow.Add(certLifetime)
	if !id.NotAfter.Equal(wantNotAfter) {
		t.Errorf("not after = %s, want %s", id.NotAfter, wantNotAfter)
	}

	if _, err := os.Stat(f.cfg.BootstrapTokenFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("bootstrap token still present after registration: stat err = %v, want os.ErrNotExist", err)
	}
	for _, p := range []string{f.cfg.CertFile, f.cfg.KeyFile} {
		if got := f.mode(p); got != 0o600 {
			t.Errorf("%s mode = %#o, want %#o", p, got, 0o600)
		}
	}
	if got := f.mode(f.cfg.CAFile); got != 0o644 {
		t.Errorf("%s mode = %#o, want %#o", f.cfg.CAFile, got, 0o644)
	}

	if n := len(f.registry.registerReqs); n != 1 {
		t.Fatalf("Register calls = %d, want 1", n)
	}
	req := f.registry.registerReqs[0]
	if req.GetBootstrapToken() != "one-time-token" {
		t.Errorf("bootstrap token = %q, want %q (trailing newline must be trimmed)", req.GetBootstrapToken(), "one-time-token")
	}
	if len(f.connector.presented) != 1 || f.connector.presented[0] != nil {
		t.Errorf("Register presented an identity = %v, want nil (Register is the only method without a client certificate)", f.connector.presented)
	}
	if f.connector.closes != 1 {
		t.Errorf("Registry connections closed = %d, want 1", f.connector.closes)
	}
}

func TestIdentityBootstrapSendsCSRAndKeepsPrivateKeyLocal(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	f.bootstrapped()

	req := f.registry.registerReqs[0]
	csr, err := x509.ParseCertificateRequest(req.GetCsr())
	if err != nil {
		t.Fatalf("Register carried something that is not a PKCS#10 CSR: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Errorf("CSR signature invalid: %v", err)
	}
	if got, want := csr.Subject.CommonName, "mac-mini-01"; got != want {
		t.Errorf("CSR common name = %q, want %q", got, want)
	}
	if len(csr.URIs) != 1 || csr.URIs[0].String() != tunnel.NodeURI("mac-mini-01").String() {
		t.Errorf("CSR URI SANs = %v, want [%s]", csr.URIs, tunnel.NodeURI("mac-mini-01"))
	}

	// The private key must exist only on disk: no field of the request may
	// contain PEM-encoded key material.
	keyPEM := f.read(f.cfg.KeyFile)
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatalf("key file does not hold a PKCS#8 private key, got block %v", block)
	}
	wire := string(req.GetCsr()) + req.GetNodeId() + req.GetBootstrapToken() + req.GetAgentVersion()
	if strings.Contains(wire, "PRIVATE KEY") || strings.Contains(wire, string(block.Bytes)) {
		t.Error("the Register request carries private key material; the key must never leave the node")
	}
}

func TestIdentityBootstrapRejectsRegistryIdentityMismatch(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	f.registry.nodeID = "somebody-else"
	f.writeToken("one-time-token")

	_, err := f.manager().Ensure(context.Background())
	if !tunnel.IsFatal(err) {
		t.Fatalf("Ensure error = %v, want a fatal identity error", err)
	}
	if _, statErr := os.Stat(f.cfg.CertFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("a rejected certificate was still written: stat err = %v, want os.ErrNotExist", statErr)
	}
}

func TestIdentityBootstrapRejectsInconsistentNotAfter(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	f.registry.notAfterLie = identityNow.Add(365 * 24 * time.Hour)
	f.writeToken("one-time-token")

	_, err := f.manager().Ensure(context.Background())
	if !tunnel.IsFatal(err) {
		t.Fatalf("Ensure error = %v, want a fatal identity error when not_after disagrees with the certificate", err)
	}
}

func TestIdentityBootstrapTokenProblems(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *identityFixture)
	}{
		{
			name:  "no token file",
			setup: func(f *identityFixture) {},
		},
		{
			name: "empty token file",
			setup: func(f *identityFixture) {
				f.writeToken("   ")
			},
		},
		{
			name: "token readable beyond its owner",
			setup: func(f *identityFixture) {
				f.writeToken("one-time-token")
				if err := os.Chmod(f.cfg.BootstrapTokenFile, 0o644); err != nil {
					f.t.Fatalf("chmod token: %v", err)
				}
			},
		},
		{
			name: "bootstrap_token_file not configured",
			setup: func(f *identityFixture) {
				f.cfg.BootstrapTokenFile = ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newIdentityFixture(t, "mac-mini-01")
			tt.setup(f)

			_, err := f.manager().Ensure(context.Background())
			if !tunnel.IsFatal(err) {
				t.Fatalf("Ensure error = %v, want a fatal identity error", err)
			}
			if n := len(f.registry.registerReqs); n != 0 {
				t.Errorf("Register calls = %d, want 0: a bad token must not reach the Registry", n)
			}
		})
	}
}

func TestIdentityRegistryErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantFatal bool
		wantCode  runtime.ErrorCode
	}{
		{"spent or forged token", status.Error(codes.Unauthenticated, "token spent"), true, runtime.ErrorUnauthorized},
		{"tenant mismatch", status.Error(codes.PermissionDenied, "wrong tenant"), true, runtime.ErrorUnauthorized},
		{"node id rejected", status.Error(codes.InvalidArgument, "bad node id"), true, runtime.ErrorInvalidConfig},
		{"registry down", status.Error(codes.Unavailable, "connection refused"), false, runtime.ErrorConnection},
		{"registry slow", status.Error(codes.DeadlineExceeded, "timeout"), false, runtime.ErrorTimeout},
		{"registry busy", status.Error(codes.ResourceExhausted, "slow down"), false, runtime.ErrorRateLimited},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newIdentityFixture(t, "mac-mini-01")
			f.registry.registerErr = tt.err
			f.writeToken("one-time-token")

			_, err := f.manager().Ensure(context.Background())
			if err == nil {
				t.Fatal("Ensure error = nil, want an error")
			}
			if got := tunnel.IsFatal(err); got != tt.wantFatal {
				t.Errorf("fatal = %t, want %t (err = %v)", got, tt.wantFatal, err)
			}
			var rerr *runtime.RuntimeError
			if !errors.As(err, &rerr) {
				t.Fatalf("error type = %T, want *runtime.RuntimeError", err)
			}
			if rerr.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", rerr.Code, tt.wantCode)
			}
			if rerr.Retryable == tt.wantFatal {
				t.Errorf("retryable = %t, want %t", rerr.Retryable, !tt.wantFatal)
			}
			// A token file kept for a retryable failure is what lets the
			// Agent register once the Registry is back.
			_, statErr := os.Stat(f.cfg.BootstrapTokenFile)
			if statErr != nil {
				t.Errorf("bootstrap token removed after a failed registration: %v", statErr)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Loading a stored identity
// -----------------------------------------------------------------------

func TestIdentityLoadReusesStoredCertificate(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	_, first := f.bootstrapped()

	second, err := f.manager().Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure (load): %v", err)
	}
	if second.NodeID != first.NodeID || !second.NotAfter.Equal(first.NotAfter) {
		t.Errorf("reloaded identity = (%s, %s), want (%s, %s)", second.NodeID, second.NotAfter, first.NodeID, first.NotAfter)
	}
	if n := len(f.registry.registerReqs); n != 1 {
		t.Errorf("Register calls = %d, want 1: a stored certificate must not trigger re-registration", n)
	}
}

func TestIdentityRejectsLooseFilePermissions(t *testing.T) {
	tests := []struct {
		name string
		file func(f *identityFixture) string
		mode os.FileMode
	}{
		{"certificate group readable", func(f *identityFixture) string { return f.cfg.CertFile }, 0o640},
		{"certificate world readable", func(f *identityFixture) string { return f.cfg.CertFile }, 0o644},
		{"key group readable", func(f *identityFixture) string { return f.cfg.KeyFile }, 0o640},
		{"key world writable", func(f *identityFixture) string { return f.cfg.KeyFile }, 0o666},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newIdentityFixture(t, "mac-mini-01")
			f.bootstrapped()
			path := tt.file(f)
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatalf("chmod %s: %v", path, err)
			}

			_, err := f.manager().Ensure(context.Background())
			if !tunnel.IsFatal(err) {
				t.Fatalf("Ensure error = %v, want a fatal identity error for mode %#o", err, tt.mode)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name the offending file %s", err, path)
			}
		})
	}
}

func TestIdentityRejectsNodeIDMismatch(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	f.bootstrapped()

	// Same files, different configured node_id: the Agent must refuse at
	// startup rather than be rejected by a Gateway at Hello time.
	f.cfg.NodeID = "mac-mini-02"
	_, err := f.manager().Ensure(context.Background())
	if !tunnel.IsFatal(err) {
		t.Fatalf("Ensure error = %v, want a fatal identity error", err)
	}
	if !strings.Contains(err.Error(), "mac-mini-01") || !strings.Contains(err.Error(), "mac-mini-02") {
		t.Errorf("error %q should name both the certificate identity and the configured one", err)
	}
}

func TestIdentityRejectsIncompleteInstallation(t *testing.T) {
	tests := []struct {
		name   string
		remove func(f *identityFixture) string
	}{
		{"key missing", func(f *identityFixture) string { return f.cfg.KeyFile }},
		{"certificate missing", func(f *identityFixture) string { return f.cfg.CertFile }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newIdentityFixture(t, "mac-mini-01")
			f.bootstrapped()
			f.writeToken("another-token") // must not be used to paper over the gap
			if err := os.Remove(tt.remove(f)); err != nil {
				t.Fatalf("remove: %v", err)
			}

			_, err := f.manager().Ensure(context.Background())
			if !tunnel.IsFatal(err) {
				t.Fatalf("Ensure error = %v, want a fatal identity error", err)
			}
			if n := len(f.registry.registerReqs); n != 1 {
				t.Errorf("Register calls = %d, want 1: a half-installed identity must not silently re-register", n)
			}
		})
	}
}

func TestIdentityRejectsCertificateFromAnotherCA(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	f.bootstrapped()

	// Replace the trusted bundle with an unrelated CA: the stored leaf no
	// longer chains, which must be fatal rather than silently trusted.
	other := newTestCA(t)
	if err := os.WriteFile(f.cfg.CAFile, other.pem, 0o644); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	_, err := f.manager().Ensure(context.Background())
	if !tunnel.IsFatal(err) {
		t.Fatalf("Ensure error = %v, want a fatal identity error", err)
	}
}

func TestIdentityExpiredCertificate(t *testing.T) {
	tests := []struct {
		name          string
		writeToken    bool
		wantRegisters int
		wantErr       bool
	}{
		{name: "no token means operator intervention", writeToken: false, wantRegisters: 1, wantErr: true},
		{name: "token allows re-registration", writeToken: true, wantRegisters: 2, wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newIdentityFixture(t, "mac-mini-01")
			f.bootstrapped()
			f.clock.advance(certLifetime + time.Hour)
			if tt.writeToken {
				f.writeToken("replacement-token")
			}

			id, err := f.manager().Ensure(context.Background())
			switch {
			case tt.wantErr && !tunnel.IsFatal(err):
				t.Fatalf("Ensure error = %v, want a fatal identity error", err)
			case !tt.wantErr && err != nil:
				t.Fatalf("Ensure: %v", err)
			case !tt.wantErr && !id.NotAfter.After(f.clock.Now()):
				t.Errorf("re-registered certificate expires at %s, which is not in the future of %s", id.NotAfter, f.clock.Now())
			}
			if n := len(f.registry.registerReqs); n != tt.wantRegisters {
				t.Errorf("Register calls = %d, want %d", n, tt.wantRegisters)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Renewal
// -----------------------------------------------------------------------

func TestIdentityRenewalThreshold(t *testing.T) {
	tests := []struct {
		name      string
		elapsed   time.Duration
		wantRenew bool
	}{
		{"fresh certificate", 0, false},
		{"half spent", certLifetime / 2, false},
		{"just above one third remaining", certLifetime*2/3 - time.Hour, false},
		{"just below one third remaining", certLifetime*2/3 + time.Hour, true},
		{"almost expired", certLifetime - time.Hour, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newIdentityFixture(t, "mac-mini-01")
			m, id := f.bootstrapped()
			f.clock.advance(tt.elapsed)

			if got := m.NeedsRenewal(id); got != tt.wantRenew {
				t.Fatalf("NeedsRenewal = %t, want %t (%s elapsed of %s)", got, tt.wantRenew, tt.elapsed, certLifetime)
			}

			next, err := m.Ensure(context.Background())
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			gotRenewed := len(f.registry.renewReqs) == 1
			if gotRenewed != tt.wantRenew {
				t.Errorf("RenewCertificate calls = %d, want renewal = %t", len(f.registry.renewReqs), tt.wantRenew)
			}
			if tt.wantRenew && !next.NotAfter.After(id.NotAfter) {
				t.Errorf("renewed not_after = %s, want later than %s", next.NotAfter, id.NotAfter)
			}
		})
	}
}

func TestIdentityRenewalRotatesKeyAndPresentsCurrentCertificate(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	m, id := f.bootstrapped()
	keyBefore := f.read(f.cfg.KeyFile)
	certBefore := f.read(f.cfg.CertFile)

	f.clock.advance(certLifetime - time.Hour)
	next, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if got := string(f.read(f.cfg.KeyFile)); got == string(keyBefore) {
		t.Error("the private key was not rotated; a renewal must generate a fresh key")
	}
	if got := string(f.read(f.cfg.CertFile)); got == string(certBefore) {
		t.Error("the stored certificate was not replaced")
	}
	if next.NodeID != id.NodeID {
		t.Errorf("node id after renewal = %q, want %q: a rotation must not change the node identity", next.NodeID, id.NodeID)
	}
	if n := len(f.connector.presented); n != 2 || f.connector.presented[1] == nil {
		t.Fatalf("RenewCertificate was called without a client certificate; it requires the current identity (presented = %v)", f.connector.presented)
	}
	if got := f.connector.presented[1].NodeID; got != id.NodeID {
		t.Errorf("renewal presented identity %q, want %q", got, id.NodeID)
	}
	if f.connector.closes != 2 {
		t.Errorf("Registry connections closed = %d, want 2", f.connector.closes)
	}

	// The rotated identity must be loadable on the next start.
	reloaded, err := f.manager().Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure after rotation: %v", err)
	}
	if !reloaded.NotAfter.Equal(next.NotAfter) {
		t.Errorf("reloaded not_after = %s, want %s", reloaded.NotAfter, next.NotAfter)
	}
}

func TestIdentityRenewalWithoutCABundleKeepsTheCurrentOne(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	m, id := f.bootstrapped()
	f.registry.omitCABundle = true // a Registry that only re-issues the leaf
	f.clock.advance(certLifetime - time.Hour)

	next, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got, want := string(next.CABundlePEM()), string(id.CABundlePEM()); got != want {
		t.Error("the CA bundle changed although the renewal response carried none")
	}
	if got, want := string(f.read(f.cfg.CAFile)), string(id.CABundlePEM()); got != want {
		t.Error("the stored CA bundle was overwritten with an empty one")
	}
}

func TestIdentityBootstrapWithoutCABundleIsFatal(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	f.registry.omitCABundle = true
	f.writeToken("one-time-token")

	// At registration there is no previous bundle to fall back on, so a
	// response without one leaves the Agent unable to verify any replica.
	_, err := f.manager().Ensure(context.Background())
	if !tunnel.IsFatal(err) {
		t.Fatalf("Ensure error = %v, want a fatal identity error", err)
	}
}

func TestIdentityRenewalFailure(t *testing.T) {
	tests := []struct {
		name        string
		renewErr    error
		wantErr     bool
		wantKeepOld bool
	}{
		{
			name:        "registry unreachable keeps serving with the current certificate",
			renewErr:    status.Error(codes.Unavailable, "connection refused"),
			wantErr:     false,
			wantKeepOld: true,
		},
		{
			name:     "revoked identity is fatal",
			renewErr: status.Error(codes.PermissionDenied, "revoked"),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newIdentityFixture(t, "mac-mini-01")
			m, id := f.bootstrapped()
			f.registry.renewErr = tt.renewErr
			f.clock.advance(certLifetime - time.Hour)

			got, err := m.Ensure(context.Background())
			switch {
			case tt.wantErr:
				if !tunnel.IsFatal(err) {
					t.Fatalf("Ensure error = %v, want a fatal identity error", err)
				}
			case err != nil:
				t.Fatalf("Ensure: %v", err)
			case tt.wantKeepOld && !got.NotAfter.Equal(id.NotAfter):
				t.Errorf("not_after = %s, want the current certificate's %s", got.NotAfter, id.NotAfter)
			}
		})
	}
}

// -----------------------------------------------------------------------
// TLS
// -----------------------------------------------------------------------

func TestIdentityTLSConfig(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	_, id := f.bootstrapped()

	cfg := id.TLSConfig()
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want %#x (TLS 1.3)", cfg.MinVersion, tls.VersionTLS13)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates = %d, want 1: the tunnel is always mutually authenticated", len(cfg.Certificates))
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs = nil, want the Registry CA: the Agent must verify every Gateway replica")
	}
	if cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify = true, want false")
	}

	// Each caller gets its own copy, so one replica's dial options cannot
	// leak into another's.
	other := id.TLSConfig()
	if other == cfg {
		t.Error("TLSConfig returned the same pointer twice; each connection needs its own copy")
	}
	other.ServerName = "gw-1.example.com"
	if cfg.ServerName != "" {
		t.Errorf("mutating one TLSConfig changed another: ServerName = %q", cfg.ServerName)
	}
}

func TestIdentityOneCertificateServesEveryReplica(t *testing.T) {
	f := newIdentityFixture(t, "mac-mini-01")
	_, id := f.bootstrapped()

	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(f.ca.pem) {
		t.Fatal("cannot build the client CA pool")
	}

	// Two replicas with different names and different server certificates,
	// both reached with the one node identity.
	for _, host := range []string{"gw-1.example.com", "gw-2.example.com"} {
		t.Run(host, func(t *testing.T) {
			serverCfg := &tls.Config{
				MinVersion:   tls.VersionTLS13,
				Certificates: []tls.Certificate{f.ca.issueServer(t, host)},
				ClientAuth:   tls.RequireAndVerifyClientCert,
				ClientCAs:    clientCAs,
				Time:         f.clock.Now,
			}
			clientCfg := id.TLSConfig()
			clientCfg.ServerName = host
			clientCfg.Time = f.clock.Now

			peer, err := handshake(t, clientCfg, serverCfg)
			if err != nil {
				t.Fatalf("mTLS handshake with %s: %v", host, err)
			}
			if len(peer) == 0 {
				t.Fatal("the replica saw no client certificate")
			}
			if got := peer[0].URIs; len(got) != 1 || got[0].String() != tunnel.NodeURI("mac-mini-01").String() {
				t.Errorf("client certificate URI SANs = %v, want [%s]", got, tunnel.NodeURI("mac-mini-01"))
			}
		})
	}
}

// handshake runs one TLS handshake over an in-memory pipe and returns the
// certificate chain the server saw. No listener, no port, no network.
func handshake(t *testing.T, clientCfg, serverCfg *tls.Config) ([]*x509.Certificate, error) {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	client := tls.Client(clientConn, clientCfg)
	server := tls.Server(serverConn, serverCfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errc := make(chan error, 1)
	go func() { errc <- client.HandshakeContext(ctx) }()

	serverErr := server.HandshakeContext(ctx)
	clientErr := <-errc
	if serverErr != nil {
		return nil, serverErr
	}
	if clientErr != nil {
		return nil, clientErr
	}
	return server.ConnectionState().PeerCertificates, nil
}

// -----------------------------------------------------------------------
// Configuration
// -----------------------------------------------------------------------

func TestNewIdentityManagerRejectsInvalidConfig(t *testing.T) {
	base := func() tunnel.IdentityConfig {
		dir := t.TempDir()
		return tunnel.IdentityConfig{
			NodeID:           "mac-mini-01",
			RegistryEndpoint: "registry.example.com:8444",
			CertFile:         filepath.Join(dir, "node.crt"),
			KeyFile:          filepath.Join(dir, "node.key"),
			CAFile:           filepath.Join(dir, "ca.crt"),
		}
	}

	tests := []struct {
		name    string
		mutate  func(cfg *tunnel.IdentityConfig)
		wantErr bool
	}{
		{"valid", func(cfg *tunnel.IdentityConfig) {}, false},
		{"endpoint with scheme", func(cfg *tunnel.IdentityConfig) { cfg.RegistryEndpoint = "https://registry.example.com:8444" }, true},
		{"endpoint with path", func(cfg *tunnel.IdentityConfig) { cfg.RegistryEndpoint = "registry.example.com:8444/identity" }, true},
		{"endpoint without port", func(cfg *tunnel.IdentityConfig) { cfg.RegistryEndpoint = "registry.example.com" }, true},
		{"endpoint with bad port", func(cfg *tunnel.IdentityConfig) { cfg.RegistryEndpoint = "registry.example.com:0" }, true},
		{"endpoint missing", func(cfg *tunnel.IdentityConfig) { cfg.RegistryEndpoint = "" }, true},
		{"cert file missing", func(cfg *tunnel.IdentityConfig) { cfg.CertFile = "" }, true},
		{"key file missing", func(cfg *tunnel.IdentityConfig) { cfg.KeyFile = "" }, true},
		{"ca file missing", func(cfg *tunnel.IdentityConfig) { cfg.CAFile = "" }, true},
		{"renew fraction out of range", func(cfg *tunnel.IdentityConfig) { cfg.RenewFraction = 1.5 }, true},
		{"empty node id is allowed", func(cfg *tunnel.IdentityConfig) { cfg.NodeID = "" }, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)

			_, err := tunnel.NewIdentityManager(cfg, &fakeConnector{})
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("NewIdentityManager error = %v, want error = %t", err, tt.wantErr)
			}
			if tt.wantErr && !tunnel.IsFatal(err) {
				t.Errorf("error %v is not fatal; a bad configuration can never be fixed by retrying", err)
			}
		})
	}
}

func TestNewIdentityManagerRequiresConnector(t *testing.T) {
	dir := t.TempDir()
	_, err := tunnel.NewIdentityManager(tunnel.IdentityConfig{
		RegistryEndpoint: "registry.example.com:8444",
		CertFile:         filepath.Join(dir, "node.crt"),
		KeyFile:          filepath.Join(dir, "node.key"),
		CAFile:           filepath.Join(dir, "ca.crt"),
	}, nil)
	if !tunnel.IsFatal(err) {
		t.Fatalf("error = %v, want a fatal identity error: only the Registry can issue a certificate", err)
	}
}

func TestNewGRPCRegistryConnectorValidatesEndpoint(t *testing.T) {
	if _, err := tunnel.NewGRPCRegistryConnector("https://registry.example.com:8444", nil); !tunnel.IsFatal(err) {
		t.Fatalf("error = %v, want a fatal identity error for a URL endpoint", err)
	}
	if _, err := tunnel.NewGRPCRegistryConnector("registry.example.com:8444", nil); err != nil {
		t.Fatalf("NewGRPCRegistryConnector: %v", err)
	}
}
