package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"AIServeWeave/common/nodeid"
)

// pki is a throwaway certificate authority. The test signs real certificates
// and the servers really verify them: an mTLS test that faked the handshake
// would not notice the day the node identity SAN convention drifted apart
// between the two sides.
type pki struct {
	t      *testing.T
	key    *ecdsa.PrivateKey
	cert   *x509.Certificate
	caPEM  []byte
	caPool *x509.CertPool
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "AIServeWeave e2e CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("signing the CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing the CA certificate: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	return &pki{t: t, key: key, cert: cert, caPEM: caPEM, caPool: pool}
}

// serverTLS returns the TLS configuration for one Gateway replica: it presents
// a certificate for 127.0.0.1 and requires a client certificate this CA signed.
// RequireAndVerifyClientCert is the setting the whole authentication model
// rests on — without it the server would see an unverified peer and, by
// design, authenticate nobody.
func (p *pki) serverTLS() *tls.Config {
	p.t.Helper()
	cert := p.issue("gateway-replica", nil, []string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)}, x509.ExtKeyUsageServerAuth)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    p.caPool,
	}
}

// writeNodeIdentity issues a node certificate and writes the three files the
// Agent's IdentityManager loads. This is the "offline issuance" path the tunnel
// README describes for running before a real Registry exists: the certificate
// is valid and correctly named, it simply was not obtained by exchanging a
// bootstrap token.
func (p *pki) writeNodeIdentity(dir, nodeID string) (certFile, keyFile, caFile string) {
	p.t.Helper()
	certFile = filepath.Join(dir, "node.crt")
	keyFile = filepath.Join(dir, "node.key")
	caFile = filepath.Join(dir, "ca.crt")

	cert := p.issue(nodeID, []*url.URL{nodeid.URI(nodeID)}, nil, nil, x509.ExtKeyUsageClientAuth)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		p.t.Fatalf("marshalling the node key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	// 0600 is not decoration: the Agent refuses to load a key any wider.
	writeFile(p.t, certFile, certPEM, 0o600)
	writeFile(p.t, keyFile, keyPEM, 0o600)
	writeFile(p.t, caFile, p.caPEM, 0o600)
	return certFile, keyFile, caFile
}

func (p *pki) issue(cn string, uris []*url.URL, dns []string, ips []net.IP, usage x509.ExtKeyUsage) tls.Certificate {
	p.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		p.t.Fatalf("generating a leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		// A day of validity keeps the Agent's renewal threshold far away:
		// a certificate close to expiry would send it to a Registry that
		// does not exist in this test.
		NotAfter:    time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{usage},
		URIs:        uris,
		DNSNames:    dns,
		IPAddresses: ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.cert, &key.PublicKey, p.key)
	if err != nil {
		p.t.Fatalf("signing a leaf certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		p.t.Fatalf("parsing a leaf certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
