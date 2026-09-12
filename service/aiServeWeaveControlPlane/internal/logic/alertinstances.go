// alertinstances.go implements STATUS.md's P09/C29 read and
// acknowledge-only surface over fired alerts — creation, touching and
// resolution are the evaluation loop's job (internal/alertengine), not a
// platform actor's; this file only covers what an operator does through
// the API.
//
// alertinstances.go 实现 STATUS.md P09/C29 对已触发告警的只读与"仅确认"
// 操作面——创建、续期与解除是评估循环(internal/alertengine)的工作，不是
// 平台身份该做的事；本文件只覆盖运维通过 API 能做的部分。
package logic

import (
	"context"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// ListAlertInstances reads one page of alert instances.
//
// ListAlertInstances 读取一页告警实例。
func (s *Service) ListAlertInstances(ctx context.Context, actor Actor, query store.ListQuery, filter store.AlertInstanceFilter) (store.Page[model.AlertInstance], error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return store.Page[model.AlertInstance]{}, err
	}
	return s.store.ListAlertInstances(ctx, query, filter)
}

// AcknowledgeAlert marks one firing instance as acknowledged.
// store.ErrConflict (translated) surfaces unchanged when the instance is
// already resolved — the handler renders that as a definite "no", not
// something to retry.
//
// AcknowledgeAlert 把一个 firing 的实例标记为 acknowledged。实例已经
// resolved 时，store.ErrConflict(经过转译)原样透出——handler 把它渲染成
// 一个确定的"否"，而不是什么值得重试的东西。
func (s *Service) AcknowledgeAlert(ctx context.Context, actor Actor, id string) (model.AlertInstance, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return model.AlertInstance{}, err
	}
	instance, err := s.store.AcknowledgeAlertInstance(ctx, id, actor.UserID, s.clock.Now())
	if err != nil {
		return model.AlertInstance{}, translate(err)
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionAlertAcknowledge, id, "", actor.IP)
	return instance, nil
}
