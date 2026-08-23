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
// fields filled in with their documented defaults, and BaseURL's trailing
// slash trimmed. It does not modify c. An explicit negative value on a
// duration or MaxConcurrent is preserved as-is (the documented opt-out of a
// default), and an invalid BaseURL is left unchanged for Validate to
// reject with a clear error rather than normalized away.
func (c Config) Normalize() Config {
	n := c

	if n.ProbeTimeout == 0 {
		n.ProbeTimeout = defaultProbeTimeout
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
// ErrorInvalidConfig.
func (c Config) Validate() error {
	var problems []string

	if strings.TrimSpace(c.ID) == "" {
		problems = append(problems, "id must not be empty")
	}

	switch c.Kind {
	case KindVLLM, KindSGLang, KindOllama, KindComfyUI:
	default:
		problems = append(problems, fmt.Sprintf("kind %q is not a registered runtime kind", c.Kind))
	}

	switch {
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
