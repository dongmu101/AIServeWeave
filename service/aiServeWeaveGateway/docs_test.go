package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"AIServeWeave/common/browserlang"
)

// TestDocsLocalizedEntries checks request-local HTML and unchanged raw specification bytes.
// TestDocsLocalizedEntries 检查请求作用域的 HTML 与逐字不变的原始规范。
func TestDocsLocalizedEntries(t *testing.T) {
	mux := http.NewServeMux()
	if err := mountDocs(mux); err != nil {
		t.Fatal(err)
	}
	for _, locale := range browserlang.All() {
		for _, path := range []string{"/docs/", "/docs/index.html"} {
			t.Run(string(locale)+path, func(t *testing.T) {
				r := httptest.NewRequest("GET", path, nil)
				r.Header.Set("Accept-Language", string(locale))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if w.Code != 200 || w.Header().Get("Content-Language") != string(locale) || !strings.Contains(w.Body.String(), `lang="`+string(locale)+`"`) {
					t.Fatalf("status/language=%d/%q, want 200/%q", w.Code, w.Header().Get("Content-Language"), locale)
				}
				r = httptest.NewRequest("GET", "/docs/i18n/"+string(locale)+".json", nil)
				w = httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if w.Code != 200 || !strings.Contains(w.Body.String(), `"ui.authorize"`) {
					t.Errorf("resource status=%d, want 200 with controls", w.Code)
				}
			})
		}
	}
	r := httptest.NewRequest("GET", "/openapi.yaml", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if !bytes.Equal(w.Body.Bytes(), openAPISpec) {
		t.Fatal("raw specification changed")
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/docs", nil))
	if w.Code != 301 || w.Header().Get("Location") != "/docs/" {
		t.Errorf("redirect=%d/%q, want 301 /docs/", w.Code, w.Header().Get("Location"))
	}
}

// TestDocsSavedLocaleAndHead checks precedence without setting any session cookie.
// TestDocsSavedLocaleAndHead 检查已保存偏好优先，且不设置任何会话 Cookie。
func TestDocsSavedLocaleAndHead(t *testing.T) {
	mux := http.NewServeMux()
	if err := mountDocs(mux); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		r := httptest.NewRequest(method, "/docs/", nil)
		r.Header.Set("Accept-Language", "en-US")
		r.AddCookie(&http.Cookie{Name: "aisw_docs_locale", Value: "de-DE"})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if got := w.Header().Get("Content-Language"); got != "de-DE" {
			t.Errorf("language=%q, want de-DE", got)
		}
		if cookies := w.Result().Cookies(); len(cookies) != 0 {
			t.Errorf("cookies=%v, want none for read-only page", cookies)
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Errorf("HEAD bytes=%d, want 0", w.Body.Len())
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/docs/i18n.test.mjs", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("test source status=%d, want 404", w.Code)
	}
}
