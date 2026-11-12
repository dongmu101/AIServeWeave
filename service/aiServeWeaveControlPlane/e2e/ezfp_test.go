package e2e_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing/ezfp"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/logic"
)

// epaySign is written here independently of billing/ezfp, from the
// published rule, so the test checks the client against the protocol rather
// than against itself.
func epaySign(t *testing.T, key *rsa.PrivateKey, params map[string]string) string {
	t.Helper()
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k != "sign" && k != "sign_type" && strings.TrimSpace(v) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + params[k]
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "&")))
	raw, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func epayVerify(key *rsa.PublicKey, params map[string]string) bool {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k != "sign" && k != "sign_type" && strings.TrimSpace(v) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + params[k]
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "&")))
	raw, err := base64.StdEncoding.DecodeString(params["sign"])
	return err == nil && rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], raw) == nil
}

// epayServer is an 易支付 stand-in that checks the merchant signature on
// every call and signs its answers with the platform key.
type epayServer struct {
	t        *testing.T
	merchant *rsa.PublicKey
	platform *rsa.PrivateKey
	mu       sync.Mutex
	creates  []map[string]string
	refunds  []map[string]string
}

func (e *epayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	form := map[string]string{}
	for k := range r.PostForm {
		form[k] = r.PostForm.Get(k)
	}
	if !epayVerify(e.merchant, form) || form["pid"] != "1001" {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": -1, "msg": "签名错误"})
		return
	}
	answer := map[string]string{"code": "0", "pid": "1001", "timestamp": strconv.FormatInt(time.Now().Unix(), 10)}
	e.mu.Lock()
	switch r.URL.Path {
	case "/api/pay/create":
		e.creates = append(e.creates, form)
		answer["trade_no"], answer["pay_type"] = "T"+form["out_trade_no"], "jump"
		answer["pay_info"] = "https://pay.example.test/submit/" + form["out_trade_no"]
	case "/api/pay/refund":
		e.refunds = append(e.refunds, form)
		answer["refund_no"], answer["out_refund_no"], answer["trade_no"], answer["money"] =
			"R"+form["out_refund_no"], form["out_refund_no"], form["trade_no"], form["money"]
	default:
		answer = map[string]string{"code": "-1"}
	}
	e.mu.Unlock()
	if answer["code"] == "0" {
		answer["sign"], answer["sign_type"] = epaySign(e.t, e.platform, answer), "RSA"
	}
	out := map[string]any{}
	for k, v := range answer {
		out[k] = v
	}
	out["code"], _ = strconv.Atoi(answer["code"])
	_ = json.NewEncoder(w).Encode(out)
}

// TestEzfpOnlyDeploymentEndToEnd runs a CNY package through the real 易支付
// client: the operator lists a package without a Dodo product, the tenant
// checks out with Alipay, a signed GET notification credits it once, and an
// operator refund takes it back.
func TestEzfpOnlyDeploymentEndToEnd(t *testing.T) {
	merchant, _ := rsa.GenerateKey(rand.Reader, 2048)
	platform, _ := rsa.GenerateKey(rand.Reader, 2048)
	upstream := &epayServer{t: t, merchant: &merchant.PublicKey, platform: platform}
	server := httptest.NewTLSServer(upstream)
	t.Cleanup(server.Close)
	merchantDER, _ := x509.MarshalPKCS8PrivateKey(merchant)
	platformDER, _ := x509.MarshalPKIXPublicKey(&platform.PublicKey)
	client, err := ezfp.New(ezfp.Config{
		PID: "1001", PrivateKey: base64.StdEncoding.EncodeToString(merchantDER), PublicKey: base64.StdEncoding.EncodeToString(platformDER),
		BaseURL: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := buildHarness(t, harnessOpts{ezfp: &logic.EzfpConfig{
		Client: client, Types: []string{"alipay", "wxpay"},
		NotifyURL: "https://cp.example.test/billing/v1/ezfp/notify", ReturnURL: "https://console.example.test/console/billing",
	}})
	operator := bootstrapPlatformOperator(h, "ezfp-operator@example.com")
	created, owner := bootstrap(h, "易支付商户", "ezfp-owner@example.com")

	// Dodo is not configured: its webhook and portal do not exist.
	if got := h.call(http.MethodPost, "/billing/v1/dodo/webhook", "", nil, nil); got != http.StatusNotFound {
		t.Fatalf("Dodo webhook: %d", got)
	}
	if got := h.call(http.MethodPost, "/admin/v1/billing/portal", owner, nil, nil); got != http.StatusNotFound {
		t.Fatalf("Dodo portal: %d", got)
	}
	var pkg struct {
		ID string `json:"id"`
	}
	if got := h.call(http.MethodPost, "/operator/v1/billing/packages", operator, map[string]any{
		"name": "100 点", "kind": "fixed", "dodo_product_id": "", "credits_micro": 100_000_000, "price_minor": 6800, "currency": "CNY", "active": true,
	}, &pkg); got != http.StatusCreated {
		t.Fatalf("create CNY package: %d", got)
	}
	if got := h.call(http.MethodPut, "/operator/v1/billing/tenants/"+created.Tenant.ID+"/mode", operator, map[string]string{"mode": "prepaid"}, nil); got != http.StatusNoContent {
		t.Fatalf("set prepaid: %d", got)
	}
	var overview struct {
		BalanceMicro   int64 `json:"balance_micro"`
		PaymentMethods struct {
			Dodo      bool     `json:"dodo"`
			Ezfp      bool     `json:"ezfp"`
			EzfpTypes []string `json:"ezfp_types"`
		} `json:"payment_methods"`
		Packages []map[string]any `json:"packages"`
	}
	if got := h.call(http.MethodGet, "/admin/v1/billing", owner, nil, &overview); got != http.StatusOK || overview.PaymentMethods.Dodo ||
		!overview.PaymentMethods.Ezfp || len(overview.PaymentMethods.EzfpTypes) != 2 || len(overview.Packages) != 1 ||
		overview.Packages[0]["ezfp_available"] != true || overview.Packages[0]["dodo_available"] != false {
		t.Fatalf("overview: %d %+v", got, overview)
	}

	var checkout struct {
		OrderID     string `json:"order_id"`
		CheckoutURL string `json:"checkout_url"`
	}
	if got := h.call(http.MethodPost, "/admin/v1/billing/checkout", owner, map[string]string{
		"package_id": pkg.ID, "provider": "ezfp", "pay_type": "alipay",
	}, &checkout); got != http.StatusCreated || checkout.CheckoutURL != "https://pay.example.test/submit/"+checkout.OrderID {
		t.Fatalf("checkout: %d %+v", got, checkout)
	}
	sent := upstream.creates[0]
	if sent["method"] != "jump" || sent["type"] != "alipay" || sent["money"] != "68.00" || sent["out_trade_no"] != checkout.OrderID ||
		sent["param"] != created.Tenant.ID || sent["clientip"] == "" || sent["notify_url"] != "https://cp.example.test/billing/v1/ezfp/notify" {
		t.Fatalf("create request: %+v", sent)
	}

	notify := func(fields map[string]string, sign bool) (int, string) {
		t.Helper()
		if sign {
			fields["sign"], fields["sign_type"] = epaySign(t, platform, fields), "RSA"
		}
		query := url.Values{}
		for k, v := range fields {
			query.Set(k, v)
		}
		resp, err := http.Get(h.base + "/billing/v1/ezfp/notify?" + query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	paid := func() map[string]string {
		return map[string]string{
			"pid": "1001", "trade_no": "T" + checkout.OrderID, "out_trade_no": checkout.OrderID, "api_trade_no": "2088",
			"type": "alipay", "trade_status": "TRADE_SUCCESS", "name": "100 点", "money": "68.00",
			"param": created.Tenant.ID, "buyer": "2088***", "timestamp": strconv.FormatInt(time.Now().Unix(), 10),
		}
	}
	forged := paid()
	forged["sign"], forged["sign_type"] = epaySign(t, merchant, forged), "RSA"
	if code, body := notify(forged, false); code != http.StatusUnauthorized || body != "fail" {
		t.Fatalf("forged notify: %d %q", code, body)
	}
	if code, body := notify(paid(), true); code != http.StatusOK || body != "success" {
		t.Fatalf("notify: %d %q", code, body)
	}
	if code, body := notify(paid(), true); code != http.StatusOK || body != "success" {
		t.Fatalf("replayed notify: %d %q", code, body)
	}
	if got := h.call(http.MethodGet, "/admin/v1/billing", owner, nil, &overview); got != http.StatusOK || overview.BalanceMicro != 100_000_000 {
		t.Fatalf("balance after payment: %d %d", got, overview.BalanceMicro)
	}

	var refund struct {
		Status string `json:"status"`
	}
	if got := h.call(http.MethodPost, "/operator/v1/billing/orders/"+checkout.OrderID+"/refund", operator,
		map[string]string{"reason": "测试退款"}, &refund); got != http.StatusCreated || refund.Status != "succeeded" {
		t.Fatalf("refund: %d %+v", got, refund)
	}
	if r := upstream.refunds[0]; r["trade_no"] != "T"+checkout.OrderID || r["money"] != "68.00" || !strings.HasPrefix(r["out_refund_no"], "rfd_") {
		t.Fatalf("refund request: %+v", r)
	}
	if got := h.call(http.MethodGet, "/admin/v1/billing", owner, nil, &overview); got != http.StatusOK || overview.BalanceMicro != 0 {
		t.Fatalf("balance after refund: %d %d", got, overview.BalanceMicro)
	}
	var orders struct {
		Items []map[string]any `json:"items"`
	}
	if got := h.call(http.MethodGet, "/admin/v1/billing/orders", owner, nil, &orders); got != http.StatusOK || len(orders.Items) != 1 ||
		orders.Items[0]["status"] != "refunded" || orders.Items[0]["provider"] != "ezfp" || orders.Items[0]["pay_type"] != "alipay" ||
		orders.Items[0]["payment_id"] != "T"+checkout.OrderID {
		t.Fatalf("orders: %d %+v", got, orders.Items)
	}
}
