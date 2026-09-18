package logic

import (
	"context"
	"strings"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/modelpullrouter"
)

// TriggerModelPull asks nodeID, wherever it is currently connected among the
// configured Gateway replicas, to start pulling names from its local
// manifest (STATUS.md's P2 model distribution subtask 1). It forwards
// rather than executes: the actual fetch, and any name-level rejection
// (unknown name, quota exceeded, ...), happen at the Agent and are only ever
// observed later through a status read, which this layer does not gate —
// see ModelPullRouter's doc comment for why Status bypasses this layer
// entirely.
//
// ErrNotFound is returned when no configured replica reports nodeID
// connected: this call had nothing to forward to, the same reasoning
// modelpullapi's own 404 uses for a single replica. An audit entry is
// recorded only when the trigger was actually delivered somewhere — see
// service.go's audit doc comment for why a request that found nothing to
// act on leaves none, the rule ApproveNode already follows.
//
// TriggerModelPull 要求 nodeID（无论它当前连在哪个已配置副本上）开始从本地
// 清单（STATUS.md 的 P2 模型分发子任务一）拉取 names。它只转发、不执行：真正
// 的抓取、以及任何名字级别的拒绝（未知名字、超出配额……）都发生在 Agent 一
// 侧，只能之后通过一次状态读取才能观察到——本层完全不对此设门，见
// ModelPullRouter 文档注释里 Status 为什么整个绕开本层的说明。
//
// 当没有任何已配置副本报告 nodeID 已连接时返回 ErrNotFound：这次调用没有
// 任何可转发的对象，与 modelpullapi 自己在单个副本上用 404 表达的是同一个
// 理由。只有触发确实被下发到了某处，才会记一条审计——为什么一次没找到任何
// 可执行对象的请求不留审计，见 service.go 的 audit 文档注释，ApproveNode
// 已经遵循的同一条规则。
func (s *Service) TriggerModelPull(ctx context.Context, actor Actor, nodeID string, names []string) (modelpullrouter.Result, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return modelpullrouter.Result{}, err
	}
	if s.modelPullRouter == nil {
		return modelpullrouter.Result{}, ErrModelPullRouterUnconfigured
	}
	if nodeID == "" || len(names) == 0 {
		return modelpullrouter.Result{}, ErrInvalidInput
	}
	result, err := s.modelPullRouter.Trigger(ctx, nodeID, names)
	if err != nil {
		return modelpullrouter.Result{}, err
	}
	if !result.Connected {
		return result, ErrNotFound
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionModelPullTrigger, nodeID, "names "+strings.Join(names, ","), actor.IP)
	return result, nil
}
