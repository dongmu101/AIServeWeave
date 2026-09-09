package objectstore_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
)

// TestBackendsConformance runs the same behavioral contract against every
// Backend implementation, so a difference between them is a bug in whichever
// one disagrees rather than something a caller has to special-case per
// backend kind.
//
// TestBackendsConformance 对每个 Backend 实现跑同一套行为契约，因此三者之间
// 的任何差异都是不一致的那一个的缺陷，而不是调用方需要按后端种类分别处理的
// 东西。
func TestBackendsConformance(t *testing.T) {
	backends := map[string]func(t *testing.T) objectstore.Backend{
		"local":  newLocalBackend,
		"s3":     newS3Backend,
		"webdav": newWebDAVBackend,
	}

	for name, newBackend := range backends {
		t.Run(name, func(t *testing.T) {
			t.Run("put and open round-trip", func(t *testing.T) {
				b := newBackend(t)
				ctx := context.Background()
				const want = "hello, objectstore"
				if err := b.Put(ctx, "dir/file.bin", strings.NewReader(want), int64(len(want))); err != nil {
					t.Fatalf("Put: %v", err)
				}
				rc, info, err := b.Open(ctx, "dir/file.bin")
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				defer rc.Close()
				got, err := io.ReadAll(rc)
				if err != nil {
					t.Fatalf("ReadAll: %v", err)
				}
				if string(got) != want {
					t.Errorf("content = %q, want %q", got, want)
				}
				if info.Size != int64(len(want)) {
					t.Errorf("Size = %d, want %d", info.Size, len(want))
				}
			})

			t.Run("put with unknown size", func(t *testing.T) {
				b := newBackend(t)
				ctx := context.Background()
				const want = "streamed without a declared size"
				if err := b.Put(ctx, "unsized", strings.NewReader(want), -1); err != nil {
					t.Fatalf("Put: %v", err)
				}
				rc, _, err := b.Open(ctx, "unsized")
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				defer rc.Close()
				got, err := io.ReadAll(rc)
				if err != nil {
					t.Fatalf("ReadAll: %v", err)
				}
				if string(got) != want {
					t.Errorf("content = %q, want %q", got, want)
				}
			})

			t.Run("put rejects a short body", func(t *testing.T) {
				b := newBackend(t)
				err := b.Put(context.Background(), "short", strings.NewReader("ab"), 10)
				if err == nil {
					t.Fatal("Put with body shorter than declared size: want error, got nil")
				}
			})

			t.Run("put rejects a long body", func(t *testing.T) {
				b := newBackend(t)
				err := b.Put(context.Background(), "long", strings.NewReader("abcdefgh"), 3)
				if err == nil {
					t.Fatal("Put with body longer than declared size: want error, got nil")
				}
			})

			t.Run("open missing key is ErrNotFound", func(t *testing.T) {
				b := newBackend(t)
				if _, _, err := b.Open(context.Background(), "never-written"); !errors.Is(err, objectstore.ErrNotFound) {
					t.Errorf("Open of a missing key: err = %v, want ErrNotFound", err)
				}
			})

			t.Run("delete is idempotent", func(t *testing.T) {
				b := newBackend(t)
				ctx := context.Background()
				if err := b.Put(ctx, "to-delete", strings.NewReader("x"), 1); err != nil {
					t.Fatalf("Put: %v", err)
				}
				if err := b.Delete(ctx, "to-delete"); err != nil {
					t.Fatalf("first Delete: %v", err)
				}
				if err := b.Delete(ctx, "to-delete"); err != nil {
					t.Errorf("second Delete on an already-absent key: %v, want nil", err)
				}
				if _, _, err := b.Open(ctx, "to-delete"); !errors.Is(err, objectstore.ErrNotFound) {
					t.Errorf("Open after Delete: err = %v, want ErrNotFound", err)
				}
			})

			for _, key := range []string{"", "/absolute", "a/../b", "a/./b", "a\\b", "a//b", "..", "."} {
				key := key
				t.Run("rejects invalid key "+key, func(t *testing.T) {
					b := newBackend(t)
					if err := b.Put(context.Background(), key, strings.NewReader("x"), 1); err == nil {
						t.Errorf("Put(%q): want error, got nil", key)
					}
					if _, _, err := b.Open(context.Background(), key); err == nil {
						t.Errorf("Open(%q): want error, got nil", key)
					}
					if err := b.Delete(context.Background(), key); err == nil {
						t.Errorf("Delete(%q): want error, got nil", key)
					}
				})
			}
		})
	}
}
