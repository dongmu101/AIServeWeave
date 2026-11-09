package claudecode

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
)

// nativeFixtureEnvironment keeps the CLI's first-party API URL while routing all
// Messages traffic to a TLS loopback server. No real credentials or inference are used.
// nativeFixtureEnvironment 保留 CLI 官方 API 地址，通过 TLS 回环服务测试代理链路，
// 不使用真实凭据或推理服务。
func nativeFixtureEnvironment(t *testing.T, handler http.Handler) []string {
	t.Helper()
	ca, leaf, err := upstreamCertificates()
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewUnstartedServer(handler)
	api.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	api.StartTLS()
	t.Cleanup(api.Close)
	dir := t.TempDir()
	caFile := filepath.Join(dir, "fixture-ca.pem")
	if err := os.WriteFile(caFile, ca, 0600); err != nil {
		t.Fatal(err)
	}
	base := append(withAccountDir(probeEnv(os.Environ()), t.TempDir()), "ANTHROPIC_API_KEY=loopback-fixture-only", "NODE_EXTRA_CA_CERTS="+caFile,
		"DISABLE_TELEMETRY=1", "DISABLE_NON_ESSENTIAL_MODEL_CALLS=1")
	proxy, err := startUpstreamProxy(&upstreamCapture{}, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.close)
	proxy.transport.Proxy = nil
	dial := proxy.transport.DialContext
	proxy.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != upstreamAPIHost+":443" {
			return nil, errors.New("fixture refuses external destination")
		}
		return dial(ctx, network, api.Listener.Addr().String())
	}
	env, err := proxy.environ(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// TestNativeCLIHistorySizeContract exercises growing history using the installed CLI and a loopback API.
// TestNativeCLIHistorySizeContract 用真实 CLI 和回环 API 验证长任务中逐渐增大的历史。
func TestNativeCLIHistorySizeContract(t *testing.T) {
	program := installedContractCLI(t)
	captured := make(chan map[string]json.RawMessage, 1)
	env := nativeFixtureEnvironment(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasSuffix(req.URL.Path, "/messages") {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		var document map[string]json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxRequestBytes)).Decode(&document); err != nil {
			http.Error(w, "invalid document", 400)
			return
		}
		select {
		case captured <- document:
		case <-req.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var events bytes.Buffer
		fixtureRound(&events, "", "history-size-ok")
		for _, line := range strings.Split(events.String(), "\n") {
			if line == "" {
				continue
			}
			var item struct{ Event json.RawMessage }
			_ = json.Unmarshal([]byte(line), &item)
			var kind struct{ Type string }
			_ = json.Unmarshal(item.Event, &kind)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind.Type, item.Event)
		}
	}))
	for _, tc := range []struct {
		size             int
		guarded, escaped bool
	}{
		{size: 128 << 10}, {size: 512 << 10}, {size: 1 << 20}, {size: 2 << 20}, {size: 4 << 20}, {size: 16 << 20}, {size: 31 << 20},
		{size: 4 << 20, guarded: true}, {size: 512 << 10, escaped: true},
	} {
		t.Run(fmt.Sprintf("%d/guarded=%t/escaped=%t", tc.size, tc.guarded, tc.escaped), func(t *testing.T) {
			var logs bytes.Buffer
			rt, err := NewFactory(program)(runtime.Config{ID: "fixture", Kind: runtime.KindClaude}, runtime.Dependencies{
				Clock: runtime.NewSystemClock(), Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			r := rt.(*Runtime)
			defer r.Close()
			r.models = map[string]struct{}{"claude-opus-5-5": {}}
			r.bridge.env = env
			text := strings.Repeat("x", tc.size)
			if tc.escaped {
				text = strings.Repeat("\"\\", tc.size/2)
			}
			document := map[string]any{
				"model": "claude-opus-5-5", "max_tokens": 1024,
				"messages": []any{map[string]string{"role": "user", "content": text}},
			}
			if tc.guarded {
				document["messages"] = []any{map[string]string{"role": "user", "content": "synthetic guarded request"}}
				document["safeguards"] = []any{map[string]any{"type": "dangerous_tool_use", "classifier_context": map[string]any{"opaque": text}}}
			}
			body, _ := json.Marshal(document)
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			stream, err := r.Messages(ctx, runtime.MessagesRequest{Model: "claude-opus-5-5", TenantID: "tenant", KeyID: "key", Native: true, JSON: body})
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for {
				_, err := stream.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("native history %d bytes failed: %v; diagnostics=%s", tc.size, err, &logs)
				}
			}
			select {
			case document := <-captured:
				var expected map[string]json.RawMessage
				_ = json.Unmarshal(body, &expected)
				for key, value := range expected {
					if canonical(value) != canonical(document[key]) {
						t.Fatalf("native request changed field %s at %d bytes", key, tc.size)
					}
				}
			default:
				t.Fatal("CLI never reached the loopback API")
			}
		})
	}
}
