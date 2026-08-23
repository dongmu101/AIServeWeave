// Package comfyui adapts an already-running ComfyUI server to the
// runtime.WorkflowRuntime contract: submitting API Format workflows,
// following their progress over ComfyUI's instance-wide WebSocket,
// reconciling final state against History, cancelling within safe limits,
// and streaming output artifacts.
package comfyui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"AIServeWeave/common/runtime"
)

const (
	// defaultMaxResponseBytes bounds a single JSON response body. It is
	// larger than the LLM adapters' limit because /history and /queue grow
	// with the workflow graph, which is user-supplied.
	defaultMaxResponseBytes = 16 << 20 // 16 MiB

	// defaultMaxArtifactBytes bounds one artifact download. Artifacts are
	// streamed to the caller rather than buffered, so this only guards
	// against a server that never stops sending.
	defaultMaxArtifactBytes = 512 << 20 // 512 MiB

	// maxErrorMessageLen bounds how much of an upstream error body reaches
	// a RuntimeError's Message.
	maxErrorMessageLen = 2048
)

// Endpoint paths. They are collected here so the set of routes this adapter
// is allowed to touch can be read at a glance — ComfyUI also serves routes
// that upload files or change server state, and none of them appear below.
const (
	pathSystemStats = "/system_stats"
	pathFeatures    = "/features"
	pathObjectInfo  = "/object_info"
	pathModels      = "/models"
	pathPrompt      = "/prompt"
	pathQueue       = "/queue"
	pathHistory     = "/history"
	pathInterrupt   = "/interrupt"
	pathView        = "/view"
	pathWebSocket   = "/ws"
)

// hopByHopHeaders are headers a caller must not override via Config.Headers.
var hopByHopHeaders = map[string]bool{
	"Host":                true,
	"Content-Length":      true,
	"Connection":          true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
}

// ClientConfig configures a Client. BaseURL must be an http(s) URL without
// userinfo, query or fragment; any path is treated as a prefix that is
// preserved on every request, including the WebSocket URL.
type ClientConfig struct {
	BaseURL          string
	APIKey           string
	Headers          map[string]string
	HTTPClient       *http.Client
	MaxResponseBytes int64
	MaxArtifactBytes int64
	RuntimeID        string
}

// Client is ComfyUI's HTTP protocol client. It is safe for concurrent use.
//
// It is deliberately separate from the openai package's client: ComfyUI
// needs query parameters, streamed artifact bodies and a derived WebSocket
// URL, none of which belong in a transport shared by the LLM adapters. The
// error classification itself is not duplicated — it comes from the shared
// helpers in the runtime package.
type Client struct {
	baseURL      *url.URL
	httpClient   *http.Client
	apiKey       string
	headers      map[string]string
	maxRespBytes int64
	maxArtBytes  int64
	runtimeID    string
}

// NewClient validates cfg and builds a Client. It performs no network I/O.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("comfyui: base URL is required")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("comfyui: invalid base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("comfyui: base URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.User != nil {
		return nil, fmt.Errorf("comfyui: base URL must not contain userinfo")
	}
	if u.RawQuery != "" {
		return nil, fmt.Errorf("comfyui: base URL must not contain a query string")
	}
	if u.Fragment != "" {
		return nil, fmt.Errorf("comfyui: base URL must not contain a fragment")
	}
	for name := range cfg.Headers {
		if hopByHopHeaders[http.CanonicalHeaderKey(name)] {
			return nil, fmt.Errorf("comfyui: header %q must not be set via config", name)
		}
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	maxResp := cfg.MaxResponseBytes
	if maxResp <= 0 {
		maxResp = defaultMaxResponseBytes
	}
	maxArt := cfg.MaxArtifactBytes
	if maxArt <= 0 {
		maxArt = defaultMaxArtifactBytes
	}

	return &Client{
		baseURL:      u,
		httpClient:   httpClient,
		apiKey:       cfg.APIKey,
		headers:      cfg.Headers,
		maxRespBytes: maxResp,
		maxArtBytes:  maxArt,
		runtimeID:    cfg.RuntimeID,
	}, nil
}

// resolve joins path and query onto the base URL, preserving any configured
// path prefix instead of overwriting it.
func (c *Client) resolve(path string, query url.Values) *url.URL {
	resolved := *c.baseURL
	resolved.Path = strings.TrimSuffix(c.baseURL.Path, "/") + path
	resolved.RawQuery = query.Encode()
	return &resolved
}

// WebSocketURL returns the event-stream URL for clientID, with the http(s)
// scheme swapped for ws(s) and the configured path prefix preserved.
func (c *Client) WebSocketURL(clientID string) string {
	u := c.resolve(pathWebSocket, url.Values{"clientId": []string{clientID}})
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	return u.String()
}

// Header returns the headers a WebSocket dial must carry so the connection
// is authenticated exactly like the HTTP requests are.
func (c *Client) Header() http.Header {
	header := make(http.Header)
	if c.apiKey != "" {
		header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range c.headers {
		header.Set(k, v)
	}
	return header
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body []byte) (*http.Request, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.resolve(path, query).String(), bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// Do sends a request and, when out is non-nil, decodes a 2xx JSON body into
// it. Non-2xx responses and transport failures are returned as
// *runtime.RuntimeError with a Message already redacted of the API key.
func (c *Client) Do(ctx context.Context, operation, method, path string, query url.Values, reqBody, out any) error {
	resp, err := c.doRaw(ctx, operation, method, path, query, reqBody)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBytes, truncated, err := readLimited(resp.Body, c.maxRespBytes)
	if err != nil {
		return c.transportError(ctx, operation, err)
	}
	if truncated {
		return c.tooLargeError(operation, resp.StatusCode)
	}
	if out != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return &runtime.RuntimeError{
				Code:       runtime.ErrorProtocol,
				RuntimeID:  c.runtimeID,
				Kind:       runtime.KindComfyUI,
				Operation:  operation,
				StatusCode: resp.StatusCode,
				Message:    "decode response: invalid JSON",
				Cause:      err,
			}
		}
	}
	return nil
}

// OpenStream sends a GET and hands back the 2xx response with its body
// still open, for endpoints whose payload must be streamed rather than
// buffered: /object_info, which can be tens of megabytes, and /view, which
// returns the artifact itself. The caller must close the body.
func (c *Client) OpenStream(ctx context.Context, operation, path string, query url.Values) (*http.Response, error) {
	return c.doRaw(ctx, operation, http.MethodGet, path, query, nil)
}

// doRaw performs one request and converts any non-2xx into a
// *runtime.RuntimeError, closing that response body before returning, so
// callers never receive a non-2xx *http.Response.
func (c *Client) doRaw(ctx context.Context, operation, method, path string, query url.Values, reqBody any) (*http.Response, error) {
	var bodyBytes []byte
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return nil, c.localError(operation, runtime.ErrorInvalidConfig, fmt.Sprintf("encode request: %v", err))
		}
		bodyBytes = b
	}

	req, err := c.newRequest(ctx, method, path, query, bodyBytes)
	if err != nil {
		return nil, c.localError(operation, runtime.ErrorInvalidConfig, fmt.Sprintf("build request: %v", err))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, operation, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		respBytes, truncated, readErr := readLimited(resp.Body, c.maxRespBytes)
		if readErr != nil {
			return nil, c.transportError(ctx, operation, readErr)
		}
		if truncated {
			return nil, c.tooLargeError(operation, resp.StatusCode)
		}
		return nil, c.errorFromResponse(operation, resp.StatusCode, respBytes)
	}
	return resp, nil
}

func (c *Client) localError(operation string, code runtime.ErrorCode, message string) *runtime.RuntimeError {
	return &runtime.RuntimeError{
		Code:      code,
		RuntimeID: c.runtimeID,
		Kind:      runtime.KindComfyUI,
		Operation: operation,
		Message:   message,
	}
}

func (c *Client) tooLargeError(operation string, statusCode int) *runtime.RuntimeError {
	return &runtime.RuntimeError{
		Code:       runtime.ErrorResponseTooLarge,
		RuntimeID:  c.runtimeID,
		Kind:       runtime.KindComfyUI,
		Operation:  operation,
		StatusCode: statusCode,
		Message:    "response exceeded maximum allowed size",
	}
}

func (c *Client) transportError(ctx context.Context, operation string, err error) *runtime.RuntimeError {
	code := runtime.ClassifyTransportError(err)
	if ctx.Err() != nil {
		code = runtime.ErrorTimeout
	}
	if code == "" {
		code = runtime.ErrorConnection
	}
	return &runtime.RuntimeError{
		Code:      code,
		RuntimeID: c.runtimeID,
		Kind:      runtime.KindComfyUI,
		Operation: operation,
		Message:   runtime.Redact(err.Error(), c.apiKey),
		Cause:     err,
		Retryable: code == runtime.ErrorConnection || code == runtime.ErrorTimeout,
	}
}

func (c *Client) errorFromResponse(operation string, status int, body []byte) *runtime.RuntimeError {
	code := runtime.ErrorCodeFromStatus(status)
	return &runtime.RuntimeError{
		Code:       code,
		RuntimeID:  c.runtimeID,
		Kind:       runtime.KindComfyUI,
		Operation:  operation,
		StatusCode: status,
		Message:    runtime.Redact(parseErrorMessage(body), c.apiKey),
		Retryable:  code == runtime.ErrorRateLimited || code == runtime.ErrorUpstream,
	}
}

// comfyErrorEnvelope is ComfyUI's validation-failure body. node_errors is
// kept out of the message on purpose: it is keyed by node id and can be as
// large as the workflow itself.
type comfyErrorEnvelope struct {
	Error json.RawMessage `json:"error"`
}

type comfyErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Details string `json:"details"`
}

// parseErrorMessage extracts a human-readable message from a ComfyUI error
// body, which carries "error" as either an object or a bare string
// depending on which layer rejected the request. Anything else falls back to
// the raw body so diagnostics are not lost.
func parseErrorMessage(body []byte) string {
	if len(body) == 0 {
		return "empty error response"
	}
	var env comfyErrorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && len(env.Error) > 0 {
		var detail comfyErrorDetail
		if err := json.Unmarshal(env.Error, &detail); err == nil && detail.Message != "" {
			if detail.Details != "" {
				return truncate(detail.Message+": "+detail.Details, maxErrorMessageLen)
			}
			return truncate(detail.Message, maxErrorMessageLen)
		}
		var plain string
		if err := json.Unmarshal(env.Error, &plain); err == nil && plain != "" {
			return truncate(plain, maxErrorMessageLen)
		}
	}
	return truncate(string(body), maxErrorMessageLen)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

// readLimited reads up to limit+1 bytes, reporting truncated=true rather
// than buffering an unbounded body.
func readLimited(r io.Reader, limit int64) (data []byte, truncated bool, err error) {
	data, err = io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

// --- wire types and endpoint calls ------------------------------------

type systemStatsResponse struct {
	System struct {
		ComfyUIVersion string `json:"comfyui_version"`
		PythonVersion  string `json:"python_version"`
		OS             string `json:"os"`
	} `json:"system"`
	Devices []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"devices"`
}

// SystemStats reads GET /system_stats, the endpoint that both identifies a
// ComfyUI server and reports its liveness without touching the queue.
func (c *Client) SystemStats(ctx context.Context, operation string) (systemStatsResponse, error) {
	var resp systemStatsResponse
	if err := c.Do(ctx, operation, http.MethodGet, pathSystemStats, nil, nil, &resp); err != nil {
		return systemStatsResponse{}, err
	}
	return resp, nil
}

// Features reads GET /features. The payload is a free-form feature map that
// varies by build, so it is kept as raw values and only its key set is used.
func (c *Client) Features(ctx context.Context) (map[string]json.RawMessage, error) {
	var resp map[string]json.RawMessage
	if err := c.Do(ctx, "discover", http.MethodGet, pathFeatures, nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ObjectInfoKeys reads GET /object_info and returns only the node type
// names. The response embeds every node's full input schema and routinely
// runs to tens of megabytes, so it is decoded as a token stream and the
// schemas are skipped rather than buffered.
func (c *Client) ObjectInfoKeys(ctx context.Context, limit int) (keys []string, truncated bool, err error) {
	resp, err := c.OpenStream(ctx, "discover", pathObjectInfo, nil)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	keys, truncated, err = decodeTopLevelKeys(resp.Body, limit)
	if err != nil {
		return nil, false, &runtime.RuntimeError{
			Code:      runtime.ErrorProtocol,
			RuntimeID: c.runtimeID,
			Kind:      runtime.KindComfyUI,
			Operation: "discover",
			Message:   fmt.Sprintf("decode %s: %v", pathObjectInfo, err),
			Cause:     err,
		}
	}
	return keys, truncated, nil
}

// ModelFolders reads GET /models, the list of model directories the server
// exposes.
func (c *Client) ModelFolders(ctx context.Context) ([]string, error) {
	var folders []string
	if err := c.Do(ctx, "discover", http.MethodGet, pathModels, nil, nil, &folders); err != nil {
		return nil, err
	}
	return folders, nil
}

// ModelFiles reads GET /models/{folder}, the files inside one model folder.
func (c *Client) ModelFiles(ctx context.Context, folder string) ([]string, error) {
	var files []string
	path := pathModels + "/" + url.PathEscape(folder)
	if err := c.Do(ctx, "discover", http.MethodGet, path, nil, nil, &files); err != nil {
		return nil, err
	}
	return files, nil
}

type promptRequest struct {
	Prompt    json.RawMessage `json:"prompt"`
	ClientID  string          `json:"client_id"`
	ExtraData map[string]any  `json:"extra_data,omitempty"`
}

type promptResponse struct {
	PromptID string `json:"prompt_id"`
	Number   int    `json:"number"`
}

// SubmitPrompt posts an API Format workflow to POST /prompt.
func (c *Client) SubmitPrompt(ctx context.Context, req promptRequest) (promptResponse, error) {
	var resp promptResponse
	if err := c.Do(ctx, "submit", http.MethodPost, pathPrompt, nil, req, &resp); err != nil {
		return promptResponse{}, err
	}
	if resp.PromptID == "" {
		return promptResponse{}, &runtime.RuntimeError{
			Code:      runtime.ErrorProtocol,
			RuntimeID: c.runtimeID,
			Kind:      runtime.KindComfyUI,
			Operation: "submit",
			Message:   "server accepted the workflow but returned no prompt_id",
		}
	}
	return resp, nil
}

// queueResponse mirrors GET /queue. Each entry is a heterogeneous array
// whose second element is the prompt id; the rest (the workflow itself, its
// extra data, its outputs) is not decoded, because nothing here needs it and
// it is as large as the submitted graph.
type queueResponse struct {
	Running [][]json.RawMessage `json:"queue_running"`
	Pending [][]json.RawMessage `json:"queue_pending"`
}

// queueView is the decoded, adapter-facing form of GET /queue.
type queueView struct {
	// RunningIDs holds the prompt ids ComfyUI reports as executing. It is a
	// slice, not a single id, because the field is an array and a build
	// that runs more than one at a time must not be silently misread.
	RunningIDs []string
	PendingIDs []string
}

// Position returns the 1-based queue position of promptID among pending
// entries, or 0 when it is not pending.
func (q queueView) Position(promptID string) int {
	for i, id := range q.PendingIDs {
		if id == promptID {
			return i + 1
		}
	}
	return 0
}

func (q queueView) isRunning(promptID string) bool {
	for _, id := range q.RunningIDs {
		if id == promptID {
			return true
		}
	}
	return false
}

// Queue reads GET /queue and decodes just the prompt ids.
func (c *Client) Queue(ctx context.Context, operation string) (queueView, error) {
	var resp queueResponse
	if err := c.Do(ctx, operation, http.MethodGet, pathQueue, nil, nil, &resp); err != nil {
		return queueView{}, err
	}
	return queueView{
		RunningIDs: promptIDsFromQueue(resp.Running),
		PendingIDs: promptIDsFromQueue(resp.Pending),
	}, nil
}

// promptIDsFromQueue pulls element [1] out of each queue entry. Entries that
// are too short or not a string are skipped rather than failing the whole
// read: one malformed entry must not blind the adapter to the rest of the
// queue.
func promptIDsFromQueue(entries [][]json.RawMessage) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if len(entry) < 2 {
			continue
		}
		var id string
		if err := json.Unmarshal(entry[1], &id); err != nil || id == "" {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// historyEntry is one run's record in GET /history/{prompt_id}.
type historyEntry struct {
	Status struct {
		StatusStr string `json:"status_str"`
		Completed bool   `json:"completed"`
		// Messages is a list of [event_name, payload] pairs; only the event
		// name is read, to tell an interruption apart from a failure.
		Messages [][]json.RawMessage `json:"messages"`
	} `json:"status"`
	Outputs map[string]json.RawMessage `json:"outputs"`
}

// History reads GET /history/{prompt_id}. found is false when the server
// has no record of the run, which is not an error: a queued run has no
// history yet.
func (c *Client) History(ctx context.Context, operation, promptID string) (entry historyEntry, found bool, err error) {
	var resp map[string]historyEntry
	path := pathHistory + "/" + url.PathEscape(promptID)
	if err := c.Do(ctx, operation, http.MethodGet, path, nil, nil, &resp); err != nil {
		return historyEntry{}, false, err
	}
	entry, found = resp[promptID]
	return entry, found, nil
}

// DeleteFromQueue removes a still-pending run via POST /queue.
func (c *Client) DeleteFromQueue(ctx context.Context, promptID string) error {
	body := map[string]any{"delete": []string{promptID}}
	return c.Do(ctx, "cancel", http.MethodPost, pathQueue, nil, body, nil)
}

// Interrupt stops whatever ComfyUI is currently executing via POST
// /interrupt. It takes no prompt id — the server interrupts its current
// job — which is why the Runtime only calls it under the exclusive-instance
// rules.
func (c *Client) Interrupt(ctx context.Context) error {
	return c.Do(ctx, "cancel", http.MethodPost, pathInterrupt, nil, nil, nil)
}

// View opens GET /view for one artifact, leaving the body open for the
// caller to stream and close.
func (c *Client) View(ctx context.Context, ref runtime.ArtifactRef) (*http.Response, error) {
	query := url.Values{}
	query.Set("filename", ref.Filename)
	if ref.Subfolder != "" {
		query.Set("subfolder", ref.Subfolder)
	}
	if ref.Type != "" {
		query.Set("type", ref.Type)
	}
	return c.OpenStream(ctx, "open_artifact", pathView, query)
}

// MaxArtifactBytes reports the configured per-artifact size cap.
func (c *Client) MaxArtifactBytes() int64 { return c.maxArtBytes }

// decodeTopLevelKeys streams a JSON object and returns its top-level keys,
// skipping every value without materializing it. It stops after limit keys
// and reports truncated=true, so a server with an unexpectedly huge node
// catalogue cannot make discovery allocate without bound.
func decodeTopLevelKeys(r io.Reader, limit int) (keys []string, truncated bool, err error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return nil, false, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, false, fmt.Errorf("expected a JSON object, got %v", tok)
	}

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, false, fmt.Errorf("expected an object key, got %v", tok)
		}
		if err := skipValue(dec); err != nil {
			return nil, false, err
		}
		if limit > 0 && len(keys) >= limit {
			return keys, true, nil
		}
		keys = append(keys, key)
	}
	return keys, false, nil
}

// skipValue consumes exactly one JSON value from dec, descending through
// nested objects and arrays without decoding them into memory.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // scalar
	}
	depth := 1
	if delim == '}' || delim == ']' {
		return fmt.Errorf("unexpected %v", delim)
	}
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch d, ok := tok.(json.Delim); {
		case !ok:
		case d == '{' || d == '[':
			depth++
		default:
			depth--
		}
	}
	return nil
}
