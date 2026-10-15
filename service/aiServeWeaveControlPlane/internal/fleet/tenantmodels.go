package fleet

import (
	"context"
	"time"

	"AIServeWeave/common/nodeview"
)

// TenantModelService is the public description of an Agent-reported AI backend.
// Agent and service identifiers distinguish reports; addresses and diagnostics stay private.
//
// TenantModelService 是 Agent 上报的 AI 后端面向租户的说明。
// Agent 与服务标识用于区分上报来源；地址与诊断文本保持私有。
type TenantModelService struct {
	// AgentID identifies the reporting Agent. / AgentID 标识上报的 Agent。
	AgentID string `json:"agent_id"`
	// ServiceID identifies a runtime on that Agent. / ServiceID 标识该 Agent 上的运行时。
	ServiceID  string           `json:"service_id"`
	NodeOnline bool             `json:"node_online"`
	Kind       string           `json:"kind"`
	State      string           `json:"state"`
	Version    string           `json:"version"`
	Models     []nodeview.Model `json:"models"`
}

// TenantModelCatalog exposes Agent-reported service and model facts to authenticated tenants.
// TenantModelCatalog 向已认证租户展示 Agent 上报的服务与模型事实。
type TenantModelCatalog struct {
	CollectedAt time.Time            `json:"collected_at"`
	Partial     bool                 `json:"partial"`
	Services    []TenantModelService `json:"services"`
}

// TenantModels reads the default Gateway's Agent inventory through a field allowlist.
// This shared discovery catalog does not claim that models are routable for a tenant.
//
// TenantModels 通过字段允许列表读取连接到默认 Gateway 的 Agent 清单。
// 这份共享发现目录不承诺模型已对租户开放推理路由。
func (a *Aggregator) TenantModels(ctx context.Context) (TenantModelCatalog, error) {
	snapshot, err := a.GatewayNodes(ctx, "default")
	if err != nil {
		return TenantModelCatalog{}, err
	}
	out := TenantModelCatalog{
		CollectedAt: snapshot.CollectedAt,
		Partial:     snapshot.Partial,
		Services:    make([]TenantModelService, 0),
	}
	if snapshot.Partial {
		return out, nil
	}
	for _, node := range snapshot.Nodes {
		for _, backend := range node.Runtimes {
			out.Services = append(out.Services, TenantModelService{
				AgentID: node.NodeID, ServiceID: backend.ID,
				NodeOnline: node.Live,
				Kind:       backend.Kind, State: backend.State, Version: backend.Version,
				Models: backend.Models,
			})
		}
	}
	return out, nil
}
