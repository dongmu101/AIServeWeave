package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var (
	errAuthCheck     = errors.New("CLI authentication check failed; output withheld")
	errLoginRequired = errors.New("CLI subscription login unavailable in this execution context")
	errProbeSetup    = errors.New("could not create isolated probe resources")
	errRoundTrip     = errors.New("CLI did not demonstrate two tool rounds and consume the client token")
)

type boundedCapture struct {
	buffer bytes.Buffer
	limit  int
}

// Write rejects excess data without exposing ReaderFrom's unbounded fast path.
// Write 拒绝超量数据，避免暴露 ReaderFrom 无界复制的快速路径。
func (b *boundedCapture) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, errAuthCheck
	}
	return b.buffer.Write(p)
}

func checkAuth(ctx context.Context, program string, env []string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "aisw-claude-auth-")
	if err != nil {
		return errProbeSetup
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, program, "auth", "status", "--json")
	var output = boundedCapture{limit: 64 * 1024}
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, &output, io.Discard
	cmd.WaitDelay = time.Second
	if err := configureProcess(cmd); err != nil {
		return err
	}
	defer func() { _ = cmd.Cancel() }()
	err = cmd.Run()
	var status struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
	}
	if json.Unmarshal(output.buffer.Bytes(), &status) != nil {
		return errAuthCheck
	}
	if !status.LoggedIn || status.AuthMethod != "claude.ai" {
		return errLoginRequired
	}
	if err != nil {
		return errAuthCheck
	}
	return nil
}

type probeReport struct {
	Session               string       `json:"session"`
	Scenario              string       `json:"scenario"`
	Passed                bool         `json:"cli_mcp_probe_passed"`
	ClientCalls           int          `json:"synthetic_client_calls"`
	ResultOrder           []string     `json:"broker_result_order"`
	Events                eventSummary `json:"events"`
	MessagesCompatibility string       `json:"messages_api_compatibility"`
}

func runProbe(ctx context.Context, program, model, session, scenario string) (probeReport, error) {
	report := probeReport{Session: session, Scenario: scenario, MessagesCompatibility: "not_tested"}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dir, err := os.MkdirTemp("", "aisw-claude-probe-")
	if err != nil {
		return report, errProbeSetup
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return report, errProbeSetup
	}
	defer listener.Close()
	token, expected := randomID(), randomID()
	config := map[string]any{"mcpServers": map[string]any{"aisw_probe": map[string]any{
		"type": "http", "url": "http://" + listener.Addr().String() + "/mcp", "headers": map[string]string{"Authorization": "Bearer " + token},
	}}}
	data, err := json.Marshal(config)
	if err != nil {
		return report, errProbeSetup
	}
	configPath := filepath.Join(dir, "mcp.json")
	if os.WriteFile(configPath, data, 0600) != nil {
		return report, errProbeSetup
	}
	b := newBroker(principal{session, randomID()})
	server := &http.Server{Handler: mcpHandler(b, token, listener.Addr().String()), ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	served := make(chan struct{})
	go func() { defer close(served); _ = server.Serve(listener) }()
	clientDone := make(chan clientStats, 1)
	go func() { clientDone <- serveProbeClient(ctx, b, expected, scenario) }()
	prompt := "Call the probe_echo tool with label first. After receiving its result, call probe_echo with label second. After receiving the second result, output that result verbatim and stop. Do not call any other tools."
	wantTurns, wantStops := 2, 3
	if scenario == "parallel" {
		prompt = "In a single response, call probe_echo twice in parallel: once with label first and once with label second. Both calls must be issued before waiting for results. Then output the result of the second-labeled call verbatim and stop."
		wantTurns, wantStops = 1, 2
	}
	report.Events, err = runCLI(ctx, program, probeArgs(model, configPath), dir, probeEnv(os.Environ()), strings.NewReader(prompt), expected)
	cancel()
	b.close()
	_ = server.Close()
	<-served
	stats := <-clientDone
	report.ClientCalls, report.ResultOrder = stats.Calls, stats.Order
	if err != nil {
		return report, err
	}
	if stats.First != 1 || stats.Second != 1 || stats.Calls != 2 || report.Events.ToolStarts != 2 || report.Events.ToolTurns != wantTurns || report.Events.MessageStops != wantStops || !report.Events.SentinelSeen {
		return report, errRoundTrip
	}
	report.Passed = true
	return report, nil
}
