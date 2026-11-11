package claudecode

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
)

func TestNativeSettingsSizeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		relay      bool
	}{
		{"small", strings.Repeat("x", 128<<10), false},
		{"large history", strings.Repeat("x", 2<<20), true},
		{"escaped tool output", strings.Repeat("\"\\", 300<<10), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{"stream": true, "model": "fixture", "messages": []any{map[string]string{"role": "user", "content": tc.text}}, "betas": []string{"feature-a"}}
			settings, request, err := nativeInputSettings(extra)
			if err != nil || len(settings) > maxCLISettingsBytes || (request != nil) != tc.relay {
				t.Fatalf("settings/request/error = %d/%t/%v", len(settings), request != nil, err)
			}
			var parsed struct{ Env map[string]string }
			if json.Unmarshal(settings, &parsed) != nil {
				t.Fatal("invalid settings JSON")
			}
			var body map[string]json.RawMessage
			if json.Unmarshal([]byte(parsed.Env["CLAUDE_CODE_EXTRA_BODY"]), &body) != nil || canonical(body["betas"]) != canonical(extra["betas"]) {
				t.Fatal("lost protocol selectors")
			}
			if tc.relay {
				if _, exists := body["messages"]; exists {
					t.Fatal("large history still entered settings")
				}
				// Unmarshal into a fresh map: absent fields must not survive a previous decode.
				body = nil
				if json.Unmarshal(request, &body) != nil {
					t.Fatal("invalid relay document")
				}
				if _, exists := body["betas"]; exists {
					t.Fatal("SDK header selector entered API JSON")
				}
			}
			if canonical(body["messages"]) != canonical(extra["messages"]) {
				t.Fatal("native history changed")
			}
		})
	}
}

func TestLargeNativeProxyForwarding(t *testing.T) {
	for _, kind := range []string{"stream", "gzip", "gzip-case", "side-call"} {
		t.Run(kind, func(t *testing.T) {
			type received struct {
				body   []byte
				header http.Header
			}
			seen := make(chan received, 1)
			env := nativeFixtureEnvironment(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes))
				seen <- received{body, r.Header.Clone()}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			original := map[string]any{"model": "fixture", "stream": true, "messages": []any{map[string]string{"role": "user", "content": strings.Repeat("history", 400<<10)}}}
			_, payload, err := nativeInputSettings(original)
			if err != nil || payload == nil {
				t.Fatal("expected a large native request")
			}
			proxy, err := startUpstreamProxy(&upstreamCapture{}, env, payload)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(proxy.close)
			roots := x509.NewCertPool()
			roots.AppendCertsFromPEM(proxy.caPEM)
			endpoint, _ := url.Parse(proxy.proxyURL)
			transport := &http.Transport{Proxy: http.ProxyURL(endpoint), TLSClientConfig: &tls.Config{RootCAs: roots}}
			t.Cleanup(transport.CloseIdleConnections)
			seed := map[string]any{"stream": kind != "side-call", "messages": []any{map[string]string{"role": "user", "content": "small seed"}}, "metadata": map[string]bool{"generated": true}}
			data, _ := json.Marshal(seed)
			if strings.HasPrefix(kind, "gzip") {
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				_, _ = writer.Write(data)
				_ = writer.Close()
				data = compressed.Bytes()
			}
			req, _ := http.NewRequestWithContext(t.Context(), "POST", "https://"+upstreamAPIHost+"/v1/messages", bytes.NewReader(data))
			req.Header.Set("Authorization", "Bearer fixture-only")
			req.Header.Set("Anthropic-Beta", "feature-a")
			if strings.HasPrefix(kind, "gzip") {
				req.Header.Set("Content-Encoding", "gzip")
				if kind == "gzip-case" {
					req.Header.Set("Content-Encoding", "GZip")
				}
			}
			response, err := (&http.Client{Transport: transport}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("relay returned %d", response.StatusCode)
			}
			actual := <-seen
			var body map[string]json.RawMessage
			_ = json.Unmarshal(actual.body, &body)
			want := original
			if kind == "side-call" {
				want = seed
			}
			for key, value := range want {
				if canonical(body[key]) != canonical(value) {
					t.Fatalf("changed field %s", key)
				}
			}
			if canonical(body["metadata"]) != canonical(seed["metadata"]) || actual.header.Get("Authorization") != req.Header.Get("Authorization") || actual.header.Get("Anthropic-Beta") != "feature-a" || actual.header.Get("Content-Encoding") != "" {
				t.Fatal("relay changed authentication/selectors/metadata or retained stale compression")
			}
		})
	}
}

func TestLocalCLIFailureIsBackendFailure(t *testing.T) {
	r := &Runtime{cfg: runtime.Config{ID: "fixture"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, cause := range []error{errMissingResult, errCLIExit, errCLIProtocol, errCLIResult, errUnsafeTools, errCLIStart, errProbeSetup} {
		var failure *runtime.RuntimeError
		if !errors.As(r.roundError(cause), &failure) || failure.Code != runtime.ErrorUpstream || failure.RuntimeID != "fixture" {
			t.Fatalf("local CLI failure incorrectly reported as client error: %+v", failure)
		}
	}
}
