package tunnelwire

import (
	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"encoding/json"
	"google.golang.org/protobuf/proto"
)

// Native Messages size limits, shared by the Gateway front doors, the tunnel
// and the Agent so every hop accepts the same documents.
//
// 原生 Messages 的大小上限，由 Gateway 前门、隧道与 Agent 共用，使每一跳接受相同的文档。
const (
	// MaxMessagesJSONBytes bounds one native Messages document or event. It
	// matches the Anthropic Messages API's own 32 MB request limit, so a
	// request the API itself would accept is never refused for its size.
	//
	// MaxMessagesJSONBytes 限制一份原生 Messages 文档或事件。它与 Anthropic Messages
	// API 自身 32 MB 的请求上限一致，API 本身会接受的请求不会因大小被拒绝。
	MaxMessagesJSONBytes = 32 << 20
	// MaxTunnelMessageBytes is the gRPC message limit both tunnel ends set:
	// a Messages document travels inline in one request frame, so the
	// limit is the document plus room for the frame's other fields.
	//
	// MaxTunnelMessageBytes 是隧道两端设置的 gRPC 消息上限：Messages 文档内联在一个
	// 请求帧里传输，因此上限为文档大小加上帧内其他字段的余量。
	MaxTunnelMessageBytes = MaxMessagesJSONBytes + 1<<20
)

// MarshalMessagesRequest encodes ownership separately from the inference document.
// MarshalMessagesRequest 将归属标识与推理文档分开编码。
func MarshalMessagesRequest(req runtime.MessagesRequest) ([]byte, error) {
	if err := req.ValidateProtocol(); err != nil {
		return nil, err
	}
	if len(req.JSON) > MaxMessagesJSONBytes || !json.Valid(req.JSON) || req.TenantID == "" || req.KeyID == "" || len(req.TenantID) > 256 || len(req.KeyID) > 256 {
		return nil, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid Messages document"}
	}
	return proto.Marshal(&tunnelv1.MessagesRequest{Model: req.Model, TenantId: req.TenantID, KeyId: req.KeyID, MessagesJson: req.JSON, ApiVersion: req.APIVersion, BetaFeatures: req.BetaFeatures, Native: req.Native})
}

// UnmarshalMessagesRequest decodes the shared native Messages contract.
// UnmarshalMessagesRequest 解码共享的原生 Messages 契约。
func UnmarshalMessagesRequest(data []byte) (runtime.MessagesRequest, error) {
	var pb tunnelv1.MessagesRequest
	if err := proto.Unmarshal(data, &pb); err != nil {
		return runtime.MessagesRequest{}, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid Messages payload"}
	}
	if len(pb.MessagesJson) > MaxMessagesJSONBytes || !json.Valid(pb.MessagesJson) || pb.TenantId == "" || pb.KeyId == "" || len(pb.TenantId) > 256 || len(pb.KeyId) > 256 {
		return runtime.MessagesRequest{}, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid Messages document"}
	}
	req := runtime.MessagesRequest{Model: pb.Model, TenantID: pb.TenantId, KeyID: pb.KeyId, JSON: pb.MessagesJson, APIVersion: pb.ApiVersion, BetaFeatures: pb.BetaFeatures, Native: pb.Native}
	return req, req.ValidateProtocol()
}

// MarshalMessagesEvent encodes one bounded native SSE data document.
// MarshalMessagesEvent 编码一条有界的原生 SSE 数据文档。
func MarshalMessagesEvent(event runtime.MessagesEvent) ([]byte, error) {
	if len(event.JSON) > MaxMessagesJSONBytes || !json.Valid(event.JSON) {
		return nil, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid Messages event"}
	}
	return proto.Marshal(&tunnelv1.MessagesEvent{EventJson: event.JSON})
}

// UnmarshalMessagesEvent decodes one native SSE data document.
// UnmarshalMessagesEvent 解码一条原生 SSE 数据文档。
func UnmarshalMessagesEvent(data []byte) (runtime.MessagesEvent, error) {
	var pb tunnelv1.MessagesEvent
	if err := proto.Unmarshal(data, &pb); err != nil {
		return runtime.MessagesEvent{}, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid Messages event"}
	}
	if len(pb.EventJson) > MaxMessagesJSONBytes || !json.Valid(pb.EventJson) {
		return runtime.MessagesEvent{}, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid Messages event"}
	}
	return runtime.MessagesEvent{JSON: pb.EventJson}, nil
}
