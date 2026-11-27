package logic

import (
	"context"
	"encoding/hex"
	"regexp"
	"time"
	"unicode/utf8"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// fileIDPattern is what a file id may look like: the shape of the public
// artifact ids the Gateway mints, which files reuse.
// fileIDPattern 是文件 id 的形式：即 Gateway 铸造、files 沿用的公开产物 id 的形式。
var fileIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// RegisterFileParams is one file the Gateway is about to write.
// RegisterFileParams 是 Gateway 即将写入的一个文件。
type RegisterFileParams struct {
	ID          string
	TenantID    string
	Storage     string
	ObjectKey   string
	BizType     string
	BizID       string
	Kind        string
	Filename    string
	ContentType string
}

// FileListParams selects files for ListFiles; see store.FileFilter.
// FileListParams 为 ListFiles 选择文件，见 store.FileFilter。
type FileListParams = store.FileFilter

// fitsColumn reports whether v is valid UTF-8 of at most max characters, the
// width of the column it goes into.
// fitsColumn 报告 v 是否为至多 max 个字符的合法 UTF-8，即它要写入的列的宽度。
func fitsColumn(v string, max int) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) <= max
}

// RegisterFile records a file as pending before its bytes are written. A
// second registration of the same id replaces the first and keeps its
// registration time.
//
// RegisterFile 在文件字节写入之前把它登记为 pending。同一 id 再次登记时替换前一次，并保留
// 其登记时间。
func (s *Service) RegisterFile(ctx context.Context, p RegisterFileParams) (model.File, error) {
	switch {
	case !fileIDPattern.MatchString(p.ID),
		p.Storage != model.FileStorageDeployment && p.Storage != model.FileStorageTenant,
		p.Storage == model.FileStorageTenant && p.TenantID == "",
		!fitsColumn(p.TenantID, model.IDSize),
		p.ObjectKey == "" || !fitsColumn(p.ObjectKey, 1024),
		p.BizType != model.FileBizJob && p.BizType != model.FileBizStudioRun,
		p.BizID == "" || !fitsColumn(p.BizID, 64),
		p.Kind == "" || !fitsColumn(p.Kind, 32),
		!fitsColumn(p.Filename, 512),
		!fitsColumn(p.ContentType, 255):
		return model.File{}, ErrInvalidInput
	}
	now := s.clock.Now()
	f := model.File{
		ID: p.ID, TenantID: p.TenantID, Storage: p.Storage, ObjectKey: p.ObjectKey,
		BizType: p.BizType, BizID: p.BizID, Kind: p.Kind, Filename: p.Filename, ContentType: p.ContentType,
		Status: model.FileStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.RegisterFile(ctx, &f); err != nil {
		return model.File{}, translate(err)
	}
	return f, nil
}

// CompleteFile records that a file's bytes are written. ErrNotFound when it
// was never registered or has been deleted.
//
// CompleteFile 记录文件字节已写完。从未登记或已删除时返回 ErrNotFound。
func (s *Service) CompleteFile(ctx context.Context, id string, sizeBytes int64, sha256Hex, contentType string) error {
	if !fileIDPattern.MatchString(id) || sizeBytes < 0 || !fitsColumn(contentType, 255) {
		return ErrInvalidInput
	}
	if sha256Hex != "" {
		if raw, err := hex.DecodeString(sha256Hex); err != nil || len(raw) != 32 {
			return ErrInvalidInput
		}
	}
	return translate(s.store.CompleteFile(ctx, id, store.FileCompletion{
		SizeBytes: sizeBytes, SHA256: sha256Hex, ContentType: contentType, UpdatedAt: s.clock.Now(),
	}))
}

// DeleteFile removes a file's record. A file already gone is not an error.
// DeleteFile 删除文件记录。文件已不在不算错误。
func (s *Service) DeleteFile(ctx context.Context, id string) error {
	if !fileIDPattern.MatchString(id) {
		return ErrInvalidInput
	}
	return translate(s.store.DeleteFile(ctx, id))
}

// ListFiles reads one page of files registered before p.Before, oldest
// first, for the Gateway's cleanup.
//
// ListFiles 读取一页登记时间早于 p.Before 的文件，最旧的在前，供 Gateway 清理使用。
func (s *Service) ListFiles(ctx context.Context, p FileListParams) ([]model.File, error) {
	oneOf := func(v string, allowed ...string) bool {
		if v == "" {
			return true
		}
		for _, a := range allowed {
			if v == a {
				return true
			}
		}
		return false
	}
	if p.Before.IsZero() || p.Limit < 0 ||
		!oneOf(p.Storage, model.FileStorageDeployment, model.FileStorageTenant) ||
		!oneOf(p.BizType, model.FileBizJob, model.FileBizStudioRun) ||
		!oneOf(p.Status, model.FileStatusPending, model.FileStatusReady) {
		return nil, ErrInvalidInput
	}
	p.Before = p.Before.UTC().Truncate(time.Microsecond)
	files, err := s.store.ListFiles(ctx, p)
	return files, translate(err)
}
