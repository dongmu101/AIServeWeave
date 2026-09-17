package openai

import (
	"context"
	"encoding/json"
	"io"
	"strconv"

	"AIServeWeave/common/runtime"
)

// transcriptionResponseDTO is the OpenAI-compatible JSON body a backend
// answers with for response_format "json". Language and Duration are
// OpenAI's "verbose_json" fields; this package does not request that format,
// but a backend may include them anyway, so they are read when present
// rather than assumed absent.
type transcriptionResponseDTO struct {
	Text     string   `json:"text"`
	Language string   `json:"language,omitempty"`
	Duration *float64 `json:"duration,omitempty"`
}

// Transcribe calls POST /v1/audio/transcriptions or /v1/audio/translations
// (req.Task selects which) with audio streamed as a multipart file part, and
// returns the backend's answer. audio is read to EOF and never buffered
// whole — see Client.doMultipart.
func Transcribe(ctx context.Context, c *Client, req runtime.AudioTranscriptionRequest, audio io.Reader) (runtime.AudioTranscriptionResponse, error) {
	path := "/v1/audio/transcriptions"
	if req.Task == runtime.AudioTaskTranslate {
		path = "/v1/audio/translations"
	}

	responseFormat := req.ResponseFormat
	if responseFormat == "" {
		responseFormat = "json"
	}

	fields := map[string]string{
		"model":           req.Model,
		"response_format": responseFormat,
	}
	// language only means anything for transcription: translation's output
	// language is always English, so a caller-supplied hint here would be
	// misleading rather than merely ignored.
	if req.Language != nil && req.Task == runtime.AudioTaskTranscribe {
		fields["language"] = *req.Language
	}
	if req.Prompt != nil {
		fields["prompt"] = *req.Prompt
	}
	if req.Temperature != nil {
		fields["temperature"] = strconv.FormatFloat(*req.Temperature, 'f', -1, 64)
	}

	filename := req.Filename
	if filename == "" {
		filename = "audio"
	}

	resp, err := c.doMultipart(ctx, "transcribe", path, fields, "file", filename, audio)
	if err != nil {
		return runtime.AudioTranscriptionResponse{}, err
	}
	defer resp.Body.Close()

	respBytes, truncated, err := readLimited(resp.Body, c.maxRespBytes)
	if err != nil {
		return runtime.AudioTranscriptionResponse{}, c.transportError(ctx, "transcribe", err)
	}
	if truncated {
		return runtime.AudioTranscriptionResponse{}, c.tooLargeError("transcribe", resp.StatusCode)
	}

	if responseFormat == "text" {
		return runtime.AudioTranscriptionResponse{Text: string(respBytes)}, nil
	}

	var dto transcriptionResponseDTO
	if err := json.Unmarshal(respBytes, &dto); err != nil {
		return runtime.AudioTranscriptionResponse{}, &runtime.RuntimeError{
			Code:       runtime.ErrorProtocol,
			RuntimeID:  c.runtimeID,
			Kind:       c.kind,
			Operation:  "transcribe",
			StatusCode: resp.StatusCode,
			Message:    "decode response: invalid JSON",
			Cause:      err,
		}
	}
	return runtime.AudioTranscriptionResponse{
		Text:     dto.Text,
		Language: dto.Language,
		Duration: dto.Duration,
	}, nil
}
