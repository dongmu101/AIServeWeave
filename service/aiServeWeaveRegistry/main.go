// Command aiserveweave-registry runs the control-plane registry: it signs
// node certificates against a one-time bootstrap token and maintains the
// authoritative roster of Gateway replicas that every node's tunnel client
// dials against.
//
// The same binary doubles as the bootstrap-token minting tool via -mint-token,
// standing in for the Console described in the top-level README until that
// exists — see service/aiServeWeaveRegistry/README.md for its limitations.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/ca"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/registryserver"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString("registry: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	addr := flag.String("addr", ":9090", "address the gRPC listener binds")
	dataDir := flag.String("data-dir", "./data/registry", "directory holding the CA key pair and the bootstrap token store")
	tlsHosts := flag.String("tls-host", "", "comma-separated hostnames/IPs the self-issued server certificate covers; empty uses the -addr host")
	certFile := flag.String("tls-cert", "", "PEM certificate this Registry presents; empty self-issues one from its own CA")
	keyFile := flag.String("tls-key", "", "PEM private key for -tls-cert")
	mintToken := flag.Bool("mint-token", false, "mint a bootstrap token and print it to stdout instead of running the server")
	tokenTTL := flag.Duration("ttl", 15*time.Minute, "-mint-token only: how long the minted token stays valid")
	flag.Parse()

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	tokens, err := tokenstore.Open(filepath.Join(*dataDir, "tokens.json"))
	if err != nil {
		return err
	}

	if *mintToken {
		return runMintToken(tokens, *tokenTTL)
	}

	root, err := ca.LoadOrCreate(filepath.Join(*dataDir, "ca"))
	if err != nil {
		return err
	}

	server, err := registryserver.New(registryserver.Config{CA: root, Tokens: tokens, Logger: logger})
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	logger.Info("registry stopped")
	return nil
}

// runMintToken mints one bootstrap token and prints it to stdout. It is a
// transitional operator tool, not an RPC: see the Registry README for why
// running it alongside a live server carries a small, accepted race on the
// shared token file.
func runMintToken(tokens *tokenstore.Store, ttl time.Duration) error {
	token, err := tokens.Mint(ttl, time.Now())
	if err != nil {
		return err
	}
	os.Stdout.WriteString(token + "\n")
	return nil
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
