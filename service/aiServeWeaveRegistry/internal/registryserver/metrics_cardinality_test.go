package registryserver_test

import (
	"context"
	"crypto/tls"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/durationpb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/ca"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/identitystore"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/registryserver"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

// startRegistryWithMetrics is startRegistryWithGatewayToken (identity_test.go)
// with mx wired into registryserver.Config.Metrics, so this file's test can
// assert on what the real RPC methods actually recorded rather than what a
// hand-driven recorder call would record. It is a separate function rather
// than a new parameter on the shared helper so the many existing call sites
// of startRegistry/startRegistryWithGatewayToken stay untouched.
//
// startRegistryWithMetrics 是把 mx 接入 registryserver.Config.Metrics 的
// startRegistryWithGatewayToken(identity_test.go)。本文件的测试据此断言真实
// RPC 方法实际记录了什么，而不是手动调用 recorder 会记录什么。它是一个独立函数
// 而不是给共享 helper 加新参数，好让 startRegistry/startRegistryWithGatewayToken
// 现有的众多调用点保持不变。
func startRegistryWithMetrics(t *testing.T, mx *metricstest.Collector) *registryFixture {
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
		AdminToken: testAdminToken, Metrics: mx,
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

// TestMetricLabelValuesAreBounded is the executable form of metrics.go's
// closed result/operation/action vocabularies: it drives every RPC through a
// mix of success and failure branches, then asserts every label value any
// metric actually recorded is one of the constants those methods are allowed
// to pass — mirroring tunnelserver/metrics_test.go's
// TestMetricLabelValuesAreBounded for this package's own metrics.
//
// TestMetricLabelValuesAreBounded 是 metrics.go 封闭的 result/operation/action
// 取值集合的可执行版本：驱动每个 RPC 走一组成功与失败分支，然后断言任何指标实际
// 记录下的标签值，都落在这些方法被允许传入的那些常量里——对应本包自己的指标，
// 照抄 tunnelserver/metrics_test.go 的 TestMetricLabelValuesAreBounded。
func TestMetricLabelValuesAreBounded(t *testing.T) {
	mx := metricstest.New()
	f := startRegistryWithMetrics(t, mx)
	admin, ctx := tokenAdminClient(t, f)
	identity := rawIdentityClient(t, f)

	// MintToken: one success (used below), one failure (non-positive ttl).
	tok, err := admin.MintToken(ctx, &tunnelv1.MintTokenRequest{Ttl: durationpb.New(15 * time.Minute)})
	if err != nil {
		t.Fatalf("MintToken() error = %v", err)
	}
	if _, err := admin.MintToken(ctx, &tunnelv1.MintTokenRequest{Ttl: durationpb.New(0)}); err == nil {
		t.Fatal("MintToken() with a non-positive ttl = nil error, want a rejection")
	}

	// Register: success (new), reconnect (idempotent replay after approval,
	// via a fresh token bound to the same node_id), and a rejection (invalid
	// bootstrap token).
	f.approve(t, "node-a")
	if _, err := identity.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-a", Csr: newCSR(t), BootstrapToken: tok.GetToken(),
	}); err != nil {
		t.Fatalf("Register() first call error = %v", err)
	}
	tok2, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	if _, err := identity.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-a", Csr: newCSR(t), BootstrapToken: tok2,
	}); err == nil {
		t.Fatal("Register() with a different key for the same node_id = nil error, want a conflict")
	}
	if _, err := identity.Register(ctx, &tunnelv1.RegisterRequest{
		NodeId: "node-b", Csr: newCSR(t), BootstrapToken: "not-a-real-token",
	}); err == nil {
		t.Fatal("Register() with an invalid token = nil error, want a rejection")
	}

	// RenewCertificate: success, over the real mTLS wire path via the Agent's
	// own client code.
	tok3, err := f.tokens.Mint(15*time.Minute, time.Now())
	if err != nil {
		t.Fatalf("Mint() error = %v", err)
	}
	manager := newAgentIdentityManager(t, f, "node-c", tok3)
	f.approve(t, "node-c")
	id, err := manager.Ensure(ctx)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if _, err := manager.Renew(ctx, id); err != nil {
		t.Fatalf("Renew() error = %v", err)
	}

	// RevokeToken: success, then a not-found on a value that was never minted.
	if _, err := admin.RevokeToken(ctx, &tunnelv1.RevokeTokenRequest{Token: tok2}); err != nil {
		t.Fatalf("RevokeToken() error = %v", err)
	}
	if _, err := admin.RevokeToken(ctx, &tunnelv1.RevokeTokenRequest{Token: "never-minted"}); err == nil {
		t.Fatal("RevokeToken() for an unknown token = nil error, want not-found")
	}

	// DisableNode/EnableNode/ApproveNode/SetMaintenance/ClearMaintenance:
	// success each, plus one unauthorized call reusing a bad token.
	badCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer wrong-token")
	if _, err := admin.DisableNode(badCtx, &tunnelv1.DisableNodeRequest{NodeId: "node-a"}); err == nil {
		t.Fatal("DisableNode() with a bad token = nil error, want unauthorized")
	}
	if _, err := admin.DisableNode(ctx, &tunnelv1.DisableNodeRequest{NodeId: "node-a"}); err != nil {
		t.Fatalf("DisableNode() error = %v", err)
	}
	if _, err := admin.EnableNode(ctx, &tunnelv1.EnableNodeRequest{NodeId: "node-a"}); err != nil {
		t.Fatalf("EnableNode() error = %v", err)
	}
	f.approve(t, "node-d")
	if _, err := admin.ApproveNode(ctx, &tunnelv1.ApproveNodeRequest{NodeId: "node-d"}); err != nil {
		t.Fatalf("ApproveNode() error = %v", err)
	}
	if _, err := admin.SetMaintenance(ctx, &tunnelv1.SetMaintenanceRequest{NodeId: "node-a"}); err != nil {
		t.Fatalf("SetMaintenance() error = %v", err)
	}
	if _, err := admin.ClearMaintenance(ctx, &tunnelv1.ClearMaintenanceRequest{NodeId: "node-a"}); err != nil {
		t.Fatalf("ClearMaintenance() error = %v", err)
	}

	// ListNodeStates: success.
	if _, err := admin.ListNodeStates(ctx, &tunnelv1.ListNodeStatesRequest{}); err != nil {
		t.Fatalf("ListNodeStates() error = %v", err)
	}

	// Join: one replica joins, exercising the connected-replicas gauge.
	joined := dialJoin(t, f)
	joined.join("replica-a", "10.0.0.1:8443", tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE)
	joined.recv()

	allowed := map[string]map[string]bool{
		"result": {
			registryserver.ResultSuccess: true, registryserver.ResultReconnect: true,
			registryserver.ResultConflict: true, registryserver.ResultPendingApproval: true,
			registryserver.ResultInvalid: true, registryserver.ResultUnauthorized: true,
			registryserver.ResultNotFound: true, registryserver.ResultInternal: true,
		},
		"operation": {registryserver.TokenOpMint: true, registryserver.TokenOpRevoke: true},
		"action": {
			registryserver.NodeActionApprove: true, registryserver.NodeActionDisable: true,
			registryserver.NodeActionEnable: true, registryserver.NodeActionMaintenanceSet: true,
			registryserver.NodeActionMaintenanceClear: true,
		},
	}

	for _, s := range mx.All() {
		for key, value := range s.Labels {
			set, checked := allowed[key]
			if !checked {
				t.Errorf("metric %s has an unexpected label key %q", s.Name, key)
				continue
			}
			if !set[value] {
				t.Errorf("metric %s label %s = %q, which is not a bounded value", s.Name, key, value)
			}
		}
	}
}
