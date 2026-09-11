package logic

import (
	"context"
	"errors"
	"strconv"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/session"
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
