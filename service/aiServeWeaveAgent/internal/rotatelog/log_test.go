package rotatelog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogsRemainBoundedAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	for i := 0; i < 4; i++ {
		w, err := Open(path, 16, 2)
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 20; j++ {
			if _, err := w.Write([]byte("12345678\n")); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	files, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("log files=%d, want 3", len(files))
	}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 16 {
			t.Errorf("size=%d, want <=16", info.Size())
		}
	}
}
