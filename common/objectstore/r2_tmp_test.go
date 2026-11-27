package objectstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func closeIdle(t *testing.T) {
	t.Cleanup(func() {
		for _, allow := range []bool{false, true} {
			sharedGuardedClient(allow).CloseIdleConnections()
		}
	})
}

const r2Endpoint = "https://7b6f95a6aeccb6dd19daca20f47214c1.r2.cloudflarestorage.com"

func r2Config(keyID, secret string, pathStyle bool) Config {
	return Config{Kind: "s3", S3: S3Config{Endpoint: r2Endpoint, Region: "auto", Bucket: "open7x",
		UsePathStyle: pathStyle, AccessKeyID: keyID, SecretAccessKey: secret}}
}

// Without real keys: the endpoint passes the guard, resolves, speaks TLS,
// and a refused signature is classified as ErrAuth.
func TestTmpR2WithFakeKeys(t *testing.T) {
	closeIdle(t)
	if err := CheckEndpoint(r2Endpoint, false); err != nil {
		t.Fatalf("CheckEndpoint = %v", err)
	}
	for _, pathStyle := range []bool{true, false} {
		b, err := NewTenant(r2Config("0123456789abcdef0123456789abcdef", "fakefakefakefakefakefakefakefakefakefakefakefakefakefakefakefake", pathStyle), false)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err = Probe(ctx, b)
		cancel()
		t.Logf("path_style=%v: Probe = %v", pathStyle, err)
		if !errors.Is(err, ErrAuth) {
			t.Errorf("path_style=%v: want ErrAuth", pathStyle)
		}
	}
}

// With real keys from the environment (never printed).
func TestTmpR2WithRealKeys(t *testing.T) {
	keyID, secret := os.Getenv("AISW_R2_KEY_ID"), os.Getenv("AISW_R2_SECRET")
	if keyID == "" || secret == "" {
		t.Skip("set AISW_R2_KEY_ID and AISW_R2_SECRET")
	}
	closeIdle(t)
	b, err := NewTenant(r2Config(keyID, secret, true), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := Probe(ctx, b); err != nil {
		t.Fatalf("Probe = %v", err)
	}
}
