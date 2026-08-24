// Package ca implements the Registry's certificate authority: the root key
// pair that never leaves this process, and the signing operations that turn a
// node's or this process's own CSR into a certificate. Nothing here is
// shared with Agent or Gateway — the CA private key is the one piece of the
// system that must stay confined to a single process, per AGENTS.md's
// security rule that it never reaches a Gateway replica.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"AIServeWeave/common/nodeid"
)

// NodeCertLifetime is how long a node certificate is valid for. It matches
// the value the Agent's identity tests are written against
// (tunnel/identity_test.go's certLifetime), which is itself the number the
// tunnel README documents for certificate rotation timing.
const NodeCertLifetime = 30 * 24 * time.Hour

// rootCertLifetime is how long the self-signed root is valid for. It is long
// on purpose: rotating the root means re-issuing every node's certificate,
// which this Registry has no tooling for yet.
const rootCertLifetime = 10 * 365 * 24 * time.Hour

// clockSkew backdates NotBefore on every certificate this CA issues, so a
// node or peer whose clock runs slightly behind the Registry's does not see
// a not-yet-valid certificate.
const clockSkew = time.Minute

const (
	keyFileMode  os.FileMode = 0o600
	certFileMode os.FileMode = 0o644
	caDirMode    os.FileMode = 0o700
)

// CA is the Registry's certificate authority. The zero value is not usable;
// construct one with LoadOrCreate.
type CA struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pem  []byte // the root certificate, PEM-encoded, returned as ca_bundle_pem
}

// LoadOrCreate loads the root key and certificate from dir, generating a
// fresh self-signed root the first time it is called against an empty dir.
// dir is created if it does not exist.
func LoadOrCreate(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, caDirMode); err != nil {
		return nil, fmt.Errorf("ca: cannot create %s: %w", dir, err)
	}
	keyPath := filepath.Join(dir, "ca-key.pem")
	certPath := filepath.Join(dir, "ca-cert.pem")

	keyPEM, keyErr := os.ReadFile(keyPath)
	certPEM, certErr := os.ReadFile(certPath)
	switch {
	case keyErr == nil && certErr == nil:
		return loadCA(keyPEM, certPEM)
	case os.IsNotExist(keyErr) && os.IsNotExist(certErr):
		return createCA(keyPath, certPath)
	case keyErr != nil:
		return nil, fmt.Errorf("ca: cannot read %s: %w", keyPath, keyErr)
	default:
		return nil, fmt.Errorf("ca: cannot read %s: %w", certPath, certErr)
	}
}

func loadCA(keyPEM, certPEM []byte) (*CA, error) {
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, errors.New("ca: root key file contains no PEM block")
	}
	rawKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: cannot parse root key: %w", err)
	}
	key, ok := rawKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("ca: root key is not ECDSA")
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, errors.New("ca: root certificate file contains no PEM block")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: cannot parse root certificate: %w", err)
	}
	return &CA{key: key, cert: cert, pem: certPEM}, nil
}

func createCA(keyPath, certPath string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: cannot generate root key: %w", err)
	}
	now := time.Now()
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "AIServeWeave Registry Root"},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(rootCertLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("ca: cannot self-sign root certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("ca: cannot parse freshly signed root certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("ca: cannot encode root key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	if err := writeFileAtomic(keyPath, keyPEM, keyFileMode); err != nil {
		return nil, fmt.Errorf("ca: cannot write root key: %w", err)
	}
	if err := writeFileAtomic(certPath, certPEM, certFileMode); err != nil {
		return nil, fmt.Errorf("ca: cannot write root certificate: %w", err)
	}
	return &CA{key: key, cert: cert, pem: certPEM}, nil
}

// Bundle returns the root certificate, PEM-encoded. This is what Register and
// RenewCertificate hand back as ca_bundle_pem.
func (c *CA) Bundle() []byte {
	return append([]byte(nil), c.pem...)
}

// Pool returns a cert pool containing just the root certificate, for
// verifying certificates this CA issued.
func (c *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(c.cert)
	return pool
}

// Sign parses csrDER, checks its signature, and issues a node certificate
// naming nodeID through the canonical URI SAN (common/nodeid.URI) — the same
// convention tunnel/identity_test.go's fake Registry signs against, so a
// certificate issued here satisfies exactly what the Agent's IdentityManager
// validates on the other end.
func (c *CA) Sign(csrDER []byte, nodeID string, now time.Time) (certPEM []byte, notAfter time.Time, err error) {
	if nodeID == "" {
		return nil, time.Time{}, errors.New("ca: node_id is required")
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("ca: cannot parse certificate request: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, time.Time{}, fmt.Errorf("ca: certificate request signature is invalid: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, time.Time{}, err
	}
	// X.509 stores validity times with only whole-second precision, so the
	// returned notAfter is truncated to match exactly what a caller parsing
	// the certificate back out will see; RegisterResponse.not_after must
	// equal the certificate's own NotAfter down to the tick (Agent's
	// IdentityManager.adopt rejects a mismatch as a protocol error).
	notAfter = now.Add(NodeCertLifetime).Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: nodeID},
		URIs:                  []*url.URL{nodeid.URI(nodeID)},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("ca: cannot sign node certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), notAfter, nil
}

// IssueServerCert mints a server-auth certificate for this Registry's own
// gRPC listener, signed by the same root CA. It exists so a first deployment
// can start without an operator-supplied certificate: the ca-cert.pem this
// CA writes to disk is the bundle Agents and Gateway replicas are configured
// to trust.
func (c *CA) IssueServerCert(hosts []string, now time.Time) (certPEM, keyPEM []byte, notAfter time.Time, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("ca: cannot generate server key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	notAfter = now.Add(NodeCertLifetime).Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "aiserveweave-registry"},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("ca: cannot sign server certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, time.Time{}, fmt.Errorf("ca: cannot encode server key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, notAfter, nil
}

// randomSerial returns a random 128-bit certificate serial number, as
// recommended practice for avoiding collisions without a persistent counter.
func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("ca: cannot generate certificate serial: %w", err)
	}
	return serial, nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, so a reader never observes a partially written key or
// certificate.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
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
