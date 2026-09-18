package comfyuimanaged

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	aiswruntime "AIServeWeave/common/runtime"
)

// fakeClock is a minimal runtime.Clock a test fully controls, matching
// AGENTS.md's "tests never advance time with a real time.Sleep" convention.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(0, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer fires immediately but first advances the clock's own Now() by d,
// so a caller that loops on "poll, then wait for a timer tick" sees time
// pass on every iteration without this test ever blocking in real wall-clock
// time.
func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	fired := c.now
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- fired
	return ch, func() bool { return true }
}

var _ aiswruntime.Clock = (*fakeClock)(nil)

// fakeDockerConfig controls every branch the fake docker script (below) can
// take, keyed by docker subcommand.
type fakeDockerConfig struct {
	inspectOutput string // "" means "No such container" (exit 1)
	imagePresent  bool
	pullFail      bool
	runFail       bool
	stopNotFound  bool
	rmNotFound    bool
}

// newFakeDocker writes a POSIX shell script named "docker" into a temp
// directory, prepends that directory to PATH for the duration of the test,
// and returns a function that reads back every invocation the script
// recorded (one line per call, args space-joined) for assertions.
func newFakeDocker(t *testing.T, cfg fakeDockerConfig) func() []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake docker script requires a POSIX shell")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")

	inspectBody := `echo "No such container: $*" >&2; exit 1`
	if cfg.inspectOutput != "" {
		inspectBody = fmt.Sprintf(`echo %q; exit 0`, cfg.inspectOutput)
	}
	imageBody := `echo "No such image: $*" >&2; exit 1`
	if cfg.imagePresent {
		imageBody = `echo "[{}]"; exit 0`
	}
	pullBody := "exit 0"
	if cfg.pullFail {
		pullBody = `echo "pull failed" >&2; exit 1`
	}
	runBody := `echo "fakecontainerid"; exit 0`
	if cfg.runFail {
		runBody = `echo "run failed" >&2; exit 1`
	}
	stopBody := "exit 0"
	if cfg.stopNotFound {
		stopBody = `echo "No such container: $*" >&2; exit 1`
	}
	rmBody := "exit 0"
	if cfg.rmNotFound {
		rmBody = `echo "No such container: $*" >&2; exit 1`
	}

	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %q
case "$1" in
  inspect) %s ;;
  image) %s ;;
  pull) %s ;;
  run) %s ;;
  stop) %s ;;
  rm) %s ;;
  *) echo "fake docker: unrecognized subcommand $1" >&2; exit 1 ;;
esac
`, logPath, inspectBody, imageBody, pullBody, runBody, stopBody, rmBody)

	scriptPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker script: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+origPath)

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

func testLauncher(clock aiswruntime.Clock) *Launcher {
	return NewLauncher(clock, slog.New(slog.DiscardHandler))
}

func testSpec() Spec {
	return Spec{
		ContainerName: "aiserveweave-comfyui",
		Image:         "ghcr.io/example/comfyui:1.4.2",
		Port:          18188,
		GPUDevices:    []string{"0"},
		ModelPaths:    map[string]string{"checkpoints": "/models/checkpoints", "loras": "/models/loras"},
		StoragePaths:  map[string]string{"output": "/data/output"},
		MemoryLimit:   "32g",
	}
}

func TestSpecValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr bool
	}{
		{name: "valid spec", mutate: func(s *Spec) {}, wantErr: false},
		{name: "missing container name", mutate: func(s *Spec) { s.ContainerName = "" }, wantErr: true},
		{name: "zero port", mutate: func(s *Spec) { s.Port = 0 }, wantErr: true},
		{name: "negative port", mutate: func(s *Spec) { s.Port = -1 }, wantErr: true},
		{name: "no tag", mutate: func(s *Spec) { s.Image = "ghcr.io/example/comfyui" }, wantErr: true},
		{name: "latest tag rejected", mutate: func(s *Spec) { s.Image = "ghcr.io/example/comfyui:latest" }, wantErr: true},
		{name: "registry host with its own port is not mistaken for a tag", mutate: func(s *Spec) {
			s.Image = "ghcr.io:5000/example/comfyui:1.4.2"
		}, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := testSpec()
			tt.mutate(&spec)
			err := spec.validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildRunArgs(t *testing.T) {
	spec := testSpec()
	args := buildRunArgs(spec)
	joined := strings.Join(args, " ")

	wantContains := []string{
		"run -d",
		"--name aiserveweave-comfyui",
		"-p 127.0.0.1:18188:8188",
		"--gpus device=0",
		"--memory 32g",
		"-v /models/checkpoints:checkpoints:ro",
		"-v /models/loras:loras:ro",
		"-v /data/output:output",
		"ghcr.io/example/comfyui:1.4.2",
	}
	for _, want := range wantContains {
		if !strings.Contains(joined, want) {
			t.Errorf("buildRunArgs() = %q, want it to contain %q", joined, want)
		}
	}
	// Storage mounts must never carry ":ro" — only ModelPaths does.
	if strings.Contains(joined, "/data/output:output:ro") {
		t.Errorf("buildRunArgs() mounted a storage path read-only: %q", joined)
	}

	// Deterministic ordering: two calls on the same Spec produce identical args.
	again := buildRunArgs(spec)
	if strings.Join(again, " ") != joined {
		t.Errorf("buildRunArgs() is not deterministic: %q vs %q", joined, strings.Join(again, " "))
	}
}

func TestBuildRunArgsNoGPUOrMemory(t *testing.T) {
	spec := testSpec()
	spec.GPUDevices = nil
	spec.MemoryLimit = ""
	args := buildRunArgs(spec)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--gpus") {
		t.Errorf("buildRunArgs() with no GPUDevices should omit --gpus, got %q", joined)
	}
	if strings.Contains(joined, "--memory") {
		t.Errorf("buildRunArgs() with no MemoryLimit should omit --memory, got %q", joined)
	}
}

func TestStatus(t *testing.T) {
	tests := []struct {
		name    string
		cfg     fakeDockerConfig
		want    State
		wantErr bool
	}{
		{name: "not found", cfg: fakeDockerConfig{}, want: StatePending},
		{name: "created", cfg: fakeDockerConfig{inspectOutput: "created|0"}, want: StateStarting},
		{name: "restarting", cfg: fakeDockerConfig{inspectOutput: "restarting|0"}, want: StateStarting},
		{name: "running", cfg: fakeDockerConfig{inspectOutput: "running|0"}, want: StateRunning},
		{name: "exited clean", cfg: fakeDockerConfig{inspectOutput: "exited|0"}, want: StateStopped},
		{name: "exited nonzero", cfg: fakeDockerConfig{inspectOutput: "exited|1"}, want: StateFailed},
		{name: "dead", cfg: fakeDockerConfig{inspectOutput: "dead|137"}, want: StateFailed},
		{name: "unrecognized status", cfg: fakeDockerConfig{inspectOutput: "paused|0"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newFakeDocker(t, tt.cfg)
			l := testLauncher(newFakeClock())
			got, err := l.Status(context.Background(), "aiserveweave-comfyui")
			if (err != nil) != tt.wantErr {
				t.Fatalf("Status() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("Status() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStartAdoptsAlreadyRunningContainer(t *testing.T) {
	calls := newFakeDocker(t, fakeDockerConfig{inspectOutput: "running|0"})
	l := testLauncher(newFakeClock())
	if err := l.Start(context.Background(), testSpec()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	for _, line := range calls() {
		if strings.HasPrefix(line, "run ") || strings.HasPrefix(line, "pull ") {
			t.Errorf("Start() on an already-running container should not call %q", line)
		}
	}
}

func TestStartPullsMissingImageThenRuns(t *testing.T) {
	calls := newFakeDocker(t, fakeDockerConfig{imagePresent: false})
	l := testLauncher(newFakeClock())
	if err := l.Start(context.Background(), testSpec()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	var sawPull, sawRun, pullBeforeRun bool
	for _, line := range calls() {
		switch {
		case strings.HasPrefix(line, "pull "):
			sawPull = true
		case strings.HasPrefix(line, "run "):
			sawRun = true
			pullBeforeRun = sawPull
		}
	}
	if !sawPull || !sawRun {
		t.Fatalf("Start() calls = %v, want both a pull and a run", calls())
	}
	if !pullBeforeRun {
		t.Errorf("Start() ran docker run before docker pull completed: %v", calls())
	}
}

func TestStartSkipsPullWhenImageAlreadyPresent(t *testing.T) {
	calls := newFakeDocker(t, fakeDockerConfig{imagePresent: true})
	l := testLauncher(newFakeClock())
	if err := l.Start(context.Background(), testSpec()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	for _, line := range calls() {
		if strings.HasPrefix(line, "pull ") {
			t.Errorf("Start() should not pull when the image is already present, calls = %v", calls())
		}
	}
}

func TestStartRemovesStaleContainerBeforeRecreating(t *testing.T) {
	calls := newFakeDocker(t, fakeDockerConfig{inspectOutput: "exited|1", imagePresent: true})
	l := testLauncher(newFakeClock())
	if err := l.Start(context.Background(), testSpec()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	var sawRemove, sawRun, removeBeforeRun bool
	for _, line := range calls() {
		switch {
		case strings.HasPrefix(line, "rm -f"):
			sawRemove = true
		case strings.HasPrefix(line, "run "):
			sawRun = true
			removeBeforeRun = sawRemove
		}
	}
	if !sawRemove || !sawRun || !removeBeforeRun {
		t.Fatalf("Start() on a failed container should rm -f then run, calls = %v", calls())
	}
}

func TestStartRejectsInvalidSpecWithoutTouchingDocker(t *testing.T) {
	calls := newFakeDocker(t, fakeDockerConfig{})
	l := testLauncher(newFakeClock())
	spec := testSpec()
	spec.Image = "ghcr.io/example/comfyui:latest"
	if err := l.Start(context.Background(), spec); err == nil {
		t.Fatal("Start() with a \"latest\" tag should fail")
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("Start() with an invalid Spec should not invoke docker at all, calls = %v", got)
	}
}

func TestStartFailsWhenRunFails(t *testing.T) {
	newFakeDocker(t, fakeDockerConfig{imagePresent: true, runFail: true})
	l := testLauncher(newFakeClock())
	if err := l.Start(context.Background(), testSpec()); err == nil {
		t.Fatal("Start() should fail when docker run fails")
	}
}

func TestStopOnMissingContainerIsNotAnError(t *testing.T) {
	newFakeDocker(t, fakeDockerConfig{stopNotFound: true, rmNotFound: true})
	l := testLauncher(newFakeClock())
	if err := l.Stop(context.Background(), "does-not-exist"); err != nil {
		t.Errorf("Stop() on a nonexistent container should not error, got %v", err)
	}
}

func TestStopStopsThenRemoves(t *testing.T) {
	calls := newFakeDocker(t, fakeDockerConfig{})
	l := testLauncher(newFakeClock())
	if err := l.Stop(context.Background(), "aiserveweave-comfyui"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	got := calls()
	if len(got) != 2 || !strings.HasPrefix(got[0], "stop ") || !strings.HasPrefix(got[1], "rm ") {
		t.Fatalf("Stop() calls = %v, want [stop ..., rm ...]", got)
	}
}

func TestWaitReadySucceedsOnceListenerAccepts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	l := testLauncher(newFakeClock())
	spec := Spec{Port: port}
	if err := l.WaitReady(context.Background(), spec, time.Second); err != nil {
		t.Errorf("WaitReady() error = %v", err)
	}
}

func TestWaitReadyTimesOutWithoutRealSleep(t *testing.T) {
	clock := newFakeClock()
	l := testLauncher(clock)
	// Nothing listens on this port, and the fake clock's NewTimer fires
	// its channel immediately while still advancing Now() by the
	// requested duration, so the deadline is crossed without the test
	// actually waiting in wall-clock time.
	spec := Spec{Port: unusedLoopbackPort(t)}
	start := time.Now()
	err := l.WaitReady(context.Background(), spec, 3*time.Second)
	if err == nil {
		t.Fatal("WaitReady() should time out when nothing listens on the port")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("WaitReady() took %s of real time, want it driven by the fake clock instead", elapsed)
	}
}

// unusedLoopbackPort finds a loopback port nothing is listening on by
// opening and immediately closing a listener.
func unusedLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestIsNotFound(t *testing.T) {
	if isNotFound(nil) {
		t.Error("isNotFound(nil) = true, want false")
	}
	if !isNotFound(errors.New("comfyuimanaged: docker inspect: Error: No such container: x")) {
		t.Error("isNotFound() should recognize docker's \"No such\" message")
	}
	if !isNotFound(errors.New("comfyuimanaged: docker inspect: error: no such object: x")) {
		t.Error("isNotFound() should recognize docker's lowercase \"no such object\" message (real docker CLI, not just the fake)")
	}
	if isNotFound(errors.New("comfyuimanaged: docker inspect: permission denied")) {
		t.Error("isNotFound() should not match unrelated errors")
	}
}
