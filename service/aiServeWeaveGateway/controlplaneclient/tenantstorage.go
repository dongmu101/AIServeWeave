package controlplaneclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"AIServeWeave/common/objectstore"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// TenantStorageClient reads tenants' own object storage, with credentials,
// from the control plane's internal API. It implements
// httpapi.TenantStorageSource.
//
// TenantStorageClient 从控制面内部 API 读取租户自有对象存储（含凭据）。它实现
// httpapi.TenantStorageSource。
type TenantStorageClient struct {
	c *JobsClient
}

var _ httpapi.TenantStorageSource = (*TenantStorageClient)(nil)

// NewTenantStorageClient returns a TenantStorageClient; cfg is validated as
// NewJobsClient validates it.
//
// NewTenantStorageClient 返回一个 TenantStorageClient；cfg 的校验与 NewJobsClient 相同。
func NewTenantStorageClient(cfg JobsClientConfig) (*TenantStorageClient, error) {
	c, err := NewJobsClient(cfg)
	if err != nil {
		return nil, err
	}
	return &TenantStorageClient{c: c}, nil
}

type tenantStorageWire struct {
	TenantID              string `json:"tenant_id"`
	Configured            bool   `json:"configured"`
	AllowPrivateEndpoints bool   `json:"allow_private_endpoints"`
	Version               uint64 `json:"version"`
	Kind                  string `json:"kind"`
	S3                    *struct {
		Endpoint        string `json:"endpoint"`
		Region          string `json:"region"`
		Bucket          string `json:"bucket"`
		Prefix          string `json:"prefix"`
		PathStyle       bool   `json:"path_style"`
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
	} `json:"s3"`
	WebDAV *struct {
		URL      string `json:"url"`
		Dir      string `json:"dir"`
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"webdav"`
	RetentionDays int `json:"retention_days"`
}

func (w tenantStorageWire) toConfig(tenantID string) (httpapi.TenantStorageConfig, error) {
	out := httpapi.TenantStorageConfig{TenantID: tenantID, AllowPrivate: w.AllowPrivateEndpoints}
	if !w.Configured {
		return out, nil
	}
	out.Configured, out.Version, out.RetentionDays = true, w.Version, w.RetentionDays
	out.Backend.Kind = w.Kind
	switch {
	case w.Kind == "s3" && w.S3 != nil:
		out.Backend.S3 = objectstore.S3Config{
			Endpoint: w.S3.Endpoint, Region: w.S3.Region, Bucket: w.S3.Bucket, Prefix: w.S3.Prefix, UsePathStyle: w.S3.PathStyle,
			AccessKeyID: w.S3.AccessKeyID, SecretAccessKey: w.S3.SecretAccessKey,
		}
	case w.Kind == "webdav" && w.WebDAV != nil:
		out.Backend.WebDAV = objectstore.WebDAVConfig{URL: w.WebDAV.URL, Dir: w.WebDAV.Dir, Username: w.WebDAV.Username, Password: w.WebDAV.Password}
	default:
		return httpapi.TenantStorageConfig{}, errors.New("controlplaneclient: the control plane described a tenant storage this Gateway does not understand")
	}
	return out, nil
}

// TenantStorage implements httpapi.TenantStorageSource. A 404 comes from a
// control plane that predates tenant storage, whose tenants therefore have
// none.
//
// TenantStorage 实现 httpapi.TenantStorageSource。404 来自尚不支持租户存储的控制面，其租户
// 因此都没有自有存储。
func (t *TenantStorageClient) TenantStorage(ctx context.Context, tenantID string) (httpapi.TenantStorageConfig, error) {
	var wire tenantStorageWire
	err := t.c.call(ctx, http.MethodGet, "/internal/v1/tenants/"+url.PathEscape(tenantID)+"/storage", nil, &wire)
	if errors.Is(err, ErrNotFound) {
		return httpapi.TenantStorageConfig{TenantID: tenantID}, nil
	}
	if err != nil {
		return httpapi.TenantStorageConfig{}, err
	}
	return wire.toConfig(tenantID)
}

// RetainedTenantStorage implements httpapi.TenantStorageSource. A 404 is an
// empty last page, for the reason TenantStorage gives.
//
// RetainedTenantStorage 实现 httpapi.TenantStorageSource。404 视为空的末页，理由同
// TenantStorage。
func (t *TenantStorageClient) RetainedTenantStorage(ctx context.Context, after string) ([]httpapi.TenantStorageConfig, string, error) {
	var page struct {
		Items []tenantStorageWire `json:"items"`
		Next  string              `json:"next"`
	}
	err := t.c.call(ctx, http.MethodGet, "/internal/v1/tenant-storage/retained?after="+url.QueryEscape(after), nil, &page)
	if errors.Is(err, ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	out := make([]httpapi.TenantStorageConfig, 0, len(page.Items))
	for _, item := range page.Items {
		if item.TenantID == "" {
			continue
		}
		cfg, err := item.toConfig(item.TenantID)
		if err != nil {
			continue
		}
		out = append(out, cfg)
	}
	return out, page.Next, nil
}
