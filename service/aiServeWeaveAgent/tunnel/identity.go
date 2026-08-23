package tunnel

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
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/nodeid"
	"AIServeWeave/common/runtime"
)

// This file owns the node's single identity: the key pair generated locally,
// the certificate the Registry issues for it, and the TLS configuration every
// tunnel to every Gateway replica shares. Two rules shape all of it:
//
//   - The private key never leaves the node. Only a CSR is sent, and a
//     rotation generates a fresh key rather than re-certifying the old one.
//   - Identity problems are terminal, not transient. A wrong node_id, a
//     rejected bootstrap token or a file with loose permissions can never be
//     fixed by retrying, so they are reported as fatal (see ErrFatal in
//     client.go) and the caller moves the connection to `failed` instead of
//     backing off and reconnecting forever.
//
// Beyond runtime.Clock and runtime.RuntimeError this file does not touch the
// runtime package: type mirroring stays confined to convert.go.

// identityOperation is the Operation recorded on identity errors, so a
// failure is attributable without a request context.
const identityOperation = "tunnel_identity"

// defaultRenewFraction is the share of a certificate's total lifetime that
// must remain before renewal is triggered, per the README: rotate once less
// than a third of the validity window is left.
const defaultRenewFraction = 1.0 / 3.0

// secretFileMode is the exact mode required on the node key and certificate.
// Anything more permissive is refused rather than silently tightened, because
// a widened mode means the file may already have been read by another user.
const secretFileMode os.FileMode = 0o600

// caFileMode is the mode written for the CA bundle. The bundle is public
// material, so it is world-readable but never group- or world-writable.
const caFileMode os.FileMode = 0o644

// identityDirMode is the mode used when creating a missing parent directory
// for the identity files.
const identityDirMode os.FileMode = 0o700

// IdentityConfig is the `tunnel.identity` configuration section plus the
// collaborators the identity code needs. It mirrors the YAML in README.md.
type IdentityConfig struct {
	// NodeID is the configured node identity. It may be empty, in which case
	// the Registry assigns one at registration; when it is set, a certificate
	// naming a different node is refused rather than used.
	NodeID string
	// RegistryEndpoint is the Registry's host:port. It must not be a Gateway
	// VIP: certificate issuance is a control-plane call.
	RegistryEndpoint string
	// CertFile, KeyFile and CAFile are the on-disk identity. CertFile and
	// KeyFile must be mode 0600.
	CertFile string
	KeyFile  string
	CAFile   string
	// BootstrapTokenFile holds the one-time registration token. It is read
	// once and deleted after the certificate is stored; it is only required
	// when no certificate exists yet.
	BootstrapTokenFile string
	// AgentVersion is reported to the Registry for auditing.
	AgentVersion string
	// RenewFraction overrides defaultRenewFraction. It must be in (0, 1).
	RenewFraction float64
	// Clock defaults to runtime.NewSystemClock; tests inject a fake one so
	// expiry and renewal thresholds are exercised without real sleeps.
	Clock runtime.Clock
	// Logger defaults to slog.Default.
	Logger *slog.Logger
}

// validate checks the configuration and fills in defaults, returning every
// problem at once so an operator fixes one file rather than one field.
func (c *IdentityConfig) validate() error {
	var problems []string

	if err := validateEndpoint("registry_endpoint", c.RegistryEndpoint); err != nil {
		problems = append(problems, err.Error())
	}
	for _, f := range []struct{ name, value string }{
		{"cert_file", c.CertFile},
		{"key_file", c.KeyFile},
		{"ca_file", c.CAFile},
	} {
		if strings.TrimSpace(f.value) == "" {
			problems = append(problems, f.name+" is required")
		}
	}
	if c.RenewFraction == 0 {
		c.RenewFraction = defaultRenewFraction
	}
	if c.RenewFraction <= 0 || c.RenewFraction >= 1 {
		problems = append(problems, "renew_fraction must be in (0, 1), got "+strconv.FormatFloat(c.RenewFraction, 'g', -1, 64))
	}
	if c.Clock == nil {
		c.Clock = runtime.NewSystemClock()
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}

	if len(problems) > 0 {
		return fatalIdentityError(runtime.ErrorInvalidConfig,
			"invalid tunnel identity configuration: "+strings.Join(problems, "; "), nil)
	}
	return nil
}

// validateEndpoint enforces the README's endpoint rule: a bare host:port,
// never a URL. A scheme or path here would mean the Agent could be pointed at
// an arbitrary URL, which the tunnel design explicitly forbids.
func validateEndpoint(name, endpoint string) error {
	if strings.TrimSpace(endpoint) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.Contains(endpoint, "://") || strings.Contains(endpoint, "/") {
		return fmt.Errorf("%s must be host:port without a scheme or path, got %q", name, endpoint)
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return fmt.Errorf("%s must be host:port, got %q", name, endpoint)
	}
	if host == "" {
		return fmt.Errorf("%s must have a host, got %q", name, endpoint)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("%s must have a port in [1, 65535], got %q", name, endpoint)
	}
	return nil
}

// Identity is the node's certified identity: one key pair and one certificate
// shared by every tunnel, because a node has exactly one identity no matter
// how many Gateway replicas it is connected to.
type Identity struct {
	// NodeID is the authoritative node identity taken from the certificate,
	// not from configuration.
	NodeID string
	// NotBefore and NotAfter are the certificate's validity window.
	NotBefore time.Time
	NotAfter  time.Time

	certificate tls.Certificate
	leaf        *x509.Certificate
	caPEM       []byte
	roots       *x509.CertPool
}

// Leaf returns the parsed node certificate.
func (id *Identity) Leaf() *x509.Certificate { return id.leaf }

// CABundlePEM returns the PEM-encoded CA bundle that issued this identity.
func (id *Identity) CABundlePEM() []byte {
	out := make([]byte, len(id.caPEM))
	copy(out, id.caPEM)
	return out
}

// TLSConfig returns the client TLS configuration for a tunnel: TLS 1.3 only,
// always presenting the node certificate, always verifying the peer against
// the Registry CA. A fresh copy is returned on every call so one replica's
// connection cannot mutate another's, and the same Identity therefore serves
// every replica endpoint — gRPC derives ServerName from the dial target.
func (id *Identity) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{id.certificate},
		RootCAs:      id.roots,
	}
}

// remainingFraction reports how much of the certificate's total lifetime is
// still left at now, clamped to [0, 1].
func (id *Identity) remainingFraction(now time.Time) float64 {
	lifetime := id.NotAfter.Sub(id.NotBefore)
	if lifetime <= 0 {
		return 0
	}
	remaining := id.NotAfter.Sub(now)
	switch {
	case remaining <= 0:
		return 0
	case remaining >= lifetime:
		return 1
	default:
		return float64(remaining) / float64(lifetime)
	}
}

// RegistryConnector opens a NodeIdentity client to the Registry. It is an
// interface so tests drive registration and rotation against an in-process
// fake instead of a real Registry.
type RegistryConnector interface {
	// Connect dials the Registry. id is nil for the bootstrap call — the one
	// method in the whole contract that presents no client certificate —
	// and otherwise the connection is mTLS with the node's current identity.
	// The returned io.Closer releases the connection.
	Connect(ctx context.Context, id *Identity) (tunnelv1.NodeIdentityClient, io.Closer, error)
}

// grpcRegistryConnector is the production RegistryConnector.
type grpcRegistryConnector struct {
	endpoint string
	caPEM    []byte
}

// NewGRPCRegistryConnector returns the production RegistryConnector for the
// Registry at endpoint. caPEM is the CA bundle used to authenticate the
// Registry during bootstrap, when no node certificate exists yet; it may be
// nil, in which case the host's root store is used. Once an Identity exists
// its own CA bundle is used instead.
func NewGRPCRegistryConnector(endpoint string, caPEM []byte) (RegistryConnector, error) {
	if err := validateEndpoint("registry_endpoint", endpoint); err != nil {
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig, err.Error(), nil)
	}
	return &grpcRegistryConnector{endpoint: endpoint, caPEM: append([]byte(nil), caPEM...)}, nil
}

func (c *grpcRegistryConnector) Connect(_ context.Context, id *Identity) (tunnelv1.NodeIdentityClient, io.Closer, error) {
	var tlsCfg *tls.Config
	if id != nil {
		tlsCfg = id.TLSConfig()
	} else {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS13}
		if len(c.caPEM) > 0 {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(c.caPEM) {
				return nil, nil, fatalIdentityError(runtime.ErrorInvalidConfig,
					"ca bundle contains no usable certificate", nil)
			}
			tlsCfg.RootCAs = pool
		}
	}

	conn, err := grpc.NewClient(c.endpoint, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, nil, transientIdentityError(runtime.ErrorConnection,
			"cannot create a Registry client for "+c.endpoint, err)
	}
	return tunnelv1.NewNodeIdentityClient(conn), conn, nil
}

// IdentityManager loads, registers and rotates the node identity. It is the
// only writer of the identity files.
type IdentityManager struct {
	cfg       IdentityConfig
	connector RegistryConnector
	clock     runtime.Clock
	logger    *slog.Logger
}

// NewIdentityManager validates cfg and returns the manager. connector is
// required: without a Registry there is no way to obtain or rotate a
// certificate, and the Gateway deliberately has no issuing capability.
func NewIdentityManager(cfg IdentityConfig, connector RegistryConnector) (*IdentityManager, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if connector == nil {
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig, "registry connector is required", nil)
	}
	return &IdentityManager{
		cfg:       cfg,
		connector: connector,
		clock:     cfg.Clock,
		logger:    cfg.Logger,
	}, nil
}

// Ensure returns a usable node identity: it loads the stored certificate, or
// exchanges the bootstrap token for one when nothing is stored yet, and
// rotates the certificate when less than RenewFraction of its lifetime
// remains.
//
// A renewal that fails for a transient reason is logged and the current,
// still-valid certificate is returned: a Registry outage must not take a node
// out of service while its certificate is good. A fatal failure is returned
// as such so the caller can enter `failed` rather than retry.
func (m *IdentityManager) Ensure(ctx context.Context) (*Identity, error) {
	id, err := m.load()
	switch {
	case errors.Is(err, os.ErrNotExist):
		m.logger.Info("no node certificate on disk; registering with the Registry",
			slog.String("registry_endpoint", m.cfg.RegistryEndpoint))
		return m.bootstrap(ctx)
	case err != nil:
		return nil, err
	}

	now := m.clock.Now()
	if now.Before(id.NotBefore) {
		return nil, fatalIdentityError(runtime.ErrorUnauthorized,
			fmt.Sprintf("node certificate for %q is not valid until %s; check the system clock",
				id.NodeID, id.NotBefore.UTC().Format(time.RFC3339)), nil)
	}
	if !now.Before(id.NotAfter) {
		if m.hasBootstrapToken() {
			m.logger.Warn("node certificate expired; re-registering with the bootstrap token",
				slog.String("node_id", id.NodeID),
				slog.Time("not_after", id.NotAfter))
			return m.bootstrap(ctx)
		}
		return nil, fatalIdentityError(runtime.ErrorUnauthorized,
			fmt.Sprintf("node certificate for %q expired at %s and no bootstrap token is available; issue a new token",
				id.NodeID, id.NotAfter.UTC().Format(time.RFC3339)), nil)
	}

	if !m.NeedsRenewal(id) {
		return id, nil
	}

	renewed, err := m.Renew(ctx, id)
	if err == nil {
		return renewed, nil
	}
	if IsFatal(err) {
		return nil, err
	}
	m.logger.Warn("certificate renewal failed; continuing with the current certificate",
		slog.String("node_id", id.NodeID),
		slog.Time("not_after", id.NotAfter),
		slog.String("error", err.Error()))
	return id, nil
}

// NeedsRenewal reports whether less than the configured fraction of id's
// lifetime remains and it should therefore be rotated.
func (m *IdentityManager) NeedsRenewal(id *Identity) bool {
	return id.remainingFraction(m.clock.Now()) < m.cfg.RenewFraction
}

// Renew rotates the certificate through NodeIdentity.RenewCertificate, which
// requires the current certificate to still be valid. A fresh key pair is
// generated so a rotation also rotates the key; the new identity is written to
// disk and returned. Callers reconnect their tunnels one at a time with it, so
// the node is never unreachable to every replica at once.
func (m *IdentityManager) Renew(ctx context.Context, id *Identity) (*Identity, error) {
	key, keyPEM, err := generateNodeKey()
	if err != nil {
		return nil, err
	}
	csr, err := createCSR(key, id.NodeID)
	if err != nil {
		return nil, err
	}

	client, closer, err := m.connector.Connect(ctx, id)
	if err != nil {
		return nil, err
	}
	defer closeQuietly(m.logger, closer)

	resp, err := client.RenewCertificate(ctx, &tunnelv1.RenewRequest{NodeId: id.NodeID, Csr: csr})
	if err != nil {
		return nil, registryCallError("RenewCertificate", err)
	}

	caPEM := resp.GetCaBundlePem()
	if len(caPEM) == 0 {
		caPEM = id.caPEM
	}
	next, err := m.adopt(id.NodeID, resp.GetCertificatePem(), keyPEM, caPEM, notAfterOf(resp.GetNotAfter()))
	if err != nil {
		return nil, err
	}
	m.logger.Info("node certificate rotated",
		slog.String("node_id", next.NodeID),
		slog.Time("not_after", next.NotAfter))
	return next, nil
}

// bootstrap exchanges the one-time token for a certificate and deletes the
// token file afterwards.
func (m *IdentityManager) bootstrap(ctx context.Context) (*Identity, error) {
	token, err := m.readBootstrapToken()
	if err != nil {
		return nil, err
	}

	key, keyPEM, err := generateNodeKey()
	if err != nil {
		return nil, err
	}
	// An empty NodeID is legal: the Registry then assigns one and the
	// response is authoritative either way.
	csr, err := createCSR(key, m.cfg.NodeID)
	if err != nil {
		return nil, err
	}

	client, closer, err := m.connector.Connect(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer closeQuietly(m.logger, closer)

	resp, err := client.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId:         m.cfg.NodeID,
		Csr:            csr,
		BootstrapToken: token,
		AgentVersion:   m.cfg.AgentVersion,
	})
	if err != nil {
		return nil, registryCallError("Register", err)
	}

	nodeID := resp.GetNodeId()
	if nodeID == "" {
		nodeID = m.cfg.NodeID
	}
	if m.cfg.NodeID != "" && nodeID != m.cfg.NodeID {
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig,
			fmt.Sprintf("Registry issued an identity for node %q but this Agent is configured as %q", nodeID, m.cfg.NodeID), nil)
	}

	id, err := m.adopt(nodeID, resp.GetCertificatePem(), keyPEM, resp.GetCaBundlePem(), notAfterOf(resp.GetNotAfter()))
	if err != nil {
		return nil, err
	}

	// The token is spent on the Registry the moment Register succeeded, so
	// failing to delete the local copy is worth an error in the log but must
	// not undo a successful registration.
	if err := os.Remove(m.cfg.BootstrapTokenFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.logger.Error("bootstrap token file could not be deleted; remove it manually",
			slog.String("path", m.cfg.BootstrapTokenFile),
			slog.String("error", err.Error()))
	}

	m.logger.Info("node registered",
		slog.String("node_id", id.NodeID),
		slog.Time("not_after", id.NotAfter))
	return id, nil
}

// adopt validates a freshly issued certificate against the key it was issued
// for and the CA that issued it, persists all three files, and returns the
// resulting Identity. Nothing is written before every check passes, so a
// Registry that answers with a mismatched certificate cannot corrupt a
// working identity on disk.
func (m *IdentityManager) adopt(nodeID string, certPEM, keyPEM, caPEM []byte, notAfter time.Time) (*Identity, error) {
	id, err := m.newIdentity(certPEM, keyPEM, caPEM, nodeID, false)
	if err != nil {
		return nil, err
	}
	if !notAfter.IsZero() && !notAfter.Equal(id.NotAfter) {
		return nil, fatalIdentityError(runtime.ErrorProtocol,
			fmt.Sprintf("Registry reported not_after %s but the certificate expires at %s",
				notAfter.UTC().Format(time.RFC3339), id.NotAfter.UTC().Format(time.RFC3339)), nil)
	}
	if err := m.persist(certPEM, keyPEM, caPEM); err != nil {
		return nil, err
	}
	return id, nil
}

// newIdentity parses and fully validates a certificate/key/CA triple: the key
// must match the certificate, the certificate must chain to the CA, and it
// must name the expected node. wantNodeID may be empty, in which case the
// certificate's own identity is adopted.
//
// allowExpired governs only the freshness half of the chain check. A stored
// certificate is loaded with allowExpired set so that an expired one still
// parses and Ensure can report expiry — or re-register — with a message an
// operator can act on, rather than surfacing as an unhelpful chain failure. A
// certificate the Registry just issued is always checked against the current
// time, so one that is already expired or not yet valid is refused outright.
func (m *IdentityManager) newIdentity(certPEM, keyPEM, caPEM []byte, wantNodeID string, allowExpired bool) (*Identity, error) {
	if len(certPEM) == 0 {
		return nil, fatalIdentityError(runtime.ErrorProtocol, "node certificate is empty", nil)
	}
	if len(caPEM) == 0 {
		return nil, fatalIdentityError(runtime.ErrorProtocol, "ca bundle is empty", nil)
	}

	keyPair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig,
			"node certificate and private key do not form a usable pair", err)
	}
	leaf, err := x509.ParseCertificate(keyPair.Certificate[0])
	if err != nil {
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig, "node certificate cannot be parsed", err)
	}
	keyPair.Leaf = leaf

	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig, "ca bundle contains no usable certificate", nil)
	}
	intermediates := x509.NewCertPool()
	for _, der := range keyPair.Certificate[1:] {
		if c, err := x509.ParseCertificate(der); err == nil {
			intermediates.AddCert(c)
		}
	}
	verifyAt := m.clock.Now()
	if allowExpired && (verifyAt.Before(leaf.NotBefore) || !verifyAt.Before(leaf.NotAfter)) {
		verifyAt = leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) / 2)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		CurrentTime:   verifyAt,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return nil, fatalIdentityError(runtime.ErrorUnauthorized,
			"node certificate does not chain to the configured CA", err)
	}

	certNodeID, err := nodeIDFromCertificate(leaf)
	if err != nil {
		return nil, err
	}
	if wantNodeID != "" && certNodeID != wantNodeID {
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig,
			fmt.Sprintf("node certificate names %q but this Agent is configured as %q", certNodeID, wantNodeID), nil)
	}

	return &Identity{
		NodeID:      certNodeID,
		NotBefore:   leaf.NotBefore,
		NotAfter:    leaf.NotAfter,
		certificate: keyPair,
		leaf:        leaf,
		caPEM:       append([]byte(nil), caPEM...),
		roots:       roots,
	}, nil
}

// load reads the stored identity. It returns an error wrapping os.ErrNotExist
// when neither the certificate nor the key exists, which is the signal to
// bootstrap; a half-present pair is a fatal error rather than a reason to
// re-register, because silently discarding one half of an identity would hide
// a botched deployment.
func (m *IdentityManager) load() (*Identity, error) {
	certExists, err := m.checkSecretFile(m.cfg.CertFile, "cert_file")
	if err != nil {
		return nil, err
	}
	keyExists, err := m.checkSecretFile(m.cfg.KeyFile, "key_file")
	if err != nil {
		return nil, err
	}
	switch {
	case !certExists && !keyExists:
		return nil, fmt.Errorf("node identity not stored: %w", os.ErrNotExist)
	case certExists != keyExists:
		missing, present := m.cfg.KeyFile, m.cfg.CertFile
		if !certExists {
			missing, present = m.cfg.CertFile, m.cfg.KeyFile
		}
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig,
			fmt.Sprintf("node identity is incomplete: %s exists but %s is missing", present, missing), nil)
	}

	certPEM, err := os.ReadFile(m.cfg.CertFile)
	if err != nil {
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig, "cannot read "+m.cfg.CertFile, err)
	}
	keyPEM, err := os.ReadFile(m.cfg.KeyFile)
	if err != nil {
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig, "cannot read "+m.cfg.KeyFile, err)
	}
	caPEM, err := os.ReadFile(m.cfg.CAFile)
	if err != nil {
		return nil, fatalIdentityError(runtime.ErrorInvalidConfig, "cannot read "+m.cfg.CAFile, err)
	}

	return m.newIdentity(certPEM, keyPEM, caPEM, m.cfg.NodeID, true)
}

// checkSecretFile reports whether path exists and enforces mode 0600 on it.
// A mode wider than 0600 is fatal: the file may already have been read by
// another user, so tightening it silently would hide a real exposure.
func (m *IdentityManager) checkSecretFile(path, name string) (bool, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fatalIdentityError(runtime.ErrorInvalidConfig, "cannot stat "+path, err)
	case info.IsDir():
		return false, fatalIdentityError(runtime.ErrorInvalidConfig, name+" "+path+" is a directory", nil)
	}
	if mode := info.Mode().Perm(); mode != secretFileMode {
		return false, fatalIdentityError(runtime.ErrorInvalidConfig,
			fmt.Sprintf("%s %s has mode %#o, want %#o", name, path, mode, secretFileMode), nil)
	}
	return true, nil
}

// hasBootstrapToken reports whether a bootstrap token file is present, so an
// expired certificate can be replaced by re-registering instead of failing.
func (m *IdentityManager) hasBootstrapToken() bool {
	if m.cfg.BootstrapTokenFile == "" {
		return false
	}
	info, err := os.Stat(m.cfg.BootstrapTokenFile)
	return err == nil && !info.IsDir()
}

// readBootstrapToken reads the one-time token. The token itself never enters
// a log line or an error message — only the path does.
func (m *IdentityManager) readBootstrapToken() (string, error) {
	if strings.TrimSpace(m.cfg.BootstrapTokenFile) == "" {
		return "", fatalIdentityError(runtime.ErrorInvalidConfig,
			"no node certificate is stored and bootstrap_token_file is not configured", nil)
	}
	info, err := os.Stat(m.cfg.BootstrapTokenFile)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fatalIdentityError(runtime.ErrorInvalidConfig,
			"no node certificate is stored and no bootstrap token at "+m.cfg.BootstrapTokenFile, nil)
	case err != nil:
		return "", fatalIdentityError(runtime.ErrorInvalidConfig, "cannot stat "+m.cfg.BootstrapTokenFile, err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return "", fatalIdentityError(runtime.ErrorInvalidConfig,
			fmt.Sprintf("bootstrap_token_file %s has mode %#o, which is readable beyond its owner",
				m.cfg.BootstrapTokenFile, mode), nil)
	}

	raw, err := os.ReadFile(m.cfg.BootstrapTokenFile)
	if err != nil {
		return "", fatalIdentityError(runtime.ErrorInvalidConfig, "cannot read "+m.cfg.BootstrapTokenFile, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fatalIdentityError(runtime.ErrorInvalidConfig,
			"bootstrap token file "+m.cfg.BootstrapTokenFile+" is empty", nil)
	}
	return token, nil
}

// persist writes the identity files, each one atomically so a crash mid-write
// cannot leave a certificate that does not match its key. The key is written
// first: a key without a certificate is recoverable by re-registering, while a
// certificate without its key is not usable at all.
func (m *IdentityManager) persist(certPEM, keyPEM, caPEM []byte) error {
	for _, f := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{m.cfg.KeyFile, keyPEM, secretFileMode},
		{m.cfg.CertFile, certPEM, secretFileMode},
		{m.cfg.CAFile, caPEM, caFileMode},
	} {
		if err := writeFileAtomic(f.path, f.data, f.mode); err != nil {
			return fatalIdentityError(runtime.ErrorInvalidConfig, "cannot write "+f.path, err)
		}
	}
	return nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, so readers never observe a partially written identity file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, identityDirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeded

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// generateNodeKey generates the node private key. It is ECDSA P-256: TLS 1.3
// only, small handshakes, and cheap enough that rotating the key on every
// renewal costs nothing. The key is returned both parsed and PEM-encoded; it
// exists only in this process and on local disk, never on the wire.
func generateNodeKey() (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, transientIdentityError(runtime.ErrorUpstream, "cannot generate a node key", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, transientIdentityError(runtime.ErrorUpstream, "cannot encode the node key", err)
	}
	return key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// createCSR builds the PKCS#10 request sent to the Registry. The node_id is
// carried both as the common name and as the canonical URI SAN; nodeID may be
// empty, in which case the Registry assigns the identity.
func createCSR(key *ecdsa.PrivateKey, nodeID string) ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: nodeID},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	if nodeID != "" {
		tmpl.URIs = []*url.URL{NodeURI(nodeID)}
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, transientIdentityError(runtime.ErrorUpstream, "cannot create a certificate request", err)
	}
	return der, nil
}

// NodeURI returns the canonical URI SAN for nodeID. It is a thin alias for
// nodeid.URI, kept so existing callers and tests in this package do not have to
// name the shared package for a value that is part of the node's identity.
func NodeURI(nodeID string) *url.URL { return nodeid.URI(nodeID) }

// nodeIDFromCertificate extracts the node identity a certificate asserts,
// translating nodeid's sentinel into this package's fatal identity error: a
// certificate that names no node is a configuration fault, never a transient
// one, so it must not be retried.
func nodeIDFromCertificate(leaf *x509.Certificate) (string, error) {
	id, err := nodeid.FromCertificate(leaf)
	if err != nil {
		return "", fatalIdentityError(runtime.ErrorInvalidConfig,
			"node certificate carries no node identity: expected a "+nodeid.Expected(), err)
	}
	return id, nil
}

// notAfterOf converts the Registry's reported expiry, returning the zero time
// when it was not set.
func notAfterOf(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// registryCallError classifies a failed NodeIdentity call. Authentication and
// argument errors are fatal — no amount of retrying fixes a spent token or a
// rejected node_id — while unavailability and deadlines are transient.
func registryCallError(method string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return transientIdentityError(runtime.ErrorConnection, "Registry "+method+" failed", err)
	}
	switch st.Code() {
	case codes.Unauthenticated, codes.PermissionDenied:
		return fatalIdentityError(runtime.ErrorUnauthorized,
			"Registry rejected "+method+": "+st.Code().String(), err)
	case codes.InvalidArgument, codes.NotFound, codes.AlreadyExists, codes.FailedPrecondition, codes.Unimplemented:
		return fatalIdentityError(runtime.ErrorInvalidConfig,
			"Registry rejected "+method+": "+st.Code().String(), err)
	case codes.DeadlineExceeded:
		return transientIdentityError(runtime.ErrorTimeout, "Registry "+method+" timed out", err)
	case codes.ResourceExhausted:
		return transientIdentityError(runtime.ErrorRateLimited, "Registry "+method+" was rate limited", err)
	default:
		return transientIdentityError(runtime.ErrorConnection,
			"Registry "+method+" failed: "+st.Code().String(), err)
	}
}

// fatalIdentityError builds an identity error that no retry can fix. The
// message is safe for logs; the cause is joined with ErrFatal so both
// errors.Is(err, ErrFatal) and errors.Is(err, <original>) match.
func fatalIdentityError(code runtime.ErrorCode, msg string, cause error) *runtime.RuntimeError {
	return &runtime.RuntimeError{
		Code:      code,
		Operation: identityOperation,
		Message:   msg,
		Retryable: false,
		Cause:     errors.Join(ErrFatal, cause),
	}
}

// transientIdentityError builds an identity error worth retrying: the
// Registry is unreachable, slow or busy, none of which says anything about
// the validity of the identity itself.
func transientIdentityError(code runtime.ErrorCode, msg string, cause error) *runtime.RuntimeError {
	return &runtime.RuntimeError{
		Code:      code,
		Operation: identityOperation,
		Message:   msg,
		Retryable: true,
		Cause:     cause,
	}
}

// closeQuietly closes the Registry connection, logging rather than returning
// a close failure: by the time it runs the certificate is already stored.
func closeQuietly(logger *slog.Logger, closer io.Closer) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		logger.Debug("closing the Registry connection failed", slog.String("error", err.Error()))
	}
}
