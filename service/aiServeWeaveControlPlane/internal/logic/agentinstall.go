package logic

import (
	"context"
	"errors"
	"strings"
	"time"

	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/agentenrollment"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/installcode"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// Install credential kinds a caller may ask for.
//
// 调用方可以请求的安装凭据类型。
const (
	// InstallCredentialCode issues a one-time code redeemed at install time.
	//
	// InstallCredentialCode 签发在安装时兑换的一次性兑换码。
	InstallCredentialCode = "code"
	// InstallCredentialToken mints the node token immediately.
	//
	// InstallCredentialToken 立即签出节点令牌。
	InstallCredentialToken = "token"
)

// redeemTokenTTL is how long the token minted at redemption stays usable. The
// Agent registers within seconds of redemption, so it is the shortest the
// Registry accepts.
//
// redeemTokenTTL 是兑换时签出的令牌的可用时长。Agent 在兑换后数秒内注册，因此取
// Registry 接受的最短值。
const redeemTokenTTL = 15 * time.Minute

// InstallIssuer mints raw node credentials and reads the public CA, as
// agentenrollment.Service does.
//
// InstallIssuer 签出原始节点凭据并读取公开 CA，即 agentenrollment.Service。
type InstallIssuer interface {
	CA() (agentenrollment.Certificate, error)
	MintCredential(ctx context.Context, nodeID string, ttl time.Duration) (agentenrollment.Credential, error)
}

// AgentInstallConfig enables Agent install codes.
//
// AgentInstallConfig 启用 Agent 安装兑换码。
type AgentInstallConfig struct {
	// Issuer mints the credentials.
	//
	// Issuer 签出凭据。
	Issuer InstallIssuer
	// PublicURL is returned with each issued credential so the Console can
	// print the command; it has no trailing slash.
	//
	// PublicURL 随每个签发的凭据返回，供 Console 生成命令；末尾不含斜杠。
	PublicURL string
}

// WithAgentInstall turns Agent install codes on. Without it every install
// method returns ErrNotFound, matching the routes that are not mounted.
//
// WithAgentInstall 开启 Agent 安装兑换码。没有它时每个安装方法都返回 ErrNotFound，与未
// 挂载的路由一致。
func WithAgentInstall(cfg AgentInstallConfig) Option {
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	return func(s *Service) { s.agentInstall = &cfg }
}

// InstallIssue is what issuing returns once: either a code, or the token
// itself with the facts needed to enroll with it.
//
// InstallIssue 是签发时仅返回一次的结果：要么是兑换码，要么是令牌本身及用它接入所需的信息。
type InstallIssue struct {
	Credential     string
	Code           string
	NodeID         string
	ExpiresAt      time.Time
	InstallBaseURL string
	// Token fields are set only for InstallCredentialToken.
	//
	// 以下令牌字段只在 InstallCredentialToken 时设置。
	BootstrapToken string
	CASHA256       string
	RegistryAddr   string
	GatewayAddr    string
}

// InstallRedemption is what redeeming a code returns once.
//
// InstallRedemption 是兑换一个码时仅返回一次的结果。
type InstallRedemption struct {
	NodeID         string
	RegistryAddr   string
	GatewayAddr    string
	CAPEM          string
	CASHA256       string
	BootstrapToken string
	TokenExpiresAt time.Time
}

// InstallCA returns the public CA for a token-credential install. It needs no
// session: the caller is the Gateway, authenticated by the shared secret.
//
// InstallCA 返回令牌直写安装所用的公开 CA。它不需要会话：调用方是 Gateway，由共享密钥认证。
func (s *Service) InstallCA() (agentenrollment.Certificate, error) {
	if s.agentInstall == nil {
		return agentenrollment.Certificate{}, ErrNotFound
	}
	ca, err := s.agentInstall.Issuer.CA()
	if err != nil {
		return agentenrollment.Certificate{}, ErrUnavailable
	}
	return ca, nil
}

// CreateOperatorInstall issues an install credential for a new platform node.
// The addresses are written into the redemption, never connected to.
//
// CreateOperatorInstall 为一台新的平台节点签发安装凭据。地址只写入兑换结果，从不被连接。
func (s *Service) CreateOperatorInstall(ctx context.Context, actor Actor, registry, gateway, credential string, ttl time.Duration, sharing ...bool) (InstallIssue, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return InstallIssue{}, err
	}
	if s.agentInstall == nil {
		return InstallIssue{}, ErrNotFound
	}
	if !agentenrollment.ValidAddress(registry) || !agentenrollment.ValidAddress(gateway) {
		return InstallIssue{}, ErrInvalidInput
	}
	nodeID, err := agentenrollment.NewNodeID()
	if err != nil {
		return InstallIssue{}, ErrUnavailable
	}
	if ttl < 15*time.Minute || ttl > 24*time.Hour || (credential != "" && credential != InstallCredentialCode && credential != InstallCredentialToken) {
		return InstallIssue{}, ErrInvalidInput
	}
	if err := s.setEnrollmentSharing(ctx, actor, nodeID, len(sharing) > 0 && sharing[0]); err != nil {
		return InstallIssue{}, err
	}
	out, err := s.issueInstall(ctx, actor, model.PlatformScope, nodeID, registry, gateway, credential, ttl)
	if err != nil {
		_ = s.store.DeleteNodeAccess(context.WithoutCancel(ctx), nodeID)
	}
	return out, err
}

// CreateTenantInstall issues an install credential for a new node of the
// actor's tenant. The tenant_nodes row is written first and counts against
// the tenant's limits, exactly as CreateTenantNode does, and is removed again
// if issuing fails.
//
// CreateTenantInstall 为 actor 所属商户的一台新节点签发安装凭据。tenant_nodes 行先写入并
// 计入商户限额，与 CreateTenantNode 一致，签发失败时再删除。
func (s *Service) CreateTenantInstall(ctx context.Context, actor Actor, credential string, ttl time.Duration) (InstallIssue, error) {
	if err := s.requireTenantNodes(actor); err != nil {
		return InstallIssue{}, err
	}
	if s.agentInstall == nil {
		return InstallIssue{}, ErrNotFound
	}
	if !actor.canManageNodes() || actor.UserID == "" {
		return InstallIssue{}, ErrForbidden
	}
	now := s.clock.Now().UTC()
	active, created, err := s.store.CountTenantNodes(ctx, actor.TenantID, now.Add(-24*time.Hour))
	if err != nil {
		return InstallIssue{}, translate(err)
	}
	if active >= int64(s.tenantNodes.MaxPerTenant) || created >= int64(s.tenantNodes.MaxCreatesPerDay) {
		return InstallIssue{}, ErrConflict
	}
	nodeID, err := nodeaccess.NewTenantNodeID(actor.TenantID)
	if err != nil {
		return InstallIssue{}, err
	}
	row := model.TenantNode{NodeID: nodeID, TenantID: actor.TenantID, CreatedBy: actor.UserID, CreatedAt: now, EnrollBy: now.Add(ttl)}
	if err := s.store.CreateTenantNode(ctx, &row); err != nil {
		return InstallIssue{}, translate(err)
	}
	out, err := s.issueInstall(ctx, actor, actor.TenantID, nodeID, s.tenantNodes.RegistryAddr, s.tenantNodes.GatewayAddr, credential, ttl)
	if err != nil {
		// The caller may have gone away; the row must still go.
		// 调用方可能已经离开；这一行仍然必须删除。
		_ = s.store.DeleteTenantNode(context.WithoutCancel(ctx), nodeID)
	}
	return out, err
}

// issueInstall is the part shared by operator and tenant issuing; undoing a
// tenant_nodes row on failure is the caller's job.
//
// issueInstall 是运维与商户签发共用的部分；失败时撤销 tenant_nodes 行由调用方负责。
func (s *Service) issueInstall(ctx context.Context, actor Actor, scope, nodeID, registry, gateway, credential string, ttl time.Duration) (InstallIssue, error) {
	if ttl < 15*time.Minute || ttl > 24*time.Hour {
		return InstallIssue{}, ErrInvalidInput
	}
	now := s.clock.Now().UTC()
	switch credential {
	case InstallCredentialCode, "":
		code, err := installcode.New()
		if err != nil {
			return InstallIssue{}, ErrUnavailable
		}
		hash, err := installcode.Hash(code)
		if err != nil {
			return InstallIssue{}, ErrUnavailable
		}
		expires := now.Add(ttl)
		row := model.AgentInstallCode{CodeHash: hash, NodeID: nodeID, TenantID: scope, RegistryAddr: registry, GatewayAddr: gateway, CreatedBy: actor.UserID, CreatedAt: now, ExpiresAt: expires}
		if err := s.store.CreateAgentInstallCode(ctx, &row); err != nil {
			return InstallIssue{}, translate(err)
		}
		s.audit(ctx, scope, actor.UserID, model.ActionInstallCodeCreate, nodeID,
			"credential=code expires_at="+expires.Format(time.RFC3339), actor.IP)
		return InstallIssue{Credential: InstallCredentialCode, Code: code, NodeID: nodeID, ExpiresAt: expires, InstallBaseURL: s.agentInstall.PublicURL}, nil
	case InstallCredentialToken:
		cred, err := s.agentInstall.Issuer.MintCredential(ctx, nodeID, ttl)
		if errors.Is(err, agentenrollment.ErrInvalidInput) {
			return InstallIssue{}, ErrInvalidInput
		}
		if err != nil {
			return InstallIssue{}, ErrUnavailable
		}
		s.audit(ctx, scope, actor.UserID, model.ActionInstallCodeCreate, nodeID,
			"credential=token expires_at="+cred.ExpiresAt.UTC().Format(time.RFC3339), actor.IP)
		return InstallIssue{Credential: InstallCredentialToken, NodeID: nodeID, ExpiresAt: cred.ExpiresAt, InstallBaseURL: s.agentInstall.PublicURL,
			BootstrapToken: cred.Token, CASHA256: cred.CASHA256, RegistryAddr: registry, GatewayAddr: gateway}, nil
	default:
		return InstallIssue{}, ErrInvalidInput
	}
}

// RedeemInstallCode exchanges a one-time code for the node's enrollment
// material. A code that is malformed, unknown, already redeemed, expired or
// whose tenant node was deleted is ErrNotFound without saying which, so a
// caller learns nothing by guessing. If the token cannot be minted the
// redemption is undone, so the holder can retry until the code expires.
//
// RedeemInstallCode 用一次性码换取节点的接入材料。格式错误、未知、已兑换、已过期、或其商户
// 节点已被删除的码一律返回 ErrNotFound 且不区分原因，使猜测者得不到任何信息。令牌签不出来
// 时撤销本次兑换，持有者可在码过期前重试。
func (s *Service) RedeemInstallCode(ctx context.Context, code string) (InstallRedemption, error) {
	if s.agentInstall == nil {
		return InstallRedemption{}, ErrNotFound
	}
	hash, err := installcode.Hash(code)
	if err != nil {
		return InstallRedemption{}, ErrNotFound
	}
	row, err := s.store.RedeemAgentInstallCode(ctx, hash, s.clock.Now().UTC())
	if err != nil {
		return InstallRedemption{}, translate(err)
	}
	if row.TenantID != model.PlatformScope {
		node, err := s.store.GetTenantNode(ctx, row.NodeID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && node.RevokedAt != nil) {
			return InstallRedemption{}, ErrNotFound
		}
		if err != nil {
			s.unredeem(ctx, hash)
			return InstallRedemption{}, translate(err)
		}
	}
	cred, err := s.agentInstall.Issuer.MintCredential(ctx, row.NodeID, redeemTokenTTL)
	if err != nil {
		s.unredeem(ctx, hash)
		return InstallRedemption{}, ErrUnavailable
	}
	s.audit(ctx, row.TenantID, row.CreatedBy, model.ActionInstallCodeRedeem, row.NodeID,
		"expires_at="+cred.ExpiresAt.UTC().Format(time.RFC3339), "")
	return InstallRedemption{NodeID: row.NodeID, RegistryAddr: row.RegistryAddr, GatewayAddr: row.GatewayAddr,
		CAPEM: cred.CAPEM, CASHA256: cred.CASHA256, BootstrapToken: cred.Token, TokenExpiresAt: cred.ExpiresAt}, nil
}

// unredeem undoes a redemption whose token could not be minted. The request
// may have been cancelled, but the code must still become usable again.
//
// unredeem 撤销一次令牌未能签出的兑换。请求可能已被取消，但码仍必须重新可用。
func (s *Service) unredeem(ctx context.Context, hash string) {
	_ = s.store.UnredeemAgentInstallCode(context.WithoutCancel(ctx), hash)
}
