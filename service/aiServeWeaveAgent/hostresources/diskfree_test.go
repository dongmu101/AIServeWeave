package hostresources

import (
	"runtime"
	"testing"
)

func TestDiskFreeBytes(t *testing.T) {
	dir := t.TempDir()

	free, err := DiskFreeBytes(dir)
	switch runtime.GOOS {
	case "linux", "darwin":
		if err != nil {
			t.Fatalf("DiskFreeBytes(%q) error: %v", dir, err)
		}
		if free <= 0 {
			t.Fatalf("DiskFreeBytes(%q) = %d, want > 0", dir, free)
		}
	default:
		if err == nil {
			t.Fatalf("DiskFreeBytes(%q) on %s: want an error, got free=%d", dir, runtime.GOOS, free)
		}
	}
}

func TestDiskFreeBytes_MissingPath(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("DiskFreeBytes is unsupported on this platform")
	}
	if _, err := DiskFreeBytes("/does/not/exist/at/all"); err == nil {
		t.Fatal("DiskFreeBytes on a missing path: want an error, got nil")
	}
}
