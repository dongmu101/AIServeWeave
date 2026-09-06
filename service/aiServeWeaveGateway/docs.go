package main

import (
	"embed"
	"io/fs"
	"net/http"
)

// openAPISpec is the OpenAPI 3.0 description of every route this binary
// serves, on both the public front door and the operator listener. It is
// embedded rather than read from disk so the binary is self-contained: a
// deployment that copies only the executable still gets working docs.
//
// openAPISpec 是本二进制所提供的每一条路由（公开前门与运维监听器两边）的 OpenAPI 3.0
// 描述。它被嵌入而不是从磁盘读取，好让这个二进制自包含：只拷贝可执行文件的部署，文档
// 依然能用。
//
//go:embed openapi.yaml
var openAPISpec []byte

// docsUI is a vendored Swagger UI build. It is embedded rather than loaded
// from a CDN so /docs works on an air-gapped deployment and never depends on
// a third party being reachable at request time.
//
// docsUI 是随包分发的 Swagger UI 构建产物。它被嵌入而不是从 CDN 加载，好让 /docs 在
// 一个隔离网络的部署上也能用，并且从不依赖请求发生时某个第三方是否可达。
//
//go:embed docsui
var docsUI embed.FS

// mountDocs registers the API documentation routes on mux: GET /openapi.yaml
// serves the raw spec, and GET /docs/ serves a Swagger UI reading it. Neither
// route requires a tenant API key — the spec describes the API, it does not
// call it — so this is called on the top-level mux before front's
// authenticated routes are added, not through httpapi.
//
// mountDocs 在 mux 上注册 API 文档路由：GET /openapi.yaml 提供原始 spec，GET /docs/
// 提供读取它的 Swagger UI。两条路由都不需要租户 API Key——spec 描述的是这个 API，而不是
// 在调用它——因此这是在顶层 mux 上调用的，先于 front 那些需要鉴权的路由被加入，不经过
// httpapi。
func mountDocs(mux *http.ServeMux) error {
	sub, err := fs.Sub(docsUI, "docsui")
	if err != nil {
		return err
	}
	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(openAPISpec)
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs/", http.StatusMovedPermanently)
	})
	mux.Handle("GET /docs/", http.StripPrefix("/docs/", http.FileServer(http.FS(sub))))
	return nil
}
