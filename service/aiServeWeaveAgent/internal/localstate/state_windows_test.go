package localstate_test

import (
	"os"
	"path/filepath"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
	"golang.org/x/sys/windows"
)

func TestPrivateStateRejectsEveryoneRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := localstate.WriteFile(path, []byte("private identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	sid, err := localstate.UserSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := localstate.CheckPrivate(path, true); err == nil {
		t.Fatal("CheckPrivate error=nil, want rejection of Everyone read")
	}
}

func TestReplacementSharingFailurePreservesState(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "identity")
	source := filepath.Join(dir, "next")
	if err := localstate.WriteFile(target, []byte("old identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := localstate.WriteFile(source, []byte("new identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := localstate.Replace(source, target); err == nil {
		windows.CloseHandle(h)
		t.Fatal("occupied replacement succeeded, want sharing failure")
	}
	old, err := os.ReadFile(target)
	if err != nil {
		windows.CloseHandle(h)
		t.Fatal(err)
	}
	if string(old) != "old identity" {
		windows.CloseHandle(h)
		t.Fatalf("identity=%q, want old identity", old)
	}
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	if err := localstate.Replace(source, target); err != nil {
		t.Fatal(err)
	}
	next, err := os.ReadFile(target)
	if err != nil || string(next) != "new identity" {
		t.Fatalf("replacement=(%q,%v), want new identity", next, err)
	}
}
