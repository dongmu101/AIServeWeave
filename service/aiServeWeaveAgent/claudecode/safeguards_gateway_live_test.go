//go:build darwin || linux || windows

package claudecode

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// acceptanceGatewayProxy observes the existing gateway route without changing its request body.
// acceptanceGatewayProxy 只观察既有 Gateway 链路，不改写推理请求或记录凭据。
func acceptanceGatewayProxy(target *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			path := r.In.URL.Path
			base := strings.TrimRight(target.Path, "/")
			if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
				path = strings.TrimPrefix(path, "/v1")
			}
			r.Out.URL.Scheme, r.Out.URL.Host = target.Scheme, target.Host
			r.Out.URL.Path, r.Out.URL.RawPath = base+path, ""
			r.Out.Host = target.Host
		},
		ErrorLog: nil,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "gateway acceptance transport failed", http.StatusBadGateway)
		},
	}
}

// TestLiveSafeguardsGateway uses the caller's existing gateway configuration; subscription auth stays remote.
// TestLiveSafeguardsGateway 使用客户端现有 Gateway 配置；订阅认证仍由远端 Agent 管理。
func TestLiveSafeguardsGateway(t *testing.T) {
	if os.Getenv("AISW_CLAUDE_GATEWAY_TEST") != "1" {
		t.Skip("set AISW_CLAUDE_GATEWAY_TEST=1 to test the configured remote gateway")
	}
	program := os.Getenv("AISW_CLAUDE_PATH")
	if program == "" {
		program = "claude"
	}
	account := os.Getenv("CLAUDE_CONFIG_DIR")
	if account == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal("cannot locate caller settings")
		}
		account = filepath.Join(home, ".claude")
	}
	var settings struct{ Env map[string]json.RawMessage }
	if file, err := os.Open(filepath.Join(account, "settings.json")); err == nil {
		defer file.Close()
		if json.NewDecoder(io.LimitReader(file, 1<<20)).Decode(&settings) != nil {
			t.Fatal("cannot parse caller settings; contents withheld")
		}
	}
	value := func(key string) string {
		if value, ok := os.LookupEnv(key); ok {
			return value
		}
		var value string
		_ = json.Unmarshal(settings.Env[key], &value)
		return value
	}
	base := os.Getenv("AISW_CLAUDE_GATEWAY_URL")
	if base == "" {
		base = value("ANTHROPIC_BASE_URL")
	}
	target, err := url.Parse(base)
	if err != nil || target.Host == "" || (target.Scheme != "https" && target.Scheme != "http") || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		t.Fatal("a valid caller gateway URL is required")
	}
	token, key := value("ANTHROPIC_AUTH_TOKEN"), value("ANTHROPIC_API_KEY")
	if token == "" && key == "" {
		t.Fatal("configure a caller gateway Key in the environment or Claude settings; subscription login is only required on the remote Agent")
	}
	model := os.Getenv("AISW_CLAUDE_MODEL")
	if model == "" {
		model = value("ANTHROPIC_MODEL")
	}
	if model == "" {
		model = "claude-opus-5-5"
	}
	env := probeEnv(os.Environ())
	if token != "" {
		env = append(env, "ANTHROPIC_AUTH_TOKEN="+token)
	}
	if key != "" {
		env = append(env, "ANTHROPIC_API_KEY="+key)
	}
	t.Logf("gateway=%s model=%s; caller credentials withheld", target.Host, model)
	runSafeguardsAcceptance(t, program, model, env, target)
}
