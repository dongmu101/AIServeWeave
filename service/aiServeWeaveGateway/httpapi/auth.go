package httpapi

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

// apiKeyAuthenticator checks the Authorization header against a static set
// of keys. There is no database in this build, so keys come from
// configuration, not a hashed and stored credential: an operator rotates a
// leaked key by removing it from that configuration and restarting.
type apiKeyAuthenticator struct {
	keys   map[string]struct{}
	logger *slog.Logger
}

func newAPIKeyAuthenticator(keys []string, logger *slog.Logger) *apiKeyAuthenticator {
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			set[k] = struct{}{}
		}
	}
	if len(set) == 0 {
		logger.Warn("no API keys configured; every request is admitted without authentication")
	}
	return &apiKeyAuthenticator{keys: set, logger: logger}
}

// middleware rejects a request that does not carry one of the configured
// keys as a Bearer token. When no keys are configured at all, every request
// is admitted — see newAPIKeyAuthenticator's warning.
func (a *apiKeyAuthenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(a.keys) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		key, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || !a.authorized(key) {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", "missing or invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *apiKeyAuthenticator) authorized(key string) bool {
	// Every configured key is compared, rather than stopping at the first
	// match, so the response time does not leak how many keys exist or
	// where in the set a near-miss landed.
	var ok bool
	for candidate := range a.keys {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			ok = true
		}
	}
	return ok
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return "", false
	}
	return token, true
}
