// Package registryserver implements the Registry's gRPC surface: NodeIdentity
// (certificate issuance and renewal), GatewayDirectory (the Gateway replica
// roster), and TokenAdmin (bootstrap-token mint/revoke, STATUS.md's S02).
// All three are thin adapters over ca.CA, tokenstore.Store and
// identitystore.Store — this package owns request validation and wire
// shapes, not cryptography or storage.
package registryserver

import (
	"log/slog"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/ca"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/identitystore"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

// Server implements both tunnelv1.NodeIdentityServer and
// tunnelv1.GatewayDirectoryServer. The zero value is not usable; construct
// one with New.
type Server struct {
	tunnelv1.UnimplementedNodeIdentityServer
	tunnelv1.UnimplementedGatewayDirectoryServer
	tunnelv1.UnimplementedTokenAdminServer

	ca           *ca.CA
	tokens       *tokenstore.Store
	identities   *identitystore.Store
	clock        runtime.Clock
	logger       *slog.Logger
	adminToken   string
	gatewayToken string

	roster rosterState
}

// Config supplies Server's dependencies.
type Config struct {
	CA     *ca.CA
	Tokens *tokenstore.Store
	// Identities tracks which public key each node_id was last issued a
	// certificate for, so Register (STATUS.md's S01) can tell a harmless
	// reconnect from a node_id reappearing under a different key. Required,
	// like CA and Tokens: Register has nowhere else to keep this state.
	//
	// Identities 追踪每个 node_id 最近一次被签发证书时绑定的是哪把公钥，
	// 好让 Register（STATUS.md 的 S01）能分辨一次无害的重连与一个 node_id
	// 带着不同的 key 再次出现。与 CA、Tokens 一样是必需的：Register 没有
	// 别的地方保存这份状态。
	Identities *identitystore.Store

	// Clock supplies time. Nil uses the system clock; tests inject a fake so
	// token expiry and certificate timestamps are exercised without
	// sleeping.
	Clock runtime.Clock

	// Logger receives request-lifecycle events. Nil discards them.
	Logger *slog.Logger

	// AdminToken guards MintToken, RevokeToken, DisableNode and EnableNode
	// (STATUS.md's S02/S03). Empty disables all four — every call is
	// refused — which is also why main.go only registers
	// tunnelv1.TokenAdminServer with the gRPC server when an operator has
	// actually configured -admin-token-file; leaving it unregistered when
	// there is no secret to check is the belt to this field's suspenders.
	AdminToken string

	// GatewayToken guards GatewayDirectory.Join (STATUS.md's S03), separate
	// from AdminToken because a Gateway replica only needs to prove it may
	// join the roster, not mint/revoke tokens or disable nodes — handing
	// every replica the wider AdminToken would make its blast radius the
	// same as an operator's. Empty leaves Join open to anyone who can reach
	// the Registry's gRPC listener, matching this method's behavior before
	// S03; GatewayDirectory stays registered either way, since Join is core
	// functionality a Gateway cannot run without, unlike TokenAdmin's
	// optional registration.
	GatewayToken string
}

// New returns a Server ready to be registered with a gRPC server.
func New(cfg Config) (*Server, error) {
	if cfg.CA == nil {
		return nil, errMissing("CA")
	}
	if cfg.Tokens == nil {
		return nil, errMissing("Tokens")
	}
	if cfg.Identities == nil {
		return nil, errMissing("Identities")
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
		ca:           cfg.CA,
		tokens:       cfg.Tokens,
		identities:   cfg.Identities,
		clock:        clock,
		logger:       logger,
		adminToken:   cfg.AdminToken,
		gatewayToken: cfg.GatewayToken,
	}
	s.roster.replicas = make(map[string]*tunnelv1.GatewayReplica)
	// Seed the revoked set from what was already disabled in a prior run, so
	// the first roster this process broadcasts already reflects it instead
	// of a freshly started Registry looking like it forgot every disable
	// until the next DisableNode/EnableNode call.
	revoked := make(map[string]struct{})
	for _, id := range cfg.Identities.DisabledNodeIDs() {
		revoked[id] = struct{}{}
	}
	s.roster.revoked = revoked
	return s, nil
}

func errMissing(field string) error {
	return &missingConfigError{field: field}
}

type missingConfigError struct{ field string }

func (e *missingConfigError) Error() string {
	return "registryserver: Config." + e.field + " is required"
}
