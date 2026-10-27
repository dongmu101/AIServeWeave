package browserlang

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

func TestResolve(t *testing.T) {
	var cases []struct{ Name, Saved, Accept, Want string }
	b, err := os.ReadFile("testdata/negotiation.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			if got := Resolve(tc.Saved, tc.Accept); string(got) != tc.Want {
				t.Errorf("Resolve = %q, want %q", got, tc.Want)
			}
		})
	}
}

func TestPreferenceCookie(t *testing.T) {
	for _, name := range []string{"aisw_console_locale", "aisw_agent_locale", "aisw_docs_locale"} {
		t.Run(name, func(t *testing.T) {
			c := PreferenceCookie(name, Locale("de-DE"), true)
			if c.Name != name || c.Value != "de-DE" || c.Path != "/" || c.MaxAge != 31536000 || c.SameSite != http.SameSiteLaxMode || !c.Secure || c.HttpOnly {
				t.Errorf("cookie = %#v, want name %q, de-DE, /, one year, Lax, Secure, JS-readable", c, name)
			}
		})
	}
	if _, ok := Parse("../../de-DE"); ok {
		t.Fatal("Parse accepted a path, want rejection")
	}
	if got := PreferenceCookie("aisw_docs_locale", Locale("invalid"), false).Value; got != "zh-CN" {
		t.Errorf("invalid locale cookie = %q, want zh-CN", got)
	}
}
