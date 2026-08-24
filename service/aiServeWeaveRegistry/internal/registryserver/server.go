// Package registryserver implements the Registry's gRPC surface: NodeIdentity
// (certificate issuance and renewal) and GatewayDirectory (the Gateway
// replica roster). Both are thin adapters over ca.CA and tokenstore.Store —
// this package owns request validation and wire shapes, not cryptography or
// storage.
package registryserver

import (
	"log/slog"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/ca"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

// Server implements both tunnelv1.NodeIdentityServer and
// tunnelv1.GatewayDirectoryServer. The zero value is not usable; construct
// one with New.
type Server struct {
	tunnelv1.UnimplementedNodeIdentityServer
	tunnelv1.UnimplementedGatewayDirectoryServer

	ca     *ca.CA
	tokens *tokenstore.Store
	clock  runtime.Clock
	logger *slog.Logger

	roster rosterState
}

// Config supplies Server's dependencies.
type Config struct {
	CA     *ca.CA
	Tokens *tokenstore.Store

	// Clock supplies time. Nil uses the system clock; tests inject a fake so
	// token expiry and certificate timestamps are exercised without
	// sleeping.
	Clock runtime.Clock

	// Logger receives request-lifecycle events. Nil discards them.
	Logger *slog.Logger
}

// New returns a Server ready to be registered with a gRPC server.
func New(cfg Config) (*Server, error) {
	if cfg.CA == nil {
		return nil, errMissing("CA")
	}
	if cfg.Tokens == nil {
		return nil, errMissing("Tokens")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		ca:     cfg.CA,
		tokens: cfg.Tokens,
		clock:  clock,
		logger: logger,
	}
	s.roster.replicas = make(map[string]*tunnelv1.GatewayReplica)
	return s, nil
}

func errMissing(field string) error {
	return &missingConfigError{field: field}
}

type missingConfigError struct{ field string }

func (e *missingConfigError) Error() string {
	return "registryserver: Config." + e.field + " is required"
}
