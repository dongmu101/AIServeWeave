package httpapi_test

import (
	"AIServeWeave/common/accountaccess"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExpiredAccountSubscriptionRefusesNewWorkButKeepsReads(t *testing.T) {
	verifier := &fakeVerifier{answer: func(string) (httpapi.Identity, error) {
		return httpapi.Identity{TenantID: "buyer", KeyID: "key", Accounts: &accountaccess.Policy{Only: true, Managed: []accountaccess.Lease{{NodeID: "agent", RuntimeID: "mine", ExpiresAt: time.Unix(0, 0)}}}}, nil
	}}
	srv, _ := newServer(t, httpapi.Config{Verifier: verifier})
	for _, test := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/v1/chat/completions", 402},
		{http.MethodPost, "/v1/messages", 402},
		{http.MethodGet, "/v1/models", 200},
	} {
		r, err := http.NewRequest(test.method, srv.URL+test.path, strings.NewReader(`{"model":"model","messages":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+goodKey)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != test.want {
			t.Fatalf("%s %s: %d %s", test.method, test.path, resp.StatusCode, body)
		}
		if test.want == 402 && !strings.Contains(string(body), "account_subscription_expired") {
			t.Fatal(string(body))
		}
	}
}
