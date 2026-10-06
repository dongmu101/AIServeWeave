package tunnelwire

import (
	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"encoding/json"
	"google.golang.org/protobuf/proto"
)

// MarshalMessagesRequest encodes ownership separately from the inference document.
// MarshalMessagesRequest 将归属标识与推理文档分开编码。
func MarshalMessagesRequest(req runtime.MessagesRequest) ([]byte, error) {
	if len(req.JSON) > 1<<20 || !json.Valid(req.JSON) || req.TenantID == "" || req.KeyID == "" || len(req.TenantID) > 256 || len(req.KeyID) > 256 {
		return nil, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid Messages document"}
	}
	return proto.Marshal(&tunnelv1.MessagesRequest{Model: req.Model, TenantId: req.TenantID, KeyId: req.KeyID, MessagesJson: req.JSON})
}

// UnmarshalMessagesRequest decodes the shared native Messages contract.
// UnmarshalMessagesRequest 解码共享的原生 Messages 契约。
func UnmarshalMessagesRequest(data []byte) (runtime.MessagesRequest, error) {
	var pb tunnelv1.MessagesRequest
	if err := proto.Unmarshal(data, &pb); err != nil {
		return runtime.MessagesRequest{}, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid Messages payload"}
	}
	if len(pb.MessagesJson) > 1<<20 || !json.Valid(pb.MessagesJson) || pb.TenantId == "" || pb.KeyId == "" || len(pb.TenantId) > 256 || len(pb.KeyId) > 256 {
		return runtime.MessagesRequest{}, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid Messages document"}
	}
	return runtime.MessagesRequest{Model: pb.Model, TenantID: pb.TenantId, KeyID: pb.KeyId, JSON: pb.MessagesJson}, nil
}

// MarshalMessagesEvent encodes one bounded native SSE data document.
// MarshalMessagesEvent 编码一条有界的原生 SSE 数据文档。
func MarshalMessagesEvent(event runtime.MessagesEvent) ([]byte, error) {
	if len(event.JSON) > 1<<20 || !json.Valid(event.JSON) {
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
	if len(pb.EventJson) > 1<<20 || !json.Valid(pb.EventJson) {
		return runtime.MessagesEvent{}, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid Messages event"}
	}
	return runtime.MessagesEvent{JSON: pb.EventJson}, nil
}
