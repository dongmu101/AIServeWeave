package tunnelwire

import (
	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"encoding/json"
	"net/http"
	"slices"

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
	return proto.Marshal(&tunnelv1.MessagesEvent{EventJson: event.JSON, UpstreamHeaders: UpstreamHeaderToProto(event.Header)})
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
	return runtime.MessagesEvent{JSON: pb.EventJson, Header: UpstreamHeaderFromProto(pb.UpstreamHeaders)}, nil
}

// UpstreamHeaderToProto filters h with runtime.SanitizeUpstreamHeader and
// mirrors it in canonical name order; nil when nothing is kept.
//
// UpstreamHeaderToProto 用 runtime.SanitizeUpstreamHeader 过滤 h，并按规范名称顺序映射到
// 线上格式；没有保留任何内容时返回 nil。
func UpstreamHeaderToProto(h http.Header) []*tunnelv1.UpstreamHeader {
	h = runtime.SanitizeUpstreamHeader(h)
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []*tunnelv1.UpstreamHeader
	for _, name := range names {
		out = append(out, &tunnelv1.UpstreamHeader{Name: name, Values: h[name]})
	}
	return out
}

// UpstreamHeaderFromProto restores the headers and filters them again, so a
// peer that skipped the filter still cannot inject a header.
//
// UpstreamHeaderFromProto 还原响应头并再次过滤，未执行过滤的对端也无法注入响应头。
func UpstreamHeaderFromProto(pb []*tunnelv1.UpstreamHeader) http.Header {
	if len(pb) == 0 {
		return nil
	}
	h := make(http.Header, len(pb))
	for _, header := range pb {
		h[header.GetName()] = append(h[header.GetName()], header.GetValues()...)
	}
	return runtime.SanitizeUpstreamHeader(h)
}

// UpstreamResponseToProto mirrors a captured upstream answer, dropping an
// invalid error document and an out-of-range status.
//
// UpstreamResponseToProto 映射截获到的上游应答，丢弃无效的错误文档与越界的状态码。
func UpstreamResponseToProto(u *runtime.UpstreamResponse) *tunnelv1.UpstreamResponse {
	if u == nil || u.StatusCode < 100 || u.StatusCode > 999 {
		return nil
	}
	pb := &tunnelv1.UpstreamResponse{StatusCode: int32(u.StatusCode), Headers: UpstreamHeaderToProto(u.Header)}
	if runtime.ValidUpstreamErrorBody(u.Body) {
		pb.Body = u.Body
	}
	return pb
}

// UpstreamResponseFromProto restores a captured upstream answer under the same checks.
// UpstreamResponseFromProto 以相同校验还原截获到的上游应答。
func UpstreamResponseFromProto(pb *tunnelv1.UpstreamResponse) *runtime.UpstreamResponse {
	if pb == nil || pb.GetStatusCode() < 100 || pb.GetStatusCode() > 999 {
		return nil
	}
	u := &runtime.UpstreamResponse{StatusCode: int(pb.GetStatusCode()), Header: UpstreamHeaderFromProto(pb.GetHeaders())}
	if runtime.ValidUpstreamErrorBody(pb.GetBody()) {
		u.Body = append(json.RawMessage(nil), pb.GetBody()...)
	}
	return u
}
