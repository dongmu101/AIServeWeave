package registryserver

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/nodeid"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/identitystore"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/tokenstore"
)

// Register implements tunnelv1.NodeIdentityServer. It is the only method in
// the whole contract that accepts a connection with no client certificate:
// authentication here comes entirely from the one-time bootstrap token.
//
// Signing happens before the identity ledger is checked, not after: the
// alternative order would let a conflicting request reserve nodeID and then
// fail signing, leaving the ledger pointing at a key nobody ever proved they
// hold. With this order, a request that turns out to conflict has merely
// spent its already-consumed bootstrap token on a certificate this handler
// never returns — a wasted signature, not a corrupted ledger (STATUS.md's
// S01).
//
// A node_id-bound token (minted via TokenAdmin.MintToken, STATUS.md's S02)
// changes the ledger step: the bound node_id overrides whatever the request
// proposed, and the ledger write bypasses the conflict check the same way
// RenewCertificate's does, because the operator's authenticated mint call —
// not a comparison against ledger history — is this reinstall's proof of
// authorization.
//
// Register 实现 tunnelv1.NodeIdentityServer。它是整个契约里唯一一个接受无
// 客户端证书连接的方法：这里的认证完全来自一次性引导令牌。
//
// 签发发生在检查身份账本之前，而不是之后：反过来的顺序会让一次冲突的请求先
// 占住 nodeID，再在签发上失败，把账本指向一把从没人证明持有过的 key。按现在
// 这个顺序，一次事后判定为冲突的请求，代价只是把已经消耗掉的引导令牌，换成
// 了一张本方法从不交回去的证书——是一次白白的签名，而不是一份被弄脏的账本
// （STATUS.md 的 S01）。
//
// 一枚绑定了 node_id 的令牌（通过 TokenAdmin.MintToken 铸造，STATUS.md 的
// S02）会改变账本这一步：绑定的 node_id 会覆盖请求里提出的任何值，账本写入
// 也会像 RenewCertificate 那样跳过冲突检查——因为这次重装的授权证明来自运维
// 那次经过认证的铸造调用，而不是与账本历史的比对。
func (s *Server) Register(ctx context.Context, req *tunnelv1.RegisterRequest) (*tunnelv1.RegisterResponse, error) {
	now := s.clock.Now()
	// The token is never logged, on success or failure: it is a bearer
	// credential until consumed, exactly like the API keys AGENTS.md's
	// security rules cover.
	boundNodeID, err := s.tokens.Consume(req.GetBootstrapToken(), now)
	if err != nil {
		if errors.Is(err, tokenstore.ErrInvalidToken) {
			s.metrics.Register(ResultUnauthorized)
			return nil, status.Error(codes.Unauthenticated, "bootstrap token is invalid or expired")
		}
		s.logger.Error("bootstrap token store failed", slog.String("error", err.Error()))
		s.metrics.Register(ResultInternal)
		return nil, status.Error(codes.Internal, "cannot validate bootstrap token")
	}

	nodeID := req.GetNodeId()
	switch {
	case boundNodeID != "" && nodeID != "" && nodeID != boundNodeID:
		s.metrics.Register(ResultUnauthorized)
		return nil, status.Errorf(codes.PermissionDenied,
			"bootstrap token is bound to node_id %q, not %q", boundNodeID, nodeID)
	case boundNodeID != "":
		nodeID = boundNodeID
	case nodeID == "":
		// Before STATUS.md's P01, an empty node_id was a supported
		// convenience: the Registry generated a random one and handed it
		// back. Approval gating makes that convenience incoherent — an
		// operator cannot approve a node_id by name before it exists, and a
		// freshly generated id would differ on every retry, so a "pending"
		// record from one attempt could never be the one an operator
		// approves. An unbound registration must therefore now name itself,
		// so an operator has something stable to approve; a node-bound
		// token (which already skips this gate below) is the path for a
		// caller that still wants the Registry to assign the identity.
		s.metrics.Register(ResultInvalid)
		return nil, status.Error(codes.InvalidArgument,
			"node_id is required: an unbound bootstrap token can no longer self-assign one, since it would have nothing stable for an operator to approve")
	}

	// A disabled node_id (TokenAdmin.DisableNode, STATUS.md's S03) is refused
	// even when the bootstrap token itself is otherwise valid: minting a
	// fresh token does not undo a disable, only TokenAdmin.EnableNode does —
	// the two are independent admin actions on purpose, so an operator can
	// revoke a compromised node without also having to hunt down and revoke
	// every bootstrap token that might still let it back in.
	if s.identities.IsDisabled(nodeID) {
		s.metrics.Register(ResultUnauthorized)
		return nil, status.Errorf(codes.PermissionDenied, "node_id %q is disabled", nodeID)
	}

	fingerprint, err := csrFingerprint(req.GetCsr())
	if err != nil {
		s.metrics.Register(ResultInvalid)
		return nil, status.Errorf(codes.InvalidArgument, "cannot read the certificate request: %v", err)
	}

	// A node_id presented with an unbound bootstrap token must be approved
	// by an operator before Register will sign anything for it (STATUS.md's
	// P01). A bound token skips this: minting it was itself the approval,
	// the same reasoning that already lets the bound-token branch below
	// bypass Reserve's conflict check. A node_id with no ledger entry at all
	// is exactly as unapproved as one explicitly marked pending, so both
	// collapse into the same refusal — the attempt is recorded either way,
	// which is what lets an operator find it in TokenAdmin.ListNodeStates
	// and approve it for the Agent's next retry.
	if boundNodeID == "" {
		approved := true
		if s.identities.IsPending(nodeID) {
			approved = false
		} else if !s.identities.HasRecord(nodeID) {
			approved = false
		}
		if !approved {
			if err := s.identities.RecordPending(nodeID, now); err != nil {
				s.logger.Error("identity store failed", slog.String("error", err.Error()))
				s.metrics.Register(ResultInternal)
				return nil, status.Error(codes.Internal, "cannot record node identity")
			}
			s.logger.Warn("node registration pending operator approval",
				slog.String("node_id", nodeID),
				slog.String("agent_version", req.GetAgentVersion()))
			s.metrics.Register(ResultPendingApproval)
			return nil, status.Errorf(codes.PermissionDenied,
				"node_id %q is pending operator approval; an operator must approve it before registration can proceed", nodeID)
		}
	}

	certPEM, notAfter, err := s.ca.Sign(req.GetCsr(), nodeID, now)
	if err != nil {
		s.metrics.Register(ResultInvalid)
		return nil, status.Errorf(codes.InvalidArgument, "cannot issue a node certificate: %v", err)
	}

	if boundNodeID != "" {
		// Set, not Reserve: see this method's doc comment. A node-bound
		// token authorizes exactly one reinstall, so there is nothing left
		// to check the ledger against.
		if err := s.identities.Set(nodeID, fingerprint, now); err != nil {
			s.logger.Error("identity store failed", slog.String("error", err.Error()))
			s.metrics.Register(ResultInternal)
			return nil, status.Error(codes.Internal, "cannot record node identity")
		}
		s.logger.Info("node registered via a node-bound bootstrap token (authorized reinstall)",
			slog.String("node_id", nodeID),
			slog.String("agent_version", req.GetAgentVersion()))
		s.metrics.Register(ResultSuccess)
		return &tunnelv1.RegisterResponse{
			NodeId:         nodeID,
			CertificatePem: certPEM,
			CaBundlePem:    s.ca.Bundle(),
			NotAfter:       timestamppb.New(notAfter),
		}, nil
	}

	outcome, err := s.identities.Reserve(nodeID, fingerprint, now)
	if err != nil {
		s.logger.Error("identity store failed", slog.String("error", err.Error()))
		s.metrics.Register(ResultInternal)
		return nil, status.Error(codes.Internal, "cannot record node identity")
	}
	if outcome == identitystore.OutcomeConflict {
		// This is deliberately one bucket, not two: telling "reinstall" and
		// "spoofing" apart needs an authorization signal an unbound
		// bootstrap token does not carry. An operator minting a node_id-
		// bound token (TokenAdmin.MintToken, S02) is exactly that signal —
		// handled above — for anyone who used one; a request that reaches
		// this branch did not, so an operator resolving it by hand —
		// confirming the reinstall out of band, then clearing this node_id's
		// ledger entry, or simply minting a bound token instead next time —
		// is the correct escalation, not something this handler should
		// guess at. See the Registry README's 已知限制/下一步 section for
		// the operator-facing writeup.
		//
		// 这里刻意只有一个桶，而不是两个：要在「重装」与「冒用」之间做出
		// 区分，需要一个未绑定的引导令牌不携带的授权信号。运维铸造一枚
		// node_id 绑定的令牌（TokenAdmin.MintToken，S02）正是这样一个信号
		// ——用了它的请求在上面的分支已经处理；走到这个分支说明没有用——
		// 因此由运维手动解决——线下确认确实是重装，再清空这个 node_id 在
		// 账本里的记录，或者干脆下次改用一枚绑定的令牌——才是正确的处理
		// 方式，而不是让本方法自己去猜。运维侧的处理说明见 Registry README
		// 的已知限制/下一步 一节。
		s.logger.Warn("node_id re-registered with a different public key",
			slog.String("node_id", nodeID),
			slog.String("agent_version", req.GetAgentVersion()))
		s.metrics.Register(ResultConflict)
		return nil, status.Errorf(codes.AlreadyExists,
			"node_id %q is already registered with a different key; an operator must confirm this is a reinstall before it can proceed", nodeID)
	}

	s.logger.Info("node registered",
		slog.String("node_id", nodeID),
		slog.String("agent_version", req.GetAgentVersion()),
		slog.Bool("first_registration", outcome == identitystore.OutcomeNew))
	if outcome == identitystore.OutcomeNew {
		s.metrics.Register(ResultSuccess)
	} else {
		s.metrics.Register(ResultReconnect)
	}
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
		s.metrics.CertRenewal(ResultUnauthorized)
		return nil, err
	}
	if peerID != req.GetNodeId() {
		s.metrics.CertRenewal(ResultUnauthorized)
		return nil, status.Errorf(codes.Unauthenticated,
			"client certificate names %q but the request is for %q", peerID, req.GetNodeId())
	}
	// A disabled node must not be able to keep itself alive by renewing —
	// its certificate is still cryptographically valid until it expires, and
	// renewal is exactly what would otherwise let it outlast a disable.
	if s.identities.IsDisabled(req.GetNodeId()) {
		s.metrics.CertRenewal(ResultUnauthorized)
		return nil, status.Errorf(codes.PermissionDenied, "node_id %q is disabled", req.GetNodeId())
	}

	fingerprint, err := csrFingerprint(req.GetCsr())
	if err != nil {
		s.metrics.CertRenewal(ResultInvalid)
		return nil, status.Errorf(codes.InvalidArgument, "cannot read the certificate request: %v", err)
	}

	now := s.clock.Now()
	certPEM, notAfter, err := s.ca.Sign(req.GetCsr(), req.GetNodeId(), now)
	if err != nil {
		s.metrics.CertRenewal(ResultInvalid)
		return nil, status.Errorf(codes.InvalidArgument, "cannot issue a node certificate: %v", err)
	}

	// Set, not Reserve: peerID == req.GetNodeId() above already proved this
	// caller holds nodeID's current private key, and a renewal routinely
	// presents a freshly generated one — Reserve would refuse that as a
	// conflict with the key on record.
	//
	// 用 Set 而不是 Reserve：上面 peerID == req.GetNodeId() 的校验已经证明
	// 调用方持有 nodeID 当前的私钥，而一次续期常规地会呈递一把新生成的
	// key——用 Reserve 处理，会把它当作与在案的 key 冲突而拒绝。
	if err := s.identities.Set(req.GetNodeId(), fingerprint, now); err != nil {
		s.logger.Error("identity store failed", slog.String("error", err.Error()))
		s.metrics.CertRenewal(ResultInternal)
		return nil, status.Error(codes.Internal, "cannot record node identity")
	}

	s.logger.Info("node certificate renewed", slog.String("node_id", req.GetNodeId()))
	s.metrics.CertRenewal(ResultSuccess)
	return &tunnelv1.RenewResponse{
		CertificatePem: certPEM,
		CaBundlePem:    s.ca.Bundle(),
		NotAfter:       timestamppb.New(notAfter),
	}, nil
}

// csrFingerprint parses csrDER far enough to fingerprint the public key it
// carries, independent of ca.CA.Sign's own parsing of the same bytes. The
// duplication is deliberate: ca.CA stays a stateless signer that knows
// nothing about node identity history, and identitystore stays ignorant of
// X.509, so this package — the one that already imports both — is where the
// two meet.
//
// csrFingerprint 解析 csrDER，取出其中携带的公钥并计算指纹，这与 ca.CA.Sign
// 对同一段字节自己的解析是相互独立的。这份重复是刻意的：ca.CA 保持一个对节点
// 身份历史一无所知的无状态签发者，identitystore 保持对 X.509 一无所知，因此
// 由本包——已经同时导入两者的这个包——来完成两者的交汇。
func csrFingerprint(csrDER []byte) (string, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return "", err
	}
	return identitystore.Fingerprint(csr.PublicKey)
}
