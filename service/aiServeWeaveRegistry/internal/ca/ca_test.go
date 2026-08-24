package ca_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
	"time"

	"AIServeWeave/common/nodeid"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/ca"
)

func TestLoadOrCreateGeneratesThenReloadsTheSameRoot(t *testing.T) {
	dir := t.TempDir()

	first, err := ca.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	second, err := ca.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate() reload error = %v", err)
	}
	if string(first.Bundle()) != string(second.Bundle()) {
		t.Fatalf("reload produced a different root certificate; bundles should be byte-identical")
	}
}

func TestSignIssuesACertificateTheAgentSideValidatesAsClientAuth(t *testing.T) {
	dir := t.TempDir()
	root, err := ca.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}

	csrDER, key := newCSR(t, "node-1")
	now := time.Now()
	certPEM, notAfter, err := root.Sign(csrDER, "node-1", now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	block, _ := pemDecode(t, certPEM)
	leaf, err := x509.ParseCertificate(block)
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}

	// The certificate must chain to the root CA using ClientAuth, exactly
	// what tunnel.IdentityManager.newIdentity checks on the Agent side.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:       root.Pool(),
		CurrentTime: now,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("issued certificate does not chain as ClientAuth: %v", err)
	}

	gotID, err := nodeid.FromCertificate(leaf)
	if err != nil {
		t.Fatalf("nodeid.FromCertificate() error = %v", err)
	}
	if gotID != "node-1" {
		t.Errorf("certificate identity = %q, want %q", gotID, "node-1")
	}

	if !leaf.NotBefore.Before(now) {
		t.Errorf("NotBefore = %v, want it backdated before %v to tolerate clock skew", leaf.NotBefore, now)
	}
	if !leaf.NotAfter.Equal(notAfter) {
		t.Errorf("returned notAfter %v does not match certificate NotAfter %v", notAfter, leaf.NotAfter)
	}
	if got, want := notAfter.Sub(now), ca.NodeCertLifetime; got < want-time.Minute || got > want+time.Minute {
		t.Errorf("certificate lifetime = %v, want approximately %v", got, want)
	}

	if !leaf.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey) {
		t.Errorf("issued certificate carries a different public key than the CSR")
	}
}

func TestSignRejectsAnInvalidCSRSignature(t *testing.T) {
	dir := t.TempDir()
	root, err := ca.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}

	if _, _, err := root.Sign([]byte("not a csr"), "node-1", time.Now()); err == nil {
		t.Fatal("Sign() with a garbage CSR = nil error, want an error")
	}
}

func TestSignRejectsAnEmptyNodeID(t *testing.T) {
	dir := t.TempDir()
	root, err := ca.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}
	csrDER, _ := newCSR(t, "node-1")

	if _, _, err := root.Sign(csrDER, "", time.Now()); err == nil {
		t.Fatal("Sign() with an empty node_id = nil error, want an error")
	}
}

func TestIssueServerCertChainsWithServerAuth(t *testing.T) {
	dir := t.TempDir()
	root, err := ca.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate() error = %v", err)
	}

	certPEM, keyPEM, notAfter, err := root.IssueServerCert([]string{"127.0.0.1", "registry.local"}, time.Now())
	if err != nil {
		t.Fatalf("IssueServerCert() error = %v", err)
	}
	if len(keyPEM) == 0 {
		t.Fatal("IssueServerCert() returned no private key")
	}
	certBlock, _ := pemDecode(t, certPEM)
	leaf, err := x509.ParseCertificate(certBlock)
	if err != nil {
		t.Fatalf("parse server certificate: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:       root.Pool(),
		DNSName:     "registry.local",
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CurrentTime: notAfter.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("issued server certificate does not chain as ServerAuth for its DNS SAN: %v", err)
	}
}

func newCSR(t *testing.T, nodeID string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: nodeID},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	return der, key
}

// pemDecode is a tiny local helper so tests do not have to spell out
// encoding/pem decoding boilerplate repeatedly.
func pemDecode(t *testing.T, data []byte) ([]byte, string) {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatalf("no PEM block found in %q", data)
	}
	return block.Bytes, block.Type
}
