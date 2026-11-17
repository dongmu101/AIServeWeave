package logic

import (
	"context"
	"errors"
	"strconv"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/session"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// Logout revokes only the current session and records a non-secret audit row
// when a live session changed.
//
// Logout 只吊销当前会话，并在确有有效会话发生变化时记录一条不含机密的审计。
func (s *Service) Logout(ctx context.Context, actor Actor) error {
	subject, action, err := actorSessionSubject(actor, true)
	if err != nil {
		return err
	}
	if s.sessions == nil {
		return ErrUnavailable
	}
	changed, err := s.sessions.Revoke(ctx, subject, actor.SessionID)
	if err != nil {
		return sessionError(err)
	}
	if changed {
		s.audit(ctx, actor.TenantID, actor.UserID, action, actor.UserID, "", actor.IP)
	}
	return nil
}

// RevokeOwnSessions revokes every session owned by the current account and
// records the bounded count, never a session identifier.
//
// RevokeOwnSessions 吊销当前账户拥有的全部会话，并记录这个有界数量，绝不记录会话标识。
func (s *Service) RevokeOwnSessions(ctx context.Context, actor Actor) error {
	subject, action, err := actorSessionSubject(actor, false)
	if err != nil {
		return err
	}
	if s.sessions == nil {
		return ErrUnavailable
	}
	count, err := s.sessions.RevokeAll(ctx, subject)
	if err != nil {
		return sessionError(err)
	}
	if count > 0 {
		s.audit(ctx, actor.TenantID, actor.UserID, action, actor.UserID, "sessions revoked "+strconv.Itoa(count), actor.IP)
	}
	return nil
}

func actorSessionSubject(actor Actor, logout bool) (session.Subject, string, error) {
	if actor.SessionID == "" || actor.UserID == "" || actor.TenantID == "" || actor.Role == "" {
		return session.Subject{}, "", ErrForbidden
	}
	if isPlatformActor(actor) {
		action := model.ActionPlatformOperatorSessionsRevoke
		if logout {
			action = model.ActionPlatformSessionLogout
		}
		return session.Subject{Kind: session.SubjectPlatformOperator, ID: actor.UserID}, action, nil
	}
	if actor.TenantID == model.PlatformScope {
		return session.Subject{}, "", ErrForbidden
	}
	action := model.ActionUserSessionsRevoke
	if logout {
		action = model.ActionSessionLogout
	}
	return session.Subject{Kind: session.SubjectTenantUser, ID: actor.UserID}, action, nil
}

func sessionError(err error) error {
	switch {
	case errors.Is(err, session.ErrUnavailable):
		return ErrUnavailable
	case errors.Is(err, session.ErrMutationActive):
		return ErrConflict
	case errors.Is(err, session.ErrInvalid):
		return ErrNotFound
	default:
		return err
	}
}

// SessionHandoffUser returns the account behind actor's session so the caller
// can issue it a second, independent session — how a Console session signs
// the same person into Studio without a password. The account must still be
// active and hold the role the session asserts; a demoted or disabled user
// gets nothing new. The event is audited as a login, since that is what it
// grants.
//
// SessionHandoffUser 返回 actor 会话背后的账户，供调用方为它另签一个独立会话——Console
// 会话无需密码即可让同一个人登录 Studio，靠的就是它。账户须仍处于启用状态，且角色与会话
// 主张的一致；被降级或停用的用户拿不到新会话。该事件按登录记入审计，因为它授予的正是登录。
func (s *Service) SessionHandoffUser(ctx context.Context, actor Actor) (model.User, error) {
	if actor.UserID == "" || actor.TenantID == "" || isPlatformActor(actor) {
		return model.User{}, ErrForbidden
	}
	user, err := s.store.GetUser(ctx, actor.TenantID, actor.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return model.User{}, ErrForbidden
		}
		return model.User{}, err
	}
	if user.Status != model.StatusActive || user.Role != actor.Role {
		return model.User{}, ErrForbidden
	}
	s.audit(ctx, user.TenantID, user.ID, model.ActionUserLogin, user.ID, "session handoff", actor.IP)
	return user, nil
}
