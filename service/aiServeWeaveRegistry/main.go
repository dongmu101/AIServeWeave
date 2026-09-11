// Command aiserveweave-registry runs the control-plane registry: it signs
// node certificates against a one-time bootstrap token and maintains the
// authoritative roster of Gateway replicas that every node's tunnel client
// dials against.
//
// The same binary doubles as the bootstrap-token admin tool via -mint-token
// and -revoke-token, standing in for the Console described in the top-level
// README until that exists. Both are gRPC clients of the running server's
// TokenAdmin service (STATUS.md's S02), not direct file access: minting and
// revocation both need to observe and update the same mutex-guarded state
// Register consumes, which only the server process can do safely — see
// service/aiServeWeaveRegistry/README.md.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/durationpb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/metrics"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/ca"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/identitystore"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/registryserver"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

// minAdminTokenLen matches the control plane's OperatorToken minimum
// (internal/config/config.go's Validate), the codebase's existing
// admin-shared-secret convention, so the two do not drift apart.
const minAdminTokenLen = 32

// version is stamped at build time via -ldflags="-X main.version=...", see
// the root Dockerfile and scripts/build-release.sh; "dev" is what a plain
// `go build` produces.
//
// version 在构建时通过 -ldflags="-X main.version=..." 注入，见根 Dockerfile 与
// scripts/build-release.sh；直接 `go build` 得到的就是 "dev"。
var version = "dev"

func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString("registry: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	addr := flag.String("addr", ":9090", "address the gRPC listener binds")
	metricsAddr := flag.String("metrics-addr", "127.0.0.1:9091",
		"address the Prometheus /metrics listener binds; loopback by default, empty disables it")
	dataDir := flag.String("data-dir", "./data/registry", "directory holding the CA key pair, the bootstrap token store, and the node identity ledger")
	tlsHosts := flag.String("tls-host", "", "comma-separated hostnames/IPs the self-issued server certificate covers; empty uses the -addr host")
	certFile := flag.String("tls-cert", "", "PEM certificate this Registry presents; empty self-issues one from its own CA")
	keyFile := flag.String("tls-key", "", "PEM private key for -tls-cert")
	adminTokenFile := flag.String("admin-token-file", "",
		"path to the shared secret guarding TokenAdmin (mint/revoke/disable/enable), at least 32 bytes; server mode: empty disables the service; -mint-token/-revoke-token/-disable-node/-enable-node: always required")
	gatewayTokenFile := flag.String("gateway-token-file", "",
		"path to the shared secret guarding GatewayDirectory.Join (STATUS.md's S03), at least 32 bytes; server mode: empty leaves Join open, as before S03")
	mintToken := flag.Bool("mint-token", false, "call TokenAdmin.MintToken on a running Registry and print the token to stdout, instead of running the server")
	tokenTTL := flag.Duration("ttl", 15*time.Minute, "-mint-token only: how long the minted token stays valid")
	bindNodeID := flag.String("bind-node-id", "", "-mint-token only: bind the minted token to this node_id, authorizing a reinstall (see the Registry README)")
	revokeToken := flag.String("revoke-token", "", "call TokenAdmin.RevokeToken on a running Registry for this token value, instead of running the server")
	disableNode := flag.String("disable-node", "", "call TokenAdmin.DisableNode on a running Registry for this node_id, instead of running the server")
	enableNode := flag.String("enable-node", "", "call TokenAdmin.EnableNode on a running Registry for this node_id, instead of running the server")
	registryAddr := flag.String("registry-addr", "", "-mint-token/-revoke-token/-disable-node/-enable-node only: address of the running Registry to call; empty derives it from -addr")
	issueServerCert := flag.Bool("issue-server-cert", false,
		"issue a server certificate from this Registry's CA and write it to -out-dir instead of running the server")
	outDir := flag.String("out-dir", "", "-issue-server-cert only: directory to write server-cert.pem and server-key.pem into")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		os.Stdout.WriteString("aiserveweave-registry " + version + "\n")
		return nil
	}

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	if *mintToken || *revokeToken != "" || *disableNode != "" || *enableNode != "" {
		return runTokenAdminClient(os.Stdout, *dataDir, *registryAddr, *addr, *adminTokenFile, tokenAdminAction{
			mint: *mintToken, ttl: *tokenTTL, bindNodeID: *bindNodeID, revoke: *revokeToken,
			disableNodeID: *disableNode, enableNodeID: *enableNode,
		})
	}

	root, err := ca.LoadOrCreate(filepath.Join(*dataDir, "ca"))
	if err != nil {
		return err
	}

	if *issueServerCert {
		return runIssueServerCert(root, *outDir, splitCommaList(*tlsHosts))
	}

	tokens, err := tokenstore.Open(filepath.Join(*dataDir, "tokens.json"))
	if err != nil {
		return err
	}
	identities, err := identitystore.Open(filepath.Join(*dataDir, "identities.json"))
	if err != nil {
		return err
	}
	adminToken, err := loadAdminToken(*adminTokenFile)
	if err != nil {
		return err
	}
	gatewayToken, err := loadSharedSecret(*gatewayTokenFile, "-gateway-token-file")
	if err != nil {
		return err
	}

	registry := metrics.New(registryserver.Descriptions())

	server, err := registryserver.New(registryserver.Config{
		CA: root, Tokens: tokens, Identities: identities, Logger: logger,
		AdminToken: adminToken, GatewayToken: gatewayToken,
		Metrics: registry,
	})
	if err != nil {
		return err
	}

	creds, err := serverCredentials(root, *certFile, *keyFile, *addr, *tlsHosts)
	if err != nil {
		return err
	}

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer(grpc.Creds(creds))
	tunnelv1.RegisterNodeIdentityServer(grpcServer, server)
	tunnelv1.RegisterGatewayDirectoryServer(grpcServer, server)
	if adminToken != "" {
		tunnelv1.RegisterTokenAdminServer(grpcServer, server)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var metricsServer *http.Server
	if *metricsAddr == "" {
		logger.Warn("no -metrics-addr; this replica exports no metrics")
	} else {
		metricsServer = registry.Server(*metricsAddr)
		go func() {
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics listener stopped", slog.Any("error", err))
			}
		}()
		logger.Info("metrics listening", slog.String("metrics_addr", *metricsAddr))
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcServer.Serve(lis) }()
	logger.Info("registry started", slog.String("addr", lis.Addr().String()), slog.String("data_dir", *dataDir))

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			return err
		}
	}

	grpcServer.GracefulStop()
	// Closed last, after the gRPC listener has drained, so a scrape mid-drain
	// still sees the connected-replica gauge go to zero rather than reporting
	// stale capacity for a replica that is already gone.
	//
	// 在 gRPC 监听器排空之后才关闭，这样一次落在排空过程中的抓取，看到的是已经
	// 归零的已连接副本量表，而不是一个其实已经离开的副本留下的过期容量。
	if metricsServer != nil {
		_ = metricsServer.Close()
	}
	logger.Info("registry stopped")
	return nil
}

// runIssueServerCert writes a server certificate signed by this Registry's CA.
//
// It exists because the Gateway's tunnel listener needs one and has nowhere
// else to get it: an Agent verifies that listener against the Registry CA
// (tunnel/identity.go's TLSConfig), so a certificate from any other authority
// is one every Agent will refuse. The Registry signs node certificates over
// RPC, but those are ClientAuth and carry a node SAN — they are not what a
// listener presents.
//
// Like -mint-token this is a CLI mode rather than an RPC, and for the same
// reason: it is an operator action performed once at deployment time, by
// somebody who already has filesystem access to the CA.
//
// runIssueServerCert 写出一张由本 Registry 的 CA 签发的服务端证书。
//
// 它之所以存在，是因为 Gateway 的隧道监听器需要一张，而别处拿不到：Agent 用 Registry
// CA 来验证那个监听器（tunnel/identity.go 的 TLSConfig），因此任何其他签发方的证书都
// 会被每一个 Agent 拒绝。Registry 通过 RPC 签发的是节点证书，那是 ClientAuth 且带节点
// SAN——不是监听器该出示的东西。
//
// 与 -mint-token 一样，它是 CLI 模式而不是 RPC，理由相同：这是部署时执行一次的运维
// 动作，执行者本来就对 CA 有文件系统访问权。
func runIssueServerCert(root *ca.CA, outDir string, hosts []string) error {
	if outDir == "" {
		return errors.New("-issue-server-cert needs -out-dir")
	}
	if len(hosts) == 0 {
		return errors.New("-issue-server-cert needs -tls-host, e.g. -tls-host gateway,127.0.0.1")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	certPEM, keyPEM, notAfter, err := root.IssueServerCert(hosts, time.Now())
	if err != nil {
		return err
	}
	certPath := filepath.Join(outDir, "server-cert.pem")
	keyPath := filepath.Join(outDir, "server-key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return err
	}
	// The private key is 0600 for the same reason the CA key is: a listener's
	// key is as good as the listener to anyone who reads it.
	//
	// 私钥是 0600，理由与 CA 私钥相同：对任何读到它的人来说，一个监听器的私钥就等同于
	// 那个监听器本身。
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s and %s for %v, valid until %s\n",
		certPath, keyPath, hosts, notAfter.UTC().Format(time.RFC3339))
	return nil
}

// tokenAdminAction selects and parameterizes runTokenAdminClient's single
// call: exactly one of mint or revoke is meaningful, mirroring how -mint-token
// and -revoke-token are themselves mutually exclusive CLI modes.
type tokenAdminAction struct {
	mint          bool
	ttl           time.Duration
	bindNodeID    string
	revoke        string
	disableNodeID string
	enableNodeID  string
}

// runTokenAdminClient dials a running Registry's TokenAdmin service and
// performs one mint or revoke call, printing the result to out. It is a
// gRPC client rather than direct file access — see this file's package doc
// comment for why that is the point of S02, not an incidental implementation
// choice.
func runTokenAdminClient(out io.Writer, dataDir, registryAddr, listenAddr, adminTokenFile string, action tokenAdminAction) error {
	adminToken, err := loadAdminToken(adminTokenFile)
	if err != nil {
		return err
	}
	if adminToken == "" {
		return errors.New("-mint-token/-revoke-token/-disable-node/-enable-node needs -admin-token-file")
	}

	root, err := ca.LoadOrCreate(filepath.Join(dataDir, "ca"))
	if err != nil {
		return err
	}
	target := registryAddr
	if target == "" {
		target = defaultRegistryAddr(listenAddr)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(root.Bundle()) {
		return errors.New("registry: CA bundle contains no usable certificate")
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
	})))
	if err != nil {
		return fmt.Errorf("registry: cannot create a client for %s: %w", target, err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+adminToken)
	client := tunnelv1.NewTokenAdminClient(conn)

	if action.mint {
		resp, err := client.MintToken(ctx, &tunnelv1.MintTokenRequest{
			Ttl:    durationpb.New(action.ttl),
			NodeId: action.bindNodeID,
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(out, resp.GetToken())
		return nil
	}

	if action.revoke != "" {
		if _, err := client.RevokeToken(ctx, &tunnelv1.RevokeTokenRequest{Token: action.revoke}); err != nil {
			return err
		}
		fmt.Fprintln(out, "revoked")
		return nil
	}

	if action.disableNodeID != "" {
		if _, err := client.DisableNode(ctx, &tunnelv1.DisableNodeRequest{NodeId: action.disableNodeID}); err != nil {
			return err
		}
		fmt.Fprintln(out, "disabled")
		return nil
	}

	if _, err := client.EnableNode(ctx, &tunnelv1.EnableNodeRequest{NodeId: action.enableNodeID}); err != nil {
		return err
	}
	fmt.Fprintln(out, "enabled")
	return nil
}

// loadAdminToken reads the TokenAdmin shared secret from path, or returns ""
// without error if path is empty — the server-mode caller takes that as
// "leave TokenAdmin disabled," and the CLI-client-mode caller rejects it
// itself, since a secret is not optional there.
func loadAdminToken(path string) (string, error) {
	return loadSharedSecret(path, "-admin-token-file")
}

// loadSharedSecret reads a bearer-token secret from path, or returns "" without
// error if path is empty — every caller here treats an empty secret as "leave
// this guard disabled," not as a malformed configuration. flagName only
// appears in error messages, so a misconfigured file names the flag that
// needs fixing.
func loadSharedSecret(path, flagName string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("registry: cannot read %s %s: %w", flagName, path, err)
	}
	token := strings.TrimSpace(string(data))
	if len(token) < minAdminTokenLen {
		return "", fmt.Errorf("registry: %s %s must contain a secret at least %d bytes long", flagName, path, minAdminTokenLen)
	}
	return token, nil
}

// serverCredentials builds the Registry's own listener TLS configuration.
// Register is the one method that accepts a connection with no client
// certificate, so client verification is optional rather than required:
// VerifyClientCertIfGiven still validates a certificate a node does present,
// which is what RenewCertificate depends on.
func serverCredentials(root *ca.CA, certFile, keyFile, addr, tlsHosts string) (credentials.TransportCredentials, error) {
	certPEM, keyPEM, err := loadOrIssueServerCert(root, certFile, keyFile, addr, tlsHosts)
	if err != nil {
		return nil, err
	}
	cert, err := tlsCertificate(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	cfg := tlsConfig(cert, root)
	return credentials.NewTLS(&cfg), nil
}

func loadOrIssueServerCert(root *ca.CA, certFile, keyFile, addr, tlsHosts string) (certPEM, keyPEM []byte, err error) {
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, nil, errors.New("-tls-cert and -tls-key must be set together")
		}
		certPEM, err = os.ReadFile(certFile)
		if err != nil {
			return nil, nil, err
		}
		keyPEM, err = os.ReadFile(keyFile)
		if err != nil {
			return nil, nil, err
		}
		return certPEM, keyPEM, nil
	}
	hosts := splitCommaList(tlsHosts)
	if len(hosts) == 0 {
		hosts = []string{addrHost(addr)}
	}
	certPEM, keyPEM, _, err = root.IssueServerCert(hosts, time.Now())
	return certPEM, keyPEM, err
}
