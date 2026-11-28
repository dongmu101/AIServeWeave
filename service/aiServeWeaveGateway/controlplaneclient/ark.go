package controlplaneclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// ArkClient reads the Volcengine Ark key a tenant's Studio video node uses
// from the control plane's internal API. It implements httpapi.ArkSource.
//
// ArkClient 从控制面内部 API 读取租户 Studio 视频节点所用的火山方舟 Key。它实现
// httpapi.ArkSource。
type ArkClient struct {
	c *JobsClient
}

var _ httpapi.ArkSource = (*ArkClient)(nil)

// NewArkClient returns an ArkClient; cfg is validated as NewJobsClient
// validates it.
//
// NewArkClient 返回一个 ArkClient；cfg 的校验与 NewJobsClient 相同。
func NewArkClient(cfg JobsClientConfig) (*ArkClient, error) {
	c, err := NewJobsClient(cfg)
	if err != nil {
		return nil, err
	}
	return &ArkClient{c: c}, nil
}

type arkWire struct {
	Configured bool     `json:"configured"`
	Source     string   `json:"source"`
	Version    uint64   `json:"version"`
	APIKey     string   `json:"api_key"`
	BaseURL    string   `json:"base_url"`
	Models     []string `json:"models"`
}

// Ark implements httpapi.ArkSource. A 404 comes from a control plane that
// predates Ark keys, whose tenants therefore have none.
//
// Ark 实现 httpapi.ArkSource。404 来自尚不支持方舟 Key 的控制面，其租户因此都没有。
func (a *ArkClient) Ark(ctx context.Context, tenantID string) (httpapi.ArkCredential, error) {
	var wire arkWire
	err := a.c.call(ctx, http.MethodGet, "/internal/v1/tenants/"+url.PathEscape(tenantID)+"/ark", nil, &wire)
	if errors.Is(err, ErrNotFound) {
		return httpapi.ArkCredential{}, nil
	}
	if err != nil {
		return httpapi.ArkCredential{}, err
	}
	if !wire.Configured {
		return httpapi.ArkCredential{}, nil
	}
	if (wire.Source != httpapi.ArkSourceTenant && wire.Source != httpapi.ArkSourcePlatform) || wire.APIKey == "" {
		return httpapi.ArkCredential{}, errors.New("controlplaneclient: the control plane described an ark key this Gateway does not understand")
	}
	return httpapi.ArkCredential{
		Configured: true, Source: wire.Source, Version: wire.Version,
		APIKey: wire.APIKey, BaseURL: wire.BaseURL, Models: wire.Models,
	}, nil
}
