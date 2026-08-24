package tunnelwire_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
)

func ptr[T any](v T) *T { return &v }

// fixedTime is the timestamp used across round-trip fixtures. It is UTC with
// sub-second precision so the assertions catch both a dropped location and a
// truncated nanosecond field.
var fixedTime = time.Date(2026, 8, 20, 10, 30, 0, 123456789, time.UTC)

// -----------------------------------------------------------------------
// ChatRequest: optional-field presence, explicit zeros, Extra passthrough
// -----------------------------------------------------------------------

func TestConvertChatRequestOptionalFieldPresence(t *testing.T) {
	tests := []struct {
		name string
		req  runtime.ChatRequest
		// want reports, for each optional field, whether it must be present
		// on the wire.
		wantTemperature bool
		wantTopP        bool
		wantMaxTokens   bool
		wantSeed        bool
	}{
		{
			name: "all sampling fields unset",
			req:  runtime.ChatRequest{Model: "m"},
		},
		{
			name: "explicit zero values are set",
			req: runtime.ChatRequest{
				Model:       "m",
				Temperature: ptr(0.0),
				TopP:        ptr(0.0),
				MaxTokens:   ptr(0),
				Seed:        ptr(int64(0)),
			},
			wantTemperature: true,
			wantTopP:        true,
			wantMaxTokens:   true,
			wantSeed:        true,
		},
		{
			name: "only temperature set",
			req: runtime.ChatRequest{
				Model:       "m",
				Temperature: ptr(0.7),
			},
			wantTemperature: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb := tunnelwire.ChatRequestToProto(tt.req)

			if got := pb.Temperature != nil; got != tt.wantTemperature {
				t.Errorf("temperature present = %v, want %v", got, tt.wantTemperature)
			}
			if got := pb.TopP != nil; got != tt.wantTopP {
				t.Errorf("top_p present = %v, want %v", got, tt.wantTopP)
			}
			if got := pb.MaxTokens != nil; got != tt.wantMaxTokens {
				t.Errorf("max_tokens present = %v, want %v", got, tt.wantMaxTokens)
			}
			if got := pb.Seed != nil; got != tt.wantSeed {
				t.Errorf("seed present = %v, want %v", got, tt.wantSeed)
			}

			// Presence must survive an actual marshal/unmarshal, not just the
			// in-memory struct: a proto3 field without explicit presence would
			// silently drop an explicit zero here.
			encoded, err := tunnelwire.MarshalChatRequest(tt.req)
			if err != nil {
				t.Fatalf("MarshalChatRequest() error = %v", err)
			}
			got, err := tunnelwire.UnmarshalChatRequest(encoded)
			if err != nil {
				t.Fatalf("UnmarshalChatRequest() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.req) {
				t.Errorf("round trip = %+v, want %+v", got, tt.req)
			}
		})
	}
}

func TestConvertChatRequestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		req  runtime.ChatRequest
	}{
		{
			name: "empty request",
			req:  runtime.ChatRequest{},
		},
		{
			name: "minimal chat",
			req: runtime.ChatRequest{
				Model:    "llama3",
				Messages: []runtime.ChatMessage{{Role: "user", Content: "hi"}},
			},
		},
		{
			name: "every field populated",
			req: runtime.ChatRequest{
				Model: "llama3",
				Messages: []runtime.ChatMessage{
					{Role: "system", Content: "be brief"},
					{
						Role:       "assistant",
						Content:    "",
						Name:       "bot",
						ToolCallID: "call-1",
						ToolCalls: []runtime.ToolCall{{
							ID:       "call-1",
							Type:     "function",
							Function: runtime.FunctionCall{Name: "get_weather", Arguments: `{"city":"SH"}`},
						}},
					},
				},
				Temperature: ptr(0.2),
				TopP:        ptr(0.9),
				MaxTokens:   ptr(1024),
				Stop:        []string{"\n\n", "END"},
				Seed:        ptr(int64(42)),
				Tools: []runtime.Tool{{
					Type: "function",
					Function: runtime.FunctionDefinition{
						Name:        "get_weather",
						Description: "look up the weather",
						Parameters:  json.RawMessage(`{"type":"object"}`),
					},
				}},
				ToolChoice: "auto",
				ResponseFormat: &runtime.ResponseFormat{
					Type: "json_schema",
					JSONSchema: &runtime.JSONSchemaFormat{
						Name:   "reply",
						Strict: true,
						Schema: json.RawMessage(`{"type":"object"}`),
					},
				},
				Extra: map[string]json.RawMessage{
					"top_k":            json.RawMessage(`40`),
					"repetition_range": json.RawMessage(`{"a":[1,2,3]}`),
				},
			},
		},
		{
			name: "response format without json schema",
			req: runtime.ChatRequest{
				Model:          "m",
				ResponseFormat: &runtime.ResponseFormat{Type: "json_object"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := tunnelwire.MarshalChatRequest(tt.req)
			if err != nil {
				t.Fatalf("MarshalChatRequest() error = %v", err)
			}
			got, err := tunnelwire.UnmarshalChatRequest(encoded)
			if err != nil {
				t.Fatalf("UnmarshalChatRequest() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.req) {
				t.Errorf("round trip mismatch\n got = %#v\nwant = %#v", got, tt.req)
			}
		})
	}
}

func TestConvertChatRequestExtraIsVerbatim(t *testing.T) {
	// Extra carries backend-private JSON the tunnel must not interpret:
	// key order, whitespace and value shape all have to survive untouched.
	raw := json.RawMessage(`{"nested":{"b":2,"a":1},  "list":[1,"two",null]}`)
	req := runtime.ChatRequest{
		Model: "m",
		Extra: map[string]json.RawMessage{"vendor": raw},
	}

	encoded, err := tunnelwire.MarshalChatRequest(req)
	if err != nil {
		t.Fatalf("MarshalChatRequest() error = %v", err)
	}
	got, err := tunnelwire.UnmarshalChatRequest(encoded)
	if err != nil {
		t.Fatalf("UnmarshalChatRequest() error = %v", err)
	}
	if string(got.Extra["vendor"]) != string(raw) {
		t.Errorf("Extra[vendor] = %s, want %s", got.Extra["vendor"], raw)
	}
}

// -----------------------------------------------------------------------
// RuntimeError <-> TunnelError
// -----------------------------------------------------------------------

func TestConvertErrorRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		err      *runtime.RuntimeError
		sentinel error // must be matchable with errors.Is on the far side
	}{
		{
			name: "not found with sentinel cause",
			err: &runtime.RuntimeError{
				Code:      runtime.ErrorInvalidConfig,
				RuntimeID: "ollama-1",
				Kind:      runtime.KindOllama,
				Operation: "dispatch",
				Message:   "runtime not registered",
				Cause:     runtime.ErrRuntimeNotFound,
			},
			sentinel: runtime.ErrRuntimeNotFound,
		},
		{
			name: "backpressure is not retryable at this layer",
			err: &runtime.RuntimeError{
				Code:      runtime.ErrorBackpressure,
				RuntimeID: "vllm-1",
				Kind:      runtime.KindVLLM,
				Operation: "chat",
				Message:   "concurrency limit reached",
				Cause:     runtime.ErrConcurrencyLimit,
			},
			sentinel: runtime.ErrConcurrencyLimit,
		},
		{
			name: "retryable upstream error with status code",
			err: &runtime.RuntimeError{
				Code:       runtime.ErrorUpstream,
				RuntimeID:  "sglang-1",
				Kind:       runtime.KindSGLang,
				Operation:  "chat_stream",
				StatusCode: 503,
				Message:    "backend unavailable",
				Retryable:  true,
			},
		},
		{
			name: "capability rejection",
			err: &runtime.RuntimeError{
				Code:      runtime.ErrorCapability,
				RuntimeID: "comfyui-1",
				Kind:      runtime.KindComfyUI,
				Operation: "chat",
				Message:   "capability chat unsupported",
				Cause:     runtime.ErrCapabilityUnsupported,
			},
			sentinel: runtime.ErrCapabilityUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb := tunnelwire.ErrorToProto(tt.err)
			if pb == nil {
				t.Fatal("ErrorToProto() = nil, want a TunnelError")
			}
			if pb.GetCode() != string(tt.err.Code) {
				t.Errorf("code = %q, want %q", pb.GetCode(), tt.err.Code)
			}
			if pb.GetRetryable() != tt.err.Retryable {
				t.Errorf("retryable = %v, want %v", pb.GetRetryable(), tt.err.Retryable)
			}

			got := tunnelwire.ErrorFromProto(pb)
			var re *runtime.RuntimeError
			if !errors.As(got, &re) {
				t.Fatalf("ErrorFromProto() = %T, want *runtime.RuntimeError", got)
			}
			if re.Code != tt.err.Code {
				t.Errorf("Code = %q, want %q", re.Code, tt.err.Code)
			}
			if re.RuntimeID != tt.err.RuntimeID {
				t.Errorf("RuntimeID = %q, want %q", re.RuntimeID, tt.err.RuntimeID)
			}
			if re.Kind != tt.err.Kind {
				t.Errorf("Kind = %q, want %q", re.Kind, tt.err.Kind)
			}
			if re.Operation != tt.err.Operation {
				t.Errorf("Operation = %q, want %q", re.Operation, tt.err.Operation)
			}
			if re.StatusCode != tt.err.StatusCode {
				t.Errorf("StatusCode = %d, want %d", re.StatusCode, tt.err.StatusCode)
			}
			if re.Message != tt.err.Message {
				t.Errorf("Message = %q, want %q", re.Message, tt.err.Message)
			}
			if re.Retryable != tt.err.Retryable {
				t.Errorf("Retryable = %v, want %v", re.Retryable, tt.err.Retryable)
			}

			// Code matching must work through the RuntimeError.Is shortcut.
			if !errors.Is(got, &runtime.RuntimeError{Code: tt.err.Code}) {
				t.Errorf("errors.Is(got, &RuntimeError{Code: %q}) = false, want true", tt.err.Code)
			}
			if tt.sentinel != nil && !errors.Is(got, tt.sentinel) {
				t.Errorf("errors.Is(got, %v) = false, want true", tt.sentinel)
			}
		})
	}
}

func TestConvertErrorAllSentinelsSurvive(t *testing.T) {
	// Every sentinel a caller may branch on has to be restorable, otherwise a
	// Gateway-side errors.Is silently stops matching once the node moves
	// behind a tunnelwire.
	sentinels := []error{
		runtime.ErrFactoryAlreadyRegistered,
		runtime.ErrRuntimeKindUnsupported,
		runtime.ErrRuntimeIDDuplicated,
		runtime.ErrRuntimeNotFound,
		runtime.ErrCancelUnsupported,
		runtime.ErrCapabilityUnknown,
		runtime.ErrCapabilityUnsupported,
		runtime.ErrConcurrencyLimit,
		runtime.ErrRuntimeClosed,
		context.Canceled,
		context.DeadlineExceeded,
	}

	for _, sentinel := range sentinels {
		t.Run(sentinel.Error(), func(t *testing.T) {
			src := &runtime.RuntimeError{Code: runtime.ErrorUpstream, Message: "x", Cause: sentinel}
			got := tunnelwire.ErrorFromProto(tunnelwire.ErrorToProto(src))
			if !errors.Is(got, sentinel) {
				t.Errorf("errors.Is(got, %v) = false, want true", sentinel)
			}
		})
	}
}

func TestConvertErrorDropsUnsanitizedCause(t *testing.T) {
	// RuntimeError.Cause may hold unsanitized detail (URLs, response bodies,
	// credentials). Only named sentinels are allowed on the wire.
	secret := "sk-live-abcdef123456"
	src := &runtime.RuntimeError{
		Code:      runtime.ErrorUnauthorized,
		RuntimeID: "vllm-1",
		Operation: "chat",
		Message:   "upstream rejected the request",
		Cause:     errors.New("POST https://backend.internal/v1/chat: Authorization: Bearer " + secret),
	}

	pb := tunnelwire.ErrorToProto(src)
	if pb.GetCause() != "" {
		t.Errorf("cause = %q, want empty for a non-sentinel cause", pb.GetCause())
	}
	if strings.Contains(pb.String(), secret) {
		t.Error("serialized TunnelError contains the credential from Cause")
	}
	if strings.Contains(pb.String(), "backend.internal") {
		t.Error("serialized TunnelError contains the upstream URL from Cause")
	}

	got := tunnelwire.ErrorFromProto(pb)
	var re *runtime.RuntimeError
	if !errors.As(got, &re) {
		t.Fatalf("ErrorFromProto() = %T, want *runtime.RuntimeError", got)
	}
	if re.Cause != nil {
		t.Errorf("Cause = %v, want nil", re.Cause)
	}
	if re.Code != runtime.ErrorUnauthorized {
		t.Errorf("Code = %q, want %q", re.Code, runtime.ErrorUnauthorized)
	}
}

func TestConvertErrorNil(t *testing.T) {
	if pb := tunnelwire.ErrorToProto(nil); pb != nil {
		t.Errorf("ErrorToProto(nil) = %v, want nil", pb)
	}
	if err := tunnelwire.ErrorFromProto(nil); err != nil {
		t.Errorf("ErrorFromProto(nil) = %v, want nil", err)
	}
}

func TestConvertErrorBareErrorIsClassified(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCode  runtime.ErrorCode
		wantCause error
	}{
		{
			name:      "deadline exceeded becomes timeout",
			err:       context.DeadlineExceeded,
			wantCode:  runtime.ErrorTimeout,
			wantCause: context.DeadlineExceeded,
		},
		{
			name:      "cancellation becomes connection failure",
			err:       context.Canceled,
			wantCode:  runtime.ErrorConnection,
			wantCause: context.Canceled,
		},
		{
			name:     "unclassified error keeps no detail",
			err:      errors.New("token=secret-value leaked into an unwrapped error"),
			wantCode: runtime.ErrorUpstream,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pb := tunnelwire.ErrorToProto(tt.err)
			if pb.GetCode() != string(tt.wantCode) {
				t.Errorf("code = %q, want %q", pb.GetCode(), tt.wantCode)
			}
			if strings.Contains(pb.String(), "secret-value") {
				t.Error("serialized TunnelError contains detail from an unclassified error")
			}
			if tt.wantCause != nil && !errors.Is(tunnelwire.ErrorFromProto(pb), tt.wantCause) {
				t.Errorf("errors.Is(restored, %v) = false, want true", tt.wantCause)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Operation payload contract
// -----------------------------------------------------------------------

func TestConvertOperationSpec(t *testing.T) {
	tests := []struct {
		name         string
		op           tunnelv1.Operation
		wantRequest  tunnelwire.PayloadKind
		wantResponse tunnelwire.PayloadKind
		wantShape    tunnelwire.ResponseShape
		wantBody     bool
	}{
		{
			name:         "list models",
			op:           tunnelv1.Operation_OPERATION_LIST_MODELS,
			wantRequest:  tunnelwire.PayloadNone,
			wantResponse: tunnelwire.PayloadModelList,
			wantShape:    tunnelwire.ShapeSingle,
		},
		{
			name:         "chat",
			op:           tunnelv1.Operation_OPERATION_CHAT,
			wantRequest:  tunnelwire.PayloadChatRequest,
			wantResponse: tunnelwire.PayloadChatResponse,
			wantShape:    tunnelwire.ShapeSingle,
		},
		{
			name:         "chat stream",
			op:           tunnelv1.Operation_OPERATION_CHAT_STREAM,
			wantRequest:  tunnelwire.PayloadChatRequest,
			wantResponse: tunnelwire.PayloadChatEvent,
			wantShape:    tunnelwire.ShapeStream,
		},
		{
			name:         "embed",
			op:           tunnelv1.Operation_OPERATION_EMBED,
			wantRequest:  tunnelwire.PayloadEmbeddingRequest,
			wantResponse: tunnelwire.PayloadEmbeddingResponse,
			wantShape:    tunnelwire.ShapeSingle,
		},
		{
			name:         "workflow submit carries the template as body chunks",
			op:           tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT,
			wantRequest:  tunnelwire.PayloadWorkflowRequest,
			wantResponse: tunnelwire.PayloadWorkflowRun,
			wantShape:    tunnelwire.ShapeSingle,
			wantBody:     true,
		},
		{
			name:         "workflow subscribe",
			op:           tunnelv1.Operation_OPERATION_WORKFLOW_SUBSCRIBE,
			wantRequest:  tunnelwire.PayloadRunRef,
			wantResponse: tunnelwire.PayloadWorkflowEvent,
			wantShape:    tunnelwire.ShapeStream,
		},
		{
			name:         "workflow status",
			op:           tunnelv1.Operation_OPERATION_WORKFLOW_STATUS,
			wantRequest:  tunnelwire.PayloadRunRef,
			wantResponse: tunnelwire.PayloadWorkflowStatus,
			wantShape:    tunnelwire.ShapeSingle,
		},
		{
			name:         "workflow cancel has no response body",
			op:           tunnelv1.Operation_OPERATION_WORKFLOW_CANCEL,
			wantRequest:  tunnelwire.PayloadRunRef,
			wantResponse: tunnelwire.PayloadNone,
			wantShape:    tunnelwire.ShapeNone,
		},
		{
			name:         "artifact open streams raw bytes",
			op:           tunnelv1.Operation_OPERATION_ARTIFACT_OPEN,
			wantRequest:  tunnelwire.PayloadArtifactRef,
			wantResponse: tunnelwire.PayloadArtifactBytes,
			wantShape:    tunnelwire.ShapeBody,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := tunnelwire.SpecFor(tt.op)
			if err != nil {
				t.Fatalf("SpecFor(%v) error = %v", tt.op, err)
			}
			if spec.Operation != tt.op {
				t.Errorf("Operation = %v, want %v", spec.Operation, tt.op)
			}
			if spec.Request != tt.wantRequest {
				t.Errorf("Request = %q, want %q", spec.Request, tt.wantRequest)
			}
			if spec.Response != tt.wantResponse {
				t.Errorf("Response = %q, want %q", spec.Response, tt.wantResponse)
			}
			if spec.Shape != tt.wantShape {
				t.Errorf("Shape = %q, want %q", spec.Shape, tt.wantShape)
			}
			if spec.RequestBody != tt.wantBody {
				t.Errorf("RequestBody = %v, want %v", spec.RequestBody, tt.wantBody)
			}
		})
	}
}

func TestConvertOperationSpecCoversEveryOperation(t *testing.T) {
	// The enum and the table must not drift: a new Operation without a spec
	// would fail at dispatch time instead of here.
	values := tunnelv1.Operation_name
	for value, name := range values {
		op := tunnelv1.Operation(value)
		if op == tunnelv1.Operation_OPERATION_UNSPECIFIED {
			continue
		}
		if _, err := tunnelwire.SpecFor(op); err != nil {
			t.Errorf("SpecFor(%s) error = %v, want a spec", name, err)
		}
	}
}

func TestConvertOperationSpecRejectsUnknown(t *testing.T) {
	tests := []struct {
		name string
		op   tunnelv1.Operation
	}{
		{name: "unspecified", op: tunnelv1.Operation_OPERATION_UNSPECIFIED},
		{name: "out of range", op: tunnelv1.Operation(9999)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tunnelwire.SpecFor(tt.op)
			if err == nil {
				t.Fatalf("SpecFor(%v) error = nil, want ErrorProtocol", tt.op)
			}
			var re *runtime.RuntimeError
			if !errors.As(err, &re) {
				t.Fatalf("error is %T, want *runtime.RuntimeError", err)
			}
			if re.Code != runtime.ErrorProtocol {
				t.Errorf("Code = %q, want %q", re.Code, runtime.ErrorProtocol)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Payload round trips
// -----------------------------------------------------------------------

func TestConvertPayloadRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		encode  func() ([]byte, error)
		decode  func([]byte) (any, error)
		want    any
		compare func(t *testing.T, got, want any)
	}{
		{
			name: "model list",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalModels([]runtime.Model{
					{
						ID: "llama3",
						Capabilities: runtime.CapabilitySet{
							runtime.CapabilityChat: {
								Capability: runtime.CapabilityChat,
								Level:      runtime.SupportSupported,
								Source:     runtime.SourceEndpoint,
								Detail:     "/v1/chat/completions responded",
							},
							runtime.CapabilityVision: {
								Capability: runtime.CapabilityVision,
								Level:      runtime.SupportUnknown,
								Source:     runtime.SourceRuntimeProfile,
							},
						},
					},
					{ID: "nomic-embed"},
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalModels(b) },
			want: []runtime.Model{
				{
					ID: "llama3",
					Capabilities: runtime.CapabilitySet{
						runtime.CapabilityChat: {
							Capability: runtime.CapabilityChat,
							Level:      runtime.SupportSupported,
							Source:     runtime.SourceEndpoint,
							Detail:     "/v1/chat/completions responded",
						},
						runtime.CapabilityVision: {
							Capability: runtime.CapabilityVision,
							Level:      runtime.SupportUnknown,
							Source:     runtime.SourceRuntimeProfile,
						},
					},
				},
				{ID: "nomic-embed"},
			},
		},
		{
			name: "chat response",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalChatResponse(runtime.ChatResponse{
					ID:    "chatcmpl-1",
					Model: "llama3",
					Message: runtime.ChatMessage{
						Role:    "assistant",
						Content: "hello",
						ToolCalls: []runtime.ToolCall{{
							ID:       "call-1",
							Type:     "function",
							Function: runtime.FunctionCall{Name: "f", Arguments: "{}"},
						}},
					},
					FinishReason: "stop",
					Usage:        runtime.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
					CreatedAt:    fixedTime,
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalChatResponse(b) },
			want: runtime.ChatResponse{
				ID:    "chatcmpl-1",
				Model: "llama3",
				Message: runtime.ChatMessage{
					Role:    "assistant",
					Content: "hello",
					ToolCalls: []runtime.ToolCall{{
						ID:       "call-1",
						Type:     "function",
						Function: runtime.FunctionCall{Name: "f", Arguments: "{}"},
					}},
				},
				FinishReason: "stop",
				Usage:        runtime.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
				CreatedAt:    fixedTime,
			},
		},
		{
			name: "chat event without usage",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalChatEvent(runtime.ChatEvent{
					ID:    "chatcmpl-1",
					Model: "llama3",
					Delta: runtime.ChatMessageDelta{
						Role:    "assistant",
						Content: "he",
						ToolCalls: []runtime.ToolCallDelta{{
							Index:    0,
							ID:       "call-1",
							Type:     "function",
							Function: runtime.FunctionCallDelta{Name: "f", Arguments: `{"a"`},
						}},
					},
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalChatEvent(b) },
			want: runtime.ChatEvent{
				ID:    "chatcmpl-1",
				Model: "llama3",
				Delta: runtime.ChatMessageDelta{
					Role:    "assistant",
					Content: "he",
					ToolCalls: []runtime.ToolCallDelta{{
						Index:    0,
						ID:       "call-1",
						Type:     "function",
						Function: runtime.FunctionCallDelta{Name: "f", Arguments: `{"a"`},
					}},
				},
			},
		},
		{
			name: "chat event with final usage",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalChatEvent(runtime.ChatEvent{
					ID:           "chatcmpl-1",
					FinishReason: "stop",
					Usage:        &runtime.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalChatEvent(b) },
			want: runtime.ChatEvent{
				ID:           "chatcmpl-1",
				FinishReason: "stop",
				Usage:        &runtime.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
			},
		},
		{
			name: "embedding request with dimensions",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalEmbeddingRequest(runtime.EmbeddingRequest{
					Model:      "nomic-embed",
					Input:      []string{"a", "b"},
					Dimensions: ptr(768),
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalEmbeddingRequest(b) },
			want: runtime.EmbeddingRequest{
				Model:      "nomic-embed",
				Input:      []string{"a", "b"},
				Dimensions: ptr(768),
			},
		},
		{
			name: "embedding request without dimensions",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalEmbeddingRequest(runtime.EmbeddingRequest{Model: "m", Input: []string{"a"}})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalEmbeddingRequest(b) },
			want:   runtime.EmbeddingRequest{Model: "m", Input: []string{"a"}},
		},
		{
			name: "embedding response",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalEmbeddingResponse(runtime.EmbeddingResponse{
					Model: "nomic-embed",
					Data: []runtime.Embedding{
						{Index: 0, Vector: []float32{0.1, -0.2, 0}},
						{Index: 1, Vector: []float32{1}},
					},
					Usage: runtime.Usage{PromptTokens: 4, TotalTokens: 4},
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalEmbeddingResponse(b) },
			want: runtime.EmbeddingResponse{
				Model: "nomic-embed",
				Data: []runtime.Embedding{
					{Index: 0, Vector: []float32{0.1, -0.2, 0}},
					{Index: 1, Vector: []float32{1}},
				},
				Usage: runtime.Usage{PromptTokens: 4, TotalTokens: 4},
			},
		},
		{
			name: "workflow run",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalWorkflowRun(runtime.WorkflowRun{
					ID:          "prompt-1",
					RuntimeID:   "comfyui-1",
					SubmittedAt: fixedTime,
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalWorkflowRun(b) },
			want: runtime.WorkflowRun{
				ID:          "prompt-1",
				RuntimeID:   "comfyui-1",
				SubmittedAt: fixedTime,
			},
		},
		{
			name: "workflow event",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalWorkflowEvent(runtime.WorkflowEvent{
					Type:       runtime.WorkflowEventProgress,
					RunID:      "prompt-1",
					NodeID:     "12",
					Raw:        json.RawMessage(`{"value":3,"max":20}`),
					ReceivedAt: fixedTime,
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalWorkflowEvent(b) },
			want: runtime.WorkflowEvent{
				Type:       runtime.WorkflowEventProgress,
				RunID:      "prompt-1",
				NodeID:     "12",
				Raw:        json.RawMessage(`{"value":3,"max":20}`),
				ReceivedAt: fixedTime,
			},
		},
		{
			name: "workflow status with both timestamps",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalWorkflowStatus(runtime.WorkflowStatus{
					State:         runtime.WorkflowSucceeded,
					QueuePosition: 0,
					StartedAt:     ptr(fixedTime),
					FinishedAt:    ptr(fixedTime.Add(time.Minute)),
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalWorkflowStatus(b) },
			want: runtime.WorkflowStatus{
				State:      runtime.WorkflowSucceeded,
				StartedAt:  ptr(fixedTime),
				FinishedAt: ptr(fixedTime.Add(time.Minute)),
			},
		},
		{
			name: "workflow status still queued",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalWorkflowStatus(runtime.WorkflowStatus{
					State:         runtime.WorkflowPending,
					QueuePosition: 3,
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalWorkflowStatus(b) },
			want: runtime.WorkflowStatus{
				State:         runtime.WorkflowPending,
				QueuePosition: 3,
			},
		},
		{
			name: "artifact ref",
			encode: func() ([]byte, error) {
				return tunnelwire.MarshalArtifactRef(runtime.ArtifactRef{
					RunID:     "prompt-1",
					Filename:  "ComfyUI_00001_.png",
					Subfolder: "out",
					Type:      "output",
				})
			},
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalArtifactRef(b) },
			want: runtime.ArtifactRef{
				RunID:     "prompt-1",
				Filename:  "ComfyUI_00001_.png",
				Subfolder: "out",
				Type:      "output",
			},
		},
		{
			name:   "run ref",
			encode: func() ([]byte, error) { return tunnelwire.MarshalRunRef("prompt-1") },
			decode: func(b []byte) (any, error) { return tunnelwire.UnmarshalRunRef(b) },
			want:   "prompt-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := tt.encode()
			if err != nil {
				t.Fatalf("encode error = %v", err)
			}
			got, err := tt.decode(encoded)
			if err != nil {
				t.Fatalf("decode error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("round trip mismatch\n got = %#v\nwant = %#v", got, tt.want)
			}
		})
	}
}

func TestConvertWorkflowRequestExcludesTemplate(t *testing.T) {
	// Templates are large; the payload contract sends them as DataChunks so a
	// single frame cannot exceed the receive limit.
	template := json.RawMessage(`{"1":{"class_type":"KSampler","inputs":{"seed":1}}}`)
	req := runtime.WorkflowRequest{
		Template:       template,
		ClientID:       "agent-1",
		IdempotencyKey: "idem-1",
	}

	encoded, err := tunnelwire.MarshalWorkflowRequest(req)
	if err != nil {
		t.Fatalf("MarshalWorkflowRequest() error = %v", err)
	}
	if strings.Contains(string(encoded), "KSampler") {
		t.Error("encoded WorkflowRequest contains the template; it must travel as DataChunks")
	}

	got, err := tunnelwire.UnmarshalWorkflowRequest(encoded)
	if err != nil {
		t.Fatalf("UnmarshalWorkflowRequest() error = %v", err)
	}
	if got.Template != nil {
		t.Errorf("Template = %s, want nil", got.Template)
	}
	if got.ClientID != req.ClientID || got.IdempotencyKey != req.IdempotencyKey {
		t.Errorf("got = %+v, want ClientID=%q IdempotencyKey=%q", got, req.ClientID, req.IdempotencyKey)
	}
}

func TestConvertUnmarshalRejectsMalformedPayload(t *testing.T) {
	// A tag 1 varint field truncated mid-value: invalid for every message in
	// the contract.
	malformed := []byte{0x08, 0xff}

	tests := []struct {
		name   string
		decode func([]byte) error
	}{
		{name: "chat request", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalChatRequest(b); return err }},
		{name: "chat response", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalChatResponse(b); return err }},
		{name: "chat event", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalChatEvent(b); return err }},
		{name: "embedding request", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalEmbeddingRequest(b); return err }},
		{name: "embedding response", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalEmbeddingResponse(b); return err }},
		{name: "models", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalModels(b); return err }},
		{name: "workflow request", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalWorkflowRequest(b); return err }},
		{name: "workflow run", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalWorkflowRun(b); return err }},
		{name: "workflow event", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalWorkflowEvent(b); return err }},
		{name: "workflow status", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalWorkflowStatus(b); return err }},
		{name: "run ref", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalRunRef(b); return err }},
		{name: "artifact ref", decode: func(b []byte) error { _, err := tunnelwire.UnmarshalArtifactRef(b); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.decode(malformed)
			if err == nil {
				t.Fatal("decode error = nil, want ErrorProtocol")
			}
			var re *runtime.RuntimeError
			if !errors.As(err, &re) {
				t.Fatalf("error is %T, want *runtime.RuntimeError", err)
			}
			if re.Code != runtime.ErrorProtocol {
				t.Errorf("Code = %q, want %q", re.Code, runtime.ErrorProtocol)
			}
		})
	}
}

// -----------------------------------------------------------------------
// Snapshot and Config
// -----------------------------------------------------------------------

func TestConvertSnapshotRoundTrip(t *testing.T) {
	want := runtime.Snapshot{
		Descriptor: runtime.Descriptor{
			ID:            "ollama-1",
			Kind:          runtime.KindOllama,
			BaseURL:       "http://127.0.0.1:11434",
			MaxConcurrent: 8,
			Exclusive:     true,
		},
		State: runtime.StateHealthy,
		Probe: runtime.ProbeResult{
			Kind:             runtime.KindOllama,
			Version:          "0.32.14",
			IdentityVerified: true,
			Evidence:         "/api/version returned a version string",
			ProbedAt:         fixedTime,
		},
		Health: runtime.HealthReport{
			State:     runtime.StateHealthy,
			Latency:   3 * time.Millisecond,
			CheckedAt: fixedTime,
		},
		Discovery: runtime.Discovery{
			Version:   "0.32.14",
			Models:    []runtime.Model{{ID: "llama3"}},
			NodeTypes: []string{"KSampler"},
			Capabilities: runtime.CapabilitySet{
				runtime.CapabilityChat: {
					Capability: runtime.CapabilityChat,
					Level:      runtime.SupportSupported,
					Source:     runtime.SourceEndpoint,
					Detail:     "verified",
				},
			},
			Warnings:     []string{"model metadata incomplete"},
			DiscoveredAt: fixedTime,
		},
		Inflight:  2,
		Degraded:  []string{"SGLang missing /health"},
		UpdatedAt: fixedTime,
	}

	got := tunnelwire.SnapshotFromProto(tunnelwire.SnapshotToProto(want))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch\n got = %#v\nwant = %#v", got, want)
	}
}

func TestConvertSnapshotZeroValue(t *testing.T) {
	got := tunnelwire.SnapshotFromProto(tunnelwire.SnapshotToProto(runtime.Snapshot{}))
	if !reflect.DeepEqual(got, runtime.Snapshot{}) {
		t.Errorf("zero Snapshot round trip = %#v, want the zero value", got)
	}
}

func TestConvertConfigNeverCarriesAPIKey(t *testing.T) {
	const apiKey = "sk-live-do-not-ship-me"
	cfg := runtime.Config{
		ID:                  "vllm-1",
		Kind:                runtime.KindVLLM,
		BaseURL:             "http://10.0.0.5:8000",
		APIKey:              apiKey,
		Headers:             map[string]string{"X-Tenant": "acme"},
		ProbeTimeout:        3 * time.Second,
		RequestTimeout:      5 * time.Minute,
		StreamIdleTimeout:   60 * time.Second,
		HealthInterval:      10 * time.Second,
		DiscoveryInterval:   5 * time.Minute,
		MaxConcurrent:       16,
		TLS:                 runtime.TLSConfig{CAFile: "/etc/ca.crt", ServerName: "vllm.internal"},
		CapabilityOverrides: map[runtime.Capability]runtime.SupportLevel{runtime.CapabilityTools: runtime.SupportUnsupported},
		Exclusive:           true,
	}

	spec := tunnelwire.ConfigToProto(cfg, "secret://vllm-1/api-key")
	if strings.Contains(spec.String(), apiKey) {
		t.Fatal("RuntimeSpec contains the API key value")
	}
	if spec.GetApiKeyRef() != "secret://vllm-1/api-key" {
		t.Errorf("api_key_ref = %q, want %q", spec.GetApiKeyRef(), "secret://vllm-1/api-key")
	}

	got, ref := tunnelwire.ConfigFromProto(spec)
	if got.APIKey != "" {
		t.Errorf("APIKey = %q, want empty: the Agent resolves it locally", got.APIKey)
	}
	if ref != "secret://vllm-1/api-key" {
		t.Errorf("ref = %q, want %q", ref, "secret://vllm-1/api-key")
	}

	// Everything except the credential must survive unchanged.
	want := cfg
	want.APIKey = ""
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch\n got = %#v\nwant = %#v", got, want)
	}
}

func TestConvertConfigPreservesNegativeDurations(t *testing.T) {
	// runtime.Config documents a negative duration as the explicit opt-out of
	// a default; collapsing it to zero would silently re-enable the default.
	cfg := runtime.Config{
		ID:                "vllm-1",
		Kind:              runtime.KindVLLM,
		BaseURL:           "http://10.0.0.5:8000",
		StreamIdleTimeout: -1,
		MaxConcurrent:     -1,
	}

	got, _ := tunnelwire.ConfigFromProto(tunnelwire.ConfigToProto(cfg, ""))
	if got.StreamIdleTimeout != -1 {
		t.Errorf("StreamIdleTimeout = %v, want -1", got.StreamIdleTimeout)
	}
	if got.MaxConcurrent != -1 {
		t.Errorf("MaxConcurrent = %d, want -1", got.MaxConcurrent)
	}
}

// TestArtifactListRoundTrip covers the ARTIFACT_LIST payload. The nil and
// empty cases are separate on purpose: this package's standing rule is that
// nil and an explicit zero value are not the same thing, and a run that
// produced no artifacts must not come back as a decoding failure.
//
// TestArtifactListRoundTrip 覆盖 ARTIFACT_LIST 载荷。nil 与空切片刻意分开：本包的
// 既有规矩是 nil 与显式零值不等价，而一次没有产出产物的运行不能表现为解码失败。
func TestArtifactListRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		refs []runtime.ArtifactRef
		want int
	}{
		{
			name: "several artifacts",
			refs: []runtime.ArtifactRef{
				{RunID: "prompt-1", Filename: "ComfyUI_00001_.png", Subfolder: "", Type: "output"},
				{RunID: "prompt-1", Filename: "clip.webm", Subfolder: "video", Type: "output"},
			},
			want: 2,
		},
		{
			name: "a run that produced nothing",
			refs: nil,
			want: 0,
		},
		{
			name: "an explicitly empty list",
			refs: []runtime.ArtifactRef{},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := tunnelwire.MarshalArtifactList(tt.refs)
			if err != nil {
				t.Fatalf("MarshalArtifactList() error = %v, want nil", err)
			}
			got, err := tunnelwire.UnmarshalArtifactList(encoded)
			if err != nil {
				t.Fatalf("UnmarshalArtifactList() error = %v, want nil", err)
			}
			if len(got) != tt.want {
				t.Fatalf("decoded %d artifacts, want %d", len(got), tt.want)
			}
			for i := range got {
				if got[i] != tt.refs[i] {
					t.Errorf("artifact %d = %+v, want %+v", i, got[i], tt.refs[i])
				}
			}
		})
	}
}
