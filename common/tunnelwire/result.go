package tunnelwire

import (
	"context"
	"errors"

	"AIServeWeave/common/runtime"
)

// Result is the outcome label both ends of the tunnel put on a finished
// request. It takes the same six values as the runtime package's result
// convention, so one request can be followed from the Gateway's dispatch
// counter through to the Agent's request counter without translating between
// two vocabularies.
//
// It lives here, beside the wire conversion, for the same reason everything
// else in this package does: the two ends must classify one error identically,
// and two copies of the classification would eventually disagree about which
// failures count as the node's fault.
//
// Result 是隧道两端给一次已完成请求打上的结果标签。它取与 runtime 包结果约定相同
// 的六个值，因此同一个请求可以从 Gateway 的分发计数器一路追到 Agent 的请求计数器，
// 中间不需要在两套词汇之间翻译。
//
// 它和线上格式转换放在同一个包里，理由与本包其余内容一致：两端必须对同一个错误
// 做出相同分类，而两份分类实现迟早会在「哪些失败算节点的错」上产生分歧。
type Result string

// The six result values. Raw status codes are deliberately not among them: a
// label whose value set is the HTTP status space is a label that will one day
// carry a backend's invented code.
//
// 六个结果取值。原始状态码刻意不在其中：取值空间等于 HTTP 状态码全集的标签，
// 迟早会带上某个后端自造的码。
const (
	ResultSuccess       Result = "success"
	ResultClientError   Result = "client_error"
	ResultUpstreamError Result = "upstream_error"
	ResultTimeout       Result = "timeout"
	ResultCancelled     Result = "cancelled"
	ResultBackpressure  Result = "backpressure"
)

// ResultFor maps a request outcome onto the six-value convention. It reads the
// error's code, never its message: a message may quote a prompt, an endpoint
// or a backend's response body, and none of those may reach a label.
//
// ResultFor 把一次请求的结果映射到六值约定上。它只读错误的码，从不读消息：消息
// 里可能引用 prompt、后端地址或后端响应体，这些都不允许进入标签。
func ResultFor(err error) Result {
	if err == nil {
		return ResultSuccess
	}

	var re *runtime.RuntimeError
	code := runtime.ErrorUpstream
	if errors.As(err, &re) {
		code = re.Code
	} else {
		code, _ = ClassifyBareError(err)
		if errors.Is(err, context.Canceled) {
			return ResultCancelled
		}
	}

	switch code {
	case runtime.ErrorRateLimited, runtime.ErrorBackpressure:
		return ResultBackpressure
	case runtime.ErrorTimeout:
		return ResultTimeout
	case runtime.ErrorInvalidConfig, runtime.ErrorUnauthorized, runtime.ErrorCapability,
		runtime.ErrorProtocol, runtime.ErrorResponseTooLarge, runtime.ErrorCancelUnsupported,
		runtime.ErrorProbeMismatch:
		return ResultClientError
	case runtime.ErrorConnection:
		// A link teardown is how a cancelled request surfaces: the tunnel's
		// error set has no cancellation code, so the cause is what separates
		// "the user hung up" from "the connection broke".
		//
		// 被取消的请求是以链路拆除的形式浮现的：隧道的错误集合里没有取消这个码，
		// 因此要靠 cause 才能区分「用户挂断了」与「连接断了」。
		if errors.Is(err, context.Canceled) {
			return ResultCancelled
		}
		return ResultUpstreamError
	default:
		return ResultUpstreamError
	}
}
