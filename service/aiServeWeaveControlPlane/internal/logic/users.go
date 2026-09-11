package logic

import (
	"context"
	"errors"
	"fmt"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/session"
)

// ChangeOwnPassword verifies the current password, changes it, and revokes
// every session including the caller's.
//
// ChangeOwnPassword 校验当前密码、修改它，并吊销包括调用方在内的全部会话。
func (s *Service) ChangeOwnPassword(ctx context.Context, actor Actor, currentPassword, newPassword string) error {
	if actor.TenantID == model.PlatformScope || actor.UserID == "" {
		return ErrForbidden
	}
	user, err := s.store.GetUser(ctx, actor.TenantID, actor.UserID)
	if err != nil {
		return translate(err)
	}
	if user.Status != model.StatusActive || comparePassword(user.PasswordHash, currentPassword) != nil {
		return ErrInvalidCredentials
	}
	digest, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	gate, err := s.beginUserMutation(ctx, user)
	if err != nil {
		return err
	}
	audit := s.auditEntry(actor.TenantID, actor.UserID, model.ActionUserPasswordChange, user.ID, "", actor.IP)
	_, mutationErr := s.store.UpdateUserPassword(ctx, actor.TenantID, user.ID, digest, audit)
	return s.finishMutation(ctx, gate, mutationErr)
}

// ResetUserPassword replaces another tenant user's password and revokes all
// of their sessions. Only an owner may call it.
//
// ResetUserPassword 替换同租户另一名用户的密码并吊销其全部会话。只有 owner 可调用。
func (s *Service) ResetUserPassword(ctx context.Context, actor Actor, userID, newPassword string) error {
	user, err := s.managedUser(ctx, actor, userID)
	if err != nil {
		return err
	}
	digest, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	gate, err := s.beginUserMutation(ctx, user)
	if err != nil {
		return err
	}
	audit := s.auditEntry(actor.TenantID, actor.UserID, model.ActionUserPasswordReset, user.ID, "", actor.IP)
	_, mutationErr := s.store.UpdateUserPassword(ctx, actor.TenantID, user.ID, digest, audit)
	return s.finishMutation(ctx, gate, mutationErr)
}

// ChangeUserRole changes another tenant user's role and revokes their
// sessions while preserving an active owner.
//
// ChangeUserRole 修改同租户另一名用户的角色、吊销其会话，并保留一个有效 owner。
func (s *Service) ChangeUserRole(ctx context.Context, actor Actor, userID, role string) error {
	if !validRole(role) {
		return ErrInvalidInput
	}
	user, err := s.managedUser(ctx, actor, userID)
	if err != nil {
		return err
	}
	if user.Role == role {
		return nil
	}
	gate, err := s.beginUserMutation(ctx, user)
	if err != nil {
		return err
	}
	audit := s.auditEntry(actor.TenantID, actor.UserID, model.ActionUserRoleChange, user.ID,
		fmt.Sprintf("role %s -> %s", user.Role, role), actor.IP)
	_, mutationErr := s.store.UpdateUserRole(ctx, actor.TenantID, user.ID, role, s.clock.Now(), audit)
	return s.finishMutation(ctx, gate, mutationErr)
}

// DisableUser suspends another tenant user, revokes their sessions, and
// transactionally revokes every active API Key they created.
//
// DisableUser 暂停同租户另一名用户、吊销其会话，并在事务中吊销其创建的全部有效 API Key。
func (s *Service) DisableUser(ctx context.Context, actor Actor, userID string) error {
	user, err := s.managedUser(ctx, actor, userID)
	if err != nil {
		return err
	}
	if user.Status == model.StatusSuspended {
		return nil
	}
	gate, err := s.beginUserMutation(ctx, user)
	if err != nil {
		return err
	}
	audit := s.auditEntry(actor.TenantID, actor.UserID, model.ActionUserDisable, user.ID, "user disabled and creator keys revoked", actor.IP)
	_, mutationErr := s.store.SetUserStatus(ctx, actor.TenantID, user.ID, model.StatusSuspended, s.clock.Now(), audit)
	if mutationErr == nil && s.invalidator != nil {
		s.invalidator.InvalidateAll(ctx)
	}
	return s.finishMutation(ctx, gate, mutationErr)
}

// EnableUser reactivates another tenant user without restoring sessions or
// API Keys.
//
// EnableUser 重新启用同租户另一名用户，但不恢复会话或 API Key。
func (s *Service) EnableUser(ctx context.Context, actor Actor, userID string) error {
	user, err := s.managedUser(ctx, actor, userID)
	if err != nil {
		return err
	}
	if user.Status == model.StatusActive {
		return nil
	}
	gate, err := s.beginUserMutation(ctx, user)
	if err != nil {
		return err
	}
	audit := s.auditEntry(actor.TenantID, actor.UserID, model.ActionUserEnable, user.ID, "", actor.IP)
	_, mutationErr := s.store.SetUserStatus(ctx, actor.TenantID, user.ID, model.StatusActive, s.clock.Now(), audit)
	return s.finishMutation(ctx, gate, mutationErr)
}

// RevokeUserSessions revokes every session belonging to another tenant user.
//
// RevokeUserSessions 吊销同租户另一名用户的全部会话。
func (s *Service) RevokeUserSessions(ctx context.Context, actor Actor, userID string) error {
	user, err := s.managedUser(ctx, actor, userID)
	if err != nil {
		return err
	}
	if s.sessions == nil {
		return ErrUnavailable
	}
	count, err := s.sessions.RevokeAll(ctx, userSubject(user))
	if err != nil {
		return sessionError(err)
	}
	if count > 0 {
		s.audit(ctx, actor.TenantID, actor.UserID, model.ActionUserSessionsRevoke, user.ID,
			fmt.Sprintf("sessions revoked %d", count), actor.IP)
	}
	return nil
}

func (s *Service) managedUser(ctx context.Context, actor Actor, userID string) (model.User, error) {
	if actor.Role != model.RoleOwner {
		return model.User{}, ErrForbidden
	}
	if userID == "" {
		return model.User{}, ErrInvalidInput
	}
	if actor.UserID == userID {
		return model.User{}, ErrConflict
	}
	user, err := s.store.GetUser(ctx, actor.TenantID, userID)
	if err != nil {
		return model.User{}, translate(err)
	}
	return user, nil
}

func (s *Service) beginUserMutation(ctx context.Context, user model.User) (session.Gate, error) {
	if s.sessions == nil {
		return session.Gate{}, ErrUnavailable
	}
	gate, err := s.sessions.BeginMutation(ctx, userSubject(user))
	if err != nil {
		return session.Gate{}, sessionError(err)
	}
	return gate, nil
}

func (s *Service) finishMutation(ctx context.Context, gate session.Gate, mutationErr error) error {
	endErr := s.sessions.EndMutation(ctx, gate)
	if errors.Is(endErr, session.ErrInvalid) {
		// The lease expired during a slow database transaction. Reacquiring the
		// gate revokes any session created in that gap before the operation can
		// report success. Another active mutation is equally safe: acquiring its
		// gate already performed the same revocation.
		//
		// 慢数据库事务期间租约已经过期。重新取得门会在操作报告成功前，吊销这段空隙里
		// 创建的任何会话。另一项变更已经持门也同样安全：它取得门时已经做过同一次吊销。
		replacement, repairErr := s.sessions.BeginMutation(ctx, gate.Subject)
		switch {
		case repairErr == nil:
			endErr = s.sessions.EndMutation(ctx, replacement)
		case errors.Is(repairErr, session.ErrMutationActive):
			endErr = nil
		default:
			endErr = repairErr
		}
	}
	if mutationErr != nil {
		return translate(mutationErr)
	}
	if endErr != nil {
		return sessionError(endErr)
	}
	return nil
}

func userSubject(user model.User) session.Subject {
	return session.Subject{Kind: session.SubjectTenantUser, ID: user.ID}
}
