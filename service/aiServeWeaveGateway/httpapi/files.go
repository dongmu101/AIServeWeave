package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"time"
	"unicode/utf8"

	"AIServeWeave/common/objectstore"
)

// Where a file is, what it belongs to and what it is within that. These
// match the control plane's files table; see .scratch/file-registry/spec.md.
//
// 文件所在的存储、所属的业务对象，以及在该业务内的类别。与控制面的 files 表一致，见
// .scratch/file-registry/spec.md。
const (
	fileStorageDeployment = "deployment"
	fileStorageTenant     = "tenant"

	fileBizJob       = "job"
	fileBizStudioRun = "studio_run"

	fileKindUpload = "upload"
	fileKindCopy   = "copy"
	fileKindImage  = "image"
	fileKindVideo  = "video"

	filePending = "pending"

	// maxFilenameBytes is the widest name the files table keeps. A longer
	// one is cut rather than refused: it is only shown.
	// maxFilenameBytes 是 files 表保存的最长文件名。更长的名字被截断而不是被拒绝：它只用于展示。
	maxFilenameBytes = 512
)

// FileRecord is one file the Gateway registers before writing it.
// FileRecord 是 Gateway 在写入前登记的一个文件。
type FileRecord struct {
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

// StoredFile is one registered file as the cleanup reads it.
// StoredFile 是清理读到的一个已登记文件。
type StoredFile struct {
	FileRecord
	Status    string
	CreatedAt time.Time
}

// FileQuery selects registered files: those registered before Before that
// match every non-empty field, oldest first.
//
// FileQuery 选择已登记的文件：登记时间早于 Before、且与每个非空字段相符的文件，最旧的在前。
type FileQuery struct {
	Before   time.Time
	Storage  string
	TenantID string
	BizType  string
	Kind     string
	Status   string
	Limit    int
}

// ErrFileRegistryUnsupported is RegisterFile's answer from a control plane
// that predates the files table. Writes then go ahead unrecorded, as they
// did before.
//
// ErrFileRegistryUnsupported 是早于 files 表的控制面对 RegisterFile 的答复。此时写入照常
// 进行、不做登记，与以前相同。
var ErrFileRegistryUnsupported = errors.New("httpapi: the control plane does not record files")

// FileRegistry records every file written to object storage in the control
// plane's files table.
//
// FileRegistry 把写进对象存储的每个文件记录到控制面的 files 表。
type FileRegistry interface {
	// RegisterFile records rec as pending, replacing an earlier record of
	// the same id.
	// RegisterFile 把 rec 登记为 pending，替换同一 id 的较早记录。
	RegisterFile(ctx context.Context, rec FileRecord) error
	// CompleteFile records that the file's bytes are written.
	// CompleteFile 记录文件字节已写完。
	CompleteFile(ctx context.Context, id string, size int64, sha256Hex, contentType string) error
	// DeleteFile removes the record. A record already gone is not an error.
	// DeleteFile 删除记录。记录已不在不算错误。
	DeleteFile(ctx context.Context, id string) error
	// ListFiles reads one page of records matching q.
	// ListFiles 读取一页符合 q 的记录。
	ListFiles(ctx context.Context, q FileQuery) ([]StoredFile, error)
}

// fileTarget is where a new file goes.
// fileTarget 是新文件的去处。
type fileTarget struct {
	backend objectstore.Backend
	storage string
}

// fileWritten is what put learned writing a file.
// fileWritten 是 put 写文件时得到的信息。
type fileWritten struct {
	size       int64
	sha256Hex  string
	registered bool
}

// put registers rec as pending in target, writes body under rec.ObjectKey
// and records the file as complete. Any failure removes what it wrote; an
// object that cannot be removed keeps its pending record, which the cleanup
// reaps later. A registry that cannot be reached fails the write rather
// than leave an object no record knows.
//
// put 在 target 中把 rec 登记为 pending，把 body 写到 rec.ObjectKey 下，并把文件记为已完成。
// 任何一步失败都会删除已写入的内容；删不掉的对象保留其 pending 记录，由清理稍后回收。登记
// 不可达时写入失败，而不是留下一个没有记录的对象。
func (f *fileStore) put(ctx context.Context, target fileTarget, rec FileRecord, body io.Reader, size int64) (fileWritten, error) {
	rec.Storage = target.storage
	rec.Filename = truncateUTF8(rec.Filename, maxFilenameBytes)
	var out fileWritten
	if f.registry != nil {
		switch err := f.registry.RegisterFile(ctx, rec); {
		case err == nil:
			out.registered = true
		case errors.Is(err, ErrFileRegistryUnsupported):
		default:
			return fileWritten{}, fmt.Errorf("httpapi: registering file %s: %w", rec.ID, err)
		}
	}
	hashed := newHashingReader(body)
	if err := target.backend.Put(ctx, rec.ObjectKey, hashed, size); err != nil {
		f.remove(ctx, target, rec.ID, rec.ObjectKey)
		return fileWritten{}, err
	}
	out.size, out.sha256Hex = hashed.count(), hashed.sum()
	if out.registered {
		if err := f.registry.CompleteFile(ctx, rec.ID, out.size, out.sha256Hex, rec.ContentType); err != nil {
			f.remove(ctx, target, rec.ID, rec.ObjectKey)
			return fileWritten{}, fmt.Errorf("httpapi: completing file %s: %w", rec.ID, err)
		}
	}
	return out, nil
}

// remove deletes a file this replica wrote and its record. The record stays
// when the object cannot be deleted, so the cleanup still knows of it.
//
// remove 删除本副本写入的一个文件及其记录。对象删不掉时保留记录，使清理仍然知道它。
func (f *fileStore) remove(ctx context.Context, target fileTarget, id, key string) {
	ctx = context.WithoutCancel(ctx)
	if err := target.backend.Delete(ctx, key); err != nil {
		f.logger.Warn("deleting a file failed; its record stays for the cleanup", slog.String("file_id", id), slog.Any("error", err))
		return
	}
	if f.registry == nil {
		return
	}
	if err := f.registry.DeleteFile(ctx, id); err != nil && !errors.Is(err, ErrFileRegistryUnsupported) {
		f.logger.Warn("deleting a file record failed; the cleanup removes it later", slog.String("file_id", id), slog.Any("error", err))
	}
}

// truncateUTF8 cuts s to at most max bytes without splitting a character.
// truncateUTF8 把 s 截到至多 max 字节，且不切开字符。
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// hashingReader hashes and counts what is read through it. When the body
// can seek it can too, so the S3 SDK can rewind a retried request; a rewind
// to the start restarts the hash.
//
// hashingReader 对经过它读取的内容做哈希与计数。body 可寻址时它也可寻址，S3 SDK 因此能为
// 重试的请求倒回；倒回到开头时重新开始计算哈希。
type hashingReader struct {
	r io.Reader
	h hash.Hash
	// n is the position in the body; skipped is set when it moved other
	// than by reading since the last rewind to the start.
	// n 是在 body 中的位置；自上次回到开头以来，位置若不是因读取而移动，skipped 为真。
	n       int64
	skipped bool
}

type hashingReadSeeker struct {
	*hashingReader
	s io.Seeker
}

type hashedBody interface {
	io.Reader
	count() int64
	sum() string
}

func newHashingReader(r io.Reader) hashedBody {
	h := &hashingReader{r: r, h: sha256.New()}
	if s, ok := r.(io.Seeker); ok {
		return &hashingReadSeeker{hashingReader: h, s: s}
	}
	return h
}

func (h *hashingReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	if !h.skipped {
		h.h.Write(p[:n])
	}
	h.n += int64(n)
	return n, err
}

// count is the body's size once it has been read to its end.
// count 是 body 读到末尾后的大小。
func (h *hashingReader) count() int64 { return h.n }

// sum is the SHA-256 of the body, "" when the last pass did not read it from
// its start without a skip: an unknown hash rather than a wrong one.
// sum 是 body 的 SHA-256；最后一遍不是从头连续读完时为空：宁可未知，也不给出错误的值。
func (h *hashingReader) sum() string {
	if h.skipped {
		return ""
	}
	return hex.EncodeToString(h.h.Sum(nil))
}

// Seek passes through. The S3 SDK seeks to the end to measure a body and back
// to the start to send or resend it; back at the start the hash restarts, and
// a pass that moved anywhere else stops counting toward it.
//
// Seek 直接透传。S3 SDK 会跳到末尾测量 body，再回到开头发送或重发；回到开头时重新计算哈希，
// 跳到其他位置的那一遍不再计入哈希。
func (h *hashingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	pos, err := h.s.Seek(offset, whence)
	if err != nil {
		return pos, err
	}
	switch {
	case pos == 0:
		h.h.Reset()
		h.skipped = false
	case pos != h.n:
		h.skipped = true
	}
	h.n = pos
	return pos, nil
}
