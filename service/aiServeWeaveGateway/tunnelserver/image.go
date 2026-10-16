package tunnelserver

import (
	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"context"
)

// GenerateImage dispatches one image generation and streams its meta and
// byte chunks as they arrive, holding at most one chunk at a time.
//
// GenerateImage 派发一次图像生成，并随到随转地流式交出元数据与字节分块，同一时刻
// 最多持有一个分块。
func (r *NodeRuntime) GenerateImage(ctx context.Context, req runtime.ImageRequest) (runtime.Stream[runtime.ImageEvent], error) {
	payload, err := tunnelwire.MarshalImageRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := r.srv.Dispatch(ctx, r.request(tunnelv1.Operation_OPERATION_IMAGE_GENERATE, payload, nil))
	if err != nil {
		return nil, err
	}
	return newResponseStream(resp, tunnelwire.UnmarshalImageEvent), nil
}
