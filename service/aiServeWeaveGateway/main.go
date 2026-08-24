// Command aiserveweave-gateway runs the data-plane gateway: it terminates the
// OpenAI-compatible API, schedules each request onto a node through the
// tunnel, and authenticates callers with a static API key list.
//
// Both sides are implemented: this binary listens for Agents over mutually
// authenticated TLS and tracks what each connected node can serve, and it
// binds an HTTP listener serving GET /v1/models, POST /v1/chat/completions
// (streaming and non-streaming) and POST /v1/embeddings, dispatching through
// a scheduler.Scheduler over that same tunnel state.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"AIServeWeave/service/aiServeWeaveGateway/registryclient"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
	"AIServeWeave/service/aiServeWeaveGateway/tunnelserver"
)

// drainGrace is how long connected Agents are given to finish in-flight
// requests when this replica shuts down. It is generous on purpose: cutting a
// request short to shut down a second sooner trades a user's answer for
// nothing.
const drainGrace = 30 * time.Second

func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString("gateway: " + err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, error")
	addr := flag.String("addr", ":8080", "address the HTTP API listener binds")
	apiKeys := flag.String("api-keys", "",
		"comma-separated API keys the HTTP API accepts as a Bearer token; empty disables authentication (local use only)")
	tunnelAddr := flag.String("tunnel-addr", "", "address the tunnel listener binds, e.g. :8443; empty disables the tunnel")
	certFile := flag.String("tls-cert", "", "PEM certificate this replica presents to Agents")
	keyFile := flag.String("tls-key", "", "PEM private key for -tls-cert")
	clientCAFile := flag.String("client-ca", "", "PEM CA bundle that node certificates must chain to")
	replicaID := flag.String("replica-id", "", "identity announced to Agents; defaults to the hostname")
	registryAddr := flag.String("registry-addr", "", "Registry GatewayDirectory endpoint, host:port; empty leaves the roster to be set manually via SetRoster")
	registryCA := flag.String("registry-ca", "", "PEM CA bundle verifying the Registry's server certificate")
	advertiseAddr := flag.String("tunnel-advertise-addr", "", "address Agents should dial to reach this replica's tunnel listener; defaults to -tunnel-addr, which is wrong once NAT or a load balancer sits in front of it")
	flag.Parse()

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	id := *replicaID
	if id == "" {
		host, err := os.Hostname()
		if err != nil {
			return errors.New("no -replica-id and the hostname is unavailable: " + err.Error())
		}
		id = host
	}

	server, err := tunnelserver.New(tunnelserver.Config{
		ReplicaID: id,
		Logger:    logger,
		SlotHint: &tunnelv1.SlotHint{
			MinSlots:  2,
			MaxSlots:  8,
			BulkSlots: 1,
		},
	})
	if err != nil {
		return err
	}

	sched := scheduler.New(server, scheduler.Config{})
	httpServer := &http.Server{
		Addr:    *addr,
		Handler: httpapi.New(sched, httpapi.Config{APIKeys: splitCommaList(*apiKeys), Logger: logger}),
	}
	httpServeErr := make(chan error, 1)
	go func() { httpServeErr <- httpServer.ListenAndServe() }()
	logger.Info("gateway started", slog.String("addr", *addr), slog.String("replica_id", id))

	var (
		grpcServer *grpc.Server
		tunnelLis  net.Listener
		tunnelErr  = make(chan error, 1)
	)
	if *tunnelAddr == "" {
		logger.Warn("no -tunnel-addr; no Agent can connect to this replica")
	} else {
		creds, err := tunnelCredentials(*certFile, *keyFile, *clientCAFile)
		if err != nil {
			return err
		}
		tunnelLis, err = net.Listen("tcp", *tunnelAddr)
		if err != nil {
			return err
		}
		grpcServer = grpc.NewServer(
			grpc.Creds(creds),
			// A home network's NAT ages an idle mapping out in minutes. The
			// transport-level ping keeps the mapping alive from this side
			// too, so a silent tunnel does not quietly stop existing.
			grpc.KeepaliveParams(keepalive.ServerParameters{
				Time:    20 * time.Second,
				Timeout: 10 * time.Second,
			}),
			grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
				MinTime:             10 * time.Second,
				PermitWithoutStream: true,
			}),
		)
		tunnelv1.RegisterTunnelServer(grpcServer, server)
		go func() { tunnelErr <- grpcServer.Serve(tunnelLis) }()
		logger.Info("tunnel listening",
			slog.String("replica_id", id),
			slog.String("tunnel_addr", tunnelLis.Addr().String()))
	}

	registryErr := make(chan error, 1)
	if *registryAddr != "" {
		endpoint := *advertiseAddr
		if endpoint == "" {
			endpoint = *tunnelAddr
		}
		go func() {
			registryErr <- registryclient.Run(ctx, registryclient.Config{
				Addr:      *registryAddr,
				CAFile:    *registryCA,
				ReplicaID: id,
				Endpoint:  endpoint,
				Logger:    logger,
			}, server)
		}()
		logger.Info("joining registry roster", slog.String("registry_addr", *registryAddr))
	} else {
		logger.Warn("no -registry-addr; the replica roster must be set manually via Server.SetRoster")
	}

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connected nodes")
	case err := <-httpServeErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case err := <-tunnelErr:
		if err != nil {
			return err
		}
	case err := <-registryErr:
		if err != nil {
			return err
		}
	}

	// The HTTP API is shut down first, and given the tunnel's own drain
	// grace to finish in-flight requests: those requests are still running
	// on the tunnel, so closing the tunnel first would cut them short
	// instead of letting them complete.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainGrace)
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP API did not shut down within the grace period", slog.Any("error", err))
	}
	cancel()

	if grpcServer != nil {
		// Ask the Agents to leave first, then stop accepting: GracefulStop
		// alone would wait for streams that are long-lived by design and
		// never end on their own.
		server.Close("gateway replica shutting down", drainGrace)
		stopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(drainGrace):
			logger.Warn("nodes did not disconnect within the grace period; closing the listener")
			grpcServer.Stop()
			<-stopped
		}
	}

	logger.Info("gateway stopped")
	return nil
}

// splitCommaList parses a comma-separated flag value, trimming whitespace
// and dropping empty entries.
func splitCommaList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// tunnelCredentials builds the replica's mTLS configuration. All three files
// are required: a tunnel listener without client verification would
// authenticate nobody, since the server reads the node identity only from a
// verified certificate chain.
func tunnelCredentials(certFile, keyFile, clientCAFile string) (credentials.TransportCredentials, error) {
	if certFile == "" || keyFile == "" || clientCAFile == "" {
		return nil, errors.New("-tunnel-addr requires -tls-cert, -tls-key and -client-ca: without client verification no Agent can be authenticated")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no certificate found in " + clientCAFile)
	}
	return credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}), nil
}
