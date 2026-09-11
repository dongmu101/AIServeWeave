package registryserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

// MintToken implements tunnelv1.TokenAdminServer. See the proto's
// TokenAdmin.MintToken doc comment for the node_id-binding contract.
func (s *Server) MintToken(ctx context.Context, req *tunnelv1.MintTokenRequest) (*tunnelv1.MintTokenResponse, error) {
	if err := s.requireAdmin(ctx); err != nil {
		s.metrics.TokenOp(TokenOpMint, ResultUnauthorized)
		return nil, err
	}
	ttl := req.GetTtl().AsDuration()
	if ttl <= 0 {
		s.metrics.TokenOp(TokenOpMint, ResultInvalid)
		return nil, status.Error(codes.InvalidArgument, "ttl must be positive")
	}

	now := s.clock.Now()
	token, err := s.tokens.MintForNode(ttl, now, req.GetNodeId())
	if err != nil {
		s.logger.Error("bootstrap token mint failed", slog.String("error", err.Error()))
		s.metrics.TokenOp(TokenOpMint, ResultInternal)
		return nil, status.Error(codes.Internal, "cannot mint bootstrap token")
	}

	s.logger.Info("bootstrap token minted",
		slog.Bool("node_bound", req.GetNodeId() != ""),
		slog.Duration("ttl", ttl))
	s.metrics.TokenOp(TokenOpMint, ResultSuccess)
	return &tunnelv1.MintTokenResponse{
		Token:     token,
		ExpiresAt: timestamppb.New(now.Add(ttl)),
	}, nil
}

// RevokeToken implements tunnelv1.TokenAdminServer.
func (s *Server) RevokeToken(ctx context.Context, req *tunnelv1.RevokeTokenRequest) (*tunnelv1.RevokeTokenResponse, error) {
	if err := s.requireAdmin(ctx); err != nil {
		s.metrics.TokenOp(TokenOpRevoke, ResultUnauthorized)
		return nil, err
	}

	if err := s.tokens.Revoke(req.GetToken(), s.clock.Now()); err != nil {
		if errors.Is(err, tokenstore.ErrInvalidToken) {
			s.metrics.TokenOp(TokenOpRevoke, ResultNotFound)
			return nil, status.Error(codes.NotFound, "token is unknown")
		}
		s.logger.Error("bootstrap token revoke failed", slog.String("error", err.Error()))
		s.metrics.TokenOp(TokenOpRevoke, ResultInternal)
		return nil, status.Error(codes.Internal, "cannot revoke bootstrap token")
	}

	s.logger.Info("bootstrap token revoked")
	s.metrics.TokenOp(TokenOpRevoke, ResultSuccess)
	return &tunnelv1.RevokeTokenResponse{}, nil
}

// DisableNode implements tunnelv1.TokenAdminServer (STATUS.md's S03). Beyond
// persisting the disable, it recomputes and re-broadcasts the roster's
// revoked_node_ids so every joined Gateway replica closes this node_id's
// existing Control stream(s) without waiting for it to reconnect — the
// "生效路径" half of S03, not just refusing a future Register/RenewCertificate.
func (s *Server) DisableNode(ctx context.Context, req *tunnelv1.DisableNodeRequest) (*tunnelv1.DisableNodeResponse, error) {
	if err := s.requireAdmin(ctx); err != nil {
		s.metrics.NodeStateChange(NodeActionDisable, ResultUnauthorized)
		return nil, err
	}
	nodeID := req.GetNodeId()
	if nodeID == "" {
		s.metrics.NodeStateChange(NodeActionDisable, ResultInvalid)
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	if err := s.identities.Disable(nodeID, s.clock.Now()); err != nil {
		s.logger.Error("node disable failed", slog.String("node_id", nodeID), slog.String("error", err.Error()))
		s.metrics.NodeStateChange(NodeActionDisable, ResultInternal)
		return nil, status.Error(codes.Internal, "cannot disable node")
	}
	s.roster.setRevoked(s.identities.DisabledNodeIDs())

	s.logger.Info("node disabled", slog.String("node_id", nodeID))
	s.metrics.NodeStateChange(NodeActionDisable, ResultSuccess)
	return &tunnelv1.DisableNodeResponse{}, nil
}

// EnableNode implements tunnelv1.TokenAdminServer (STATUS.md's S03).
func (s *Server) EnableNode(ctx context.Context, req *tunnelv1.EnableNodeRequest) (*tunnelv1.EnableNodeResponse, error) {
	if err := s.requireAdmin(ctx); err != nil {
		s.metrics.NodeStateChange(NodeActionEnable, ResultUnauthorized)
		return nil, err
	}
	nodeID := req.GetNodeId()
	if nodeID == "" {
		s.metrics.NodeStateChange(NodeActionEnable, ResultInvalid)
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	if err := s.identities.Enable(nodeID, s.clock.Now()); err != nil {
		s.logger.Error("node enable failed", slog.String("node_id", nodeID), slog.String("error", err.Error()))
		s.metrics.NodeStateChange(NodeActionEnable, ResultInternal)
		return nil, status.Error(codes.Internal, "cannot enable node")
	}
	s.roster.setRevoked(s.identities.DisabledNodeIDs())

	s.logger.Info("node enabled", slog.String("node_id", nodeID))
	s.metrics.NodeStateChange(NodeActionEnable, ResultSuccess)
	return &tunnelv1.EnableNodeResponse{}, nil
}

// ApproveNode implements tunnelv1.TokenAdminServer (STATUS.md's P01).
func (s *Server) ApproveNode(ctx context.Context, req *tunnelv1.ApproveNodeRequest) (*tunnelv1.ApproveNodeResponse, error) {
	if err := s.requireAdmin(ctx); err != nil {
		s.metrics.NodeStateChange(NodeActionApprove, ResultUnauthorized)
		return nil, err
	}
	nodeID := req.GetNodeId()
	if nodeID == "" {
		s.metrics.NodeStateChange(NodeActionApprove, ResultInvalid)
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	if err := s.identities.Approve(nodeID, s.clock.Now()); err != nil {
		s.logger.Error("node approve failed", slog.String("node_id", nodeID), slog.String("error", err.Error()))
		s.metrics.NodeStateChange(NodeActionApprove, ResultInternal)
		return nil, status.Error(codes.Internal, "cannot approve node")
	}

	s.logger.Info("node approved", slog.String("node_id", nodeID))
	s.metrics.NodeStateChange(NodeActionApprove, ResultSuccess)
	return &tunnelv1.ApproveNodeResponse{}, nil
}

// SetMaintenance implements tunnelv1.TokenAdminServer (STATUS.md's P01).
// Beyond persisting the flag, it recomputes and re-broadcasts the roster's
// maintenance_node_ids so every joined Gateway replica stops assigning new
// work to this node_id without waiting for it to reconnect — unlike
// DisableNode, the node_id's existing Control stream is left alone.
func (s *Server) SetMaintenance(ctx context.Context, req *tunnelv1.SetMaintenanceRequest) (*tunnelv1.SetMaintenanceResponse, error) {
	if err := s.requireAdmin(ctx); err != nil {
		s.metrics.NodeStateChange(NodeActionMaintenanceSet, ResultUnauthorized)
		return nil, err
	}
	nodeID := req.GetNodeId()
	if nodeID == "" {
		s.metrics.NodeStateChange(NodeActionMaintenanceSet, ResultInvalid)
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	if err := s.identities.SetMaintenance(nodeID, s.clock.Now()); err != nil {
		s.logger.Error("node maintenance set failed", slog.String("node_id", nodeID), slog.String("error", err.Error()))
		s.metrics.NodeStateChange(NodeActionMaintenanceSet, ResultInternal)
		return nil, status.Error(codes.Internal, "cannot set node maintenance")
	}
	s.roster.setMaintenance(s.identities.MaintenanceNodeIDs())

	s.logger.Info("node entered maintenance", slog.String("node_id", nodeID))
	s.metrics.NodeStateChange(NodeActionMaintenanceSet, ResultSuccess)
	return &tunnelv1.SetMaintenanceResponse{}, nil
}

// ClearMaintenance implements tunnelv1.TokenAdminServer (STATUS.md's P01).
func (s *Server) ClearMaintenance(ctx context.Context, req *tunnelv1.ClearMaintenanceRequest) (*tunnelv1.ClearMaintenanceResponse, error) {
	if err := s.requireAdmin(ctx); err != nil {
		s.metrics.NodeStateChange(NodeActionMaintenanceClear, ResultUnauthorized)
		return nil, err
	}
	nodeID := req.GetNodeId()
	if nodeID == "" {
		s.metrics.NodeStateChange(NodeActionMaintenanceClear, ResultInvalid)
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	if err := s.identities.ClearMaintenance(nodeID, s.clock.Now()); err != nil {
		s.logger.Error("node maintenance clear failed", slog.String("node_id", nodeID), slog.String("error", err.Error()))
		s.metrics.NodeStateChange(NodeActionMaintenanceClear, ResultInternal)
		return nil, status.Error(codes.Internal, "cannot clear node maintenance")
	}
	s.roster.setMaintenance(s.identities.MaintenanceNodeIDs())

	s.logger.Info("node exited maintenance", slog.String("node_id", nodeID))
	s.metrics.NodeStateChange(NodeActionMaintenanceClear, ResultSuccess)
	return &tunnelv1.ClearMaintenanceResponse{}, nil
}

// ListNodeStates implements tunnelv1.TokenAdminServer (STATUS.md's P01).
func (s *Server) ListNodeStates(ctx context.Context, _ *tunnelv1.ListNodeStatesRequest) (*tunnelv1.ListNodeStatesResponse, error) {
	if err := s.requireAdmin(ctx); err != nil {
		s.metrics.ListNodeStates(ResultUnauthorized)
		return nil, err
	}
	s.metrics.ListNodeStates(ResultSuccess)
	states := s.identities.States()
	resp := &tunnelv1.ListNodeStatesResponse{States: make([]*tunnelv1.NodeState, 0, len(states))}
	for _, st := range states {
		resp.States = append(resp.States, &tunnelv1.NodeState{
			NodeId:          st.NodeID,
			PendingApproval: st.PendingApproval,
			Disabled:        st.Disabled,
			Maintenance:     st.Maintenance,
			FirstSeenAt:     timestamppb.New(st.FirstSeenAt),
			LastSeenAt:      timestamppb.New(st.LastSeenAt),
		})
	}
	return resp, nil
}

// requireAdmin authenticates the caller of a TokenAdmin method against a
// "authorization: Bearer <token>" gRPC metadata entry, compared in constant
// time — the same Bearer-token convention adminapi.go (Gateway) and
// middleware.go (control plane) already use for their own admin-guarded
// surfaces, applied here to gRPC metadata instead of an HTTP header.
//
// requireAdmin 通过 gRPC metadata 里的 "authorization: Bearer <token>" 校验
// TokenAdmin 方法的调用方，比较过程是常数时间——这与 adminapi.go（Gateway）和
// middleware.go（控制面）各自守护自己的管理面时已经采用的 Bearer token 约定
// 相同，这里把它用在 gRPC metadata 而不是 HTTP 头上。
func (s *Server) requireAdmin(ctx context.Context) error {
	if s.adminToken == "" {
		return status.Error(codes.PermissionDenied, "token administration is disabled")
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing authorization metadata")
	}
	values := md.Get("authorization")
	if len(values) != 1 {
		return status.Error(codes.Unauthenticated, "missing authorization metadata")
	}
	const prefix = "Bearer "
	header := values[0]
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return status.Error(codes.Unauthenticated, "malformed authorization metadata")
	}
	if subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(s.adminToken)) != 1 {
		return status.Error(codes.PermissionDenied, "invalid admin token")
	}
	return nil
}
