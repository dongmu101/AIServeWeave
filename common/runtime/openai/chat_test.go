package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"AIServeWeave/common/runtime"
)

func TestChatSendsRequestAndDecodesResponse(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(`{
			"id": "chatcmpl-1",
			"model": "llama-3",
			"created": 1700000000,
			"choices": [{"message": {"role":"assistant","content":"hi there"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}
		}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.ChatRequest{
		Model: "llama-3",
		Messages: []runtime.ChatMessage{
			{Role: "user", Content: "hello"},
		},
	}
	resp, err := Chat(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}

	if gotBody["model"] != "llama-3" {
		t.Errorf("request model = %v, want llama-3", gotBody["model"])
	}
	if resp.ID != "chatcmpl-1" || resp.Model != "llama-3" {
		t.Errorf("unexpected response identity: %+v", resp)
	}
	if resp.Message.Role != "assistant" || resp.Message.Content != "hi there" {
		t.Errorf("unexpected response message: %+v", resp.Message)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", resp.FinishReason)
	}
	if resp.Usage.TotalTokens != 7 {
		t.Errorf("Usage.TotalTokens = %d, want 7", resp.Usage.TotalTokens)
	}
	if resp.CreatedAt.Unix() != 1700000000 {
		t.Errorf("CreatedAt = %v, want unix 1700000000", resp.CreatedAt)
	}
}

// TestChatSendsImageContentAsAnArray proves a message with ContentParts
// marshals its "content" as the OpenAI-compatible content-array shape
// (STATUS.md's P2 ChatMessage.Content structured rework), while a
// plain-text message on the same request still marshals as a bare string —
// the two shapes coexist on one request, matching what a real
// vision-capable OpenAI-compatible backend accepts.
func TestChatSendsImageContentAsAnArray(t *testing.T) {
	var gotBody struct {
		Messages []json.RawMessage `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(`{
			"id": "chatcmpl-1", "model": "llama-3", "created": 1700000000,
			"choices": [{"message": {"role":"assistant","content":"ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}
		}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.ChatRequest{
		Model: "llama-3",
		Messages: []runtime.ChatMessage{
			{Role: "system", Content: "be terse"},
			{Role: "user", ContentParts: []runtime.ContentPart{
				{Type: "text", Text: "what is this"},
				{Type: "image_url", ImageURL: &runtime.ContentImageURL{URL: "data:image/png;base64,Zm9v", Detail: "low"}},
			}},
		},
	}
	if _, err := Chat(context.Background(), c, req); err != nil {
		t.Fatal(err)
	}

	if len(gotBody.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(gotBody.Messages))
	}

	var systemMsg struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(gotBody.Messages[0], &systemMsg); err != nil {
		t.Fatalf("system message content did not decode as a plain string: %v (%s)", err, gotBody.Messages[0])
	}
	if systemMsg.Content != "be terse" {
		t.Errorf("system content = %q, want %q", systemMsg.Content, "be terse")
	}

	var userMsg struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL *struct {
				URL    string `json:"url"`
				Detail string `json:"detail"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(gotBody.Messages[1], &userMsg); err != nil {
		t.Fatalf("user message content did not decode as an array: %v (%s)", err, gotBody.Messages[1])
	}
	if len(userMsg.Content) != 2 {
		t.Fatalf("user content parts = %d, want 2", len(userMsg.Content))
	}
	if userMsg.Content[0].Type != "text" || userMsg.Content[0].Text != "what is this" {
		t.Errorf("content[0] = %+v, want text %q", userMsg.Content[0], "what is this")
	}
	img := userMsg.Content[1]
	if img.Type != "image_url" || img.ImageURL == nil || img.ImageURL.URL != "data:image/png;base64,Zm9v" || img.ImageURL.Detail != "low" {
		t.Errorf("content[1] = %+v, want image_url with url=data:image/png;base64,Zm9v detail=low", img)
	}
}

// TestChatSendsAudioAndFileContentAsAnArray proves that "input_audio" and
// "file" content parts (STATUS.md's P2 multimodal input) marshal into the
// same content-array shape TestChatSendsImageContentAsAnArray already
// exercises for image_url, each rendered as its own wire shape:
// input_audio mirrors OpenAI's own {data, format} object exactly, while
// file uses this repository's own {file_data, filename} convention (there
// is no OpenAI Chat Completions file block to mirror — see filePartDTO's
// doc comment).
func TestChatSendsAudioAndFileContentAsAnArray(t *testing.T) {
	var gotBody struct {
		Messages []json.RawMessage `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(`{
			"id": "chatcmpl-1", "model": "llama-3", "created": 1700000000,
			"choices": [{"message": {"role":"assistant","content":"ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}
		}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.ChatRequest{
		Model: "llama-3",
		Messages: []runtime.ChatMessage{
			{Role: "user", ContentParts: []runtime.ContentPart{
				{Type: "text", Text: "summarize these"},
				{Type: "input_audio", Audio: &runtime.ContentAudio{Data: "YmFzZTY0LWF1ZGlv", Format: "wav"}},
				{Type: "file", File: &runtime.ContentFile{URL: "data:application/pdf;base64,cGRm", Filename: "report.pdf"}},
			}},
		},
	}
	if _, err := Chat(context.Background(), c, req); err != nil {
		t.Fatal(err)
	}

	if len(gotBody.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(gotBody.Messages))
	}

	var userMsg struct {
		Content []struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			InputAudio *struct {
				Data   string `json:"data"`
				Format string `json:"format"`
			} `json:"input_audio"`
			File *struct {
				FileData string `json:"file_data"`
				Filename string `json:"filename"`
			} `json:"file"`
		} `json:"content"`
	}
	if err := json.Unmarshal(gotBody.Messages[0], &userMsg); err != nil {
		t.Fatalf("user message content did not decode as an array: %v (%s)", err, gotBody.Messages[0])
	}
	if len(userMsg.Content) != 3 {
		t.Fatalf("user content parts = %d, want 3", len(userMsg.Content))
	}
	audio := userMsg.Content[1]
	if audio.Type != "input_audio" || audio.InputAudio == nil || audio.InputAudio.Data != "YmFzZTY0LWF1ZGlv" || audio.InputAudio.Format != "wav" {
		t.Errorf("content[1] = %+v, want input_audio with data=YmFzZTY0LWF1ZGlv format=wav", audio)
	}
	file := userMsg.Content[2]
	if file.Type != "file" || file.File == nil || file.File.FileData != "data:application/pdf;base64,cGRm" || file.File.Filename != "report.pdf" {
		t.Errorf("content[2] = %+v, want file with file_data=data:application/pdf;base64,cGRm filename=report.pdf", file)
	}
}

func TestChatToolCallsRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		msgs := body["messages"].([]any)
		last := msgs[len(msgs)-1].(map[string]any)
		if last["tool_call_id"] != "call_1" {
			t.Errorf("tool_call_id not forwarded: %v", last)
		}
		w.Write([]byte(`{
			"id": "chatcmpl-2",
			"model": "llama-3",
			"created": 1700000000,
			"choices": [{"message": {"role":"assistant","content":"","tool_calls":[{"id":"call_2","type":"function","function":{"name":"lookup","arguments":"{\"x\":1}"}}]}, "finish_reason": "tool_calls"}]
		}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.ChatRequest{
		Model: "llama-3",
		Messages: []runtime.ChatMessage{
			{Role: "tool", ToolCallID: "call_1", Content: "42"},
		},
	}
	resp, err := Chat(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("unexpected tool calls: %+v", resp.Message.ToolCalls)
	}
}

func TestChatFullFieldSetRoundTrips(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"id":"c1","model":"m1","created":1700000000,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	temp := 0.0 // explicit zero must survive, not be dropped as "unset"
	topP := 0.9
	maxTokens := 128
	seed := int64(42)
	req := runtime.ChatRequest{
		Model:       "m1",
		Messages:    []runtime.ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: &temp,
		TopP:        &topP,
		MaxTokens:   &maxTokens,
		Stop:        []string{"\n\n"},
		Seed:        &seed,
		ToolChoice:  "auto",
		Tools: []runtime.Tool{{
			Type: "function",
			Function: runtime.FunctionDefinition{
				Name:       "lookup",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		}},
		ResponseFormat: &runtime.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: &runtime.JSONSchemaFormat{Name: "answer", Strict: true, Schema: json.RawMessage(`{"type":"object"}`)},
		},
	}
	if _, err := Chat(context.Background(), c, req); err != nil {
		t.Fatal(err)
	}

	if gotBody["temperature"] != 0.0 {
		t.Errorf("temperature = %v, want explicit 0 to survive", gotBody["temperature"])
	}
	if gotBody["top_p"] != 0.9 {
		t.Errorf("top_p = %v, want 0.9", gotBody["top_p"])
	}
	if gotBody["max_tokens"] != float64(128) {
		t.Errorf("max_tokens = %v, want 128", gotBody["max_tokens"])
	}
	if gotBody["seed"] != float64(42) {
		t.Errorf("seed = %v, want 42", gotBody["seed"])
	}
	if gotBody["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", gotBody["tool_choice"])
	}
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v, want one entry", gotBody["tools"])
	}
	rf, _ := gotBody["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v, want type json_schema", gotBody["response_format"])
	}

	if _, ok := gotBody["stream"]; ok {
		t.Errorf("non-streaming Chat must not send a stream field, got %v", gotBody["stream"])
	}
}

func TestChatOmitsUnsetOptionalFields(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"id":"c1","model":"m1","created":1700000000,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.ChatRequest{Model: "m1", Messages: []runtime.ChatMessage{{Role: "user", Content: "hi"}}}
	if _, err := Chat(context.Background(), c, req); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"temperature", "top_p", "max_tokens", "stop", "seed", "tools", "tool_choice", "response_format"} {
		if _, present := gotBody[field]; present {
			t.Errorf("unset field %q was sent as %v, want omitted", field, gotBody[field])
		}
	}
}

func TestChatExtraFieldCollisionIsRejected(t *testing.T) {
	c, err := NewClient(ClientConfig{BaseURL: "http://example.com", Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.ChatRequest{
		Model:    "m1",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "hi"}},
		Extra:    map[string]json.RawMessage{"temperature": json.RawMessage(`0.5`)},
	}
	_, err = Chat(context.Background(), c, req)
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != runtime.ErrorInvalidConfig {
		t.Fatalf("error = %v, want a RuntimeError with Code %s", err, runtime.ErrorInvalidConfig)
	}
}

func TestChatExtraFieldForwardsBackendPrivateParams(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"id":"c1","model":"m1","created":1700000000,"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	req := runtime.ChatRequest{
		Model:    "m1",
		Messages: []runtime.ChatMessage{{Role: "user", Content: "hi"}},
		Extra:    map[string]json.RawMessage{"top_k": json.RawMessage(`40`)},
	}
	if _, err := Chat(context.Background(), c, req); err != nil {
		t.Fatal(err)
	}
	if gotBody["top_k"] != float64(40) {
		t.Fatalf("top_k = %v, want 40 forwarded from Extra", gotBody["top_k"])
	}
}

func TestChatNoChoicesIsProtocolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"chatcmpl-3","model":"llama-3","created":1700000000,"choices":[]}`))
	}))
	defer srv.Close()

	c, err := NewClient(ClientConfig{BaseURL: srv.URL, Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = Chat(context.Background(), c, runtime.ChatRequest{Model: "llama-3"})
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) {
		t.Fatalf("expected *runtime.RuntimeError, got %v", err)
	}
	if rtErr.Code != runtime.ErrorProtocol {
		t.Fatalf("Code = %s, want %s", rtErr.Code, runtime.ErrorProtocol)
	}
}
