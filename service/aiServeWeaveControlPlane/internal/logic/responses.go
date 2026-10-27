package logic

import (
	"context"
	"errors"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// CreateResponseTurnParams is what a Gateway replica reports after finishing
// one Responses API turn the caller asked to persist (STATUS.md's P2
// "Responses 持久会话"). ResponseID is minted by that replica, not by this
// service — the same reason CreateJobParams' JobID is, see its doc comment.
//
// CreateResponseTurnParams 是一个 Gateway 副本在完成调用方要求持久化的一轮
// Responses API 之后所报告的内容（STATUS.md 的 P2「Responses 持久会话」）。
// ResponseID 由那个副本铸造，不是本服务铸造——理由与 CreateJobParams 的
// JobID 相同，见其文档注释。
type CreateResponseTurnParams struct {
	ResponseID         string
	TenantID           string
	PreviousResponseID string
	Model              string
	Messages           string
}

// CreateResponseTurn records one turn. Like CreateJob, a duplicate
// ResponseID is not an error: it is the persistence contract's "提交结果
// 未知" window resolving into a second report of the same turn, so the
// existing row is fetched and returned instead of surfacing a conflict a
// retrying Gateway would have to interpret.
//
// It performs no tenant-scoped authorization for the same reason CreateJob
// does not: the caller is the Gateway, authenticated by the internal shared
// secret at the handler layer, not a signed-in user acting within one
// tenant's session.
//
// CreateResponseTurn 记录一轮。与 CreateJob 一样，重复的 ResponseID 不是
// 错误：这是持久化契约的「提交结果未知」窗口演变成对同一轮的第二次报告，
// 因此会改为读取并返回已有的那一行，而不是把一个还要重试的 Gateway 自己去
// 解读的冲突暴露出来。
//
// 它不做任何按租户的授权检查，理由与 CreateJob 相同：调用方是 Gateway，在
// handler 层已由内部共享密钥认证，而不是在某个租户会话内行动的已登录用户。
func (s *Service) CreateResponseTurn(ctx context.Context, p CreateResponseTurnParams) (model.ResponseTurn, error) {
	if p.ResponseID == "" || p.TenantID == "" || p.Messages == "" {
		return model.ResponseTurn{}, ErrInvalidInput
	}
	turn := model.ResponseTurn{
		ID:                 p.ResponseID,
		TenantID:           p.TenantID,
		PreviousResponseID: p.PreviousResponseID,
		Model:              p.Model,
		Messages:           p.Messages,
		CreatedAt:          s.clock.Now(),
	}
	err := s.store.CreateResponseTurn(ctx, &turn)
	if err == nil {
		return turn, nil
	}
	if errors.Is(err, store.ErrConflict) {
		existing, getErr := s.store.GetResponseTurn(ctx, p.TenantID, p.ResponseID)
		if getErr != nil {
			// The conflict was real but the row is not visible under this
			// tenant: something else's turn holds this id.
			//
			// 冲突是真实的，但这一行在该租户下不可见：这个 id 被别的东西的
			// 轮次占用了。
			return model.ResponseTurn{}, ErrConflict
		}
		return existing, nil
	}
	return model.ResponseTurn{}, translate(err)
}

// GetResponseTurn reads one turn, scoped to the tenant the Gateway asserts.
//
// GetResponseTurn 读取一轮，限定在 Gateway 所断言的租户范围内。
func (s *Service) GetResponseTurn(ctx context.Context, tenantID, responseID string) (model.ResponseTurn, error) {
	turn, err := s.store.GetResponseTurn(ctx, tenantID, responseID)
	return turn, translate(err)
}
