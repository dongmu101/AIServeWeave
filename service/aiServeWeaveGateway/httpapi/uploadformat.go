package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

// DefaultAllowedUploadExtensions is the file-extension allowlist
// (case-insensitive, dot-prefixed) submitRun enforces on a workflow's
// InputFile parts when Config.AllowedUploadExtensions is empty. It covers
// the image/video/audio formats a ComfyUI-style generation workflow
// typically consumes; a deployment that needs a different set overrides it
// via Config.AllowedUploadExtensions.
//
// DefaultAllowedUploadExtensions 是 submitRun 在 Config.AllowedUploadExtensions
// 为空时，对工作流 InputFile 分片强制执行的文件扩展名允许列表（大小写不敏感、
// 带前导点）。它覆盖的是 ComfyUI 一类生成式工作流通常会用到的图片/视频/音频
// 格式；需要不同集合的部署通过 Config.AllowedUploadExtensions 覆盖它。
var DefaultAllowedUploadExtensions = []string{
	".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp",
	".mp4", ".webm", ".mov",
	".mp3", ".wav", ".ogg", ".flac",
}

// uploadSniffCategoryByExtension maps an allowed extension to the sniffed
// content-type prefix its bytes must start with. It is deliberately not
// part of Config: it encodes what a format inherently looks like, not a
// deployment policy, so there is nothing for an operator to tune. An
// extension absent from this map (e.g. one a deployment added to
// Config.AllowedUploadExtensions for an opaque binary format) skips the
// byte-sniff check entirely and is accepted on extension alone.
//
// uploadSniffCategoryByExtension 把一个允许的扩展名映射到其字节必须以之开头
// 的嗅探内容类型前缀。它刻意不放进 Config：这编码的是一种格式本身长什么样，
// 不是部署策略，没有什么可供运维调整。不在这张表里的扩展名（比如某个部署为
// 一种不透明的二进制格式加进 Config.AllowedUploadExtensions 的）会完全跳过
// 字节嗅探检查，只凭扩展名放行。
var uploadSniffCategoryByExtension = map[string]string{
	".png": "image/", ".jpg": "image/", ".jpeg": "image/",
	".webp": "image/", ".gif": "image/", ".bmp": "image/",
	".mp4": "video/", ".webm": "video/", ".mov": "video/",
	".mp3": "audio/", ".wav": "audio/", ".ogg": "audio/", ".flac": "audio/",
}

// uploadSniffPeekBytes bounds how much of an uploaded file's content
// validateUploadContent reads before deciding — the same 512-byte window
// net/http.DetectContentType itself documents as sufficient, so this stays a
// bounded read regardless of the file's declared or actual size, honoring
// AGENTS.md's 安全红线 against unbounded buffering at any hop.
//
// uploadSniffPeekBytes 限定 validateUploadContent 在做出判断前读取一个已
// 上传文件多少字节——与 net/http.DetectContentType 自己文档中声明的 512 字节
// 窗口一致，因此无论文件声明或实际大小如何，这始终是一次有界读取，符合
// AGENTS.md「安全红线」对任何一跳都不得无界缓冲的要求。
const uploadSniffPeekBytes = 512

// errUnsupportedUploadFormat marks an error produced by
// validateUploadFilename or validateUploadContent, so submitRun can
// recognize it downstream of submitWithFiles (which otherwise reports
// through the generic dispatch-error path, mapping unrecognized errors to a
// 500) and answer with a 400 instead — this is a caller input problem, not a
// server fault.
//
// errUnsupportedUploadFormat 标记一个由 validateUploadFilename 或
// validateUploadContent 产生的错误，好让 submitRun 在 submitWithFiles 之下
// 也能认出它（否则会走通用的 dispatch 错误处理路径，把未识别的错误映射成
// 500）并改答 400——这是调用方的输入问题，不是服务端的错。
var errUnsupportedUploadFormat = errors.New("unsupported upload format")

// normalizeAllowedExtensions lowercases an extension allowlist into a set
// for O(1) lookup. A nil/empty in returns DefaultAllowedUploadExtensions
// normalized instead, so New leaves an unconfigured deployment with the
// documented default rather than no restriction at all.
//
// normalizeAllowedExtensions 把一份扩展名允许列表小写化，转成一个 O(1)
// 查找的集合。in 为 nil/空时改用 DefaultAllowedUploadExtensions 归一化后的
// 结果，这样未配置的部署得到的是文档写明的默认值，而不是完全不设限制。
func normalizeAllowedExtensions(in []string) map[string]struct{} {
	if len(in) == 0 {
		in = DefaultAllowedUploadExtensions
	}
	out := make(map[string]struct{}, len(in))
	for _, ext := range in {
		out[strings.ToLower(ext)] = struct{}{}
	}
	return out
}

// validateUploadFilename rejects a file part whose extension is not in
// allowed. It runs before any byte of the file is read, so an obviously
// disallowed upload (a caller trying to submit a .exe as an InputFile) is
// rejected as cheaply as possible.
//
// validateUploadFilename 拒绝一个扩展名不在 allowed 里的文件分片。它在读取
// 该文件任何一个字节之前运行，因此一次明显不被允许的上传（调用方试图把
// .exe 当作 InputFile 提交）会以尽可能低的代价被拒绝。
func validateUploadFilename(filename string, allowed map[string]struct{}) error {
	ext := strings.ToLower(filepath.Ext(filename))
	if _, ok := allowed[ext]; !ok {
		return fmt.Errorf("%w: file %q has an unsupported extension %q", errUnsupportedUploadFormat, filename, ext)
	}
	return nil
}

// validateUploadContent sniffs up to uploadSniffPeekBytes of f and checks
// the result against uploadSniffCategoryByExtension's entry for filename's
// extension, so a file whose actual bytes do not match its claimed
// extension (a renamed executable wearing a ".png" name) is caught even
// though validateUploadFilename already let the name through. On success it
// returns a reader that still yields every byte of f, sniffed prefix
// included — the peek consumes nothing the caller would otherwise have
// seen. An extension absent from the category map skips the check and
// returns f unchanged, since there is nothing to sniff it against.
//
// validateUploadContent 嗅探 f 最多 uploadSniffPeekBytes 字节，对照
// uploadSniffCategoryByExtension 里 filename 扩展名对应的表项做检查，这样
// 一个真实字节与其声称的扩展名不符的文件（一个改名成 ".png" 的可执行文件）
// 即使已经通过了 validateUploadFilename 的文件名检查，也会在这里被拦下。
// 成功时它返回的 reader 仍会产出 f 的每一个字节，包括被嗅探过的开头部分
// ——这次嗅探不会让调用方少看到任何字节。扩展名不在类别表里时跳过检查，
// 原样返回 f，因为没有可供比对的类别。
func validateUploadContent(f io.Reader, filename string) (io.Reader, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	wantPrefix, ok := uploadSniffCategoryByExtension[ext]
	if !ok {
		return f, nil
	}

	peek := make([]byte, uploadSniffPeekBytes)
	n, err := io.ReadFull(f, peek)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading upload for format validation: %w", err)
	}
	peek = peek[:n]

	if sniffed := http.DetectContentType(peek); !strings.HasPrefix(sniffed, wantPrefix) {
		return nil, fmt.Errorf("%w: file %q does not look like %s content (detected %s)",
			errUnsupportedUploadFormat, filename, strings.TrimSuffix(wantPrefix, "/"), sniffed)
	}
	return io.MultiReader(bytes.NewReader(peek), f), nil
}
