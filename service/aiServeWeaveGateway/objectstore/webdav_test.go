package objectstore_test

import (
	"net/http/httptest"
	"testing"

	"golang.org/x/net/webdav"

	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
)

// newWebDAVBackend serves objectstore.WebDAV's tests against a real WebDAV
// server rather than a hand-rolled fake: golang.org/x/net/webdav implements
// the protocol (PROPFIND, MKCOL, the lot), and it is already present in this
// module's dependency graph as an indirect dependency of grpc, so using it
// here in test-only code adds nothing to any service's shipped binary — it
// is a much better test double than reimplementing WebDAV's XML wire format
// by hand.
//
// newWebDAVBackend 让 objectstore.WebDAV 的测试跑在一个真实的 WebDAV 服务端
// 上，而不是手搓一个假服务端：golang.org/x/net/webdav 实现了这个协议
// （PROPFIND、MKCOL 等等），而且它已经作为 grpc 的间接依赖存在于本模块的依赖图
// 中，因此只在测试代码里用它，不会给任何服务的发布产物增加任何东西——它比手写
// WebDAV 的 XML 线格式要好得多的测试替身。
func newWebDAVBackend(t *testing.T) objectstore.Backend {
	t.Helper()
	srv := httptest.NewServer(&webdav.Handler{
		FileSystem: webdav.NewMemFS(),
		LockSystem: webdav.NewMemLS(),
	})
	t.Cleanup(srv.Close)

	b, err := objectstore.NewWebDAV(objectstore.WebDAVConfig{URL: srv.URL, Dir: "/artifacts"})
	if err != nil {
		t.Fatalf("NewWebDAV: %v", err)
	}
	return b
}

func TestNewWebDAVRejectsEmptyURL(t *testing.T) {
	if _, err := objectstore.NewWebDAV(objectstore.WebDAVConfig{}); err == nil {
		t.Fatal("NewWebDAV with empty URL: want error, got nil")
	}
}
