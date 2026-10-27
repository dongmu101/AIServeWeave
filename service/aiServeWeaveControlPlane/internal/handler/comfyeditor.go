package handler

import (
	"net/http"
	"strings"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/comfyeditor"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
)

// NotFoundHandler is the server's fallback for every path no route matched.
// It exists because the embedded workflow editor's static release has paths
// of arbitrary depth, which go-zero's router has no wildcard for: requests
// under comfyeditor.Prefix are served there, behind the same platform-session
// guard as every other operator route, and everything else stays the plain
// 404 go-zero would have answered.
//
// NotFoundHandler 是服务器对所有未匹配路由路径的兜底。它之所以存在，是因为嵌入式
// 工作流编辑器的静态发布包路径深度不定，而 go-zero 的路由没有通配：
// comfyeditor.Prefix 之下的请求在这里提供，受与其他每条运维路由相同的平台会话守卫；
// 其余一切仍是 go-zero 本来会回答的普通 404。
func NotFoundHandler(ctx *svc.ServiceContext) http.Handler {
	if ctx.ComfyEditor == nil {
		return http.NotFoundHandler()
	}
	editor := instrumented(ctx.MetricsRegistry, comfyeditor.Prefix+"*", requirePlatformSession(ctx, ctx.ComfyEditor.ServeHTTP))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, comfyeditor.Prefix) {
			editor(w, r)
			return
		}
		http.NotFound(w, r)
	})
}
