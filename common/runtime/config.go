package runtime

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultProbeTimeout      = 3 * time.Second
	defaultRequestTimeout    = 5 * time.Minute
	defaultStreamIdleTimeout = 60 * time.Second
	defaultHealthInterval    = 10 * time.Second
	defaultDiscoveryInterval = 5 * time.Minute
	defaultMaxConcurrent     = 32
)

// forbiddenConfigHeaders are Config.Headers keys a caller must not set,
// because they are connection-scoped (RFC 7230 §6.1) or would let a config
// silently corrupt the request the runtime builds.
var forbiddenConfigHeaders = map[string]bool{
	"Host":                true,
	"Content-Length":      true,
	"Connection":          true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
}

// Normalize returns a copy of c with zero-value duration and concurrency
// fields filled in with their documented defaults, an empty BaseURL of a
// hosted kind set to DefaultBaseURL, and BaseURL's trailing slash trimmed.
// It does not modify c. An explicit negative value on a duration or
// MaxConcurrent is preserved as-is (the documented opt-out of a default),
// and an invalid BaseURL is left unchanged for Validate to reject with a
// clear error rather than normalized away.
//
// Normalize 返回 c 的副本：零值的期限与并发字段填入文档给出的默认值，托管种类
// 为空的 BaseURL 填入 DefaultBaseURL，并去掉 BaseURL 末尾的斜杠。它不修改 c。
// 期限或 MaxConcurrent 上显式的负值原样保留（这是文档约定的关闭默认值方式）；
// 无效的 BaseURL 保持不变，交给 Validate 以清晰的错误拒绝，而不是被规整掉。
func (c Config) Normalize() Config {
	n := c

	if n.ProbeTimeout == 0 {
		n.ProbeTimeout = defaultProbeTimeout
		switch {
		case n.Kind.IsLocalCLI():
			// Cold CLI startup and account discovery need more time than an HTTP probe.
			// CLI 冷启动和账户发现比 HTTP 探测需要更长时间。
			n.ProbeTimeout = 15 * time.Second
		case n.Kind.IsHosted():
			// A hosted API is across the internet, often behind a proxy.
			// 托管 API 位于互联网另一端，往往还经过代理。
			n.ProbeTimeout = 10 * time.Second
		}
	}
	if n.BaseURL == "" {
		n.BaseURL = DefaultBaseURL(n.Kind)
	}
	if n.RequestTimeout == 0 {
		n.RequestTimeout = defaultRequestTimeout
	}
	if n.StreamIdleTimeout == 0 {
		n.StreamIdleTimeout = defaultStreamIdleTimeout
	}
	if n.HealthInterval == 0 {
		n.HealthInterval = defaultHealthInterval
	}
	if n.DiscoveryInterval == 0 {
		n.DiscoveryInterval = defaultDiscoveryInterval
	}
	if n.MaxConcurrent == 0 {
		n.MaxConcurrent = defaultMaxConcurrent
	}

	n.BaseURL = normalizeBaseURL(n.BaseURL)
	return n
}

// normalizeBaseURL trims a trailing slash from the URL's path, preserving
// any configured path prefix otherwise untouched. Scheme, userinfo, query
// and fragment are left as-is: Validate is responsible for rejecting them,
// not Normalize for silently dropping them.
func normalizeBaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String()
}

// Validate checks a Normalize'd Config and reports every problem found
// (not just the first), joined into one *RuntimeError with Code
// ErrorInvalidConfig. Local CLI kinds (Kind.IsLocalCLI) use the CLI's own
// sign-in instead of HTTP.
//
// Validate 校验规范化后的配置，以 ErrorInvalidConfig 聚合报告全部问题。
// 本地 CLI 种类（Kind.IsLocalCLI）使用 CLI 自身登录，不接受 HTTP 地址或凭据。
func (c Config) Validate() error {
	var problems []string

	if strings.TrimSpace(c.ID) == "" {
		problems = append(problems, "id must not be empty")
	}

	switch {
	case c.Kind == KindVLLM, c.Kind == KindSGLang, c.Kind == KindOllama, c.Kind == KindComfyUI,
		c.Kind == KindTRTLLM, c.Kind == KindTokenSpeed:
	case c.Kind.IsLocalCLI(), c.Kind.IsHosted():
	default:
		problems = append(problems, fmt.Sprintf("kind %q is not a registered runtime kind", c.Kind))
	}

	switch {
	case c.Kind.IsLocalCLI():
		if c.BaseURL != "" || c.APIKey != "" || len(c.Headers) != 0 || c.TLS != (TLSConfig{}) {
			problems = append(problems, "CLI runtimes use local authentication; base_url, api_key, headers and TLS must be empty")
		}
	case c.BaseURL == "":
		problems = append(problems, "base_url must not be empty")
	default:
		u, err := url.Parse(c.BaseURL)
		if err != nil {
			problems = append(problems, fmt.Sprintf("base_url is not a valid URL: %v", err))
			break
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			problems = append(problems, fmt.Sprintf("base_url scheme must be http or https, got %q", u.Scheme))
		}
		if u.User != nil {
			problems = append(problems, "base_url must not contain userinfo")
		}
		if u.RawQuery != "" {
			problems = append(problems, "base_url must not contain a query string")
		}
		if u.Fragment != "" {
			problems = append(problems, "base_url must not contain a fragment")
		}
	}

	seenModels := make(map[string]bool, len(c.Models))
	for _, model := range c.Models {
		if strings.TrimSpace(model) == "" || len(model) > 256 || seenModels[model] {
			problems = append(problems, "models entries must be non-empty, at most 256 bytes and unique")
			break
		}
		seenModels[model] = true
	}

	for name := range c.Headers {
		if forbiddenConfigHeaders[http.CanonicalHeaderKey(name)] {
			problems = append(problems, fmt.Sprintf("header %q must not be set via config", name))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return &RuntimeError{
		Code:      ErrorInvalidConfig,
		RuntimeID: c.ID,
		Kind:      c.Kind,
		Operation: "validate_config",
		Message:   strings.Join(problems, "; "),
	}
}

// LogValue implements slog.LogValuer. It reports identity, addressing and
// scheduling fields, and deliberately omits APIKey, Headers values and TLS
// credential paths so a Config can be logged directly without leaking
// secrets.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", c.ID),
		slog.String("kind", string(c.Kind)),
		slog.String("base_url", c.BaseURL),
		slog.Duration("probe_timeout", c.ProbeTimeout),
		slog.Duration("request_timeout", c.RequestTimeout),
		slog.Duration("stream_idle_timeout", c.StreamIdleTimeout),
		slog.Duration("health_interval", c.HealthInterval),
		slog.Duration("discovery_interval", c.DiscoveryInterval),
		slog.Int("max_concurrent", c.MaxConcurrent),
		slog.Bool("exclusive", c.Exclusive),
		slog.Bool("tls_insecure_skip_verify", c.TLS.InsecureSkipVerify),
	)
}
