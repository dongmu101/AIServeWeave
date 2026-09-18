package logic

import (
	"context"

	"AIServeWeave/common/comfyuimanagedstatus"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/comfyuimanagedrouter"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// TriggerComfyUIManagedAction asks nodeID, wherever it is currently
// connected among the configured Gateway replicas, to apply action to its
// one locally-declared Managed ComfyUI instance (STATUS.md's P2 ComfyUI
// Managed Docker deployment, subtask two's control plane forwarding layer).
// It forwards rather than executes — the same TriggerModelPull reasoning
// applies here: the actual container operation, and its outcome, are only
// ever observed later through a status read, which this layer does not
// gate.
//
// ErrNotFound is returned when no configured replica reports nodeID
// connected, the same reasoning TriggerModelPull's own doc comment gives.
// An audit entry is recorded only when the trigger was actually delivered
// somewhere.
//
// TriggerComfyUIManagedAction 要求 nodeID（无论它当前连在哪个已配置副本上）
// 对它本地已声明的那一个 Managed ComfyUI 实例施加 action（STATUS.md 的 P2
// ComfyUI Managed Docker 部署子任务二的控制面转发层）。它只转发、不执
// 行——与 TriggerModelPull 同一理由：真正的容器操作及其结果，只能之后通过
// 一次状态读取才能观察到，本层完全不对此设门。
//
// 当没有任何已配置副本报告 nodeID 已连接时返回 ErrNotFound，理由与
// TriggerModelPull 自己的文档注释相同。只有触发确实被下发到了某处，才会
// 记一条审计。
func (s *Service) TriggerComfyUIManagedAction(ctx context.Context, actor Actor, nodeID string, action comfyuimanagedstatus.Action) (comfyuimanagedrouter.Result, error) {
	if err := s.requirePlatformActor(actor); err != nil {
		return comfyuimanagedrouter.Result{}, err
	}
	if s.comfyUIManagedRouter == nil {
		return comfyuimanagedrouter.Result{}, ErrComfyUIManagedRouterUnconfigured
	}
	if nodeID == "" || action == comfyuimanagedstatus.ActionUnspecified {
		return comfyuimanagedrouter.Result{}, ErrInvalidInput
	}
	result, err := s.comfyUIManagedRouter.Trigger(ctx, nodeID, action)
	if err != nil {
		return comfyuimanagedrouter.Result{}, err
	}
	if !result.Connected {
		return result, ErrNotFound
	}
	s.audit(ctx, model.PlatformScope, actor.UserID, model.ActionComfyUIManagedTrigger, nodeID, "action "+action.String(), actor.IP)
	return result, nil
}
