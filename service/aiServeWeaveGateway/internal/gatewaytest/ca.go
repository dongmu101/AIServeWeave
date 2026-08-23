package gatewaytest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"AIServeWeave/common/nodeid"
)

// CA issues node certificates for tests. It signs real certificates because
// the server authenticates from a parsed x509 SAN: a hand-built struct would
// let a test pass while the real SAN convention was broken.
type CA struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
}

// NewCA returns a certificate authority for one test.
func NewCA(t *testing.T) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gatewaytest CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("signing CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing CA certificate: %v", err)
	}
	return &CA{key: key, cert: cert}
}

// NodeCertificate issues a certificate carrying the canonical node identity
// SAN for nodeID.
func (ca *CA) NodeCertificate(t *testing.T, nodeID string) *x509.Certificate {
	t.Helper()
	return ca.issue(t, nodeID, []*url.URL{nodeid.URI(nodeID)}, nil)
}

// NodeCertificateWithDNS issues a certificate that names the node with a DNS
// SAN instead of the canonical URI SAN, the form a CA that cannot issue URI
// SANs produces.
func (ca *CA) NodeCertificateWithDNS(t *testing.T, nodeID string) *x509.Certificate {
	t.Helper()
	return ca.issue(t, nodeID, nil, []string{nodeID})
}

// AnonymousCertificate issues a certificate that names no node at all.
func (ca *CA) AnonymousCertificate(t *testing.T) *x509.Certificate {
	t.Helper()
	return ca.issue(t, "anonymous", nil, nil)
}

func (ca *CA) issue(t *testing.T, cn string, uris []*url.URL, dns []string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating node key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         uris,
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("signing node certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing node certificate: %v", err)
	}
	return cert
}

// PeerContext returns ctx carrying the peer information gRPC would attach for
// a connection whose client certificate was verified as nodeID.
func (ca *CA) PeerContext(ctx context.Context, nodeID string) context.Context {
	leaf := ca.mustIssue(nodeID)
	return VerifiedPeerContext(ctx, leaf)
}

func (ca *CA) mustIssue(nodeID string) *x509.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{nodeid.URI(nodeID)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		panic(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return cert
}

// VerifiedPeerContext returns ctx with peer information whose TLS state lists
// leaf as a verified chain, i.e. a client the TLS stack has already accepted.
func VerifiedPeerContext(ctx context.Context, leaf *x509.Certificate) context.Context {
	return peer.NewContext(ctx, &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
			VerifiedChains: [][]*x509.Certificate{{leaf}},
		}},
	})
}

// UnverifiedPeerContext returns ctx with peer information that carries a
// client certificate the TLS stack did *not* verify, the state a replica
// configured without client authentication would see.
func UnverifiedPeerContext(ctx context.Context, leaf *x509.Certificate) context.Context {
	return peer.NewContext(ctx, &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{leaf},
		}},
	})
}
