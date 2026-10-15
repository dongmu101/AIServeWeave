package httpapi

import (
	"net/http"
	"strings"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// writeOutputLimitWarning reports advisory limits only for the backend that actually served the request.
// writeOutputLimitWarning 仅在实际服务请求的后端采用建议性输出上限时给出明确提示。
func writeOutputLimitWarning(w http.ResponseWriter, candidate scheduler.Candidate, parameters ...string) {
	if candidate.Kind != runtime.KindCodex || len(parameters) == 0 {
		return
	}
	w.Header().Set("X-Gateway-Ignored-Parameters", strings.Join(parameters, ","))
	w.Header().Set("Warning", `299 AIServeWeave "Codex compatibility mode: output token limit is not enforced"`)
}
