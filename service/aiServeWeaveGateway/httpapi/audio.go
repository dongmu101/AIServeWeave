package httpapi

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// MaxAudioUploadBytes bounds a multipart transcription/translation request —
// the whole request, audio included. STATUS.md's P2 scope is short-form
// audio, not hours-long recordings, so this is far smaller than
// MaxWorkflowUploadBytes.
//
// MaxAudioUploadBytes 限定一次 multipart 转录/翻译请求——整个请求，音频在内。
// STATUS.md 的 P2 范围是短音频，不是数小时的录音，因此比 MaxWorkflowUploadBytes
// 小得多。
const MaxAudioUploadBytes = 25 << 20

// maxAudioUploadMemory is the in-memory threshold ParseMultipartForm uses
// before spilling the file part to a temp file — see jobs.go's
// maxWorkflowUploadMemory for why this bounds memory rather than the
// upload's own size, which MaxAudioUploadBytes already does via
// http.MaxBytesReader.
const maxAudioUploadMemory = 8 << 20

// audioUploadExtensions is this endpoint's fixed, non-configurable extension
// allowlist. Unlike a workflow's InputFile parts (uploadformat.go's
// Config.AllowedUploadExtensions, a deployment policy over several media
// kinds), a transcription endpoint has exactly one kind of input, so there is
// nothing for an operator to configure.
//
// audioUploadExtensions 是本端点固定、不可配置的扩展名允许列表。与工作流的
// InputFile 分片不同（uploadformat.go 的 Config.AllowedUploadExtensions 是
// 跨多种媒体类型的部署策略），转录端点只有一种输入类型，没有什么可供运维配置。
var audioUploadExtensions = map[string]struct{}{
	".mp3":  {},
	".wav":  {},
	".ogg":  {},
	".flac": {},
}

// audioTranscriptions implements POST /v1/audio/transcriptions.
func (h *handlers) audioTranscriptions(w http.ResponseWriter, r *http.Request) {
	h.transcribeAudio(w, r, runtime.AudioTaskTranscribe)
}

// audioTranslations implements POST /v1/audio/translations.
func (h *handlers) audioTranslations(w http.ResponseWriter, r *http.Request) {
	h.transcribeAudio(w, r, runtime.AudioTaskTranslate)
}

// transcribeAudio backs both endpoints. It parses a multipart/form-data
// request (OpenAI's audio endpoints take no other content type), picks a
// single candidate up front, and streams the file part straight into
// Scheduler.Transcribe — never buffering the whole audio body, the same
// discipline submitWithFiles already applies to a workflow InputFile.
func (h *handlers) transcribeAudio(w http.ResponseWriter, r *http.Request, task runtime.AudioTask) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"the request body must be multipart/form-data")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxAudioUploadBytes)
	if err := r.ParseMultipartForm(maxAudioUploadMemory); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"the request body is not a valid multipart upload, or is over the size limit")
		return
	}
	defer r.MultipartForm.RemoveAll()

	modelValues := r.MultipartForm.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "model is required")
		return
	}
	model := modelValues[0]

	responseFormat := "json"
	if v := r.MultipartForm.Value["response_format"]; len(v) > 0 && v[0] != "" {
		responseFormat = v[0]
	}
	if responseFormat != "json" && responseFormat != "text" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			`response_format must be "json" or "text"; srt, vtt and verbose_json are not supported`)
		return
	}

	var language *string
	if v := r.MultipartForm.Value["language"]; len(v) > 0 && v[0] != "" {
		if task == runtime.AudioTaskTranslate {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
				"language is not accepted for translation; its output language is always English")
			return
		}
		language = &v[0]
	}

	var prompt *string
	if v := r.MultipartForm.Value["prompt"]; len(v) > 0 && v[0] != "" {
		prompt = &v[0]
	}

	var temperature *float64
	if v := r.MultipartForm.Value["temperature"]; len(v) > 0 && v[0] != "" {
		t, err := strconv.ParseFloat(v[0], 64)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "temperature must be a number")
			return
		}
		temperature = &t
	}

	parts := r.MultipartForm.File["file"]
	if len(parts) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", `a file part named "file" is required`)
		return
	}
	fh := parts[0]
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if _, ok := audioUploadExtensions[ext]; !ok {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			fmt.Sprintf("unsupported file extension %q; supported: .mp3, .wav, .ogg, .flac", ext))
		return
	}

	f, err := fh.Open()
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "could not open the uploaded file")
		return
	}
	defer f.Close()

	sniffed, err := validateUploadContent(f, fh.Filename)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		return
	}

	candidates := h.sched.TranscriptionCandidates(model)
	if len(candidates) == 0 {
		handleDispatchError(w, h.logger, scheduler.ErrNoCapableNode)
		return
	}

	resp, err := h.sched.Transcribe(r.Context(), candidates[0], runtime.AudioTranscriptionRequest{
		Model:          model,
		Filename:       fh.Filename,
		Task:           task,
		Language:       language,
		Prompt:         prompt,
		Temperature:    temperature,
		ResponseFormat: responseFormat,
	}, sniffed)
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}

	if responseFormat == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(resp.Text))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Text string `json:"text"`
	}{Text: resp.Text})
}
