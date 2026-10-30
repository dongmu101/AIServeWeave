package logic

import (
	"context"
	"errors"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/agentenrollment"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// AgentEnrollmentCA returns only public CA certificates to a platform operator.
//
// AgentEnrollmentCA 仅向平台运维返回公开 CA 证书。
func (s *Service) AgentEnrollmentCA(actor Actor) (agentenrollment.Certificate, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return agentenrollment.Certificate{}, err
	}
	if s.agentEnrollment == nil {
		return agentenrollment.Certificate{}, ErrUnavailable
	}
	ca, err := s.agentEnrollment.CA()
	if err != nil {
		return agentenrollment.Certificate{}, ErrUnavailable
	}
	return ca, nil
}

// CreateAgentEnrollment issues one new-node bundle and records a credential-free audit.
//
// CreateAgentEnrollment 签发一个新节点接入包并记录不含凭据的审计。
func (s *Service) CreateAgentEnrollment(ctx context.Context, actor Actor, registry, gateway string, ttl time.Duration) (agentenrollment.Bundle, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return agentenrollment.Bundle{}, err
	}
	if s.agentEnrollment == nil {
		return agentenrollment.Bundle{}, ErrUnavailable
	}
	bundle, err := s.agentEnrollment.Create(ctx, registry, gateway, ttl)
	if errors.Is(err, agentenrollment.ErrInvalidInput) {
		return agentenrollment.Bundle{}, ErrInvalidInput
	}
	if err != nil {
		return agentenrollment.Bundle{}, ErrUnavailable
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionNodeEnrollmentCreate, bundle.NodeID,
		"expires_at="+bundle.ExpiresAt.UTC().Format(time.RFC3339), actor.IP)
	return bundle, nil
}
