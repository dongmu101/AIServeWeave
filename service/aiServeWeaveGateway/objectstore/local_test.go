package objectstore_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
)

func newLocalBackend(t *testing.T) objectstore.Backend {
	t.Helper()
	b, err := objectstore.NewLocal(objectstore.LocalConfig{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	return b
}

func TestNewLocalRejectsEmptyDir(t *testing.T) {
	if _, err := objectstore.NewLocal(objectstore.LocalConfig{}); err == nil {
		t.Fatal("NewLocal with empty Dir: want error, got nil")
	}
}

func TestNewLocalRejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := objectstore.NewLocal(objectstore.LocalConfig{Dir: file}); err == nil {
		t.Fatal("NewLocal with a file as Dir: want error, got nil")
	}
}

func TestLocalPutOverwriteLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	b, err := objectstore.NewLocal(objectstore.LocalConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	ctx := context.Background()
	if err := b.Put(ctx, "k", strings.NewReader("v1"), 2); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := b.Put(ctx, "k", strings.NewReader("value-two"), 9); err != nil {
		t.Fatalf("second Put: %v", err)
	}
	rc, info, err := b.Open(ctx, "k")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "value-two" {
		t.Errorf("Open content = %q, want %q", got, "value-two")
	}
	if info.Size != 9 {
		t.Errorf("Open size = %d, want 9", info.Size)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upload-") {
			t.Errorf("leftover temp file after Put: %s", e.Name())
		}
	}
}

func TestLocalDeleteThenOpenIsNotFound(t *testing.T) {
	b := newLocalBackend(t)
	ctx := context.Background()
	if err := b.Put(ctx, "k", strings.NewReader("v"), 1); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := b.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := b.Open(ctx, "k"); err != objectstore.ErrNotFound {
		t.Errorf("Open after Delete: err = %v, want ErrNotFound", err)
	}
}
