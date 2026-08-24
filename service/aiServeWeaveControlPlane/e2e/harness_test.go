// Package e2e_test drives the control plane end to end: a real go-zero HTTP
// server, real routing and middleware, real JWT sessions, and the Gateway's
// real verification client on the other side.
//
// The one substitution is the database, which is the in-memory store. That
// keeps the repository's rule that a default `go test ./...` needs no external
// service, while still exercising everything between an HTTP request and the
// store interface — the layers where an authorization mistake would actually
// live. The gorm implementation is verified separately against a real engine.
//
// e2e_test 包端到端地驱动控制面：真实的 go-zero HTTP 服务、真实的路由与中间件、真实的
// JWT 会话，以及另一侧 Gateway 真实的校验客户端。
//
// 唯一被替换的是数据库，换成了内存 store。这既守住了仓库那条「默认的 go test ./... 不
// 需要任何外部服务」的规则，又照样覆盖了从 HTTP 请求到 store 接口之间的全部层次——授权
// 错误真正会藏身的地方。gorm 实现另有针对真实引擎的验证。
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/service"
	"github.com/zeromicro/go-zero/rest"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/config"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/handler"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store/memstore"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/token"
)

// TestMain deliberately does not assert on goroutine counts, unlike every
// other package in this repository. go-zero's runtime starts process-wide
// background goroutines (its log writer, its stat reporter) that outlive any
// individual test by design, so the check would report a leak that is not one.
// The packages that own this service's own concurrency keep the assertion.
//
// 与本仓库其他每个包不同，TestMain 刻意不断言协程数量。go-zero 的运行时会启动进程级的
// 后台协程（它的日志写入器、统计上报器），这些协程按设计就会比任何单个测试活得更久，
// 因此该检查会报出一个并不存在的泄漏。持有本服务自身并发逻辑的那些包保留了这项断言。
func TestMain(m *testing.M) {
	logx.Disable()
	os.Exit(m.Run())
}

const (
	bootstrapToken = "bootstrap-token-that-is-long-enough-for-validation"
	internalToken  = "internal-token-that-is-long-enough-for-validation"
	accessSecret   = "access-secret-that-is-long-enough-for-validation"
	ownerPassword  = "correct-horse-battery"
)

// harness is one running control plane and the client calls a test makes
// against it.
//
// harness 是一个正在运行的控制面，以及测试对它发起的各种客户端调用。
type harness struct {
	t      *testing.T
	base   string
	store  *memstore.Store
	server *rest.Server
}

// newHarness starts a control plane on a free port and tears it down with the
// test.
//
// newHarness 在一个空闲端口上启动控制面，并随测试一并拆除。
func newHarness(t *testing.T) *harness {
	t.Helper()

	port := freePort(t)
	cfg := config.Config{
		RestConf: rest.RestConf{
			ServiceConf: service.ServiceConf{
				Name: "controlplane-e2e",
				Log:  logx.LogConf{Mode: "console", Level: "severe"},
			},
			Host: "127.0.0.1",
			Port: port,
		},
		Auth:           config.AuthConf{AccessSecret: accessSecret, AccessExpire: time.Hour},
		InternalToken:  internalToken,
		BootstrapToken: bootstrapToken,
	}

	st := memstore.New()
	issuer, err := token.NewIssuer(accessSecret, time.Hour, runtime.NewSystemClock())
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	// The ServiceContext is built field by field rather than through
	// NewServiceContext, which would dial a database. Everything the handlers
	// touch is real; only what sits behind the store interface is not.
	//
	// ServiceContext 是逐字段构造的，而不是走 NewServiceContext——后者会去连数据库。
	// handler 触及的一切都是真实的；只有 store 接口背后那部分不是。
	svcCtx := &svc.ServiceContext{
		Config: cfg,
		Logic:  logic.New(st, runtime.NewSystemClock()),
		Issuer: issuer,
	}

	server, err := rest.NewServer(cfg.RestConf)
	if err != nil {
		t.Fatalf("rest.NewServer: %v", err)
	}
	handler.RegisterHandlers(server, svcCtx)

	go server.Start()
	t.Cleanup(server.Stop)

	h := &harness{t: t, base: fmt.Sprintf("http://127.0.0.1:%d", port), store: st, server: server}
	h.waitReady()
	return h
}

// freePort asks the kernel for an unused port and gives it back, which is the
// least bad way to pick one: go-zero's server takes a port number rather than
// a listener, so it cannot be handed a bound socket.
//
// freePort 向内核要一个未被占用的端口再还回去，这是挑端口的诸多办法里最不糟的一个：
// go-zero 的 server 接收的是端口号而不是 listener，因此没法把一个已绑定的 socket 交给它。
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return port
}

// waitReady blocks until the server accepts connections.
//
// waitReady 阻塞直到服务开始接受连接。
func (h *harness) waitReady() {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", h.base[len("http://"):], 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("the control plane did not start listening: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// call performs one HTTP request and decodes the response into out when out is
// not nil. It returns the status code.
//
// call 发起一次 HTTP 请求，out 非 nil 时把响应解码进去。它返回状态码。
func (h *harness) call(method, path, bearer string, body, out any) int {
	h.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("encoding the request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, h.base+path, reader)
	if err != nil {
		h.t.Fatalf("building the request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			h.t.Fatalf("decoding the response of %s %s: %v", method, path, err)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode
}
