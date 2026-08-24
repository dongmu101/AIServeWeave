package nodeid_test

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

func TestFromPeer(t *testing.T) {
	leaf := newTestLeaf(t, "node-1")

	tests := []struct {
		name    string
		ctx     context.Context
		wantID  string
		wantErr bool
	}{
		{
			name:    "no peer information",
			ctx:     context.Background(),
			wantErr: true,
		},
		{
			name: "not TLS",
			ctx: peer.NewContext(context.Background(), &peer.Peer{
				AuthInfo: insecureAuthInfo{},
			}),
			wantErr: true,
		},
		{
			name: "no verified chain",
			ctx: peer.NewContext(context.Background(), &peer.Peer{
				AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{}},
			}),
			wantErr: true,
		},
		{
			name: "verified chain names the node",
			ctx: peer.NewContext(context.Background(), &peer.Peer{
				AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
					VerifiedChains: [][]*x509.Certificate{{leaf}},
				}},
			}),
			wantID: "node-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := nodeid.FromPeer(tt.ctx)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("FromPeer() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("FromPeer() error = %v, want nil", err)
			}
			if id != tt.wantID {
				t.Errorf("FromPeer() = %q, want %q", id, tt.wantID)
			}
		})
	}
}

// insecureAuthInfo stands in for a non-TLS credentials.AuthInfo, so FromPeer
// can be exercised against a connection that never negotiated TLS at all.
type insecureAuthInfo struct{}

func (insecureAuthInfo) AuthType() string { return "insecure" }

func newTestLeaf(t *testing.T, nodeID string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: nodeID},
		URIs:         []*url.URL{nodeid.URI(nodeID)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("self-sign leaf: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return cert
}
