package opcpay_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing/opcpay"
)

const testKey = "opk_test_secret_value"

func newServer(t *testing.T, handler func(w http.ResponseWriter, path string, body map[string]any)) *opcpay.Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("authorization header missing")
		}
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Errorf("request body is not JSON: %v", err)
				}
			}
		}
		handler(w, strings.TrimPrefix(r.URL.Path, "/opcpay/api/pay/v1"), body)
	}))
	t.Cleanup(srv.Close)
	client, err := opcpay.New(testKey, srv.URL+"/opcpay/api/pay/v1", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestNewRefusesUnsafeConfiguration(t *testing.T) {
	if _, err := opcpay.New("", "", nil); err == nil || !strings.Contains(err.Error(), opcpay.APIKeyEnv) {
		t.Fatal("empty key", err)
	}
	for _, base := range []string{"http://ai6666.com/opcpay", "https://user:pw@ai6666.com/x", "not a url"} {
		if _, err := opcpay.New(testKey, base, nil); err == nil {
			t.Fatal("accepted", base)
		}
	}
	if _, err := opcpay.New(testKey, "", nil); err != nil {
		t.Fatal("default base", err)
	}
}

func TestProductAndAccessDecodeTheContract(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, path string, body map[string]any) {
		switch path {
		case "/product":
			_, _ = io.WriteString(w, `{"capability_status":"ENABLED","product_name":"点数","billing_mode":"QUANTITY","quota_unit":"POINT",
				"charging_options":[{"sku_id":"s1","plan_id":"p","display_name":"100 积分","billing_mode":"QUANTITY","price":"9.90","currency":"CNY",
				"price_version":3,"entitlement_rule":"QUOTA","quota_unit":"POINT","quota_amount":100,"future_field":{"x":1}}]}`)
		case "/access":
			if body["user_id"] != "tnt_1" || body["amount"] != float64(5) {
				t.Errorf("access body %v", body)
			}
			_, _ = io.WriteString(w, `{"access_granted":true,"decision":"ALLOW","quota_unit":"POINT","total":"120","used":20,"remaining":100}`)
		}
	})
	p, err := client.Product(context.Background())
	if err != nil || !p.Quantity() || len(p.Options) != 1 || p.Options[0].Price != "9.90" || p.Options[0].QuotaAmount != 100 {
		t.Fatalf("%+v %v", p, err)
	}
	a, err := client.Access(context.Background(), "tnt_1", 5)
	if err != nil || a.Total != 120 || a.Remaining != 100 || a.Decision != billing.AlipayDecisionAllow {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestConsumeSendsTheExactRequest(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, path string, body map[string]any) {
		if path != "/consume" {
			t.Errorf("path %s", path)
		}
		want := map[string]any{"user_id": "tnt_1", "idempotency_key": "apr_1", "amount": float64(7), "reason": "r"}
		for k, v := range want {
			if body[k] != v {
				t.Errorf("%s = %v, want %v", k, body[k], v)
			}
		}
		if len(body) != len(want) {
			t.Errorf("extra fields: %v", body)
		}
		_, _ = io.WriteString(w, `{"status":"SUCCEEDED","confirmed":true,"idempotency_key":"apr_1","quota_unit":"POINT","consumed":7,"consume_order_id":"co"}`)
	})
	got, err := client.Consume(context.Background(), billing.AlipayConsumeRequest{UserID: "tnt_1", IdempotencyKey: "apr_1", Amount: 7, Reason: "r"})
	if err != nil || got.Status != billing.AlipayConsumeSucceeded || !got.Confirmed || got.Consumed != 7 || got.ConsumeOrderID != "co" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestErrorsCarryTheCodeButNeverTheKeyOrMessage(t *testing.T) {
	client := newServer(t, func(w http.ResponseWriter, path string, _ map[string]any) {
		switch path {
		case "/consumption":
			w.Header().Set("X-Request-ID", "req_9")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"secret upstream text","error_code":"NOT_FOUND","request_id":"req_9","retryable":false}`)
		case "/purchase":
			http.Redirect(w, &http.Request{}, "https://elsewhere.example/steal", http.StatusFound)
		default:
			_, _ = io.WriteString(w, `not json`)
		}
	})
	_, err := client.Consumption(context.Background(), "tnt_1", "apr_1")
	var apiErr *billing.AlipayError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 || apiErr.Code != "NOT_FOUND" || apiErr.RequestID != "req_9" {
		t.Fatalf("%#v", err)
	}
	if strings.Contains(err.Error(), "secret upstream text") || strings.Contains(err.Error(), testKey) {
		t.Fatal("error leaks", err)
	}
	if _, err := client.Purchase(context.Background(), billing.AlipayPurchaseRequest{UserID: "u", IdempotencyKey: "k"}); err == nil {
		t.Fatal("a redirect was followed or accepted")
	} else if code, status := billing.AlipayErrorCode(err); status != http.StatusFound || code != "HTTP_302" {
		t.Fatalf("redirect answer: %v", err)
	}
	if _, err := client.Capabilities(context.Background()); err == nil {
		t.Fatal("invalid JSON accepted")
	} else if code, _ := billing.AlipayErrorCode(err); code != "INVALID_RESPONSE" {
		t.Fatal(err)
	}
}
