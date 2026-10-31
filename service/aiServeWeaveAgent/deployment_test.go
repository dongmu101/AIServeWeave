package main

import (
	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentConfigPreservesExistingState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	opts := &tunnelOptions{gateway: "gateway.example:9090", registry: "registry.example:9090", nodeID: "node-one", certFile: filepath.Join(dir, "node.crt"), keyFile: filepath.Join(dir, "node.key"), caFile: filepath.Join(dir, "ca.crt"), bootstrapToken: filepath.Join(dir, "bootstrap.token")}
	if err := initializeDeployment(path, opts); err != nil {
		t.Fatal(err)
	}
	if err := checkDeployment(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	opts.nodeID = "different-node"
	if err := initializeDeployment(path, opts); err == nil {
		t.Fatal("second initialization succeeded, want existing configuration error")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("existing configuration changed")
	}
}

func TestPrivateEnvironmentLoadsWithoutEchoingValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.json")
	t.Setenv("AISW_DEPLOYMENT_FIXTURE_KEY", "")
	if err := localstate.WriteFile(path, []byte(`{"AISW_DEPLOYMENT_FIXTURE_KEY":"private-value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadEnvironment(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("AISW_DEPLOYMENT_FIXTURE_KEY"); got != "private-value" {
		t.Fatalf("environment value was not loaded")
	}
	if err := localstate.WriteFile(path, []byte(`{"BAD=KEY":"secret-fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadEnvironment(path); err == nil || strings.Contains(err.Error(), "secret-fixture") {
		t.Fatalf("invalid environment error=%v, want generic failure", err)
	}
}
