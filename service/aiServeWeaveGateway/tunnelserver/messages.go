package tunnelserver

import (
	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/tunnelwire"
	"context"
)

// Messages dispatches native inference semantics without forwarding HTTP headers.
// Messages 分派原生推理语义，不转发 HTTP 请求头。
func (r *NodeRuntime) Messages(ctx context.Context, req runtime.MessagesRequest) (runtime.Stream[runtime.MessagesEvent], error) {
	payload, err := tunnelwire.MarshalMessagesRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := r.srv.Dispatch(ctx, r.request(tunnelv1.Operation_OPERATION_MESSAGES, payload, nil))
	if err != nil {
		return nil, err
	}
	return newResponseStream(resp, tunnelwire.UnmarshalMessagesEvent), nil
}
