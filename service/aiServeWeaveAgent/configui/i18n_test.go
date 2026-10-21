package configui

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"AIServeWeave/common/browserlang"
	"AIServeWeave/common/runtime"
)

// TestSaveRemainsDefaultSubmit preserves Enter-to-save independently of locale.
// TestSaveRemainsDefaultSubmit 保证各语言的 Enter 默认动作仍是保存。
func TestSaveRemainsDefaultSubmit(t *testing.T) {
	for _, locale := range browserlang.All() {
		t.Run(string(locale), func(t *testing.T) {
			handler := newIndexHandler(discardLogger(), filepath.Join(t.TempDir(), "agent.yaml"), Sources{})
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Accept-Language", string(locale))
			w := httptest.NewRecorder()
			handler(w, r)
			buttons := regexp.MustCompile(`<button\b[^>]*type="submit"[^>]*>`).FindAllString(w.Body.String(), -1)
			if len(buttons) < 2 {
				t.Fatalf("submit buttons = %v, want save and explicit locale action", buttons)
			}
			if strings.Contains(buttons[0], `value="locale"`) || strings.Contains(buttons[0], "formnovalidate") {
				t.Fatalf("default submit = %q, want validating save action", buttons[0])
			}
		})
	}
}

// TestPageLocaleNegotiation checks request isolation, language headers and HEAD.
// TestPageLocaleNegotiation 检查请求隔离、语言响应头及 HEAD。
func TestPageLocaleNegotiation(t *testing.T) {
	handler := newIndexHandler(discardLogger(), filepath.Join(t.TempDir(), "agent.yaml"), Sources{})
	headings := map[browserlang.Locale]string{"zh-CN": "Agent 配置", "zh-TW": "Agent 設定", "en-US": "Agent configuration", "ja-JP": "Agent の設定", "ko-KR": "Agent 설정", "de-DE": "Agent-Konfiguration", "fr-FR": "Configuration de l’Agent", "es-ES": "Configuración del Agent"}
	for _, locale := range browserlang.All() {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(string(locale)+method, func(t *testing.T) {
				r := httptest.NewRequest(method, "/", nil)
				r.Header.Set("Accept-Language", string(locale))
				w := httptest.NewRecorder()
				handler(w, r)
				if got := w.Header().Get("Content-Language"); got != string(locale) {
					t.Fatalf("Content-Language=%q, want %q", got, locale)
				}
				if method == http.MethodGet && !strings.Contains(w.Body.String(), headings[locale]) {
					t.Errorf("body missing heading %q", headings[locale])
				}
				if method == http.MethodHead && w.Body.Len() != 0 {
					t.Errorf("HEAD bytes=%d, want 0", w.Body.Len())
				}
			})
		}
	}
	var workers sync.WaitGroup
	for _, locale := range []string{"de-DE", "ja-JP"} {
		workers.Go(func() {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Accept-Language", locale)
			w := httptest.NewRecorder()
			handler(w, r)
			if got := w.Header().Get("Content-Language"); got != locale {
				t.Errorf("parallel language=%q, want %q", got, locale)
			}
		})
	}
	workers.Wait()
}

// TestLocalePostRetainsDraftWithoutWritingConfig exercises the no-JavaScript path.
// TestLocalePostRetainsDraftWithoutWritingConfig 验证无 JavaScript 的表单回显路径不写入配置。
func TestLocalePostRetainsDraftWithoutWritingConfig(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agent.yaml")
	before := []byte("gateway:\n  node_id: saved-node\n")
	if err := os.WriteFile(file, before, 0600); err != nil {
		t.Fatal(err)
	}
	handler := newIndexHandler(discardLogger(), file, Sources{})
	values := url.Values{"action": {"locale"}, "locale": {"de-DE"}, "node_id": {"unsaved-原文"}, "max_gateways": {"invalid-draft"}, "runtimes": {"local,codex,"}, "auto_discover": {"on"}}
	r := httptest.NewRequest("POST", "https://localhost/", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler(w, r)
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("config/error=%q/%v, want unchanged %q", after, err, before)
	}
	for _, value := range []string{"unsaved-原文", "invalid-draft", "local,codex,"} {
		if !strings.Contains(w.Body.String(), value) {
			t.Errorf("response missing draft %q", value)
		}
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies=%v, want one preference", cookies)
	}
	c := cookies[0]
	if c.Name != "aisw_agent_locale" || c.Value != "de-DE" || c.Path != "/" || c.MaxAge != 31536000 || c.SameSite != http.SameSiteLaxMode || !c.Secure || c.HttpOnly {
		t.Errorf("cookie=%+v, want secure JS-readable locale preference", c)
	}
}

// TestSaveFailureNeverExposesInternalError checks the localized safe failure.
// TestSaveFailureNeverExposesInternalError 检查保存失败只显示本地化安全文案。
func TestSaveFailureNeverExposesInternalError(t *testing.T) {
	handler := newIndexHandler(discardLogger(), filepath.Join(t.TempDir(), "private-path-marker", "agent.yaml"), Sources{})
	r := httptest.NewRequest("POST", "/", strings.NewReader("node_id=retained-node"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept-Language", "en-US")
	w := httptest.NewRecorder()
	handler(w, r)
	if strings.Contains(w.Body.String(), "private-path-marker") {
		t.Fatal("response exposed internal save path")
	}
	if !strings.Contains(w.Body.String(), "retained-node") {
		t.Fatal("response lost form draft")
	}
}

// TestLocalizedSaveAndLiveFragments checks all languages with real form and snapshot data.
// TestLocalizedSaveAndLiveFragments 使用真实表单与快照样本检查全部语言。
func TestLocalizedSaveAndLiveFragments(t *testing.T) {
	for _, locale := range browserlang.All() {
		t.Run(string(locale), func(t *testing.T) {
			sources := Sources{Gateway: func() GatewaySnapshot {
				return GatewaySnapshot{Enabled: true, Started: true, States: map[string]string{"fixture-gateway:8443": "serving"}}
			}, Runtimes: func() []runtime.Snapshot {
				return []runtime.Snapshot{{Descriptor: runtime.Descriptor{ID: "runtime-stable", Kind: "ollama"}, State: runtime.StateHealthy, Discovery: runtime.Discovery{Models: []runtime.Model{{ID: "model-stable"}}}}}
			}}
			handler := newIndexHandler(discardLogger(), filepath.Join(t.TempDir(), "agent.yaml"), sources)
			request := httptest.NewRequest("POST", "/", strings.NewReader("node_id=stable-node"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Accept-Language", string(locale))
			response := httptest.NewRecorder()
			handler(response, request)
			if !strings.Contains(response.Body.String(), newPageText(locale).Text("a0084")) || !strings.Contains(response.Body.String(), "model-stable") {
				t.Errorf("save response missing localized success or model ID for %s", locale)
			}
			fragment := httptest.NewRecorder()
			request = httptest.NewRequest("GET", "/status/gateway", nil)
			request.AddCookie(&http.Cookie{Name: "aisw_agent_locale", Value: string(locale)})
			newGatewayHandler(sources.Gateway)(fragment, request)
			if got := fragment.Header().Get("Content-Language"); got != string(locale) {
				t.Errorf("fragment language=%q, want %q", got, locale)
			}
			if !strings.Contains(fragment.Body.String(), newPageText(locale).Text("a0089")) || !strings.Contains(fragment.Body.String(), "fixture-gateway:8443") {
				t.Errorf("fragment missing translated state or stable endpoint for %s", locale)
			}
		})
	}
}

// TestStaticLanguageResources checks key/parameter completeness and request-data isolation.
// TestStaticLanguageResources 检查键与参数完整性，以及与请求数据的隔离。
func TestStaticLanguageResources(t *testing.T) {
	parameters := func(text string) []string {
		matches := regexp.MustCompile(`\{([a-zA-Z]\w*)\}`).FindAllString(text, -1)
		slices.Sort(matches)
		return matches
	}
	for _, locale := range browserlang.All() {
		for key, source := range catalogues[browserlang.Default] {
			if got, want := parameters(catalogues[locale][key]), parameters(source); !slices.Equal(got, want) {
				t.Errorf("%s:%s parameters=%v, want %v", locale, key, got, want)
			}
		}
		for _, method := range []string{"GET", "HEAD"} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(method, "/i18n/"+string(locale)+".json", nil)
			serveLanguageResource(w, r)
			if w.Code != 200 {
				t.Errorf("%s %s status=%d, want 200", method, locale, w.Code)
			}
			if method == "HEAD" && w.Body.Len() != 0 {
				t.Errorf("HEAD resource bytes=%d, want 0", w.Body.Len())
			}
			if strings.Contains(w.Body.String(), "stable-node") {
				t.Error("static resource contains request data")
			}
		}
	}
}
