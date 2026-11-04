// Package attachments converts caller-owned attachments into bounded model input.
// Package attachments 将调用方附件转换为有界的模型输入；程序和模型路径只取 Agent 本地配置。
package attachments

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid    = errors.New("attachment: invalid content or unsupported format")
	ErrLimit      = errors.New("attachment: size, page, duration or output limit exceeded")
	ErrDependency = errors.New("attachment: required local converter or speech model is unavailable")
	ErrConversion = errors.New("attachment: local conversion failed")
	ErrBusy       = errors.New("attachment: conversion capacity exhausted")
)

const MaxOutputBytes = 32 << 20

// Config is local-only. It must never be populated from an inference request.
// Config 仅限本地配置，不能来自推理请求或隧道下发。
type Config struct {
	LibreOfficePath  string `yaml:"libreoffice_path,omitempty"`
	PDFToPPMPath     string `yaml:"pdftoppm_path,omitempty"`
	PDFToTextPath    string `yaml:"pdftotext_path,omitempty"`
	PDFInfoPath      string `yaml:"pdfinfo_path,omitempty"`
	FFmpegPath       string `yaml:"ffmpeg_path,omitempty"`
	FFprobePath      string `yaml:"ffprobe_path,omitempty"`
	WhisperPath      string `yaml:"whisper_path,omitempty"`
	WhisperModelPath string `yaml:"whisper_model_path,omitempty"`
	MaxFileBytes     int64  `yaml:"max_file_bytes,omitempty"`
	MaxDocumentPages int    `yaml:"max_document_pages,omitempty"`
	MaxVideoSeconds  int    `yaml:"max_video_seconds,omitempty"`
	MaxVideoFrames   int    `yaml:"max_video_frames,omitempty"`
	MaxConcurrent    int    `yaml:"max_concurrent,omitempty"`
	Timeout          string `yaml:"timeout,omitempty"`
}

// Source is an inline base64 file or UTF-8 text source; URLs are excluded.
// Source 是内联 base64 文件或 UTF-8 文本，不接受 URL。
type Source struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

// Part is the attachment boundary shared with the Claude adapter.
// Part 是附件处理模块与 Claude 适配器之间的内容边界。
type Part struct {
	Type   string  `json:"type"`
	Text   string  `json:"text,omitempty"`
	Title  string  `json:"title,omitempty"`
	Source *Source `json:"source,omitempty"`
}

// Processor shares a bounded conversion budget across the Agent's CLI accounts.
// Processor 在 Agent 的 CLI 账户间共享有界转换名额。
type Processor struct {
	cfg     Config
	slots   chan struct{}
	timeout time.Duration
}

// New validates local configuration without starting or installing converters.
// New 校验本地配置，不启动或安装转换程序。
func New(cfg Config) (*Processor, error) {
	if cfg.MaxFileBytes == 0 {
		cfg.MaxFileBytes = 20 << 20
	}
	if cfg.MaxDocumentPages == 0 {
		cfg.MaxDocumentPages = 30
	}
	if cfg.MaxVideoSeconds == 0 {
		cfg.MaxVideoSeconds = 300
	}
	if cfg.MaxVideoFrames == 0 {
		cfg.MaxVideoFrames = 24
	}
	if cfg.MaxConcurrent == 0 {
		cfg.MaxConcurrent = 1
	}
	if cfg.Timeout == "" {
		cfg.Timeout = "180s"
	}
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil || timeout <= 0 || timeout > time.Hour || cfg.MaxFileBytes < 1 || cfg.MaxFileBytes > MaxOutputBytes || cfg.MaxDocumentPages < 1 || cfg.MaxDocumentPages > 100 || cfg.MaxVideoSeconds < 1 || cfg.MaxVideoSeconds > 3600 || cfg.MaxVideoFrames < 1 || cfg.MaxVideoFrames > 100 || cfg.MaxConcurrent < 1 || cfg.MaxConcurrent > 32 {
		return nil, ErrInvalid
	}
	for _, path := range []string{cfg.LibreOfficePath, cfg.PDFToPPMPath, cfg.PDFToTextPath, cfg.PDFInfoPath, cfg.FFmpegPath, cfg.FFprobePath, cfg.WhisperPath, cfg.WhisperModelPath} {
		if path != "" && !filepath.IsAbs(path) {
			return nil, ErrInvalid
		}
	}
	return &Processor{cfg: cfg, slots: make(chan struct{}, cfg.MaxConcurrent), timeout: timeout}, nil
}

func officeExtension(mime string) string {
	return map[string]string{
		"application/msword": ".doc", "application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
		"application/vnd.ms-excel": ".xls", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": ".xlsx",
		"application/vnd.ms-powerpoint": ".ppt", "application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
	}[mime]
}

func textMIME(mime string) bool {
	return strings.HasPrefix(mime, "text/") || mime == "application/json" || mime == "application/xml" || mime == "application/javascript" || mime == "application/x-ndjson"
}

// ValidateShape checks the wire contract without allocating decoded binary files.
// ValidateShape 校验附件协议形状，不分配完整二进制文件。
func ValidateShape(part Part) error {
	s := part.Source
	if s == nil || len(part.Title) > 1024 || !utf8.ValidString(part.Title) || strings.ContainsAny(part.Title, "\x00\r\n") {
		return ErrInvalid
	}
	if part.Type == "document" && s.Type == "text" {
		if s.MediaType != "" && s.MediaType != "text/plain" {
			return ErrInvalid
		}
		if !utf8.ValidString(s.Data) || s.Data == "" {
			return ErrInvalid
		}
		return nil
	}
	if s.Type != "base64" || s.Data == "" {
		return ErrInvalid
	}
	if part.Type == "document" {
		if s.MediaType != "application/pdf" && !textMIME(s.MediaType) && officeExtension(s.MediaType) == "" {
			return ErrInvalid
		}
	} else if part.Type == "video" {
		if s.MediaType != "video/mp4" && s.MediaType != "video/quicktime" && s.MediaType != "video/webm" {
			return ErrInvalid
		}
	} else {
		return ErrInvalid
	}
	count, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(s.Data)))
	if err != nil || count == 0 {
		return ErrInvalid
	}
	return nil
}

func (p *Processor) decode(part Part) ([]byte, error) {
	if err := ValidateShape(part); err != nil {
		return nil, err
	}
	if part.Source.Type == "text" {
		if int64(len(part.Source.Data)) > p.cfg.MaxFileBytes {
			return nil, ErrLimit
		}
		return []byte(part.Source.Data), nil
	}
	data, err := io.ReadAll(io.LimitReader(base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(part.Source.Data)), p.cfg.MaxFileBytes+1))
	if err != nil {
		return nil, ErrInvalid
	}
	if int64(len(data)) > p.cfg.MaxFileBytes {
		return nil, ErrLimit
	}
	return data, nil
}

// Convert handles one attachment. Tool results request PDF page images because
// MCP text/image results cannot carry a native Anthropic document block.
// Convert 转换单个附件；工具结果把 PDF 转成页图和文本，避免把 document 伪装成 MCP 文本。
func (p *Processor) Convert(ctx context.Context, part Part, toolResult bool) ([]Part, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := p.decode(part)
	if err != nil {
		return nil, err
	}
	if part.Type == "document" && (part.Source.Type == "text" || textMIME(part.Source.MediaType)) {
		if !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
			return nil, ErrInvalid
		}
		return []Part{{Type: "text", Text: "Document " + part.Title + ":\n" + string(data)}}, nil
	}
	if part.Type == "document" && part.Source.MediaType == "application/pdf" && !toolResult {
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return nil, ErrInvalid
		}
		return []Part{part}, nil
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return nil, ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	return p.convertLocal(ctx, part, data, toolResult)
}
