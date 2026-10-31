package cliprocess_test

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/internal/cliprocess"
)

func TestConfiguredProcessCanComplete(t *testing.T) {
	if os.Getenv("AISW_PROCESS_HELPER") == "1" {
		return
	}
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestConfiguredProcessCanComplete$")
	cmd.Env = append(os.Environ(), "AISW_PROCESS_HELPER=1")
	if err := cliprocess.Configure(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cliprocess.Run(cmd); err != nil {
		t.Fatalf("Run error=%v, want successful helper", err)
	}
}
