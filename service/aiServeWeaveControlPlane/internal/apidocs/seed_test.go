package apidocs

import (
	"strings"
	"testing"
)

// TestSeedsLoad decodes and validates every compiled-in document, so a
// malformed seed fails here instead of at the control plane's next start.
//
// TestSeedsLoad 解码并校验每份编译进来的文档，使格式有误的种子在这里失败，
// 而不是等到控制面下次启动时才暴露。
func TestSeedsLoad(t *testing.T) {
	seeds, err := Seeds()
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) == 0 {
		t.Fatal("no seeds compiled in")
	}
	routes := map[string]string{}
	for _, s := range seeds {
		if s.Title == "" || s.Category == "" {
			t.Errorf("%s: missing title or category", s.Slug)
		}
		// Two pages may document one route (streamed and non-streamed chat),
		// but each must say which one it is through its title.
		// 两个页面可以描述同一条路由（流式与非流式 Chat），但须以标题区分。
		key := s.Method + " " + s.Path + " " + s.Title
		if other, ok := routes[key]; ok {
			t.Errorf("%s and %s document the same route under the same title", s.Slug, other)
		}
		routes[key] = s.Slug
		for _, ex := range s.Content.RequestExamples {
			if s.Content.Auth == "bearer" && !strings.Contains(ex.Code, "{{API_KEY}}") {
				t.Errorf("%s: request example %q does not use the {{API_KEY}} placeholder", s.Slug, ex.Label)
			}
		}
	}
}
