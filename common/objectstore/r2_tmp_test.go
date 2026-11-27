package objectstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

func r2Env(t *testing.T) S3Config {
	t.Helper()
	cfg := S3Config{
		Endpoint:        os.Getenv("AISW_ARTIFACT_STORAGE_S3_ENDPOINT"),
		Region:          os.Getenv("AISW_ARTIFACT_STORAGE_S3_REGION"),
		Bucket:          os.Getenv("AISW_ARTIFACT_STORAGE_S3_BUCKET"),
		Prefix:          os.Getenv("AISW_ARTIFACT_STORAGE_S3_PREFIX"),
		UsePathStyle:    os.Getenv("AISW_ARTIFACT_STORAGE_S3_PATH_STYLE") == "true",
		AccessKeyID:     os.Getenv("AISW_ARTIFACT_STORAGE_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AISW_ARTIFACT_STORAGE_S3_SECRET_ACCESS_KEY"),
	}
	if cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		t.Skip("AISW_ARTIFACT_STORAGE_S3_* not set")
	}
	return cfg
}

// patterned is a deterministic, non-seekable byte stream.
type patterned struct{ n int64 }

func (p *patterned) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = byte((p.n*31 + p.n>>7) & 0xff)
		p.n++
	}
	return len(b), nil
}

func patternedSHA(size int64) string {
	h := sha256.New()
	_, _ = io.Copy(h, io.LimitReader(&patterned{}, size))
	return hex.EncodeToString(h.Sum(nil))
}

type onlyReader struct{ io.Reader }

func readAllSHA(t *testing.T, b Backend, key string) (string, ObjectInfo) {
	t.Helper()
	body, info, err := b.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("Open(%s) = %v", key, err)
	}
	defer body.Close()
	h := sha256.New()
	if _, err := io.Copy(h, body); err != nil {
		t.Fatalf("reading %s: %v", key, err)
	}
	return hex.EncodeToString(h.Sum(nil)), info
}

func randomRoot() string {
	var id [6]byte
	_, _ = rand.Read(id[:])
	return "aisw-tmp-test/" + hex.EncodeToString(id[:])
}

func cleanupRoot(t *testing.T, b Backend, root string) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_ = b.List(ctx, root, func(e ObjectEntry) error { return b.Delete(ctx, e.Key) })
		n := 0
		_ = b.List(ctx, root, func(ObjectEntry) error { n++; return nil })
		if n != 0 {
			t.Errorf("cleanup left %d objects under %s", n, root)
		}
	})
}

func TestTmpR2Deployment(t *testing.T) {
	cfg := r2Env(t)
	closeIdle(t)
	b, err := New(Config{Kind: "s3", S3: cfg})
	if err != nil {
		t.Fatal(err)
	}
	root := randomRoot()
	cleanupRoot(t, b, root)
	ctx := context.Background()

	t.Run("probe", func(t *testing.T) {
		if err := Probe(ctx, b); err != nil {
			t.Fatalf("Probe = %v", err)
		}
	})

	t.Run("small roundtrip, overwrite, delete, missing", func(t *testing.T) {
		key := root + "/small/a.txt"
		if err := b.Put(ctx, key, strings.NewReader("hello r2"), 8); err != nil {
			t.Fatal(err)
		}
		body, info, err := b.Open(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(body)
		body.Close()
		if string(got) != "hello r2" || info.Size != 8 {
			t.Fatalf("got %q size %d", got, info.Size)
		}
		t.Logf("content type reported: %q", info.ContentType)
		if err := b.Put(ctx, key, strings.NewReader("v2"), 2); err != nil {
			t.Fatal(err)
		}
		body, _, _ = b.Open(ctx, key)
		got, _ = io.ReadAll(body)
		body.Close()
		if string(got) != "v2" {
			t.Fatalf("overwrite read %q", got)
		}
		if err := b.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
		if _, _, err := b.Open(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Open after delete = %v", err)
		}
		if err := b.Delete(ctx, key); err != nil {
			t.Fatalf("second Delete = %v", err)
		}
		if _, _, err := b.Open(ctx, root+"/never"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Open missing = %v", err)
		}
	})

	t.Run("unicode and spaces in a key", func(t *testing.T) {
		key := root + "/名字 带空格/图 片(1)+x.png"
		if err := b.Put(ctx, key, strings.NewReader("u"), 1); err != nil {
			t.Fatal(err)
		}
		found := false
		_ = b.List(ctx, root+"/名字 带空格", func(e ObjectEntry) error { found = found || e.Key == key; return nil })
		if !found {
			t.Error("unicode key not listed back")
		}
	})

	for _, tt := range []struct {
		name     string
		size     int64
		seekable bool
		declared int64
	}{
		{"empty object", 0, false, 0},
		{"1 MiB seekable", 1 << 20, true, 1 << 20},
		{"64 MiB streamed, known size", 64 << 20, false, 64 << 20},
		{"1 MiB streamed, unknown size", 1 << 20, false, -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			key := root + "/big/" + strings.ReplaceAll(tt.name, " ", "_")
			var body io.Reader = onlyReader{io.LimitReader(&patterned{}, tt.size)}
			if tt.seekable {
				data, _ := io.ReadAll(io.LimitReader(&patterned{}, tt.size))
				body = bytes.NewReader(data)
			}
			start := time.Now()
			err := b.Put(ctx, key, body, tt.declared)
			if err != nil {
				t.Fatalf("Put = %v", err)
			}
			sum, info := readAllSHA(t, b, key)
			if sum != patternedSHA(tt.size) || info.Size != tt.size {
				t.Fatalf("read back sha/size mismatch: size %d", info.Size)
			}
			t.Logf("%s: put+get in %v", tt.name, time.Since(start).Round(time.Millisecond))
		})
	}

	t.Run("wrong declared size leaves nothing", func(t *testing.T) {
		for _, c := range []struct {
			name     string
			actual   int64
			declared int64
		}{{"short", 100, 200}, {"long", 300, 200}} {
			key := root + "/wrong/" + c.name
			err := b.Put(ctx, key, onlyReader{io.LimitReader(&patterned{}, c.actual)}, c.declared)
			if err == nil {
				t.Errorf("%s body accepted", c.name)
			}
			if _, _, err := b.Open(ctx, key); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: an object exists after a refused Put: %v", c.name, err)
			}
		}
	})

	t.Run("list paginates past 1000 and allows deletes while listing", func(t *testing.T) {
		const n = 1005
		prefix := root + "/many"
		var wg sync.WaitGroup
		sem := make(chan struct{}, 48)
		var failed atomic.Int32
		for i := 0; i < n; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				key := fmt.Sprintf("%s/d%d/o%04d", prefix, i%3, i)
				if err := b.Put(ctx, key, strings.NewReader("x"), 1); err != nil {
					failed.Add(1)
				}
			}(i)
		}
		wg.Wait()
		if failed.Load() != 0 {
			t.Fatalf("%d concurrent puts failed", failed.Load())
		}
		if err := b.Put(ctx, prefix+"x/sibling", strings.NewReader("x"), 1); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		now := time.Now()
		err := b.List(ctx, prefix, func(e ObjectEntry) error {
			if seen[e.Key] {
				return fmt.Errorf("listed twice: %s", e.Key)
			}
			seen[e.Key] = true
			if e.Size != 1 || e.ModTime.IsZero() || e.ModTime.After(now.Add(5*time.Minute)) || e.ModTime.Before(now.Add(-30*time.Minute)) {
				return fmt.Errorf("entry %+v", e)
			}
			return nil
		})
		if err != nil || len(seen) != n {
			t.Fatalf("listed %d of %d: %v", len(seen), n, err)
		}
		deleted := 0
		if err := b.List(ctx, prefix, func(e ObjectEntry) error { deleted++; return b.Delete(ctx, e.Key) }); err != nil || deleted != n {
			t.Fatalf("delete while listing: %d, %v", deleted, err)
		}
		left := 0
		_ = b.List(ctx, prefix, func(ObjectEntry) error { left++; return nil })
		if left != 0 {
			t.Fatalf("%d left after delete-while-listing", left)
		}
		empty := 0
		if err := b.List(ctx, root+"/nothing-here", func(ObjectEntry) error { empty++; return nil }); err != nil || empty != 0 {
			t.Fatalf("empty prefix = %d, %v", empty, err)
		}
		stop := errors.New("stop")
		if err := b.List(ctx, prefix+"x", func(ObjectEntry) error { return stop }); !errors.Is(err, stop) {
			t.Fatalf("fn error = %v", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if err := b.Put(cctx, root+"/cancelled", strings.NewReader("x"), 1); err == nil {
			t.Fatal("Put on a cancelled context succeeded")
		}
	})
}

func TestTmpR2TenantPath(t *testing.T) {
	cfg := r2Env(t)
	closeIdle(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ok, err := NewTenant(Config{Kind: "s3", S3: cfg}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Probe(ctx, ok); err != nil {
		t.Fatalf("tenant Probe = %v", err)
	}
	left := 0
	_ = ok.List(ctx, ProbePrefix, func(ObjectEntry) error { left++; return nil })
	if left != 0 {
		t.Errorf("probe objects left: %d", left)
	}

	for _, c := range []struct {
		name string
		mut  func(*S3Config)
	}{
		{"wrong secret", func(c *S3Config) { c.SecretAccessKey = strings.Repeat("0", len(c.SecretAccessKey)) }},
		{"unknown key id", func(c *S3Config) { c.AccessKeyID = strings.Repeat("0", len(c.AccessKeyID)) }},
		{"missing bucket", func(c *S3Config) { c.Bucket = "aisw-no-such-bucket-" + hex.EncodeToString([]byte{1, 2, 3}) }},
		{"wrong account host", func(c *S3Config) { c.Endpoint = "https://00000000000000000000000000000000.r2.cloudflarestorage.com" }},
		{"unresolvable host", func(c *S3Config) { c.Endpoint = "https://aisw-does-not-exist.invalid" }},
	} {
		bad := cfg
		c.mut(&bad)
		b, err := NewTenant(Config{Kind: "s3", S3: bad}, false)
		if err != nil {
			t.Errorf("%s: NewTenant = %v", c.name, err)
			continue
		}
		pctx, pcancel := context.WithTimeout(ctx, 20*time.Second)
		err = Probe(pctx, b)
		pcancel()
		class := "other"
		for name, e := range map[string]error{"auth": ErrAuth, "permission": ErrPermission, "not_found": ErrContainerNotFound, "network": ErrUnreachable, "endpoint_not_allowed": ErrAddressNotAllowed} {
			if errors.Is(err, e) {
				class = name
			}
		}
		if err == nil {
			t.Errorf("%s: Probe succeeded", c.name)
		}
		if strings.Contains(fmt.Sprint(err), cfg.SecretAccessKey) {
			t.Errorf("%s: error leaks the secret", c.name)
		}
		t.Logf("%s -> reason %s", c.name, class)
	}
}
