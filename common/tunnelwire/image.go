package tunnelwire

import (
	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"unicode/utf8"
)

// maxImageMetaField bounds the short string fields of image generation payloads.
// maxImageMetaField 限制图像生成载荷中的短字符串字段。
const maxImageMetaField = 256

// validImageRequest enforces the image request contract on both sides of the
// tunnel. tenantID/keyID are optional attribution, so a deployment without
// API keys can still generate images; they are only length-bounded.
//
// validImageRequest 在隧道两侧执行同一套图像请求契约。tenantID/keyID 是可选的
// 归属标识，未启用 API Key 的部署同样可以生图，因此只限制长度。
func validImageRequest(model, prompt, tenantID, keyID string) bool {
	return model != "" && len(model) <= maxImageMetaField && prompt != "" && len(prompt) <= runtime.MaxImagePromptBytes &&
		utf8.ValidString(prompt) && len(tenantID) <= maxImageMetaField && len(keyID) <= maxImageMetaField
}

// MarshalImageRequest encodes an image request, refusing one outside the contract.
// MarshalImageRequest 编码图像请求，拒绝超出契约的请求。
func MarshalImageRequest(req runtime.ImageRequest) ([]byte, error) {
	if !validImageRequest(req.Model, req.Prompt, req.TenantID, req.KeyID) {
		return nil, protocolError("image generation request", nil)
	}
	return marshalPayload(&tunnelv1.ImageGenerateRequest{Model: req.Model, Prompt: req.Prompt, TenantId: req.TenantID, KeyId: req.KeyID}, "image generation request")
}

// UnmarshalImageRequest decodes an image request and re-checks the contract.
// UnmarshalImageRequest 解码图像请求并再次校验契约。
func UnmarshalImageRequest(b []byte) (runtime.ImageRequest, error) {
	var pb tunnelv1.ImageGenerateRequest
	if err := unmarshalPayload(b, &pb, "image generation request"); err != nil {
		return runtime.ImageRequest{}, err
	}
	if !validImageRequest(pb.GetModel(), pb.GetPrompt(), pb.GetTenantId(), pb.GetKeyId()) {
		return runtime.ImageRequest{}, protocolError("image generation request", nil)
	}
	return runtime.ImageRequest{Model: pb.GetModel(), Prompt: pb.GetPrompt(), TenantID: pb.GetTenantId(), KeyID: pb.GetKeyId()}, nil
}

// validImageEvent accepts exactly one of a bounded meta or a non-empty bounded chunk.
// validImageEvent 只接受有界元数据或非空有界分块二者之一。
func validImageEvent(ev runtime.ImageEvent) bool {
	if ev.Meta != nil {
		m := ev.Meta
		return len(ev.Data) == 0 && m.ContentType != "" && len(m.ContentType) <= maxImageMetaField &&
			m.Size > 0 && m.Size <= runtime.MaxImageBytes && len(m.RevisedPrompt) <= runtime.MaxImagePromptBytes
	}
	return len(ev.Data) > 0 && len(ev.Data) <= runtime.MaxImageChunkBytes
}

// MarshalImageEvent encodes one image generation stream event.
// MarshalImageEvent 编码一个图像生成流事件。
func MarshalImageEvent(ev runtime.ImageEvent) ([]byte, error) {
	if !validImageEvent(ev) {
		return nil, protocolError("image generation event", nil)
	}
	pb := &tunnelv1.ImageGenerateEvent{}
	if ev.Meta != nil {
		pb.Body = &tunnelv1.ImageGenerateEvent_Meta{Meta: &tunnelv1.ImageGenerateMeta{
			ContentType: ev.Meta.ContentType, Size: ev.Meta.Size, RevisedPrompt: ev.Meta.RevisedPrompt, Usage: usageToProto(ev.Meta.Usage),
		}}
	} else {
		pb.Body = &tunnelv1.ImageGenerateEvent_Data{Data: ev.Data}
	}
	return marshalPayload(pb, "image generation event")
}

// UnmarshalImageEvent decodes one image generation stream event.
// UnmarshalImageEvent 解码一个图像生成流事件。
func UnmarshalImageEvent(b []byte) (runtime.ImageEvent, error) {
	var pb tunnelv1.ImageGenerateEvent
	if err := unmarshalPayload(b, &pb, "image generation event"); err != nil {
		return runtime.ImageEvent{}, err
	}
	var ev runtime.ImageEvent
	switch body := pb.GetBody().(type) {
	case *tunnelv1.ImageGenerateEvent_Meta:
		if body.Meta == nil {
			return runtime.ImageEvent{}, protocolError("image generation event", nil)
		}
		ev.Meta = &runtime.ImageMeta{ContentType: body.Meta.GetContentType(), Size: body.Meta.GetSize(),
			RevisedPrompt: body.Meta.GetRevisedPrompt(), Usage: usageFromProto(body.Meta.GetUsage())}
	case *tunnelv1.ImageGenerateEvent_Data:
		ev.Data = body.Data
	}
	if !validImageEvent(ev) {
		return runtime.ImageEvent{}, protocolError("image generation event", nil)
	}
	return ev, nil
}
