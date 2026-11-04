package attachments

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

// capture refuses runaway stdout; converter diagnostics never enter public errors.
// capture 限制转换进程输出，底层诊断不进入公开错误。
type capture struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *capture) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.buffer.Len() {
		b.overflow = true
		return 0, ErrLimit
	}
	return b.buffer.Write(data)
}

func (p *Processor) run(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	if program == "" {
		return nil, ErrDependency
	}
	if info, err := os.Stat(program); err != nil || !info.Mode().IsRegular() {
		return nil, ErrDependency
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("")
	stdout := &capture{limit: 1 << 20}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	// Keep runtime/library discovery and locale, never provider credentials.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "PATH", "SYSTEMROOT", "WINDIR", "LANG", "LC_ALL", "LD_LIBRARY_PATH", "DYLD_LIBRARY_PATH":
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "TMPDIR="+dir, "TMP="+dir, "TEMP="+dir)
	if err := cliprocess.Configure(cmd); err != nil {
		return nil, ErrDependency
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	defer func() { close(done); <-stopped }()
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if directoryLimit(dir) != nil {
					cancel(ErrLimit)
					return
				}
			}
		}
	}()
	err := cliprocess.Run(cmd)
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	if stdout.overflow || directoryLimit(dir) != nil {
		return nil, ErrLimit
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return nil, ErrDependency
		}
		return nil, ErrConversion
	}
	return stdout.buffer.Bytes(), nil
}

func directoryLimit(dir string) error {
	var size int64
	count := 0
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			info, e := entry.Info()
			if e != nil {
				return e
			}
			size += info.Size()
			count++
			if size > 128<<20 || count > 512 {
				return ErrLimit
			}
		}
		return nil
	})
	if err != nil {
		return ErrLimit
	}
	return nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, ErrLimit
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return nil, ErrConversion
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrConversion
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrConversion
	}
	if info.Size() > limit {
		return nil, ErrLimit
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, ErrConversion
	}
	if int64(len(data)) > limit {
		return nil, ErrLimit
	}
	return data, nil
}

func (p *Processor) convertLocal(ctx context.Context, part Part, data []byte, tool bool) ([]Part, error) {
	dir, err := os.MkdirTemp("", "aisw-attachment-")
	if err != nil {
		return nil, ErrConversion
	}
	defer os.RemoveAll(dir)
	ext := ".pdf"
	if part.Type == "video" {
		ext = ".mp4"
		if part.Source.MediaType == "video/webm" {
			ext = ".webm"
		}
	} else if value := officeExtension(part.Source.MediaType); value != "" {
		ext = value
	}
	input := filepath.Join(dir, "input"+ext)
	if os.WriteFile(input, data, 0600) != nil {
		return nil, ErrConversion
	}
	if part.Type == "video" {
		return p.video(ctx, dir, input, part)
	}
	if ext != ".pdf" {
		profile := filepath.Join(dir, "profile", "user")
		if os.MkdirAll(profile, 0700) != nil {
			return nil, ErrConversion
		}
		settings := `<?xml version="1.0" encoding="UTF-8"?><oor:items xmlns:oor="http://openoffice.org/2001/registry"><item oor:path="/org.openoffice.Office.Common/Security/Scripting"><prop oor:name="MacroSecurityLevel" oor:op="fuse"><value>3</value></prop></item><item oor:path="/org.openoffice.Office.Common/Load"><prop oor:name="UpdateDocMode" oor:op="fuse"><value>0</value></prop></item></oor:items>`
		if os.WriteFile(filepath.Join(profile, "registrymodifications.xcu"), []byte(settings), 0600) != nil {
			return nil, ErrConversion
		}
		profilePath := filepath.ToSlash(filepath.Dir(profile))
		if !strings.HasPrefix(profilePath, "/") {
			profilePath = "/" + profilePath
		}
		profileURL := (&url.URL{Scheme: "file", Path: profilePath}).String()
		if _, err := p.run(ctx, dir, p.cfg.LibreOfficePath, "-env:UserInstallation="+profileURL, "--headless", "--nologo", "--nodefault", "--nolockcheck", "--norestore", "--convert-to", "pdf", "--outdir", dir, input); err != nil {
			return nil, err
		}
		input = filepath.Join(dir, "input.pdf")
		data, err = readBounded(input, p.cfg.MaxFileBytes)
		if err != nil {
			return nil, err
		}
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		return nil, ErrInvalid
	}
	if !tool {
		info, err := p.run(ctx, dir, p.cfg.PDFInfoPath, "-isodates", input)
		if err != nil {
			return nil, err
		}
		pages := 0
		for _, line := range strings.Split(string(info), "\n") {
			if strings.HasPrefix(line, "Pages:") {
				pages, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
			}
		}
		if pages < 1 {
			return nil, ErrConversion
		}
		if pages > p.cfg.MaxDocumentPages {
			return nil, ErrLimit
		}
		return []Part{{Type: "document", Title: part.Title, Source: &Source{Type: "base64", MediaType: "application/pdf", Data: base64.StdEncoding.EncodeToString(data)}}}, nil
	}
	return p.pdfPages(ctx, dir, input, part.Title)
}

func (p *Processor) pdfPages(ctx context.Context, dir, input, title string) ([]Part, error) {
	prefix := filepath.Join(dir, "page")
	if _, err := p.run(ctx, dir, p.cfg.PDFToPPMPath, "-f", "1", "-l", strconv.Itoa(p.cfg.MaxDocumentPages+1), "-scale-to", "1280", "-jpeg", input, prefix); err != nil {
		return nil, err
	}
	files, err := filepath.Glob(prefix + "-*.jpg")
	if err != nil || len(files) == 0 {
		return nil, ErrConversion
	}
	if len(files) > p.cfg.MaxDocumentPages {
		return nil, ErrLimit
	}
	sort.Slice(files, func(i, j int) bool { return pageIndex(files[i]) < pageIndex(files[j]) })
	textPath := filepath.Join(dir, "text.txt")
	if _, err := p.run(ctx, dir, p.cfg.PDFToTextPath, "-enc", "UTF-8", "-layout", input, textPath); err != nil {
		return nil, err
	}
	text, err := readBounded(textPath, 4<<20)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(text) {
		return nil, ErrConversion
	}
	parts := []Part{{Type: "text", Text: "Document " + title + " (page images and extracted text):\n" + string(text)}}
	remaining := int64(MaxOutputBytes - len(text))
	for i, file := range files {
		image, err := readBounded(file, remaining*3/4)
		if err != nil {
			return nil, err
		}
		remaining -= int64(base64.StdEncoding.EncodedLen(len(image)))
		parts = append(parts, Part{Type: "text", Text: fmt.Sprintf("Page %d:", i+1)}, Part{Type: "image", Source: &Source{Type: "base64", MediaType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(image)}})
	}
	return parts, nil
}

func pageIndex(path string) int {
	name := strings.TrimSuffix(filepath.Base(path), ".jpg")
	index := strings.LastIndex(name, "-")
	if index < 0 {
		return 0
	}
	n, _ := strconv.Atoi(name[index+1:])
	return n
}

func (p *Processor) video(ctx context.Context, dir, input string, part Part) ([]Part, error) {
	demuxer := "mov"
	if part.Source.MediaType == "video/webm" {
		demuxer = "matroska"
	}
	info, err := p.run(ctx, dir, p.cfg.FFprobePath, "-v", "error", "-protocol_whitelist", "file,pipe", "-f", demuxer, "-show_entries", "format=duration:stream=codec_type", "-of", "json", input)
	if err != nil {
		return nil, err
	}
	var probe struct {
		Format  struct{ Duration string }
		Streams []struct {
			CodecType string `json:"codec_type"`
		}
	}
	if json.Unmarshal(info, &probe) != nil {
		return nil, ErrConversion
	}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		return nil, ErrInvalid
	}
	if duration > float64(p.cfg.MaxVideoSeconds) {
		return nil, ErrLimit
	}
	audio, video := false, false
	for _, s := range probe.Streams {
		audio = audio || s.CodecType == "audio"
		video = video || s.CodecType == "video"
	}
	if !video {
		return nil, ErrInvalid
	}
	if audio {
		if p.cfg.WhisperPath == "" || p.cfg.WhisperModelPath == "" {
			return nil, ErrDependency
		}
		if info, err := os.Stat(p.cfg.WhisperModelPath); err != nil || !info.Mode().IsRegular() {
			return nil, ErrDependency
		}
	}
	count := min(p.cfg.MaxVideoFrames, max(1, int(math.Ceil(duration/5))))
	parts := []Part{{Type: "text", Text: fmt.Sprintf("Video %s: %.3f seconds; %d uniformly sampled frames across its duration (not every frame).", part.Title, duration, count)}}
	remaining := int64(MaxOutputBytes - 1024)
	for i := 0; i < count; i++ {
		position := (float64(i) + 0.5) * duration / float64(count)
		path := filepath.Join(dir, fmt.Sprintf("frame-%03d.jpg", i))
		if _, err := p.run(ctx, dir, p.cfg.FFmpegPath, "-nostdin", "-v", "error", "-protocol_whitelist", "file,pipe", "-f", demuxer, "-ss", fmt.Sprintf("%.6f", position), "-i", input, "-map", "0:v:0", "-an", "-frames:v", "1", "-vf", "scale=1280:1280:force_original_aspect_ratio=decrease", "-q:v", "3", "-y", path); err != nil {
			return nil, err
		}
		image, err := readBounded(path, remaining*3/4)
		if err != nil {
			return nil, err
		}
		remaining -= int64(base64.StdEncoding.EncodedLen(len(image)))
		parts = append(parts, Part{Type: "text", Text: fmt.Sprintf("Frame at approximately %.3f seconds:", position)}, Part{Type: "image", Source: &Source{Type: "base64", MediaType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(image)}})
	}
	transcript := "No audio track."
	if audio {
		wav := filepath.Join(dir, "audio.wav")
		if _, err := p.run(ctx, dir, p.cfg.FFmpegPath, "-nostdin", "-v", "error", "-protocol_whitelist", "file,pipe", "-f", demuxer, "-i", input, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-y", wav); err != nil {
			return nil, err
		}
		prefix := filepath.Join(dir, "transcript")
		if _, err := p.run(ctx, dir, p.cfg.WhisperPath, "-m", p.cfg.WhisperModelPath, "-f", wav, "-l", "auto", "-osrt", "-of", prefix, "-np"); err != nil {
			return nil, err
		}
		data, err := readBounded(prefix+".srt", min(remaining, 4<<20))
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(data) {
			return nil, ErrConversion
		}
		transcript = string(data)
		if strings.TrimSpace(transcript) == "" {
			transcript = "Audio track present; no speech recognized."
		}
	}
	return append(parts, Part{Type: "text", Text: "Audio transcript (timestamps in SRT):\n" + transcript}), nil
}
