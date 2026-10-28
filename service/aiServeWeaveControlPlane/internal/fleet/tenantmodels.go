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
	// CLIUsage is the subscription quota of the CLI this service runs, present
	// only for a codex or claude service whose Agent has reported one. It is the
	// CLI account's shared quota, not a per-tenant figure.
	//
	// CLIUsage 是该服务所运行 CLI 的订阅额度，仅当 codex 或 claude 服务的 Agent 上报过
	// 才存在。它是 CLI 账户的共享额度，不是按租户划分的数字。
	CLIUsage *nodeview.CLIUsage `json:"cli_usage,omitempty"`
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
				Models:   backend.Models,
				CLIUsage: cliUsageFor(node.CLIUsage, backend.ID, backend.Kind),
			})
		}
	}
	return out, nil
}

// Filter keeps only the models visible reports true for, and drops a service
// left with none: a tenant that may not use anything a service offers has no
// reason to learn that service exists.
//
// Filter 只保留 visible 返回 true 的模型，并去掉过滤后一个模型都不剩的服务：一个
// 不能使用某服务任何模型的租户，没有理由得知这个服务的存在。
func (c TenantModelCatalog) Filter(visible func(string) bool) TenantModelCatalog {
	out := c
	out.Services = make([]TenantModelService, 0, len(c.Services))
	for _, service := range c.Services {
		var models []nodeview.Model
		for _, model := range service.Models {
			if visible(model.ID) {
				models = append(models, model)
			}
		}
		if len(models) == 0 {
			continue
		}
		service.Models = models
		out.Services = append(out.Services, service)
	}
	return out
}

// cliUsageFor returns the quota reported for the service runtimeID of kind, or
// nil when the Agent reported none. An entry naming runtimeID wins; an entry
// with no runtime ID comes from an Agent that predates multi-account support,
// where services of the same kind on one node shared one CLI account, so it is
// matched by kind alone. The copy keeps a response from aliasing the snapshot.
//
// cliUsageFor 返回为 kind 类型的服务 runtimeID 上报的额度，Agent 没有上报时返回 nil。
// 点名 runtimeID 的记录优先；不带运行时 ID 的记录来自尚不支持多账户的旧版 Agent，那时
// 同一节点上同类型的服务共用一个 CLI 账户，因此只按 kind 匹配。复制一份是为了不让响应
// 与快照共用内存。
func cliUsageFor(usage []nodeview.CLIUsage, runtimeID, kind string) *nodeview.CLIUsage {
	var match *nodeview.CLIUsage
	for i, entry := range usage {
		if entry.Kind != kind {
			continue
		}
		if entry.RuntimeID == runtimeID {
			match = &usage[i]
			break
		}
		if entry.RuntimeID == "" && match == nil {
			match = &usage[i]
		}
	}
	if match == nil {
		return nil
	}
	copied := *match
	copied.Windows = append([]nodeview.CLIUsageWindow(nil), match.Windows...)
	return &copied
}
