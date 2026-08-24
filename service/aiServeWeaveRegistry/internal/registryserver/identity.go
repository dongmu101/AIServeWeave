package registryserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/nodeid"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

// Register implements tunnelv1.NodeIdentityServer. It is the only method in
// the whole contract that accepts a connection with no client certificate:
// authentication here comes entirely from the one-time bootstrap token.
func (s *Server) Register(ctx context.Context, req *tunnelv1.RegisterRequest) (*tunnelv1.RegisterResponse, error) {
	now := s.clock.Now()
	// The token is never logged, on success or failure: it is a bearer
	// credential until consumed, exactly like the API keys AGENTS.md's
	// security rules cover.
	if err := s.tokens.Consume(req.GetBootstrapToken(), now); err != nil {
		if errors.Is(err, tokenstore.ErrInvalidToken) {
			return nil, status.Error(codes.Unauthenticated, "bootstrap token is invalid or expired")
		}
		s.logger.Error("bootstrap token store failed", slog.String("error", err.Error()))
		return nil, status.Error(codes.Internal, "cannot validate bootstrap token")
	}

	nodeID := req.GetNodeId()
	if nodeID == "" {
		var err error
		nodeID, err = generateNodeID()
		if err != nil {
			return nil, status.Error(codes.Internal, "cannot assign a node identity")
		}
	}

	certPEM, notAfter, err := s.ca.Sign(req.GetCsr(), nodeID, now)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "cannot issue a node certificate: %v", err)
	}

	s.logger.Info("node registered",
		slog.String("node_id", nodeID),
		slog.String("agent_version", req.GetAgentVersion()))
	return &tunnelv1.RegisterResponse{
		NodeId:         nodeID,
		CertificatePem: certPEM,
		CaBundlePem:    s.ca.Bundle(),
		NotAfter:       timestamppb.New(notAfter),
	}, nil
}

// RenewCertificate implements tunnelv1.NodeIdentityServer. It requires the
// caller's current certificate to still be valid — the gRPC handshake
// already refused an expired one, so surviving to this handler is proof of
// that — and it must name the same node_id the request declares.
func (s *Server) RenewCertificate(ctx context.Context, req *tunnelv1.RenewRequest) (*tunnelv1.RenewResponse, error) {
	peerID, err := nodeid.FromPeer(ctx)
	if err != nil {
		return nil, err
	}
	if peerID != req.GetNodeId() {
		return nil, status.Errorf(codes.Unauthenticated,
			"client certificate names %q but the request is for %q", peerID, req.GetNodeId())
	}

	now := s.clock.Now()
	certPEM, notAfter, err := s.ca.Sign(req.GetCsr(), req.GetNodeId(), now)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "cannot issue a node certificate: %v", err)
	}

	s.logger.Info("node certificate renewed", slog.String("node_id", req.GetNodeId()))
	return &tunnelv1.RenewResponse{
		CertificatePem: certPEM,
		CaBundlePem:    s.ca.Bundle(),
		NotAfter:       timestamppb.New(notAfter),
	}, nil
}

// generateNodeID returns a random node identity for a Register call that left
// node_id empty. It is deliberately simple: the top-level README's "待决问题
// 3" marks collision handling and an operator-facing naming scheme as open
// questions to settle before production, not something this phase blocks on.
func generateNodeID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("registryserver: cannot generate node id: %w", err)
	}
	return "node-" + hex.EncodeToString(buf), nil
}
