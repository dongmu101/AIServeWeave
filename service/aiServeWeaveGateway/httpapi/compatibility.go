package httpapi

import (
	"net/http"
	"strings"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// advisoryOutputLimit names the backends whose output limit is accepted but
// not enforced: headless CLIs that expose no such option.
// advisoryOutputLimit 列出输出上限只被接收、不被执行的后端：不提供该选项的
// headless CLI。
var advisoryOutputLimit = map[runtime.Kind]string{
	runtime.KindCodex:     "Codex",
	runtime.KindGrok:      "Grok",
	runtime.KindOpenCode:  "OpenCode",
	runtime.KindCursor:    "Cursor",
	runtime.KindCodeBuddy: "CodeBuddy",
	runtime.KindQoder:     "Qoder",
	runtime.KindPi:        "Pi",
}

// writeOutputLimitWarning reports advisory limits only for the backend that actually served the request.
// writeOutputLimitWarning 仅在实际服务请求的后端采用建议性输出上限时给出明确提示。
func writeOutputLimitWarning(w http.ResponseWriter, candidate scheduler.Candidate, parameters ...string) {
	backend := advisoryOutputLimit[candidate.Kind]
	if backend == "" || len(parameters) == 0 {
		return
	}
	w.Header().Set("X-Gateway-Ignored-Parameters", strings.Join(parameters, ","))
	w.Header().Set("Warning", `299 AIServeWeave "`+backend+` compatibility mode: output token limit is not enforced"`)
}
