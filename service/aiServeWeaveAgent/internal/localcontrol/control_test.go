package localcontrol_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/internal/localcontrol"
)

func TestLocalMaintenanceActions(t *testing.T) {
	key := filepath.Join(t.TempDir(), "config.yaml")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan struct{})
	reloads := 0
	closeServer, err := localcontrol.Serve(ctx, key, localcontrol.Handlers{
		Status: func() any { return map[string]int{"connected_gateways": 2} },
		Reload: func(context.Context) error { reloads++; return nil },
		Stop:   func() { close(stopped) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	for _, action := range []string{"status", "reload", "stop"} {
		data, err := localcontrol.Request(t.Context(), key, action)
		if err != nil {
			t.Fatalf("Request(%q)=%v, want success", action, err)
		}
		var result struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(data, &result) != nil || !result.OK {
			t.Fatalf("response=%q, want ok", data)
		}
	}
	<-stopped
	if reloads != 1 {
		t.Fatalf("reloads=%d, want 1", reloads)
	}
	if _, err := localcontrol.Request(t.Context(), key, "execute arbitrary command"); err == nil {
		t.Fatal("Request error=nil, want invalid action rejection")
	}
}

func TestSlowReloadDoesNotBlockStatusOrStop(t *testing.T) {
	key := filepath.Join(t.TempDir(), "slow.yaml")
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	closeServer, err := localcontrol.Serve(t.Context(), key, localcontrol.Handlers{
		Status: func() any { return "responsive" },
		Reload: func(context.Context) error { <-release; return nil },
		Stop:   func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	defer unblock()
	if _, err := localcontrol.Request(t.Context(), key, "reload"); err == nil {
		t.Fatal("slow reload succeeded, want bounded failure")
	}
	for _, action := range []string{"status", "stop"} {
		if _, err := localcontrol.Request(t.Context(), key, action); err != nil {
			t.Fatalf("%s after blocked reload=%v, want success", action, err)
		}
	}
}
