package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
	"AIServeWeave/service/aiServeWeaveAgent/internal/localcontrol"
)

func TestAgentControlProcess(t *testing.T) {
	if path := os.Getenv("AISW_AGENT_PROCESS_CONFIG"); path != "" {
		flag.CommandLine = flag.NewFlagSet("agent", flag.ExitOnError)
		os.Args = []string{"agent", "-config", path, "-metrics-addr=", "-config-ui-addr=127.0.0.1:0", "-auto-discover=false"}
		main()
		return
	}
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := agentconfig.Save(path, &agentconfig.Config{}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAgentControlProcess$")
	cmd.Env = append(os.Environ(), "AISW_AGENT_PROCESS_CONFIG="+path)
	cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	timer := time.NewTicker(10 * time.Millisecond)
	defer timer.Stop()
	var response []byte
	for {
		var err error
		response, err = localcontrol.Request(ctx, path, "status")
		if err == nil && bytes.Contains(response, []byte(`"phase":"running"`)) {
			break
		}
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("Agent never exposed local status")
		case <-timer.C:
		}
	}
	var status struct {
		Status struct {
			PID     int    `json:"pid"`
			Version string `json:"version"`
		} `json:"status"`
	}
	if err := json.Unmarshal(response, &status); err != nil {
		t.Fatal(err)
	}
	if status.Status.PID != cmd.Process.Pid || status.Status.Version != version {
		t.Errorf("status=%s, want actual PID and version", response)
	}
	if _, err := localcontrol.Request(ctx, path, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("stopped Agent exit=%v, want success", err)
	}
}
