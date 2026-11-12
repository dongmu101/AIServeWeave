package ezfp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/billing"
)

// vectorKey and vectorSignature were produced with OpenSSL, the library
// behind PHP's openssl_sign that the reference SDK calls:
//
//	openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:1024 -out k.pem
//	printf '%s' 'money=1.00&name=VIP会员&out_trade_no=ord_0123&pid=1001&timestamp=1721206072&type=alipay' |
//	  openssl dgst -sha256 -sign k.pem | base64
//
// PKCS #1 v1.5 is deterministic, so matching it proves the sign content and
// the algorithm are what EZFP's own SDK produces.
//
// vectorKey 与 vectorSignature 由 OpenSSL 生成，它是参考 SDK 调用的 PHP openssl_sign
// 背后的库。PKCS #1 v1.5 是确定性的，因此与之一致即证明签名内容与算法与易支付自己的
// SDK 相同。
const (
	vectorKey       = "MIICeAIBADANBgkqhkiG9w0BAQEFAASCAmIwggJeAgEAAoGBANvWU97ie8yAjEvp+dDFyBS6cEEAUw/7Y6LJeIlQv4yyP/3aOTu5oXCyrxVpNQvGp1ZRNzOqg4ZUEhVhXpPEx6OxCG/2aFIzPtNJd4hOMHxhcx6Uo1VY0wO0/LhsXAN+S7MfFMKDdV37NQoIXhPmm1chTSD8N3RWd0gHOL01IcGTAgMBAAECgYEAq/Fv+gVU5PeqQ/oGHaeEPcow4HZbgUWTgZBpCaWxnF8Ss/9KAo/HWHk5fdqi1coTueyd63gzlQ4OctCMg7FGnyqWUHuAWiCH8kOdDCobGcPkXunBeQe9ffk5Sb5N8BUnQK9Ymw6kEv5JP03Ocdrh7Mve/3GljEjEykuixhproTkCQQD8hPrEfSjoyrwpjUrMKyyRv39R8B2YneKK0UXT6qIAcJA1XdOY38Ax/SXGvRgkftUHndRO2aNoTviBzUnzRmrtAkEA3t4HMAvs5qh52mTADTvnjTAHnBM8+o19OZ2YpMe/1mMmhAuX/DvDAFau5cfrldLQkigvA19VsNPoVoNwWsjOfwJBAOKQqbJ8HaVoqRSbCf5PymhmqeSpjNRu7GDizUULIxlD5Y+MRJrIPthbKna7GJs+FQMFcATjLsMa/8rulygoCS0CQQC0Ldyxw3JrqJmbLwj1qhCx3LrKO+N4zRsIkZju328A7HgfGSBvFAG7LFmJ++9C2YU2JLadi13D8A5Oo0UaoG4RAkBTLPzdYk0S8tMah4ggxiKIXe/7RJRRx4Eimw42nxVzGpmb0HFJQd50dXvc519U4jmmON+CDL5i4oEWg+vUrzBQ"
	vectorSignature = "lrFattp+MBFPYe5n4llWataX7pyI/O0a+mBS6sh2dSa4h0eH71DHgP2i9RamSU8rRxLyARMEAIMGx6uSeMJz6tJc/M2PR5mCY+OAi3ckERkZ38vszLOckLvbwyUc4eKtbrinMaLUtNv4O1uaXHH97hD82jQKtVSro2nhVhRWC4g="
)

func TestSignatureMatchesOpenSSL(t *testing.T) {
	key, err := ParsePrivateKey(vectorKey)
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{
		"type": "alipay", "pid": "1001", "out_trade_no": "ord_0123", "money": "1.00", "name": "VIP会员",
		"timestamp": "1721206072", "sign_type": "RSA", "sign": "ignored", "param": "", "clientip": " \t",
	}
	if got := signContent(params); got != "money=1.00&name=VIP会员&out_trade_no=ord_0123&pid=1001&timestamp=1721206072&type=alipay" {
		t.Fatalf("sign content = %q", got)
	}
	got, err := sign(key, params)
	if err != nil || got != vectorSignature {
		t.Fatalf("signature = %q, %v; want the OpenSSL one", got, err)
	}
	// The same key in PEM, wrapped at 64 columns as the SDK wraps it.
	pemKey := "-----BEGIN PRIVATE KEY-----\n"
	for i := 0; i < len(vectorKey); i += 64 {
		pemKey += vectorKey[i:min(i+64, len(vectorKey))] + "\n"
	}
	pemKey += "-----END PRIVATE KEY-----\n"
	if again, err := ParsePrivateKey(pemKey); err != nil || again.N.Cmp(key.N) != 0 {
		t.Fatalf("PEM key: %v", err)
	}
	params["sign"] = vectorSignature
	if !verify(&key.PublicKey, params) {
		t.Fatal("the OpenSSL signature does not verify")
	}
	params["money"] = "1.01"
	if verify(&key.PublicKey, params) {
		t.Fatal("a tampered amount verifies")
	}
}

func TestMoney(t *testing.T) {
	for in, want := range map[string]int64{"1": 100, "1.5": 150, "1.50": 150, "0.01": 1, "12.34": 1234, "0": 0, "007.10": 710} {
		if got, err := ParseMoney(in); err != nil || got != want {
			t.Errorf("ParseMoney(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-1", "+1", "1.234", "1e3", " 1", "1 ", "1.", ".5", "1,00", "１", "99999999999999"} {
		if _, err := ParseMoney(in); err == nil {
			t.Errorf("ParseMoney(%q) accepted", in)
		}
	}
	for in, want := range map[int64]string{1234: "12.34", 5: "0.05", 100: "1.00", 10: "0.10", 0: "0.00"} {
		if got := FormatMoney(in); got != want {
			t.Errorf("FormatMoney(%d) = %q; want %q", in, got, want)
		}
	}
}

func TestTruncateName(t *testing.T) {
	name := strings.Repeat("账", 50) // 150 bytes
	got := truncateName(name)
	if len(got) > maxNameBytes || len(got)%3 != 0 || !strings.HasPrefix(name, got) {
		t.Fatalf("truncated to %d bytes: %q", len(got), got)
	}
	if truncateName("VIP") != "VIP" {
		t.Fatal("a short name changed")
	}
}

// fakeEzfp is an EZFP server: it checks the merchant's signature on every
// request and signs its answers with the platform key.
type fakeEzfp struct {
	t        *testing.T
	merchant *rsa.PublicKey
	platform *rsa.PrivateKey
	now      time.Time

	mu      sync.Mutex
	last    url.Values
	answer  func(path string, form url.Values) map[string]any
	tamper  bool
	unsign  bool
	status  int
	rawBody string
}

func (f *fakeEzfp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	fields := map[string]string{}
	for k := range r.PostForm {
		fields[k] = r.PostForm.Get(k)
	}
	if !verify(f.merchant, fields) {
		f.t.Errorf("the request to %s does not carry a valid merchant signature", r.URL.Path)
	}
	f.mu.Lock()
	f.last = r.PostForm
	answer, tamper, unsign, status, rawBody := f.answer, f.tamper, f.unsign, f.status, f.rawBody
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
	}
	if rawBody != "" {
		_, _ = w.Write([]byte(rawBody))
		return
	}
	body := answer(r.URL.Path, r.PostForm)
	if _, ok := body["timestamp"]; !ok {
		body["timestamp"] = strconv.FormatInt(f.now.Unix(), 10)
	}
	if !unsign {
		flat := map[string]string{}
		raw, _ := json.Marshal(body)
		flat, _ = decodeAnswer(raw)
		signature, err := sign(f.platform, flat)
		if err != nil {
			f.t.Fatal(err)
		}
		body["sign"], body["sign_type"] = signature, "RSA"
	}
	if tamper {
		body["money"] = "0.01"
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeEzfp) set(fn func(*fakeEzfp)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func bare(t *testing.T, der []byte) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(der)
}

func newTestClient(t *testing.T) (*Client, *fakeEzfp, *rsa.PrivateKey) {
	t.Helper()
	merchant, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	merchantDER, _ := x509.MarshalPKCS8PrivateKey(merchant)
	platformDER, _ := x509.MarshalPKIXPublicKey(&platform.PublicKey)
	now := time.Unix(1_800_000_000, 0)
	fake := &fakeEzfp{t: t, merchant: &merchant.PublicKey, platform: platform, now: now}
	fake.answer = func(string, url.Values) map[string]any { return map[string]any{"code": 0} }
	server := httptest.NewTLSServer(fake)
	t.Cleanup(server.Close)
	client, err := New(Config{
		PID: "1001", PrivateKey: bare(t, merchantDER), PublicKey: bare(t, platformDER),
		BaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, fake, platform
}

func createAnswer(path string, form url.Values) map[string]any {
	return map[string]any{
		"code": 0, "msg": "", "trade_no": "2024071600001", "pay_type": "jump",
		"pay_info": "https://pay.example.test/submit?id=2024071600001", "pid": 1001,
	}
}

func TestCreateOrder(t *testing.T) {
	client, fake, _ := newTestClient(t)
	fake.set(func(f *fakeEzfp) { f.answer = createAnswer })
	order, err := client.CreateOrder(context.Background(), billing.EzfpOrderRequest{
		OutTradeNo: "ord_1", PayType: "wxpay", Name: strings.Repeat("账", 50), MoneyMinor: 1234,
		ClientIP: "203.0.113.9", Param: "tnt_1", NotifyURL: "https://cp.example.test/billing/v1/ezfp/notify",
		ReturnURL: "https://console.example.test/console/billing",
	})
	if err != nil || order.TradeNo != "2024071600001" || order.PayURL != "https://pay.example.test/submit?id=2024071600001" {
		t.Fatalf("order = %+v, %v", order, err)
	}
	fake.mu.Lock()
	sent := fake.last
	fake.mu.Unlock()
	for k, want := range map[string]string{
		"pid": "1001", "method": "jump", "type": "wxpay", "out_trade_no": "ord_1", "money": "12.34",
		"clientip": "203.0.113.9", "param": "tnt_1", "sign_type": "RSA", "timestamp": "1800000000",
		"notify_url": "https://cp.example.test/billing/v1/ezfp/notify", "return_url": "https://console.example.test/console/billing",
	} {
		if got := sent.Get(k); got != want {
			t.Errorf("%s = %q; want %q", k, got, want)
		}
	}
	if name := sent.Get("name"); len(name) > maxNameBytes || len(name) == 0 {
		t.Errorf("name is %d bytes", len(name))
	}
}

func TestCreateOrderRefusesWhatDoesNotVerify(t *testing.T) {
	ctx := context.Background()
	req := billing.EzfpOrderRequest{OutTradeNo: "ord_1", PayType: "alipay", Name: "VIP", MoneyMinor: 100, ClientIP: "127.0.0.1"}
	for name, setup := range map[string]func(*fakeEzfp){
		"tampered": func(f *fakeEzfp) { f.answer, f.tamper = createAnswer, true },
		"unsigned": func(f *fakeEzfp) { f.answer, f.unsign = createAnswer, true },
		"stale": func(f *fakeEzfp) {
			f.answer = func(p string, v url.Values) map[string]any {
				a := createAnswer(p, v)
				a["timestamp"] = strconv.FormatInt(f.now.Add(-MaxClockSkew-time.Second).Unix(), 10)
				return a
			}
		},
		"other merchant": func(f *fakeEzfp) {
			f.answer = func(p string, v url.Values) map[string]any { a := createAnswer(p, v); a["pid"] = 1002; return a }
		},
		"http page": func(f *fakeEzfp) {
			f.answer = func(p string, v url.Values) map[string]any {
				a := createAnswer(p, v)
				a["pay_info"] = "http://pay.example.test/x"
				return a
			}
		},
		"qrcode": func(f *fakeEzfp) {
			f.answer = func(p string, v url.Values) map[string]any {
				a := createAnswer(p, v)
				a["pay_type"] = "qrcode"
				return a
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, fake, _ := newTestClient(t)
			fake.set(setup)
			if _, err := client.CreateOrder(ctx, req); !errors.Is(err, ErrUnverified) {
				t.Fatalf("err = %v; want ErrUnverified", err)
			}
		})
	}
	t.Run("refused", func(t *testing.T) {
		client, fake, _ := newTestClient(t)
		fake.set(func(f *fakeEzfp) {
			f.answer = func(string, url.Values) map[string]any {
				return map[string]any{"code": -1, "msg": "商户不存在 <secret>"}
			}
		})
		_, err := client.CreateOrder(ctx, req)
		var refused *billing.EzfpError
		if !errors.As(err, &refused) || refused.Code != -1 || strings.Contains(err.Error(), "secret") {
			t.Fatalf("err = %v", err)
		}
	})
	for name, setup := range map[string]func(*fakeEzfp){
		"5xx":      func(f *fakeEzfp) { f.status = http.StatusBadGateway },
		"not json": func(f *fakeEzfp) { f.rawBody = "<html>busy</html>" },
		"too big":  func(f *fakeEzfp) { f.rawBody = "{\"code\":0,\"x\":\"" + strings.Repeat("a", maxResponseBytes) + "\"}" },
		"redirect": func(f *fakeEzfp) { f.status = http.StatusFound },
	} {
		t.Run(name, func(t *testing.T) {
			client, fake, _ := newTestClient(t)
			fake.set(setup)
			_, err := client.CreateOrder(ctx, req)
			var refused *billing.EzfpError
			if err == nil || errors.As(err, &refused) {
				t.Fatalf("err = %v; want an unknown outcome", err)
			}
		})
	}
}

func TestNewRefusesBadConfig(t *testing.T) {
	_, _, platform := newTestClient(t)
	merchant, _ := rsa.GenerateKey(rand.Reader, 2048)
	mDER, _ := x509.MarshalPKCS8PrivateKey(merchant)
	pDER, _ := x509.MarshalPKIXPublicKey(&platform.PublicKey)
	good := Config{PID: "1001", PrivateKey: bare(t, mDER), PublicKey: bare(t, pDER)}
	if _, err := New(good); err != nil {
		t.Fatalf("good config: %v", err)
	}
	pkcs1 := good
	pkcs1.PrivateKey = bare(t, x509.MarshalPKCS1PrivateKey(merchant))
	if _, err := New(pkcs1); err != nil {
		t.Fatalf("PKCS #1 key: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"http base":   func(c *Config) { c.BaseURL = "http://www.ezfp.cn/" },
		"base query":  func(c *Config) { c.BaseURL = "https://www.ezfp.cn/?x=1" },
		"pid":         func(c *Config) { c.PID = "abc" },
		"no private":  func(c *Config) { c.PrivateKey = "" },
		"bad private": func(c *Config) { c.PrivateKey = "bm90IGEga2V5" },
		"swapped":     func(c *Config) { c.PrivateKey, c.PublicKey = c.PublicKey, c.PrivateKey },
		"bad public":  func(c *Config) { c.PublicKey = "%%%" },
	} {
		cfg := good
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func notifyParams(t *testing.T, platform *rsa.PrivateKey, fields map[string]string) url.Values {
	t.Helper()
	signature, err := sign(platform, fields)
	if err != nil {
		t.Fatal(err)
	}
	v := url.Values{}
	for k, val := range fields {
		v.Set(k, val)
	}
	v.Set("sign", signature)
	v.Set("sign_type", "RSA")
	return v
}

func baseNotify() map[string]string {
	return map[string]string{
		"pid": "1001", "trade_no": "2024071600001", "out_trade_no": "ord_1", "api_trade_no": "4200001",
		"type": "alipay", "trade_status": "TRADE_SUCCESS", "addtime": "2024-07-01 16:47:32",
		"endtime": "2024-07-01 16:49:24", "name": "VIP 会员", "money": "12.34", "param": "tnt_1",
		"buyer": "2088xxxx", "timestamp": "1700000000", "new_field": "added later",
	}
}

func TestParseNotify(t *testing.T) {
	client, _, platform := newTestClient(t)
	// An old timestamp still verifies: settling is idempotent.
	payment, err := client.ParseNotify(notifyParams(t, platform, baseNotify()))
	if err != nil || payment.Status != billing.EzfpStatusPaid || payment.MoneyMinor != 1234 ||
		payment.TradeNo != "2024071600001" || payment.OutTradeNo != "ord_1" || payment.Param != "tnt_1" || payment.Timestamp != 1700000000 {
		t.Fatalf("payment = %+v, %v", payment, err)
	}
	pending := baseNotify()
	pending["trade_status"] = "WAIT"
	if p, err := client.ParseNotify(notifyParams(t, platform, pending)); err != nil || p.Status == billing.EzfpStatusPaid {
		t.Fatalf("a non-success trade reads as paid: %+v, %v", p, err)
	}
	for name, mutate := range map[string]func(url.Values){
		"tampered money": func(v url.Values) { v.Set("money", "1234.00") },
		"dropped field":  func(v url.Values) { v.Del("new_field") },
		"no sign":        func(v url.Values) { v.Del("sign") },
		"sign type":      func(v url.Values) { v.Set("sign_type", "MD5") },
		"repeated":       func(v url.Values) { v.Add("money", "12.34") },
		"bracketed":      func(v url.Values) { v.Set("x[]", "1") },
	} {
		v := notifyParams(t, platform, baseNotify())
		mutate(v)
		if _, err := client.ParseNotify(v); !errors.Is(err, billing.ErrInvalidSignature) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	other := baseNotify()
	other["pid"] = "1002"
	if _, err := client.ParseNotify(notifyParams(t, platform, other)); !errors.Is(err, billing.ErrInvalidSignature) {
		t.Errorf("another merchant's notify: err = %v", err)
	}
	badMoney := baseNotify()
	badMoney["money"] = "1e3"
	if _, err := client.ParseNotify(notifyParams(t, platform, badMoney)); !errors.Is(err, billing.ErrInvalidSignature) {
		t.Errorf("unreadable money: err = %v", err)
	}
}

func TestQueryOrder(t *testing.T) {
	ctx := context.Background()
	client, fake, _ := newTestClient(t)
	fake.set(func(f *fakeEzfp) {
		f.answer = func(_ string, form url.Values) map[string]any {
			return map[string]any{
				"code": 0, "trade_no": "2024071600001", "out_trade_no": form.Get("out_trade_no"), "type": "wxpay",
				"status": 1, "pid": 1001, "money": "12.34", "param": "tnt_1", "refundmoney": nil, "buyer": "",
			}
		}
	})
	p, err := client.QueryOrder(ctx, "ord_1")
	if err != nil || p.Status != billing.EzfpStatusPaid || p.MoneyMinor != 1234 || p.Type != "wxpay" {
		t.Fatalf("payment = %+v, %v", p, err)
	}
	fake.set(func(f *fakeEzfp) {
		f.answer = func(string, url.Values) map[string]any {
			return map[string]any{"code": 0, "trade_no": "T", "out_trade_no": "ord_other", "status": 1, "money": "1.00"}
		}
	})
	if _, err := client.QueryOrder(ctx, "ord_1"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("another order's answer: err = %v", err)
	}
}

func TestRefund(t *testing.T) {
	ctx := context.Background()
	req := billing.EzfpRefundRequest{OutRefundNo: "rfd_1", TradeNo: "2024071600001", MoneyMinor: 500}
	client, fake, _ := newTestClient(t)
	fake.set(func(f *fakeEzfp) {
		f.answer = func(_ string, form url.Values) map[string]any {
			return map[string]any{
				"code": 0, "msg": "退款成功", "refund_no": "R1", "out_refund_no": form.Get("out_refund_no"),
				"trade_no": form.Get("trade_no"), "money": form.Get("money"), "reducemoney": "5.00",
			}
		}
	})
	refund, err := client.Refund(ctx, req)
	if err != nil || refund.RefundNo != "R1" || refund.MoneyMinor != 500 || refund.Status != billing.EzfpRefundSucceeded {
		t.Fatalf("refund = %+v, %v", refund, err)
	}
	if got := fake.last.Get("money"); got != "5.00" {
		t.Fatalf("money sent = %q", got)
	}
	fake.set(func(f *fakeEzfp) {
		f.answer = func(string, url.Values) map[string]any { return map[string]any{"code": 1, "msg": "余额不足"} }
	})
	if _, err := client.Refund(ctx, req); !errors.Is(err, billing.ErrRefundRejected) {
		t.Fatalf("refusal: err = %v", err)
	}
	fake.set(func(f *fakeEzfp) {
		f.answer, f.unsign = func(string, url.Values) map[string]any { return map[string]any{"code": 0, "refund_no": "R1"} }, true
	})
	if _, err := client.Refund(ctx, req); err == nil || errors.Is(err, billing.ErrRefundRejected) {
		t.Fatalf("an unverified success must stay unknown: err = %v", err)
	}
	fake.set(func(f *fakeEzfp) { f.unsign, f.status = false, http.StatusInternalServerError })
	if _, err := client.Refund(ctx, req); err == nil || errors.Is(err, billing.ErrRefundRejected) {
		t.Fatalf("a 5xx must stay unknown: err = %v", err)
	}
}

func TestQueryRefund(t *testing.T) {
	ctx := context.Background()
	client, fake, _ := newTestClient(t)
	for _, status := range []int{billing.EzfpRefundSucceeded, billing.EzfpRefundFailed} {
		fake.set(func(f *fakeEzfp) {
			f.answer = func(_ string, form url.Values) map[string]any {
				return map[string]any{
					"code": 0, "refund_no": "R1", "out_refund_no": form.Get("out_refund_no"), "trade_no": "T",
					"out_trade_no": "ord_1", "money": "5.00", "status": status,
				}
			}
		})
		refund, err := client.QueryRefund(ctx, "rfd_1")
		if err != nil || refund.Status != status || refund.MoneyMinor != 500 {
			t.Fatalf("status %d: refund = %+v, %v", status, refund, err)
		}
	}
	fake.set(func(f *fakeEzfp) {
		f.answer = func(string, url.Values) map[string]any {
			return map[string]any{"code": -1, "msg": "退款单不存在"}
		}
	})
	var refused *billing.EzfpError
	if _, err := client.QueryRefund(ctx, "rfd_1"); !errors.As(err, &refused) {
		t.Fatalf("a missing refund: err = %v", err)
	}
}

func TestDecodeAnswerMatchesPHPStrings(t *testing.T) {
	got, err := decodeAnswer([]byte(`{"code":0,"pid":1001,"money":"1.00","ok":true,"no":false,"nil":null,"list":[1],"obj":{"a":1},"name":"会员"}`))
	if err != nil {
		t.Fatal(err)
	}
	if signContent(got) != "code=0&money=1.00&name=会员&ok=1&pid=1001" {
		t.Fatalf("sign content = %q", signContent(got))
	}
}
