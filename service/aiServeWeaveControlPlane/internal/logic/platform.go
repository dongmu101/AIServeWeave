// Platform operator identity, session and node-ops methods (STATUS.md's
// P01). They live in their own file, apart from the tenant-facing methods in
// service.go, the same way this service's routing already keeps the two
// worlds in separate groups (see the handler package's routes.go doc
// comment): a platform operator is authorized separately from a tenant
// role, and a reader should be able to see that boundary by which file a
// method is in.
//
// 平台运维身份、会话与节点操作方法（STATUS.md 的 P01）。它们独立成一个文件，
// 与 service.go 里面向租户的方法分开，这与本服务的路由早已把两个世界分成
// 不同分组一致（见 handler 包 routes.go 的文档注释）：平台运维身份与租户角色
// 分开授权，读者应当能仅凭一个方法在哪个文件里，就看出这条边界。
package logic

import (
	"context"
	"errors"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/registryclient"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreatePlatformOperator bootstraps a platform operator account. Like
// CreateTenant, it is guarded by the bootstrap token rather than a session
// — an operator's own sign-in does not yet exist the first time this is
// called — and its audit record names the system as the actor for the same
// reason CreateTenant's does.
//
// CreatePlatformOperator 引导创建一个平台运维账户。与 CreateTenant 一样，
// 它由 bootstrap token 而不是会话守卫——第一次调用它时，运维自己的登录尚不
// 存在——它的审计记录以系统作为行为人，理由与 CreateTenant 的相同。
func (s *Service) CreatePlatformOperator(ctx context.Context, email, password, name, ip string) (model.PlatformOperator, error) {
	email = normalizeEmail(email)
	if email == "" {
		return model.PlatformOperator{}, ErrInvalidInput
	}
	digest, err := hashPassword(password)
	if err != nil {
		return model.PlatformOperator{}, err
	}
	operator := model.PlatformOperator{
		ID:           model.NewID(model.PrefixPlatformOperator),
		Email:        email,
		PasswordHash: digest,
		Name:         name,
		Status:       model.StatusActive,
		CreatedAt:    s.clock.Now(),
	}
	if err := s.store.CreatePlatformOperator(ctx, &operator); err != nil {
		return model.PlatformOperator{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, "", model.ActionPlatformOperatorCreate, operator.ID, "", ip)
	return operator, nil
}

// PlatformAuthenticate verifies a platform operator's sign-in, mirroring
// Authenticate's timing-safe shape: the password comparison always runs,
// even against a fixed dummy digest for an unknown email, so a failure here
// cannot be used to enumerate operator accounts.
//
// PlatformAuthenticate 校验一次平台运维登录，其时序防护形态与 Authenticate
// 一致：即便 email 不存在，密码比较也照样针对一个固定的假摘要执行，因此
// 这里的失败无法被用来枚举运维账户。
func (s *Service) PlatformAuthenticate(ctx context.Context, email, password, ip string) (model.PlatformOperator, error) {
	operator, err := s.store.GetPlatformOperatorByEmail(ctx, normalizeEmail(email))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return model.PlatformOperator{}, err
	}

	digest := operator.PasswordHash
	if digest == "" {
		digest = dummyDigest
	}
	compareErr := comparePassword(digest, password)
	if err != nil || compareErr != nil || operator.Status != model.StatusActive {
		return model.PlatformOperator{}, ErrInvalidCredentials
	}

	now := s.clock.Now()
	if err := s.store.MarkPlatformOperatorLogin(ctx, operator.ID, now); err != nil {
		return model.PlatformOperator{}, err
	}
	operator.LastLoginAt = &now
	s.audit(ctx, model.PlatformScope, operator.ID, model.ActionPlatformOperatorLogin, operator.ID, "", ip)
	return operator, nil
}

// isPlatformActor reports whether actor is an authenticated platform
// operator session, as opposed to a tenant session. Every node-ops method
// below checks this even though the handler layer's requirePlatformSession
// middleware already refuses a tenant session before an Actor with the
// wrong shape can reach here — the same belt-and-braces the tenant-facing
// methods already apply to their own Actor fields, so a routing mistake
// fails closed in this layer too, not only in the one above it.
//
// isPlatformActor 报告 actor 是否是一个已认证的平台运维会话，而不是租户会话。
// 下面每一个节点操作方法都会做这项检查，即便 handler 层的
// requirePlatformSession 中间件早已在一个形状不对的 Actor 抵达这里之前将
// 租户会话拒之门外——这与面向租户的方法本就对自己的 Actor 字段采用的双重
// 保险一致：一次路由失误，不该只在上一层失效关闭，这一层也要失效关闭。
func isPlatformActor(actor Actor) bool {
	return actor.TenantID == model.PlatformScope && actor.Role == model.RolePlatformOperator
}

// requirePlatformActor is the shared guard every node-ops method opens with.
//
// requirePlatformActor 是每个节点操作方法开头共用的守卫。
func (s *Service) requirePlatformActor(actor Actor) error {
	if !isPlatformActor(actor) {
		return ErrForbidden
	}
	if s.registryClient == nil {
		return ErrRegistryUnconfigured
	}
	return nil
}

// ApproveNode clears a node_id's pending-approval mark (STATUS.md's P01),
// forwarding to the Registry and recording an audit entry only on success —
// see service.go's audit doc comment for why a failed action leaves none.
//
// ApproveNode 清除某个 node_id 的待审批标记（STATUS.md 的 P01），转发给
// Registry，并且只在成功时记一条审计——为什么失败的操作不留审计，见
// service.go 的 audit 文档注释。
func (s *Service) ApproveNode(ctx context.Context, actor Actor, nodeID string) error {
	if err := s.requirePlatformActor(actor); err != nil {
		return err
	}
	if nodeID == "" {
		return ErrInvalidInput
	}
	if err := s.registryClient.ApproveNode(ctx, nodeID); err != nil {
		return err
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionNodeApprove, nodeID, "", actor.IP)
	return nil
}

// DisableNode revokes a node_id's identity (STATUS.md's S03/P01).
//
// DisableNode 吊销一个 node_id 的身份（STATUS.md 的 S03/P01）。
func (s *Service) DisableNode(ctx context.Context, actor Actor, nodeID string) error {
	if err := s.requirePlatformActor(actor); err != nil {
		return err
	}
	if nodeID == "" {
		return ErrInvalidInput
	}
	if err := s.registryClient.DisableNode(ctx, nodeID); err != nil {
		return err
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionNodeDisable, nodeID, "", actor.IP)
	return nil
}

// EnableNode clears a prior DisableNode (STATUS.md's S03/P01).
//
// EnableNode 清除此前的 DisableNode（STATUS.md 的 S03/P01）。
func (s *Service) EnableNode(ctx context.Context, actor Actor, nodeID string) error {
	if err := s.requirePlatformActor(actor); err != nil {
		return err
	}
	if nodeID == "" {
		return ErrInvalidInput
	}
	if err := s.registryClient.EnableNode(ctx, nodeID); err != nil {
		return err
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionNodeEnable, nodeID, "", actor.IP)
	return nil
}

// EnterMaintenance puts a node_id under operator-forced maintenance
// (STATUS.md's P01): the node stays connected and finishes in-flight work,
// but receives no new dispatch.
//
// EnterMaintenance 把一个 node_id 置入运维强制的维护状态（STATUS.md 的
// P01）：节点保持连接、跑完在途请求，但不再接收新派发。
func (s *Service) EnterMaintenance(ctx context.Context, actor Actor, nodeID string) error {
	if err := s.requirePlatformActor(actor); err != nil {
		return err
	}
	if nodeID == "" {
		return ErrInvalidInput
	}
	if err := s.registryClient.SetMaintenance(ctx, nodeID); err != nil {
		return err
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionNodeMaintenanceEnter, nodeID, "", actor.IP)
	return nil
}

// ExitMaintenance clears a prior EnterMaintenance.
//
// ExitMaintenance 清除此前的 EnterMaintenance。
func (s *Service) ExitMaintenance(ctx context.Context, actor Actor, nodeID string) error {
	if err := s.requirePlatformActor(actor); err != nil {
		return err
	}
	if nodeID == "" {
		return ErrInvalidInput
	}
	if err := s.registryClient.ClearMaintenance(ctx, nodeID); err != nil {
		return err
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionNodeMaintenanceExit, nodeID, "", actor.IP)
	return nil
}

// ListNodeStates returns every node_id the Registry's identity ledger has an
// opinion about — pending approval, disabled, or under maintenance — so a
// platform operator console can render "expected state" (STATUS.md's P01).
// There is no audit record: this is a read, like CurrentTenant.
//
// ListNodeStates 返回 Registry 身份账本里有记录的每一个 node_id——待审批、
// 已禁用或维护中——好让平台运维控制台渲染出「期望状态」（STATUS.md 的 P01）。
// 这里没有审计记录：这是一次读取，与 CurrentTenant 一样。
func (s *Service) ListNodeStates(ctx context.Context, actor Actor) ([]registryclient.NodeState, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return nil, err
	}
	return s.registryClient.ListNodeStates(ctx)
}
