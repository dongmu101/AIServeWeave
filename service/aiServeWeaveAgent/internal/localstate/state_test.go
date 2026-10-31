package localstate_test

import (
	"os"
	"path/filepath"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
)

func TestPrivateStateSurvivesReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity", "key")
	for _, want := range []string{"first identity", "renewed identity"} {
		if err := localstate.WriteFile(path, []byte(want), 0o600); err != nil {
			t.Fatalf("WriteFile error=%v, want nil", err)
		}
		if err := localstate.CheckPrivate(path, true); err != nil {
			t.Fatalf("CheckPrivate error=%v, want protected file", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("ReadFile=%q error=%v, want %q", got, err, want)
		}
	}
}

func TestFailedReplacementPreservesIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := localstate.WriteFile(path, []byte("registered identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := localstate.Replace(filepath.Join(dir, "absent"), path); err == nil {
		t.Fatal("Replace error=nil, want missing source")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "registered identity" {
		t.Fatalf("identity=%q error=%v, want registered identity", got, err)
	}
}
