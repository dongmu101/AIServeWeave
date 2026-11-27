package controlplaneclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

// FilesClient records the files the Gateway writes to object storage in the
// control plane's files table (/internal/v1/files), with the same
// InternalToken as the Job API. A control plane that predates the table
// answers 404: registering then reports httpapi.ErrFileRegistryUnsupported
// and listing returns nothing.
//
// FilesClient 把 Gateway 写进对象存储的文件记录到控制面的 files 表（/internal/v1/files），
// 使用与 Job API 相同的 InternalToken。早于该表的控制面回 404：此时登记返回
// httpapi.ErrFileRegistryUnsupported，列举返回空。
type FilesClient struct {
	c *JobsClient
}

var _ httpapi.FileRegistry = (*FilesClient)(nil)

// NewFilesClient builds a FilesClient. See NewJobsClient for cfg.
// NewFilesClient 构造 FilesClient。cfg 见 NewJobsClient。
func NewFilesClient(cfg JobsClientConfig) (*FilesClient, error) {
	c, err := NewJobsClient(cfg)
	if err != nil {
		return nil, err
	}
	return &FilesClient{c: c}, nil
}

type fileWire struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Storage     string    `json:"storage"`
	ObjectKey   string    `json:"object_key"`
	BizType     string    `json:"biz_type"`
	BizID       string    `json:"biz_id"`
	Kind        string    `json:"kind"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

func filePath(id string) string { return "/internal/v1/files/" + url.PathEscape(id) }

// RegisterFile implements httpapi.FileRegistry.
// RegisterFile 实现 httpapi.FileRegistry。
func (f *FilesClient) RegisterFile(ctx context.Context, rec httpapi.FileRecord) error {
	body := struct {
		TenantID    string `json:"tenant_id"`
		Storage     string `json:"storage"`
		ObjectKey   string `json:"object_key"`
		BizType     string `json:"biz_type"`
		BizID       string `json:"biz_id"`
		Kind        string `json:"kind"`
		Filename    string `json:"filename,omitempty"`
		ContentType string `json:"content_type,omitempty"`
	}{rec.TenantID, rec.Storage, rec.ObjectKey, rec.BizType, rec.BizID, rec.Kind, rec.Filename, rec.ContentType}
	err := f.c.call(ctx, http.MethodPut, filePath(rec.ID), body, nil)
	if errors.Is(err, ErrNotFound) {
		return httpapi.ErrFileRegistryUnsupported
	}
	return err
}

// CompleteFile implements httpapi.FileRegistry. ErrNotFound means the record
// is gone.
// CompleteFile 实现 httpapi.FileRegistry。ErrNotFound 表示记录已不在。
func (f *FilesClient) CompleteFile(ctx context.Context, id string, size int64, sha256Hex, contentType string) error {
	body := struct {
		SizeBytes   int64  `json:"size_bytes"`
		SHA256      string `json:"sha256,omitempty"`
		ContentType string `json:"content_type,omitempty"`
	}{size, sha256Hex, contentType}
	return f.c.call(ctx, http.MethodPost, filePath(id)+"/complete", body, nil)
}

// DeleteFile implements httpapi.FileRegistry.
// DeleteFile 实现 httpapi.FileRegistry。
func (f *FilesClient) DeleteFile(ctx context.Context, id string) error {
	err := f.c.call(ctx, http.MethodDelete, filePath(id), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// ListFiles implements httpapi.FileRegistry.
// ListFiles 实现 httpapi.FileRegistry。
func (f *FilesClient) ListFiles(ctx context.Context, q httpapi.FileQuery) ([]httpapi.StoredFile, error) {
	values := url.Values{"before": {q.Before.UTC().Format(time.RFC3339Nano)}}
	for name, v := range map[string]string{"storage": q.Storage, "tenant_id": q.TenantID, "biz_type": q.BizType, "kind": q.Kind, "status": q.Status} {
		if v != "" {
			values.Set(name, v)
		}
	}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	var page struct {
		Items []fileWire `json:"items"`
	}
	err := f.c.call(ctx, http.MethodGet, "/internal/v1/files?"+values.Encode(), nil, &page)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.StoredFile, 0, len(page.Items))
	for _, w := range page.Items {
		out = append(out, httpapi.StoredFile{
			FileRecord: httpapi.FileRecord{
				ID: w.ID, TenantID: w.TenantID, Storage: w.Storage, ObjectKey: w.ObjectKey,
				BizType: w.BizType, BizID: w.BizID, Kind: w.Kind, Filename: w.Filename, ContentType: w.ContentType,
			},
			Status: w.Status, CreatedAt: w.CreatedAt,
		})
	}
	return out, nil
}
