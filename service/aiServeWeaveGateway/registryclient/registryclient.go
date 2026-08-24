// Package registryclient is this Gateway replica's side of the Registry's
// GatewayRoster: it joins tunnelv1.GatewayDirectory, feeds every roster the
// Registry pushes into a *tunnelserver.Server, and reconnects with backoff
// when the connection drops. Before this package existed, the roster a
// replica relayed to its Agents had to be injected by hand through
// tunnelserver.Server.SetRoster; this is the caller SetRoster was left
// waiting for.
package registryclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// RosterSetter receives every roster this client learns from the Registry.
// *tunnelserver.Server satisfies it; tests substitute a fake so they do not
// need a real Server to observe what Run relayed.
type RosterSetter interface {
	SetRoster(*tunnelv1.GatewayRoster)
}

// Config configures Run.
type Config struct {
	// Addr is the Registry's GatewayDirectory endpoint, host:port.
	Addr string
	// CAFile is the PEM bundle used to verify the Registry's server
	// certificate. Empty uses the host's root store, which only works when
	// the Registry's certificate chains to a public CA.
	CAFile string
	// ReplicaID and Endpoint are this replica's own identity and dial
	// address, exactly as reported in JoinRequest and as configured on
	// tunnelserver.Config.
	ReplicaID string
	Endpoint  string

	// Clock supplies time and reconnect timers. Nil uses the system clock;
	// tests inject a fake so backoff is exercised without sleeping.
	Clock runtime.Clock
	// Logger receives connection lifecycle events. Nil discards them.
	Logger *slog.Logger
}

// backoffInitial and backoffMax match the full-jitter reconnect formula the
// tunnel README documents for Agent-to-Gateway reconnects
// (service/aiServeWeaveAgent/tunnel/backoff.go): rand(0, min(30s, 1s*2^n)).
// Reusing the same shape here means a Registry restart and a Gateway restart
// back off in a way operators already recognize.
const (
	backoffInitial = time.Second
	backoffMax     = 30 * time.Second
)

// Run dials the Registry's GatewayDirectory, joins as cfg.ReplicaID, and
// feeds every roster it receives to server.SetRoster. It reconnects with
// full-jitter backoff on any failure and blocks until ctx is canceled, at
// which point it sends a DRAINING notice on the current stream (best effort,
// so Agents that have not yet connected to this replica learn not to) before
// returning nil.
func Run(ctx context.Context, cfg Config, server RosterSetter) error {
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	creds, err := clientCredentials(cfg.CAFile)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(cfg.Addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := tunnelv1.NewGatewayDirectoryClient(conn)

	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := joinOnce(ctx, client, cfg, server, logger)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			logger.Warn("registry roster stream failed; reconnecting",
				slog.String("registry_addr", cfg.Addr), slog.String("error", err.Error()))
		}
		attempt++
		if !sleepBackoff(ctx, clock, attempt) {
			return nil
		}
	}
}

// joinOnce runs one Join call to completion: it registers, relays roster
// updates until the stream fails or ctx is canceled, and on cancellation
// sends a best-effort DRAINING notice before returning.
func joinOnce(ctx context.Context, client tunnelv1.GatewayDirectoryClient, cfg Config, server RosterSetter, logger *slog.Logger) error {
	// The stream is opened on its own cancelable context, detached from ctx:
	// canceling ctx directly would abort the stream before the DRAINING
	// notice below has a chance to reach the wire. streamCancel is what
	// actually tears the stream down, once that notice is sent.
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()

	stream, err := client.Join(streamCtx)
	if err != nil {
		return err
	}
	if err := stream.Send(&tunnelv1.JoinRequest{
		ReplicaId: cfg.ReplicaID,
		Endpoint:  cfg.Endpoint,
		State:     tunnelv1.ReplicaState_REPLICA_STATE_ACTIVE,
	}); err != nil {
		return err
	}
	logger.Info("joined registry roster", slog.String("registry_addr", cfg.Addr), slog.String("replica_id", cfg.ReplicaID))

	recvErr := make(chan error, 1)
	go func() {
		for {
			roster, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			server.SetRoster(roster)
		}
	}()

	select {
	case <-ctx.Done():
		_ = stream.Send(&tunnelv1.JoinRequest{State: tunnelv1.ReplicaState_REPLICA_STATE_DRAINING})
		_ = stream.CloseSend()
		streamCancel()
		return nil
	case err := <-recvErr:
		if errors.Is(err, io.EOF) {
			return errors.New("registry closed the roster stream")
		}
		return err
	}
}

// sleepBackoff waits a full-jitter interval before the next reconnect
// attempt and reports whether the caller should retry (false means ctx was
// canceled while waiting).
func sleepBackoff(ctx context.Context, clock runtime.Clock, attempt int) bool {
	window := backoffInitial << min(attempt, 30)
	if window <= 0 || window > backoffMax {
		window = backoffMax
	}
	delay := time.Duration(rand.Float64() * float64(window))
	ch, stop := clock.NewTimer(delay)
	defer stop()
	select {
	case <-ctx.Done():
		return false
	case <-ch:
		return true
	}
}

func clientCredentials(caFile string) (credentials.TransportCredentials, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("registryclient: no certificate found in " + caFile)
		}
		tlsCfg.RootCAs = pool
	}
	return credentials.NewTLS(tlsCfg), nil
}
