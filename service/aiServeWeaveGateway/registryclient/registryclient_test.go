package registryclient_test

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
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveGateway/internal/gatewaytest"
	"AIServeWeave/service/aiServeWeaveGateway/registryclient"
)

// fakeRosterSetter records every roster registryclient.Run relayed, so tests
// can assert on relayed content without a real tunnelserver.Server.
type fakeRosterSetter struct {
	mu       sync.Mutex
	received []*tunnelv1.GatewayRoster
}

func (f *fakeRosterSetter) SetRoster(r *tunnelv1.GatewayRoster) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, r)
}

func (f *fakeRosterSetter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func (f *fakeRosterSetter) last() *tunnelv1.GatewayRoster {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.received) == 0 {
		return nil
	}
	return f.received[len(f.received)-1]
}

// scriptedDirectory is a fake tunnelv1.GatewayDirectoryServer. Each Join call
// sends the next roster from rosters (the last one repeats if the caller
// reconnects more times than there are scripted rosters), then either keeps
// the stream open — recording every further message, e.g. a DRAINING notice
// — or closes it immediately, depending on closeAfterSend.
type scriptedDirectory struct {
	tunnelv1.UnimplementedGatewayDirectoryServer

	mu             sync.Mutex
	rosters        []*tunnelv1.GatewayRoster
	closeAfterSend bool
	connections    int
	firstRequests  []*tunnelv1.JoinRequest
	laterRequests  []*tunnelv1.JoinRequest
}

func (d *scriptedDirectory) Join(stream tunnelv1.GatewayDirectory_JoinServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}

	d.mu.Lock()
	idx := d.connections
	d.connections++
	d.firstRequests = append(d.firstRequests, first)
	roster := d.rosters[min(idx, len(d.rosters)-1)]
	closeAfterSend := d.closeAfterSend
	d.mu.Unlock()

	if err := stream.Send(roster); err != nil {
		return err
	}
	if closeAfterSend {
		return nil
	}

	for {
		req, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return nil
		}
		d.mu.Lock()
		d.laterRequests = append(d.laterRequests, req)
		d.mu.Unlock()
	}
}

func (d *scriptedDirectory) requests() (first, later []*tunnelv1.JoinRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*tunnelv1.JoinRequest(nil), d.firstRequests...), append([]*tunnelv1.JoinRequest(nil), d.laterRequests...)
}

// testCA is a throwaway certificate authority for the fake Registry's own
// server certificate, exactly the shape a real one takes (see
// service/aiServeWeaveRegistry/internal/ca).
type testCA struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "registryclient-test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
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
	return &testCA{key: key, cert: cert, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *testCA) issueServer(t *testing.T, host string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	// A dial target that is an IP literal is checked against IPAddresses,
	// not DNSNames, since Go 1.15: without this SAN the handshake fails
	// with "doesn't contain any IP SANs" no matter what DNSNames holds.
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("sign server certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("encode server key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("build server key pair: %v", err)
	}
	return pair
}

// startFakeRegistry starts dir behind a real TLS listener, mirroring exactly
// the transport registryclient.Run dials against.
func startFakeRegistry(t *testing.T, dir *scriptedDirectory) (addr, caFile string) {
	t.Helper()
	ca := newTestCA(t)
	cert := ca.issueServer(t, "127.0.0.1")

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}})
	srv := grpc.NewServer(grpc.Creds(creds))
	tunnelv1.RegisterGatewayDirectoryServer(srv, dir)
	go srv.Serve(lis)
	t.Cleanup(srv.GracefulStop)

	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, ca.pem, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	return lis.Addr().String(), caPath
}

func TestRunRelaysEveryRosterAndSendsDrainingOnShutdown(t *testing.T) {
	dir := &scriptedDirectory{
		rosters: []*tunnelv1.GatewayRoster{
			{Version: 1, Replicas: []*tunnelv1.GatewayReplica{{ReplicaId: "gw-1", Endpoint: "10.0.0.1:8443", State: tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE}}},
		},
	}
	addr, caFile := startFakeRegistry(t, dir)

	setter := &fakeRosterSetter{}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- registryclient.Run(ctx, registryclient.Config{
			Addr:      addr,
			CAFile:    caFile,
			ReplicaID: "replica-a",
			Endpoint:  "10.0.0.9:8443",
		}, setter)
	}()

	waitFor(t, func() bool { return setter.count() == 1 })
	if got := setter.last().GetReplicas()[0].GetReplicaId(); got != "gw-1" {
		t.Errorf("relayed roster replica_id = %q, want %q", got, "gw-1")
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil after ctx cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after ctx was canceled")
	}

	first, later := dir.requests()
	if len(first) != 1 || first[0].GetReplicaId() != "replica-a" || first[0].GetState() != tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE {
		t.Fatalf("first JoinRequest = %+v, want replica-a joining ACTIVE", first)
	}
	if len(later) != 1 || later[0].GetState() != tunnelv1.ReplicaState_REPLICA_STATE_DRAINING {
		t.Fatalf("later JoinRequests = %+v, want exactly one DRAINING notice before shutdown", later)
	}
}

func TestRunReconnectsAfterTheStreamCloses(t *testing.T) {
	dir := &scriptedDirectory{
		rosters: []*tunnelv1.GatewayRoster{
			{Version: 1, Replicas: []*tunnelv1.GatewayReplica{{ReplicaId: "gw-1"}}},
			{Version: 2, Replicas: []*tunnelv1.GatewayReplica{{ReplicaId: "gw-1"}, {ReplicaId: "gw-2"}}},
		},
		closeAfterSend: true,
	}
	addr, caFile := startFakeRegistry(t, dir)

	setter := &fakeRosterSetter{}
	clock := gatewaytest.NewClock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- registryclient.Run(ctx, registryclient.Config{
			Addr:      addr,
			CAFile:    caFile,
			ReplicaID: "replica-a",
			Endpoint:  "10.0.0.9:8443",
			Clock:     clock,
		}, setter)
	}()

	waitFor(t, func() bool { return setter.count() == 1 })

	// Run is now asleep in its backoff timer, waiting to reconnect. A short
	// real sleep lets the goroutine reach clock.NewTimer before Advance is
	// called; the fake clock, not a real one, is what actually paces the
	// retry.
	time.Sleep(20 * time.Millisecond)
	clock.Advance(2 * time.Second)

	waitFor(t, func() bool { return setter.count() == 2 })
	if got := len(setter.last().GetReplicas()); got != 2 {
		t.Fatalf("roster after reconnect has %d replicas, want 2", got)
	}

	cancel()
	select {
	case <-runErr:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after ctx was canceled")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not met before the deadline")
}
