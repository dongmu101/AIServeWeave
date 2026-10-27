package fleet

import (
	"context"
	"errors"
)

// ErrUnknownGateway rejects a selector outside the configured Gateway list.
// ErrUnknownGateway 拒绝不在已配置 Gateway 清单内的选择值。
var ErrUnknownGateway = errors.New("fleet: unknown gateway")

// GatewayNodes reads only one configured Gateway's own node and model observations.
// The selector "default" selects the first configured Gateway. Other selectors
// must exactly match a configured endpoint; arbitrary URLs are never fetched.
//
// GatewayNodes 仅读取一个已配置 Gateway 自己的节点与模型观测。
// 选择值 "default" 表示第一个已配置 Gateway；其他值必须与已配置地址完全相同，
// 绝不请求任意 URL。
func (a *Aggregator) GatewayNodes(ctx context.Context, selector string) (Snapshot, error) {
	if a == nil {
		return Snapshot{}, ErrDisabled
	}
	for i, endpoint := range a.gateways {
		if selector == endpoint || (selector == "default" && i == 0) {
			selected := *a
			selected.gateways = []string{endpoint}
			return selected.Nodes(ctx)
		}
	}
	return Snapshot{}, ErrUnknownGateway
}
