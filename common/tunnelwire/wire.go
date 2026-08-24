// Package tunnelwire is the proto codec shared by the Agent and the Gateway:
// every tunnel message that mirrors a common/runtime type is converted here
// and nowhere else, so both ends of the tunnel encode and decode by the same
// rules rather than by two hand-kept copies.
//
// Two invariants hold throughout:
//   - Credentials never cross. Config's APIKey is replaced by a reference, and
//     an error's unsanitized Cause is reduced to a sentinel name.
//   - Presence is preserved. A nil pointer stays absent on the wire and an
//     explicit zero stays present, because "unset" and "explicitly zero" are
//     different requests to a backend.
package tunnelwire

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// convertOperation is the Operation recorded on errors raised while
// converting, so a malformed frame is attributable without a request context.
const convertOperation = "tunnel_convert"

// PayloadKind names the concrete type carried by a RequestHeaders.payload or a
// DataChunk.payload for a given Operation.
type PayloadKind string

const (
	// PayloadNone means no payload is carried at all.
	PayloadNone PayloadKind = "none"
	// PayloadChatRequest carries a marshalled runtime.ChatRequest.
	PayloadChatRequest PayloadKind = "chat_request"
	// PayloadChatResponse carries a marshalled runtime.ChatResponse.
	PayloadChatResponse PayloadKind = "chat_response"
	// PayloadChatEvent carries exactly one marshalled runtime.ChatEvent.
	PayloadChatEvent PayloadKind = "chat_event"
	// PayloadEmbeddingRequest carries a marshalled runtime.EmbeddingRequest.
	PayloadEmbeddingRequest PayloadKind = "embedding_request"
	// PayloadEmbeddingResponse carries a marshalled runtime.EmbeddingResponse.
	PayloadEmbeddingResponse PayloadKind = "embedding_response"
	// PayloadModelList carries a marshalled []runtime.Model.
	PayloadModelList PayloadKind = "model_list"
	// PayloadWorkflowRequest carries a runtime.WorkflowRequest without its
	// Template; the template follows as DataChunks.
	PayloadWorkflowRequest PayloadKind = "workflow_request"
	// PayloadWorkflowRun carries a marshalled runtime.WorkflowRun.
	PayloadWorkflowRun PayloadKind = "workflow_run"
	// PayloadWorkflowEvent carries exactly one marshalled runtime.WorkflowEvent.
	PayloadWorkflowEvent PayloadKind = "workflow_event"
	// PayloadWorkflowStatus carries a marshalled runtime.WorkflowStatus.
	PayloadWorkflowStatus PayloadKind = "workflow_status"
	// PayloadRunRef carries a workflow run id.
	PayloadRunRef PayloadKind = "run_ref"
	// PayloadArtifactRef carries a marshalled runtime.ArtifactRef.
	PayloadArtifactRef PayloadKind = "artifact_ref"
	// PayloadArtifactBytes carries raw artifact bytes, not a proto message.
	PayloadArtifactBytes PayloadKind = "artifact_bytes"
	// PayloadArtifactList carries a marshalled tunnelv1.ArtifactList: which
	// artifacts a run produced, and none of their bytes.
	//
	// PayloadArtifactList 携带序列化的 tunnelv1.ArtifactList：一次运行产出了哪些
	// 产物，不含它们的任何字节。
	PayloadArtifactList PayloadKind = "artifact_list"
)

// ResponseShape describes how many DataChunks an Operation's response uses,
// which is what tells a reader when a request is structurally complete.
type ResponseShape string

const (
	// ShapeNone means the response is a bare ResponseEnd with no DataChunk.
	ShapeNone ResponseShape = "none"
	// ShapeSingle means exactly one DataChunk precedes ResponseEnd.
	ShapeSingle ResponseShape = "single"
	// ShapeStream means N DataChunks, each holding exactly one event, sent as
	// soon as each event is produced. Aggregating them would destroy TTFT.
	ShapeStream ResponseShape = "stream"
	// ShapeBody means ResponseHeaders followed by N DataChunks of raw bytes.
	ShapeBody ResponseShape = "body"
)

// OperationSpec is the payload contract of one Operation: what the request
// carries, what the response carries, and how the response is framed.
type OperationSpec struct {
	Operation tunnelv1.Operation
	Request   PayloadKind
	Response  PayloadKind
	Shape     ResponseShape
	// RequestBody reports whether the request continues in DataChunks after
	// RequestHeaders. Only workflow submission does, because a workflow
	// template is large enough to threaten the frame size limit.
	RequestBody bool
}

// operationSpecs is the single source of truth for the payload encoding table
// in README.md. Adding an Operation without adding a row here is caught by
// TestConvertOperationSpecCoversEveryOperation rather than at dispatch time.
var operationSpecs = map[tunnelv1.Operation]OperationSpec{
	tunnelv1.Operation_OPERATION_LIST_MODELS: {
		Operation: tunnelv1.Operation_OPERATION_LIST_MODELS,
		Request:   PayloadNone,
		Response:  PayloadModelList,
		Shape:     ShapeSingle,
	},
	tunnelv1.Operation_OPERATION_CHAT: {
		Operation: tunnelv1.Operation_OPERATION_CHAT,
		Request:   PayloadChatRequest,
		Response:  PayloadChatResponse,
		Shape:     ShapeSingle,
	},
	tunnelv1.Operation_OPERATION_CHAT_STREAM: {
		Operation: tunnelv1.Operation_OPERATION_CHAT_STREAM,
		Request:   PayloadChatRequest,
		Response:  PayloadChatEvent,
		Shape:     ShapeStream,
	},
	tunnelv1.Operation_OPERATION_EMBED: {
		Operation: tunnelv1.Operation_OPERATION_EMBED,
		Request:   PayloadEmbeddingRequest,
		Response:  PayloadEmbeddingResponse,
		Shape:     ShapeSingle,
	},
	tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT: {
		Operation:   tunnelv1.Operation_OPERATION_WORKFLOW_SUBMIT,
		Request:     PayloadWorkflowRequest,
		Response:    PayloadWorkflowRun,
		Shape:       ShapeSingle,
		RequestBody: true,
	},
	tunnelv1.Operation_OPERATION_WORKFLOW_SUBSCRIBE: {
		Operation: tunnelv1.Operation_OPERATION_WORKFLOW_SUBSCRIBE,
		Request:   PayloadRunRef,
		Response:  PayloadWorkflowEvent,
		Shape:     ShapeStream,
	},
	tunnelv1.Operation_OPERATION_WORKFLOW_STATUS: {
		Operation: tunnelv1.Operation_OPERATION_WORKFLOW_STATUS,
		Request:   PayloadRunRef,
		Response:  PayloadWorkflowStatus,
		Shape:     ShapeSingle,
	},
	tunnelv1.Operation_OPERATION_WORKFLOW_CANCEL: {
		Operation: tunnelv1.Operation_OPERATION_WORKFLOW_CANCEL,
		Request:   PayloadRunRef,
		Response:  PayloadNone,
		Shape:     ShapeNone,
	},
	tunnelv1.Operation_OPERATION_ARTIFACT_OPEN: {
		Operation: tunnelv1.Operation_OPERATION_ARTIFACT_OPEN,
		Request:   PayloadArtifactRef,
		Response:  PayloadArtifactBytes,
		Shape:     ShapeBody,
	},
	// Listing is ShapeSingle, not ShapeBody: the reply is one bounded proto
	// message naming the artifacts, so it travels like a status query rather
	// than like the artifacts themselves.
	//
	// 列举是 ShapeSingle 而不是 ShapeBody：回复是一条有界的 proto 消息，只点名产物，
	// 因此它像一次状态查询那样传输，而不像产物本身那样。
	tunnelv1.Operation_OPERATION_ARTIFACT_LIST: {
		Operation: tunnelv1.Operation_OPERATION_ARTIFACT_LIST,
		Request:   PayloadRunRef,
		Response:  PayloadArtifactList,
		Shape:     ShapeSingle,
	},
}

// SpecFor returns the payload contract for op. An unknown or unspecified
// Operation is a protocol violation, not a capability gap: the peer sent
// something this build cannot interpret, so it is reported as ErrorProtocol
// rather than being guessed at.
func SpecFor(op tunnelv1.Operation) (OperationSpec, error) {
	spec, ok := operationSpecs[op]
	if !ok {
		return OperationSpec{}, &runtime.RuntimeError{
			Code:      runtime.ErrorProtocol,
			Operation: convertOperation,
			Message:   "unknown tunnel operation " + OperationName(op),
		}
	}
	return spec, nil
}

// OperationName renders op for an error message, falling back to its numeric
// value when the enum does not know it.
func OperationName(op tunnelv1.Operation) string {
	if name, ok := tunnelv1.Operation_name[int32(op)]; ok {
		return name
	}
	return op.String()
}

// -----------------------------------------------------------------------
// Payload marshalling
// -----------------------------------------------------------------------

// marshalPayload encodes m, turning an encoder failure into an ErrorProtocol.
func marshalPayload(m proto.Message, what string) ([]byte, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return nil, protocolError("encode "+what, err)
	}
	return b, nil
}

// unmarshalPayload decodes b into m. The underlying decoder error is attached
// as Cause for local diagnosis and deliberately kept out of Message, which is
// the only part that crosses the tunnel.
func unmarshalPayload(b []byte, m proto.Message, what string) error {
	if err := proto.Unmarshal(b, m); err != nil {
		return protocolError("decode "+what, err)
	}
	return nil
}

// protocolError builds the ErrorProtocol used for every conversion failure.
func protocolError(what string, cause error) *runtime.RuntimeError {
	return &runtime.RuntimeError{
		Code:      runtime.ErrorProtocol,
		Operation: convertOperation,
		Message:   "malformed tunnel payload: cannot " + what,
		Cause:     cause,
	}
}

// MarshalChatRequest encodes req as a CHAT or CHAT_STREAM request payload.
func MarshalChatRequest(req runtime.ChatRequest) ([]byte, error) {
	return marshalPayload(ChatRequestToProto(req), "chat request")
}

// UnmarshalChatRequest decodes a CHAT or CHAT_STREAM request payload.
func UnmarshalChatRequest(b []byte) (runtime.ChatRequest, error) {
	var pb tunnelv1.ChatRequest
	if err := unmarshalPayload(b, &pb, "chat request"); err != nil {
		return runtime.ChatRequest{}, err
	}
	return ChatRequestFromProto(&pb), nil
}

// MarshalChatResponse encodes resp as the single CHAT response chunk.
func MarshalChatResponse(resp runtime.ChatResponse) ([]byte, error) {
	return marshalPayload(ChatResponseToProto(resp), "chat response")
}

// UnmarshalChatResponse decodes the single CHAT response chunk.
func UnmarshalChatResponse(b []byte) (runtime.ChatResponse, error) {
	var pb tunnelv1.ChatResponse
	if err := unmarshalPayload(b, &pb, "chat response"); err != nil {
		return runtime.ChatResponse{}, err
	}
	return ChatResponseFromProto(&pb), nil
}

// MarshalChatEvent encodes one CHAT_STREAM event. Each event gets its own
// chunk and is sent immediately; batching events is what turns a millisecond
// TTFT into a multi-second one.
func MarshalChatEvent(ev runtime.ChatEvent) ([]byte, error) {
	return marshalPayload(ChatEventToProto(ev), "chat event")
}

// UnmarshalChatEvent decodes one CHAT_STREAM event chunk.
func UnmarshalChatEvent(b []byte) (runtime.ChatEvent, error) {
	var pb tunnelv1.ChatEvent
	if err := unmarshalPayload(b, &pb, "chat event"); err != nil {
		return runtime.ChatEvent{}, err
	}
	return ChatEventFromProto(&pb), nil
}

// MarshalEmbeddingRequest encodes req as an EMBED request payload.
func MarshalEmbeddingRequest(req runtime.EmbeddingRequest) ([]byte, error) {
	return marshalPayload(EmbeddingRequestToProto(req), "embedding request")
}

// UnmarshalEmbeddingRequest decodes an EMBED request payload.
func UnmarshalEmbeddingRequest(b []byte) (runtime.EmbeddingRequest, error) {
	var pb tunnelv1.EmbeddingRequest
	if err := unmarshalPayload(b, &pb, "embedding request"); err != nil {
		return runtime.EmbeddingRequest{}, err
	}
	return EmbeddingRequestFromProto(&pb), nil
}

// MarshalEmbeddingResponse encodes resp as the single EMBED response chunk.
func MarshalEmbeddingResponse(resp runtime.EmbeddingResponse) ([]byte, error) {
	return marshalPayload(EmbeddingResponseToProto(resp), "embedding response")
}

// UnmarshalEmbeddingResponse decodes the single EMBED response chunk.
func UnmarshalEmbeddingResponse(b []byte) (runtime.EmbeddingResponse, error) {
	var pb tunnelv1.EmbeddingResponse
	if err := unmarshalPayload(b, &pb, "embedding response"); err != nil {
		return runtime.EmbeddingResponse{}, err
	}
	return EmbeddingResponseFromProto(&pb), nil
}

// MarshalModels encodes models as the single LIST_MODELS response chunk.
func MarshalModels(models []runtime.Model) ([]byte, error) {
	return marshalPayload(ModelsToProto(models), "model list")
}

// UnmarshalModels decodes the single LIST_MODELS response chunk.
func UnmarshalModels(b []byte) ([]runtime.Model, error) {
	var pb tunnelv1.ModelList
	if err := unmarshalPayload(b, &pb, "model list"); err != nil {
		return nil, err
	}
	return ModelsFromProto(&pb), nil
}

// MarshalWorkflowRequest encodes req as a WORKFLOW_SUBMIT request payload.
// req.Template is deliberately not included: it travels as DataChunks so a
// large template cannot push a single frame past the receive limit.
func MarshalWorkflowRequest(req runtime.WorkflowRequest) ([]byte, error) {
	return marshalPayload(WorkflowRequestToProto(req), "workflow request")
}

// UnmarshalWorkflowRequest decodes a WORKFLOW_SUBMIT request payload. The
// returned Template is always nil; the caller fills it from the DataChunks
// that follow.
func UnmarshalWorkflowRequest(b []byte) (runtime.WorkflowRequest, error) {
	var pb tunnelv1.WorkflowRequest
	if err := unmarshalPayload(b, &pb, "workflow request"); err != nil {
		return runtime.WorkflowRequest{}, err
	}
	return WorkflowRequestFromProto(&pb), nil
}

// MarshalWorkflowRun encodes run as the single WORKFLOW_SUBMIT response chunk.
func MarshalWorkflowRun(run runtime.WorkflowRun) ([]byte, error) {
	return marshalPayload(WorkflowRunToProto(run), "workflow run")
}

// UnmarshalWorkflowRun decodes the single WORKFLOW_SUBMIT response chunk.
func UnmarshalWorkflowRun(b []byte) (runtime.WorkflowRun, error) {
	var pb tunnelv1.WorkflowRun
	if err := unmarshalPayload(b, &pb, "workflow run"); err != nil {
		return runtime.WorkflowRun{}, err
	}
	return WorkflowRunFromProto(&pb), nil
}

// MarshalWorkflowEvent encodes one WORKFLOW_SUBSCRIBE event chunk.
func MarshalWorkflowEvent(ev runtime.WorkflowEvent) ([]byte, error) {
	return marshalPayload(WorkflowEventToProto(ev), "workflow event")
}

// UnmarshalWorkflowEvent decodes one WORKFLOW_SUBSCRIBE event chunk.
func UnmarshalWorkflowEvent(b []byte) (runtime.WorkflowEvent, error) {
	var pb tunnelv1.WorkflowEvent
	if err := unmarshalPayload(b, &pb, "workflow event"); err != nil {
		return runtime.WorkflowEvent{}, err
	}
	return WorkflowEventFromProto(&pb), nil
}

// MarshalWorkflowStatus encodes status as the single WORKFLOW_STATUS chunk.
func MarshalWorkflowStatus(status runtime.WorkflowStatus) ([]byte, error) {
	return marshalPayload(WorkflowStatusToProto(status), "workflow status")
}

// UnmarshalWorkflowStatus decodes the single WORKFLOW_STATUS chunk.
func UnmarshalWorkflowStatus(b []byte) (runtime.WorkflowStatus, error) {
	var pb tunnelv1.WorkflowStatus
	if err := unmarshalPayload(b, &pb, "workflow status"); err != nil {
		return runtime.WorkflowStatus{}, err
	}
	return WorkflowStatusFromProto(&pb), nil
}

// MarshalRunRef encodes the run id used by WORKFLOW_SUBSCRIBE,
// WORKFLOW_STATUS and WORKFLOW_CANCEL.
func MarshalRunRef(runID string) ([]byte, error) {
	return marshalPayload(&tunnelv1.RunRef{RunId: runID}, "run reference")
}

// UnmarshalRunRef decodes a run id request payload.
func UnmarshalRunRef(b []byte) (string, error) {
	var pb tunnelv1.RunRef
	if err := unmarshalPayload(b, &pb, "run reference"); err != nil {
		return "", err
	}
	return pb.GetRunId(), nil
}

// MarshalArtifactList encodes refs as the single ARTIFACT_LIST response chunk.
func MarshalArtifactList(refs []runtime.ArtifactRef) ([]byte, error) {
	return marshalPayload(ArtifactRefsToProto(refs), "artifact list")
}

// UnmarshalArtifactList decodes the single ARTIFACT_LIST response chunk. A run
// that produced no artifacts decodes to a nil slice, not an error: having
// nothing to show is a normal outcome for a cancelled or still-queued run.
//
// UnmarshalArtifactList 解码 ARTIFACT_LIST 的单个响应块。没有产出任何产物的运行解码
// 为 nil 切片而不是错误：对一次被取消或仍在排队的运行来说，无物可示是正常结果。
func UnmarshalArtifactList(b []byte) ([]runtime.ArtifactRef, error) {
	var pb tunnelv1.ArtifactList
	if err := unmarshalPayload(b, &pb, "artifact list"); err != nil {
		return nil, err
	}
	return ArtifactRefsFromProto(&pb), nil
}

// MarshalArtifactRef encodes ref as an ARTIFACT_OPEN request payload.
func MarshalArtifactRef(ref runtime.ArtifactRef) ([]byte, error) {
	return marshalPayload(ArtifactRefToProto(ref), "artifact reference")
}

// UnmarshalArtifactRef decodes an ARTIFACT_OPEN request payload.
func UnmarshalArtifactRef(b []byte) (runtime.ArtifactRef, error) {
	var pb tunnelv1.ArtifactRef
	if err := unmarshalPayload(b, &pb, "artifact reference"); err != nil {
		return runtime.ArtifactRef{}, err
	}
	return ArtifactRefFromProto(&pb), nil
}

// -----------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------

// sentinelWireNames maps runtime sentinel errors to the names carried in
// TunnelError.cause. The slice is ordered so the name chosen for a wrapped
// cause is deterministic. Only these errors cross the tunnel: any other cause
// may hold an upstream URL, a response body or a credential, none of which the
// far side needs to branch on.
var sentinelWireNames = []struct {
	name string
	err  error
}{
	{"ErrFactoryAlreadyRegistered", runtime.ErrFactoryAlreadyRegistered},
	{"ErrRuntimeKindUnsupported", runtime.ErrRuntimeKindUnsupported},
	{"ErrRuntimeIDDuplicated", runtime.ErrRuntimeIDDuplicated},
	{"ErrRuntimeNotFound", runtime.ErrRuntimeNotFound},
	{"ErrCancelUnsupported", runtime.ErrCancelUnsupported},
	{"ErrCapabilityUnknown", runtime.ErrCapabilityUnknown},
	{"ErrCapabilityUnsupported", runtime.ErrCapabilityUnsupported},
	{"ErrConcurrencyLimit", runtime.ErrConcurrencyLimit},
	{"ErrRuntimeClosed", runtime.ErrRuntimeClosed},
	{"ErrContextCanceled", context.Canceled},
	{"ErrContextDeadlineExceeded", context.DeadlineExceeded},
}

// sentinelsByWireName is the reverse of sentinelWireNames, built once so
// ErrorFromProto restores a sentinel without scanning.
var sentinelsByWireName = func() map[string]error {
	m := make(map[string]error, len(sentinelWireNames))
	for _, s := range sentinelWireNames {
		m[s.name] = s.err
	}
	return m
}()

// Messages used when an error reaches the tunnel without a RuntimeError
// classification. They are constants precisely because the original text may
// contain detail that must not leave the node.
const (
	msgUnclassified     = "unclassified runtime error; see the Agent log for detail"
	msgDeadlineExceeded = "request deadline exceeded"
	msgCanceled         = "request cancelled"
)

// sentinelWireName returns the wire name of the first sentinel err matches, or
// "" when err is nil or carries no sentinel.
func sentinelWireName(err error) string {
	if err == nil {
		return ""
	}
	for _, s := range sentinelWireNames {
		if errors.Is(err, s.err) {
			return s.name
		}
	}
	return ""
}

// ErrorToProto converts err into the wire error a ResponseEnd carries,
// returning nil for a nil error (which is how success is signalled).
//
// A *runtime.RuntimeError is mirrored field for field, except that Cause is
// reduced to a sentinel name: Message is already sanitized by contract, Cause
// is not. An error that is not a RuntimeError keeps only its classification —
// its text is dropped rather than risking a leak, since the Agent logs the
// full error locally anyway.
func ErrorToProto(err error) *tunnelv1.TunnelError {
	if err == nil {
		return nil
	}

	var re *runtime.RuntimeError
	if errors.As(err, &re) {
		return &tunnelv1.TunnelError{
			Code:       string(re.Code),
			RuntimeId:  re.RuntimeID,
			Kind:       string(re.Kind),
			Operation:  re.Operation,
			StatusCode: int32(re.StatusCode),
			Message:    re.Message,
			Retryable:  re.Retryable,
			Cause:      sentinelWireName(re.Cause),
		}
	}

	code, message := ClassifyBareError(err)
	return &tunnelv1.TunnelError{
		Code:    string(code),
		Message: message,
		Cause:   sentinelWireName(err),
	}
}

// ClassifyBareError assigns a code and a leak-free message to an error that
// never went through the runtime layer's error model. Cancellation maps to
// ErrorConnection because that is the code the tunnel already uses for a
// request terminated by a link teardown, and the error set has no dedicated
// cancellation code.
func ClassifyBareError(err error) (runtime.ErrorCode, string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return runtime.ErrorTimeout, msgDeadlineExceeded
	case errors.Is(err, context.Canceled):
		return runtime.ErrorConnection, msgCanceled
	default:
		return runtime.ErrorUpstream, msgUnclassified
	}
}

// ErrorFromProto restores a *runtime.RuntimeError from the wire, so callers on
// the far side branch on errors.Is exactly as they would against a local
// runtime. A cause naming an unknown sentinel is dropped rather than
// synthesized: a fabricated error would match nothing correctly anyway.
func ErrorFromProto(pb *tunnelv1.TunnelError) error {
	if pb == nil {
		return nil
	}
	return &runtime.RuntimeError{
		Code:       runtime.ErrorCode(pb.GetCode()),
		RuntimeID:  pb.GetRuntimeId(),
		Kind:       runtime.Kind(pb.GetKind()),
		Operation:  pb.GetOperation(),
		StatusCode: int(pb.GetStatusCode()),
		Message:    pb.GetMessage(),
		Retryable:  pb.GetRetryable(),
		Cause:      sentinelsByWireName[pb.GetCause()],
	}
}

// -----------------------------------------------------------------------
// Chat
// -----------------------------------------------------------------------

// ChatRequestToProto mirrors req onto the wire, preserving the difference
// between an unset sampling parameter and one explicitly set to zero.
func ChatRequestToProto(req runtime.ChatRequest) *tunnelv1.ChatRequest {
	pb := &tunnelv1.ChatRequest{
		Model:          req.Model,
		Messages:       chatMessagesToProto(req.Messages),
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		MaxTokens:      intPtrToProto(req.MaxTokens),
		Stop:           req.Stop,
		Seed:           req.Seed,
		Tools:          toolsToProto(req.Tools),
		ToolChoice:     req.ToolChoice,
		ResponseFormat: responseFormatToProto(req.ResponseFormat),
	}
	if len(req.Extra) > 0 {
		pb.Extra = make(map[string][]byte, len(req.Extra))
		for k, v := range req.Extra {
			pb.Extra[k] = rawToProto(v)
		}
	}
	return pb
}

// ChatRequestFromProto restores a ChatRequest from the wire.
func ChatRequestFromProto(pb *tunnelv1.ChatRequest) runtime.ChatRequest {
	if pb == nil {
		return runtime.ChatRequest{}
	}
	req := runtime.ChatRequest{
		Model:          pb.GetModel(),
		Messages:       chatMessagesFromProto(pb.GetMessages()),
		Temperature:    pb.Temperature,
		TopP:           pb.TopP,
		MaxTokens:      intPtrFromProto(pb.MaxTokens),
		Stop:           pb.GetStop(),
		Seed:           pb.Seed,
		Tools:          toolsFromProto(pb.GetTools()),
		ToolChoice:     pb.GetToolChoice(),
		ResponseFormat: responseFormatFromProto(pb.GetResponseFormat()),
	}
	if len(pb.GetExtra()) > 0 {
		req.Extra = make(map[string]json.RawMessage, len(pb.GetExtra()))
		for k, v := range pb.GetExtra() {
			req.Extra[k] = rawFromProto(v)
		}
	}
	return req
}

func chatMessagesToProto(msgs []runtime.ChatMessage) []*tunnelv1.ChatMessage {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]*tunnelv1.ChatMessage, len(msgs))
	for i, m := range msgs {
		out[i] = chatMessageToProto(m)
	}
	return out
}

func chatMessagesFromProto(pbs []*tunnelv1.ChatMessage) []runtime.ChatMessage {
	if len(pbs) == 0 {
		return nil
	}
	out := make([]runtime.ChatMessage, len(pbs))
	for i, pb := range pbs {
		out[i] = chatMessageFromProto(pb)
	}
	return out
}

func chatMessageToProto(m runtime.ChatMessage) *tunnelv1.ChatMessage {
	return &tunnelv1.ChatMessage{
		Role:       m.Role,
		Content:    m.Content,
		Name:       m.Name,
		ToolCallId: m.ToolCallID,
		ToolCalls:  toolCallsToProto(m.ToolCalls),
	}
}

func chatMessageFromProto(pb *tunnelv1.ChatMessage) runtime.ChatMessage {
	if pb == nil {
		return runtime.ChatMessage{}
	}
	return runtime.ChatMessage{
		Role:       pb.GetRole(),
		Content:    pb.GetContent(),
		Name:       pb.GetName(),
		ToolCallID: pb.GetToolCallId(),
		ToolCalls:  toolCallsFromProto(pb.GetToolCalls()),
	}
}

func toolCallsToProto(calls []runtime.ToolCall) []*tunnelv1.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]*tunnelv1.ToolCall, len(calls))
	for i, c := range calls {
		out[i] = &tunnelv1.ToolCall{
			Id:   c.ID,
			Type: c.Type,
			Function: &tunnelv1.FunctionCall{
				Name:      c.Function.Name,
				Arguments: c.Function.Arguments,
			},
		}
	}
	return out
}

func toolCallsFromProto(pbs []*tunnelv1.ToolCall) []runtime.ToolCall {
	if len(pbs) == 0 {
		return nil
	}
	out := make([]runtime.ToolCall, len(pbs))
	for i, pb := range pbs {
		out[i] = runtime.ToolCall{
			ID:   pb.GetId(),
			Type: pb.GetType(),
			Function: runtime.FunctionCall{
				Name:      pb.GetFunction().GetName(),
				Arguments: pb.GetFunction().GetArguments(),
			},
		}
	}
	return out
}

func toolsToProto(tools []runtime.Tool) []*tunnelv1.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]*tunnelv1.Tool, len(tools))
	for i, t := range tools {
		out[i] = &tunnelv1.Tool{
			Type: t.Type,
			Function: &tunnelv1.FunctionDefinition{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  rawToProto(t.Function.Parameters),
			},
		}
	}
	return out
}

func toolsFromProto(pbs []*tunnelv1.Tool) []runtime.Tool {
	if len(pbs) == 0 {
		return nil
	}
	out := make([]runtime.Tool, len(pbs))
	for i, pb := range pbs {
		out[i] = runtime.Tool{
			Type: pb.GetType(),
			Function: runtime.FunctionDefinition{
				Name:        pb.GetFunction().GetName(),
				Description: pb.GetFunction().GetDescription(),
				Parameters:  rawFromProto(pb.GetFunction().GetParameters()),
			},
		}
	}
	return out
}

func responseFormatToProto(rf *runtime.ResponseFormat) *tunnelv1.ResponseFormat {
	if rf == nil {
		return nil
	}
	pb := &tunnelv1.ResponseFormat{Type: rf.Type}
	if rf.JSONSchema != nil {
		pb.JsonSchema = &tunnelv1.JSONSchemaFormat{
			Name:   rf.JSONSchema.Name,
			Strict: rf.JSONSchema.Strict,
			Schema: rawToProto(rf.JSONSchema.Schema),
		}
	}
	return pb
}

func responseFormatFromProto(pb *tunnelv1.ResponseFormat) *runtime.ResponseFormat {
	if pb == nil {
		return nil
	}
	rf := &runtime.ResponseFormat{Type: pb.GetType()}
	if js := pb.GetJsonSchema(); js != nil {
		rf.JSONSchema = &runtime.JSONSchemaFormat{
			Name:   js.GetName(),
			Strict: js.GetStrict(),
			Schema: rawFromProto(js.GetSchema()),
		}
	}
	return rf
}

// ChatResponseToProto mirrors a non-streaming chat result onto the wire.
func ChatResponseToProto(resp runtime.ChatResponse) *tunnelv1.ChatResponse {
	return &tunnelv1.ChatResponse{
		Id:           resp.ID,
		Model:        resp.Model,
		Message:      chatMessageToProto(resp.Message),
		FinishReason: resp.FinishReason,
		Usage:        usageToProto(&resp.Usage),
		CreatedAt:    timeToProto(resp.CreatedAt),
	}
}

// ChatResponseFromProto restores a non-streaming chat result.
func ChatResponseFromProto(pb *tunnelv1.ChatResponse) runtime.ChatResponse {
	if pb == nil {
		return runtime.ChatResponse{}
	}
	resp := runtime.ChatResponse{
		ID:           pb.GetId(),
		Model:        pb.GetModel(),
		Message:      chatMessageFromProto(pb.GetMessage()),
		FinishReason: pb.GetFinishReason(),
		CreatedAt:    timeFromProto(pb.GetCreatedAt()),
	}
	if u := usageFromProto(pb.GetUsage()); u != nil {
		resp.Usage = *u
	}
	return resp
}

// ChatEventToProto mirrors one streaming chat event onto the wire.
func ChatEventToProto(ev runtime.ChatEvent) *tunnelv1.ChatEvent {
	return &tunnelv1.ChatEvent{
		Id:           ev.ID,
		Model:        ev.Model,
		Delta:        chatMessageDeltaToProto(ev.Delta),
		FinishReason: ev.FinishReason,
		Usage:        usageToProto(ev.Usage),
	}
}

// ChatEventFromProto restores one streaming chat event. Usage stays nil on
// every event but the last, which is how a consumer tells them apart.
func ChatEventFromProto(pb *tunnelv1.ChatEvent) runtime.ChatEvent {
	if pb == nil {
		return runtime.ChatEvent{}
	}
	return runtime.ChatEvent{
		ID:           pb.GetId(),
		Model:        pb.GetModel(),
		Delta:        chatMessageDeltaFromProto(pb.GetDelta()),
		FinishReason: pb.GetFinishReason(),
		Usage:        usageFromProto(pb.GetUsage()),
	}
}

// chatMessageDeltaToProto returns nil for an empty delta so a bare
// finish-reason event does not carry an empty submessage.
func chatMessageDeltaToProto(d runtime.ChatMessageDelta) *tunnelv1.ChatMessageDelta {
	if d.Role == "" && d.Content == "" && len(d.ToolCalls) == 0 {
		return nil
	}
	pb := &tunnelv1.ChatMessageDelta{
		Role:    d.Role,
		Content: d.Content,
	}
	if len(d.ToolCalls) > 0 {
		pb.ToolCalls = make([]*tunnelv1.ToolCallDelta, len(d.ToolCalls))
		for i, c := range d.ToolCalls {
			pb.ToolCalls[i] = &tunnelv1.ToolCallDelta{
				Index: int64(c.Index),
				Id:    c.ID,
				Type:  c.Type,
				Function: &tunnelv1.FunctionCallDelta{
					Name:      c.Function.Name,
					Arguments: c.Function.Arguments,
				},
			}
		}
	}
	return pb
}

func chatMessageDeltaFromProto(pb *tunnelv1.ChatMessageDelta) runtime.ChatMessageDelta {
	if pb == nil {
		return runtime.ChatMessageDelta{}
	}
	d := runtime.ChatMessageDelta{
		Role:    pb.GetRole(),
		Content: pb.GetContent(),
	}
	if calls := pb.GetToolCalls(); len(calls) > 0 {
		d.ToolCalls = make([]runtime.ToolCallDelta, len(calls))
		for i, c := range calls {
			d.ToolCalls[i] = runtime.ToolCallDelta{
				Index: int(c.GetIndex()),
				ID:    c.GetId(),
				Type:  c.GetType(),
				Function: runtime.FunctionCallDelta{
					Name:      c.GetFunction().GetName(),
					Arguments: c.GetFunction().GetArguments(),
				},
			}
		}
	}
	return d
}

// usageToProto returns nil for nil or all-zero usage, so an intermediate
// stream event stays as small as possible.
func usageToProto(u *runtime.Usage) *tunnelv1.Usage {
	if u == nil || (u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0) {
		return nil
	}
	return &tunnelv1.Usage{
		PromptTokens:     int64(u.PromptTokens),
		CompletionTokens: int64(u.CompletionTokens),
		TotalTokens:      int64(u.TotalTokens),
	}
}

func usageFromProto(pb *tunnelv1.Usage) *runtime.Usage {
	if pb == nil {
		return nil
	}
	return &runtime.Usage{
		PromptTokens:     int(pb.GetPromptTokens()),
		CompletionTokens: int(pb.GetCompletionTokens()),
		TotalTokens:      int(pb.GetTotalTokens()),
	}
}

// -----------------------------------------------------------------------
// Embeddings and models
// -----------------------------------------------------------------------

// EmbeddingRequestToProto mirrors an embedding request onto the wire.
func EmbeddingRequestToProto(req runtime.EmbeddingRequest) *tunnelv1.EmbeddingRequest {
	return &tunnelv1.EmbeddingRequest{
		Model:      req.Model,
		Input:      req.Input,
		Dimensions: intPtrToProto(req.Dimensions),
	}
}

// EmbeddingRequestFromProto restores an embedding request.
func EmbeddingRequestFromProto(pb *tunnelv1.EmbeddingRequest) runtime.EmbeddingRequest {
	if pb == nil {
		return runtime.EmbeddingRequest{}
	}
	return runtime.EmbeddingRequest{
		Model:      pb.GetModel(),
		Input:      pb.GetInput(),
		Dimensions: intPtrFromProto(pb.Dimensions),
	}
}

// EmbeddingResponseToProto mirrors an embedding result onto the wire.
func EmbeddingResponseToProto(resp runtime.EmbeddingResponse) *tunnelv1.EmbeddingResponse {
	pb := &tunnelv1.EmbeddingResponse{
		Model: resp.Model,
		Usage: usageToProto(&resp.Usage),
	}
	if len(resp.Data) > 0 {
		pb.Data = make([]*tunnelv1.Embedding, len(resp.Data))
		for i, e := range resp.Data {
			pb.Data[i] = &tunnelv1.Embedding{Index: int64(e.Index), Vector: e.Vector}
		}
	}
	return pb
}

// EmbeddingResponseFromProto restores an embedding result.
func EmbeddingResponseFromProto(pb *tunnelv1.EmbeddingResponse) runtime.EmbeddingResponse {
	if pb == nil {
		return runtime.EmbeddingResponse{}
	}
	resp := runtime.EmbeddingResponse{Model: pb.GetModel()}
	if data := pb.GetData(); len(data) > 0 {
		resp.Data = make([]runtime.Embedding, len(data))
		for i, e := range data {
			resp.Data[i] = runtime.Embedding{Index: int(e.GetIndex()), Vector: e.GetVector()}
		}
	}
	if u := usageFromProto(pb.GetUsage()); u != nil {
		resp.Usage = *u
	}
	return resp
}

// ModelsToProto wraps a model list for the wire; protobuf has no top-level
// repeated field, so the slice needs an envelope message.
func ModelsToProto(models []runtime.Model) *tunnelv1.ModelList {
	pb := &tunnelv1.ModelList{}
	if len(models) > 0 {
		pb.Models = make([]*tunnelv1.Model, len(models))
		for i, m := range models {
			pb.Models[i] = modelToProto(m)
		}
	}
	return pb
}

// ModelsFromProto unwraps a model list.
func ModelsFromProto(pb *tunnelv1.ModelList) []runtime.Model {
	models := pb.GetModels()
	if len(models) == 0 {
		return nil
	}
	out := make([]runtime.Model, len(models))
	for i, m := range models {
		out[i] = modelFromProto(m)
	}
	return out
}

func modelToProto(m runtime.Model) *tunnelv1.Model {
	return &tunnelv1.Model{
		Id:           m.ID,
		Capabilities: capabilitySetToProto(m.Capabilities),
	}
}

func modelFromProto(pb *tunnelv1.Model) runtime.Model {
	if pb == nil {
		return runtime.Model{}
	}
	return runtime.Model{
		ID:           pb.GetId(),
		Capabilities: capabilitySetFromProto(pb.GetCapabilities()),
	}
}

// capabilitySetToProto mirrors a CapabilitySet. Both the map key and the
// evidence's own Capability field are carried: they are normally equal, but
// the conversion layer is not the place to enforce that.
func capabilitySetToProto(set runtime.CapabilitySet) map[string]*tunnelv1.CapabilityEvidence {
	if len(set) == 0 {
		return nil
	}
	out := make(map[string]*tunnelv1.CapabilityEvidence, len(set))
	for capability, ev := range set {
		out[string(capability)] = &tunnelv1.CapabilityEvidence{
			Capability: string(ev.Capability),
			Level:      string(ev.Level),
			Source:     string(ev.Source),
			Detail:     ev.Detail,
		}
	}
	return out
}

func capabilitySetFromProto(pb map[string]*tunnelv1.CapabilityEvidence) runtime.CapabilitySet {
	if len(pb) == 0 {
		return nil
	}
	out := make(runtime.CapabilitySet, len(pb))
	for name, ev := range pb {
		out[runtime.Capability(name)] = runtime.CapabilityEvidence{
			Capability: runtime.Capability(ev.GetCapability()),
			Level:      runtime.SupportLevel(ev.GetLevel()),
			Source:     runtime.CapabilitySource(ev.GetSource()),
			Detail:     ev.GetDetail(),
		}
	}
	return out
}

// -----------------------------------------------------------------------
// Workflows and artifacts
// -----------------------------------------------------------------------

// WorkflowRequestToProto mirrors a workflow submission onto the wire without
// its Template, which travels separately as DataChunks.
func WorkflowRequestToProto(req runtime.WorkflowRequest) *tunnelv1.WorkflowRequest {
	return &tunnelv1.WorkflowRequest{
		ClientId:       req.ClientID,
		IdempotencyKey: req.IdempotencyKey,
	}
}

// WorkflowRequestFromProto restores a workflow submission. Template is always
// nil; the caller assembles it from the DataChunks that follow.
func WorkflowRequestFromProto(pb *tunnelv1.WorkflowRequest) runtime.WorkflowRequest {
	if pb == nil {
		return runtime.WorkflowRequest{}
	}
	return runtime.WorkflowRequest{
		ClientID:       pb.GetClientId(),
		IdempotencyKey: pb.GetIdempotencyKey(),
	}
}

// WorkflowRunToProto mirrors a submitted run handle onto the wire.
func WorkflowRunToProto(run runtime.WorkflowRun) *tunnelv1.WorkflowRun {
	return &tunnelv1.WorkflowRun{
		Id:          run.ID,
		RuntimeId:   run.RuntimeID,
		SubmittedAt: timeToProto(run.SubmittedAt),
	}
}

// WorkflowRunFromProto restores a submitted run handle.
func WorkflowRunFromProto(pb *tunnelv1.WorkflowRun) runtime.WorkflowRun {
	if pb == nil {
		return runtime.WorkflowRun{}
	}
	return runtime.WorkflowRun{
		ID:          pb.GetId(),
		RuntimeID:   pb.GetRuntimeId(),
		SubmittedAt: timeFromProto(pb.GetSubmittedAt()),
	}
}

// WorkflowEventToProto mirrors one normalized workflow event onto the wire.
func WorkflowEventToProto(ev runtime.WorkflowEvent) *tunnelv1.WorkflowEvent {
	return &tunnelv1.WorkflowEvent{
		Type:       string(ev.Type),
		RunId:      ev.RunID,
		NodeId:     ev.NodeID,
		Raw:        rawToProto(ev.Raw),
		ReceivedAt: timeToProto(ev.ReceivedAt),
	}
}

// WorkflowEventFromProto restores one normalized workflow event.
func WorkflowEventFromProto(pb *tunnelv1.WorkflowEvent) runtime.WorkflowEvent {
	if pb == nil {
		return runtime.WorkflowEvent{}
	}
	return runtime.WorkflowEvent{
		Type:       runtime.WorkflowEventType(pb.GetType()),
		RunID:      pb.GetRunId(),
		NodeID:     pb.GetNodeId(),
		Raw:        rawFromProto(pb.GetRaw()),
		ReceivedAt: timeFromProto(pb.GetReceivedAt()),
	}
}

// WorkflowStatusToProto mirrors a workflow status snapshot onto the wire.
func WorkflowStatusToProto(status runtime.WorkflowStatus) *tunnelv1.WorkflowStatus {
	return &tunnelv1.WorkflowStatus{
		State:         string(status.State),
		QueuePosition: int64(status.QueuePosition),
		StartedAt:     timePtrToProto(status.StartedAt),
		FinishedAt:    timePtrToProto(status.FinishedAt),
		ErrorSummary:  status.ErrorSummary,
	}
}

// WorkflowStatusFromProto restores a workflow status snapshot.
func WorkflowStatusFromProto(pb *tunnelv1.WorkflowStatus) runtime.WorkflowStatus {
	if pb == nil {
		return runtime.WorkflowStatus{}
	}
	return runtime.WorkflowStatus{
		State:         runtime.WorkflowState(pb.GetState()),
		QueuePosition: int(pb.GetQueuePosition()),
		StartedAt:     timePtrFromProto(pb.GetStartedAt()),
		FinishedAt:    timePtrFromProto(pb.GetFinishedAt()),
		ErrorSummary:  pb.GetErrorSummary(),
	}
}

// ArtifactRefToProto mirrors an artifact reference onto the wire. Only the
// reference crosses: the body is streamed as raw DataChunks so nothing is
// buffered on either side.
func ArtifactRefToProto(ref runtime.ArtifactRef) *tunnelv1.ArtifactRef {
	return &tunnelv1.ArtifactRef{
		RunId:     ref.RunID,
		Filename:  ref.Filename,
		Subfolder: ref.Subfolder,
		Type:      ref.Type,
	}
}

// ArtifactRefsToProto wraps a slice of artifact references in the envelope
// protobuf needs for a top-level repeated field.
//
// ArtifactRefsToProto 把一组产物引用装进 protobuf 顶层 repeated 字段所需的信封里。
func ArtifactRefsToProto(refs []runtime.ArtifactRef) *tunnelv1.ArtifactList {
	pb := &tunnelv1.ArtifactList{}
	if len(refs) > 0 {
		pb.Artifacts = make([]*tunnelv1.ArtifactRef, len(refs))
		for i, ref := range refs {
			pb.Artifacts[i] = ArtifactRefToProto(ref)
		}
	}
	return pb
}

// ArtifactRefsFromProto unwraps an artifact list.
//
// ArtifactRefsFromProto 拆开产物列表的信封。
func ArtifactRefsFromProto(pb *tunnelv1.ArtifactList) []runtime.ArtifactRef {
	if pb == nil || len(pb.GetArtifacts()) == 0 {
		return nil
	}
	out := make([]runtime.ArtifactRef, len(pb.GetArtifacts()))
	for i, ref := range pb.GetArtifacts() {
		out[i] = ArtifactRefFromProto(ref)
	}
	return out
}

// ArtifactRefFromProto restores an artifact reference.
func ArtifactRefFromProto(pb *tunnelv1.ArtifactRef) runtime.ArtifactRef {
	if pb == nil {
		return runtime.ArtifactRef{}
	}
	return runtime.ArtifactRef{
		RunID:     pb.GetRunId(),
		Filename:  pb.GetFilename(),
		Subfolder: pb.GetSubfolder(),
		Type:      pb.GetType(),
	}
}

// -----------------------------------------------------------------------
// Snapshot and Config
// -----------------------------------------------------------------------

// SnapshotToProto mirrors one runtime.Snapshot for a RuntimeStatus report.
// Snapshot is credential-free by construction, which is what makes it safe to
// send to every Gateway replica.
func SnapshotToProto(s runtime.Snapshot) *tunnelv1.RuntimeSnapshot {
	return &tunnelv1.RuntimeSnapshot{
		Descriptor_: &tunnelv1.RuntimeDescriptor{
			Id:            s.Descriptor.ID,
			Kind:          string(s.Descriptor.Kind),
			BaseUrl:       s.Descriptor.BaseURL,
			MaxConcurrent: int64(s.Descriptor.MaxConcurrent),
			Exclusive:     s.Descriptor.Exclusive,
		},
		State: string(s.State),
		Probe: &tunnelv1.ProbeResult{
			Kind:             string(s.Probe.Kind),
			Version:          s.Probe.Version,
			IdentityVerified: s.Probe.IdentityVerified,
			Evidence:         s.Probe.Evidence,
			ProbedAt:         timeToProto(s.Probe.ProbedAt),
		},
		Health: &tunnelv1.HealthReport{
			State:        string(s.Health.State),
			Latency:      durationToProto(s.Health.Latency),
			CheckedAt:    timeToProto(s.Health.CheckedAt),
			ErrorSummary: s.Health.ErrorSummary,
		},
		Discovery: &tunnelv1.Discovery{
			Version:      s.Discovery.Version,
			Models:       ModelsToProto(s.Discovery.Models).GetModels(),
			NodeTypes:    s.Discovery.NodeTypes,
			Capabilities: capabilitySetToProto(s.Discovery.Capabilities),
			Warnings:     s.Discovery.Warnings,
			DiscoveredAt: timeToProto(s.Discovery.DiscoveredAt),
		},
		Inflight:  int64(s.Inflight),
		Degraded:  s.Degraded,
		UpdatedAt: timeToProto(s.UpdatedAt),
	}
}

// SnapshotFromProto restores a runtime.Snapshot from a RuntimeStatus report.
func SnapshotFromProto(pb *tunnelv1.RuntimeSnapshot) runtime.Snapshot {
	if pb == nil {
		return runtime.Snapshot{}
	}
	d := pb.GetDescriptor_()
	probe := pb.GetProbe()
	health := pb.GetHealth()
	disc := pb.GetDiscovery()
	return runtime.Snapshot{
		Descriptor: runtime.Descriptor{
			ID:            d.GetId(),
			Kind:          runtime.Kind(d.GetKind()),
			BaseURL:       d.GetBaseUrl(),
			MaxConcurrent: int(d.GetMaxConcurrent()),
			Exclusive:     d.GetExclusive(),
		},
		State: runtime.State(pb.GetState()),
		Probe: runtime.ProbeResult{
			Kind:             runtime.Kind(probe.GetKind()),
			Version:          probe.GetVersion(),
			IdentityVerified: probe.GetIdentityVerified(),
			Evidence:         probe.GetEvidence(),
			ProbedAt:         timeFromProto(probe.GetProbedAt()),
		},
		Health: runtime.HealthReport{
			State:        runtime.State(health.GetState()),
			Latency:      durationFromProto(health.GetLatency()),
			CheckedAt:    timeFromProto(health.GetCheckedAt()),
			ErrorSummary: health.GetErrorSummary(),
		},
		Discovery: runtime.Discovery{
			Version:      disc.GetVersion(),
			Models:       ModelsFromProto(&tunnelv1.ModelList{Models: disc.GetModels()}),
			NodeTypes:    disc.GetNodeTypes(),
			Capabilities: capabilitySetFromProto(disc.GetCapabilities()),
			Warnings:     disc.GetWarnings(),
			DiscoveredAt: timeFromProto(disc.GetDiscoveredAt()),
		},
		Inflight:  int(pb.GetInflight()),
		Degraded:  pb.GetDegraded(),
		UpdatedAt: timeFromProto(pb.GetUpdatedAt()),
	}
}

// SnapshotsToProto mirrors a whole Manager.Snapshot() result.
func SnapshotsToProto(snaps []runtime.Snapshot) []*tunnelv1.RuntimeSnapshot {
	if len(snaps) == 0 {
		return nil
	}
	out := make([]*tunnelv1.RuntimeSnapshot, len(snaps))
	for i, s := range snaps {
		out[i] = SnapshotToProto(s)
	}
	return out
}

// SnapshotsFromProto restores a whole Manager.Snapshot() result.
func SnapshotsFromProto(pbs []*tunnelv1.RuntimeSnapshot) []runtime.Snapshot {
	if len(pbs) == 0 {
		return nil
	}
	out := make([]runtime.Snapshot, len(pbs))
	for i, pb := range pbs {
		out[i] = SnapshotFromProto(pb)
	}
	return out
}

// ConfigToProto mirrors a runtime.Config for control-plane delivery, replacing
// cfg.APIKey with apiKeyRef. The key value is dropped unconditionally: a
// compromised Gateway replica must not be able to learn a node's credentials,
// and the Agent resolves the reference from its own secret source.
func ConfigToProto(cfg runtime.Config, apiKeyRef string) *tunnelv1.RuntimeSpec {
	spec := &tunnelv1.RuntimeSpec{
		Id:                cfg.ID,
		Kind:              string(cfg.Kind),
		BaseUrl:           cfg.BaseURL,
		ApiKeyRef:         apiKeyRef,
		Headers:           cfg.Headers,
		ProbeTimeout:      durationToProto(cfg.ProbeTimeout),
		RequestTimeout:    durationToProto(cfg.RequestTimeout),
		StreamIdleTimeout: durationToProto(cfg.StreamIdleTimeout),
		HealthInterval:    durationToProto(cfg.HealthInterval),
		DiscoveryInterval: durationToProto(cfg.DiscoveryInterval),
		MaxConcurrent:     int64(cfg.MaxConcurrent),
		Exclusive:         cfg.Exclusive,
	}
	if cfg.TLS != (runtime.TLSConfig{}) {
		spec.Tls = &tunnelv1.TLSSpec{
			CaFile:             cfg.TLS.CAFile,
			ServerName:         cfg.TLS.ServerName,
			InsecureSkipVerify: cfg.TLS.InsecureSkipVerify,
		}
	}
	if len(cfg.CapabilityOverrides) > 0 {
		spec.CapabilityOverrides = make(map[string]string, len(cfg.CapabilityOverrides))
		for capability, level := range cfg.CapabilityOverrides {
			spec.CapabilityOverrides[string(capability)] = string(level)
		}
	}
	return spec
}

// ConfigFromProto restores a runtime.Config and the API key reference that
// must be resolved locally. The returned Config always has an empty APIKey:
// the caller fills it from its secret source before handing the Config to
// Manager.Add or Manager.Replace.
func ConfigFromProto(pb *tunnelv1.RuntimeSpec) (runtime.Config, string) {
	if pb == nil {
		return runtime.Config{}, ""
	}
	cfg := runtime.Config{
		ID:                pb.GetId(),
		Kind:              runtime.Kind(pb.GetKind()),
		BaseURL:           pb.GetBaseUrl(),
		Headers:           pb.GetHeaders(),
		ProbeTimeout:      durationFromProto(pb.GetProbeTimeout()),
		RequestTimeout:    durationFromProto(pb.GetRequestTimeout()),
		StreamIdleTimeout: durationFromProto(pb.GetStreamIdleTimeout()),
		HealthInterval:    durationFromProto(pb.GetHealthInterval()),
		DiscoveryInterval: durationFromProto(pb.GetDiscoveryInterval()),
		MaxConcurrent:     int(pb.GetMaxConcurrent()),
		TLS: runtime.TLSConfig{
			CAFile:             pb.GetTls().GetCaFile(),
			ServerName:         pb.GetTls().GetServerName(),
			InsecureSkipVerify: pb.GetTls().GetInsecureSkipVerify(),
		},
		Exclusive: pb.GetExclusive(),
	}
	if overrides := pb.GetCapabilityOverrides(); len(overrides) > 0 {
		cfg.CapabilityOverrides = make(map[runtime.Capability]runtime.SupportLevel, len(overrides))
		for name, level := range overrides {
			cfg.CapabilityOverrides[runtime.Capability(name)] = runtime.SupportLevel(level)
		}
	}
	return cfg, pb.GetApiKeyRef()
}

// -----------------------------------------------------------------------
// Scalar helpers
// -----------------------------------------------------------------------

// intPtrToProto widens an optional Go int to the wire's int64 without losing
// the difference between unset and zero.
func intPtrToProto(p *int) *int64 {
	if p == nil {
		return nil
	}
	v := int64(*p)
	return &v
}

// intPtrFromProto narrows an optional wire int64 back to a Go int.
func intPtrFromProto(p *int64) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

// rawToProto passes raw JSON through as bytes. An empty document becomes an
// absent field rather than a zero-length one, so it round-trips as nil.
func rawToProto(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// rawFromProto restores raw JSON, normalizing an absent or empty field to nil.
func rawFromProto(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	return json.RawMessage(b)
}

// timeToProto omits a zero time entirely, so "never happened" does not arrive
// as a timestamp in year 1.
func timeToProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// timeFromProto returns the zero time for an absent timestamp.
func timeFromProto(pb *timestamppb.Timestamp) time.Time {
	if pb == nil {
		return time.Time{}
	}
	return pb.AsTime()
}

// timePtrToProto mirrors an optional timestamp, where nil genuinely means
// "not set yet" (a workflow that has not started or finished).
func timePtrToProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// timePtrFromProto restores an optional timestamp.
func timePtrFromProto(pb *timestamppb.Timestamp) *time.Time {
	if pb == nil {
		return nil
	}
	t := pb.AsTime()
	return &t
}

// durationToProto always emits a duration, including zero and negative
// values: runtime.Config reads zero as "apply the default" and a negative
// value as "explicitly opt out", so neither may be collapsed away.
func durationToProto(d time.Duration) *durationpb.Duration {
	return durationpb.New(d)
}

// durationFromProto returns zero for an absent duration.
func durationFromProto(pb *durationpb.Duration) time.Duration {
	if pb == nil {
		return 0
	}
	return pb.AsDuration()
}
