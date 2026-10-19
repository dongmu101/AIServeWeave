package geminicli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"AIServeWeave/common/runtime"
)

const (
	maxFrameBytes    = 1 << 20
	maxProtocolBytes = 4 << 20
	protocolVersion  = "2024-11-05"
)

func envValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == key {
			value = v
		}
	}
	return value
}

func (r *Runtime) localServer() (string, string, error) {
	key := ""
	for _, name := range []string{"NANOBANANA_API_KEY", "NANOBANANA_GEMINI_API_KEY", "NANOBANANA_GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		if key = envValue(r.env, name); key != "" {
			break
		}
	}
	if key == "" {
		return "", "", errAuthentication
	}
	home := envValue(r.env, "GEMINI_CLI_HOME")
	if home == "" {
		home = envValue(r.env, "HOME")
	}
	if !filepath.IsAbs(home) {
		return "", "", errProcess
	}
	dir := filepath.Join(home, ".gemini", "extensions", "nanobanana")
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", "", errProcess
	}
	defer root.Close()
	f, err := root.Open("gemini-extension.json")
	if err != nil {
		return "", "", errProcess
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return "", "", errProcess
	}
	var manifest struct {
		Name    string `json:"name"`
		Servers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Name != "nanobanana" {
		return "", "", errProcess
	}
	server := manifest.Servers["nanobanana"]
	if server.Command != "node" || len(server.Args) != 1 || server.Args[0] != "${extensionPath}/mcp-server/dist/index.js" || len(server.Env) != 0 {
		return "", "", errProcess
	}
	info, err := root.Stat("mcp-server/dist/index.js")
	if err != nil || !info.Mode().IsRegular() {
		return "", "", errProcess
	}
	return filepath.Join(dir, "mcp-server", "dist", "index.js"), key, nil
}

// childEnv passes only image credentials and transport settings, isolating Node's home.
// childEnv 只传递生图凭据和网络设置，并隔离 Node 的主目录。
func (r *Runtime) childEnv(dir, executable, key string) []string {
	allowed := map[string]bool{"LANG": true, "LC_ALL": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true, "SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "NODE_EXTRA_CA_CERTS": true, "NANOBANANA_MODEL": true}
	out := []string{"HOME=" + dir, "TMPDIR=" + dir, "NANOBANANA_API_KEY=" + key, "PATH=" + filepath.Dir(executable) + string(os.PathListSeparator) + envValue(r.env, "PATH")}
	for _, entry := range r.env {
		if k, _, ok := strings.Cut(entry, "="); ok && allowed[k] {
			out = append(out, entry)
		}
	}
	return out
}

// withServer owns one isolated MCP process, discarding every diagnostic byte.
// withServer 管理一个隔离 MCP 进程，丢弃全部诊断输出。
func (r *Runtime) withServer(ctx context.Context, run func(*protocol, string) error) error {
	script, key, err := r.localServer()
	if err != nil {
		return err
	}
	executable, err := exec.LookPath(r.node)
	if err != nil {
		return errProcess
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return errProcess
	}
	dir, err := os.MkdirTemp("", "aisw-gemini-image-")
	if err != nil {
		return errProcess
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, executable, script)
	cmd.Dir, cmd.Env, cmd.Stderr = dir, r.childEnv(dir, executable, key), io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := configureProcess(cmd); err != nil {
		return err
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return errProcess
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return errProcess
	}
	defer out.Close()
	if cmd.Start() != nil {
		return errProcess
	}
	reaped := false
	reap := func() {
		if !reaped {
			_ = cmd.Cancel()
			_ = in.Close()
			_ = out.Close()
			_ = cmd.Wait()
			reaped = true
		}
	}
	defer reap()
	p := &protocol{scanner: bufio.NewScanner(out), output: in, reap: reap}
	p.scanner.Buffer(make([]byte, 4096), maxFrameBytes+1)
	return run(p, dir)
}

type protocol struct {
	scanner *bufio.Scanner
	output  io.Writer
	bytes   int
	reap    func()
}

func (p *protocol) write(v any) error {
	if json.NewEncoder(p.output).Encode(v) != nil {
		return errProcess
	}
	return nil
}

func (p *protocol) request(id int, method string, params any, result any) error {
	if err := p.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for notifications := 0; notifications < 128; notifications++ {
		if !p.scanner.Scan() {
			return errProtocol
		}
		raw := p.scanner.Bytes()
		p.bytes += len(raw) + 1
		if len(raw) > maxFrameBytes || p.bytes > maxProtocolBytes {
			return errProtocol
		}
		var msg struct {
			Version string          `json:"jsonrpc"`
			ID      *int            `json:"id"`
			Method  string          `json:"method"`
			Result  json.RawMessage `json:"result"`
			Error   *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &msg) != nil || msg.Version != "2.0" {
			return errProtocol
		}
		if msg.Method != "" {
			if msg.ID != nil || (msg.Method != "notifications/message" && msg.Method != "notifications/progress") {
				return errProtocol
			}
			continue
		}
		if msg.ID == nil || *msg.ID != id {
			return errProtocol
		}
		if msg.Error != nil {
			return upstreamError(msg.Error.Message)
		}
		if len(msg.Result) == 0 || string(msg.Result) == "null" || json.Unmarshal(msg.Result, result) != nil {
			return errProtocol
		}
		return nil
	}
	return errProtocol
}

func (p *protocol) initialize() error {
	var hello struct {
		Version      string `json:"protocolVersion"`
		Capabilities struct {
			Tools json.RawMessage `json:"tools"`
		} `json:"capabilities"`
		Info struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := p.request(1, "initialize", map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "aisw-gemini-image", "version": "1.0.0"}}, &hello); err != nil {
		return err
	}
	if hello.Version != protocolVersion || hello.Info.Name != "nanobanana-server" || len(hello.Capabilities.Tools) == 0 || string(hello.Capabilities.Tools) == "null" {
		return errProtocol
	}
	if err := p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return err
	}
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := p.request(2, "tools/list", map[string]any{}, &list); err != nil {
		return err
	}
	for _, tool := range list.Tools {
		if tool.Name == "generate_image" {
			return nil
		}
	}
	return runtime.ErrCapabilityUnsupported
}

func upstreamError(message string) error {
	message = strings.ToLower(message)
	switch {
	case strings.Contains(message, "authentication failed"), strings.Contains(message, "api key not valid"), strings.Contains(message, "permission denied"), strings.Contains(message, "unauthenticated"):
		return errAuthentication
	case strings.Contains(message, "quota exceeded"), strings.Contains(message, "resource_exhausted"), strings.Contains(message, "429"):
		return errRateLimited
	default:
		return errUpstream
	}
}
