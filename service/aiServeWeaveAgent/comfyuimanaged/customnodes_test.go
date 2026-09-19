package comfyuimanaged

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"AIServeWeave/common/comfyuimanagedstatus"
)

func TestBuildInstallScript(t *testing.T) {
	got := buildInstallScript("/comfyui/custom_nodes", "my-node", NodeSpec{RepoURL: "https://example.com/my-node.git", Ref: "v1.0.0"})

	for _, want := range []string{
		"mkdir -p /comfyui/custom_nodes",
		"git clone --depth 1 --branch v1.0.0 https://example.com/my-node.git /comfyui/custom_nodes/my-node",
		"git -C /comfyui/custom_nodes/my-node fetch --depth 1 origin v1.0.0",
		"git -C /comfyui/custom_nodes/my-node checkout FETCH_HEAD",
		"echo v1.0.0 > /comfyui/custom_nodes/my-node/.aisw-version",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildInstallScript() = %q, want substring %q", got, want)
		}
	}
}

func TestNodeSpecValidate(t *testing.T) {
	tests := []struct {
		name    string
		nodeSet string
		spec    NodeSpec
		wantErr bool
	}{
		{"valid", "my-node", NodeSpec{RepoURL: "https://example.com/my-node.git", Ref: "v1.0.0"}, false},
		{"name with space rejected", "my node", NodeSpec{RepoURL: "https://example.com/x.git", Ref: "v1"}, true},
		{"repo url with shell metacharacter rejected", "my-node", NodeSpec{RepoURL: "https://example.com/x.git; rm -rf /", Ref: "v1"}, true},
		{"ref with space rejected", "my-node", NodeSpec{RepoURL: "https://example.com/x.git", Ref: "v1 && echo pwned"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.validate(tt.nodeSet)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// fakeExecDocker writes a fake "docker" CLI that only understands `exec`,
// logging each invocation's arguments and returning a preset stdout/exit
// code — it never actually runs the sh -c script it is handed, the same
// "assert on the constructed command line, not on a real container"
// discipline newFakeDocker (comfyuimanaged_test.go) already applies to
// inspect/image/pull/run/stop/rm. This keeps InstallCustomNode/
// ListCustomNodes tests independent of a real docker daemon, container, or
// network access to a git remote.
func fakeExecDocker(t *testing.T, stdout string, fail bool) func() []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake docker script requires a POSIX shell")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")

	exitCode := 0
	if fail {
		exitCode = 1
	}
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$1" in
  exec) printf '%%b' %q; exit %d ;;
  *) echo "fake docker: unrecognized subcommand $1" >&2; exit 1 ;;
esac
`, logPath, stdout, exitCode)

	scriptPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker script: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []string {
		data, err := os.ReadFile(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatalf("read fake docker log: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			return nil
		}
		return lines
	}
}

func TestLauncherInstallCustomNodeUnknownNameRefusedWithoutDocker(t *testing.T) {
	calls := fakeExecDocker(t, "", false)
	l := testLauncher(nil)
	err := l.InstallCustomNode(context.Background(), "aiserveweave-comfyui", "/comfyui/custom_nodes", "unknown-node", map[string]NodeSpec{
		"known-node": {RepoURL: "https://example.com/known.git", Ref: "v1"},
	})
	if err == nil {
		t.Fatal("InstallCustomNode(unknown name): want error, got nil")
	}
	if got := calls(); got != nil {
		t.Fatalf("InstallCustomNode(unknown name): want no docker invocation, got %v", got)
	}
}

func TestLauncherInstallCustomNodeSuccess(t *testing.T) {
	calls := fakeExecDocker(t, "", false)
	l := testLauncher(nil)
	allowlist := map[string]NodeSpec{"my-node": {RepoURL: "https://example.com/my-node.git", Ref: "v1.0.0"}}
	if err := l.InstallCustomNode(context.Background(), "aiserveweave-comfyui", "/comfyui/custom_nodes", "my-node", allowlist); err != nil {
		t.Fatalf("InstallCustomNode() error = %v", err)
	}
	got := calls()
	if len(got) != 1 {
		t.Fatalf("InstallCustomNode(): want exactly one docker invocation, got %v", got)
	}
	for _, want := range []string{"exec", "aiserveweave-comfyui", "sh", "-c", "my-node.git", "v1.0.0"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("InstallCustomNode() docker exec invocation = %q, want to contain %q", got[0], want)
		}
	}
}

func TestLauncherInstallCustomNodeExecFailurePropagates(t *testing.T) {
	fakeExecDocker(t, "", true)
	l := testLauncher(nil)
	allowlist := map[string]NodeSpec{"my-node": {RepoURL: "https://example.com/my-node.git", Ref: "v1.0.0"}}
	err := l.InstallCustomNode(context.Background(), "aiserveweave-comfyui", "/comfyui/custom_nodes", "my-node", allowlist)
	if err == nil {
		t.Fatal("InstallCustomNode(): want error when docker exec fails, got nil")
	}
}

func TestLauncherListCustomNodes(t *testing.T) {
	fakeExecDocker(t, "zeta-node\tv2\nalpha-node\tv1\n", false)
	l := testLauncher(nil)
	got, err := l.ListCustomNodes(context.Background(), "aiserveweave-comfyui", "/comfyui/custom_nodes")
	if err != nil {
		t.Fatalf("ListCustomNodes() error = %v", err)
	}
	want := []comfyuimanagedstatus.CustomNodeStatus{
		{Name: "alpha-node", Version: "v1"},
		{Name: "zeta-node", Version: "v2"},
	}
	if len(got) != len(want) {
		t.Fatalf("ListCustomNodes() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListCustomNodes()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLauncherListCustomNodesEmpty(t *testing.T) {
	fakeExecDocker(t, "", false)
	l := testLauncher(nil)
	got, err := l.ListCustomNodes(context.Background(), "aiserveweave-comfyui", "/comfyui/custom_nodes")
	if err != nil {
		t.Fatalf("ListCustomNodes() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListCustomNodes() = %+v, want empty", got)
	}
}

func TestLauncherListCustomNodesExecFailurePropagates(t *testing.T) {
	fakeExecDocker(t, "", true)
	l := testLauncher(nil)
	_, err := l.ListCustomNodes(context.Background(), "aiserveweave-comfyui", "/comfyui/custom_nodes")
	if err == nil {
		t.Fatal("ListCustomNodes(): want error when docker exec fails, got nil")
	}
}
