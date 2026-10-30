package localcontrol_test

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/internal/localcontrol"
	"golang.org/x/sys/windows"
)

func TestAnonymousPrincipalCannotUseControlPipe(t *testing.T) {
	key := filepath.Join(t.TempDir(), "config.yaml")
	closeServer, err := localcontrol.Serve(t.Context(), key, localcontrol.Handlers{Status: func() any { return "private" }})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	result, _, err := windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateAnonymousToken").Call(uintptr(windows.CurrentThread()))
	if result == 0 {
		t.Fatalf("impersonate anonymous: %v", err)
	}
	_, requestErr := localcontrol.Request(t.Context(), key, "status")
	if err := windows.RevertToSelf(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(requestErr, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("anonymous request=%v, want access denied", requestErr)
	}
}
