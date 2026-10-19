// Package geminitest supplies an offline Nano Banana MCP process and clock.
// Package geminitest 提供离线 Nano Banana MCP 进程与测试时钟。
package geminitest

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PNG is a complete one-pixel PNG fixture.
// PNG 是完整的单像素 PNG 测试数据。
const PNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aDHsAAAAASUVORK5CYII="

// Serve runs the fake stdio server, selecting behavior from the script file.
// Serve 从脚本文件选择行为并运行假的 stdio 服务。
func Serve(script string) int {
	data, err := os.ReadFile(script)
	if err != nil {
		return 2
	}
	mode := string(data)
	workdir, _ := os.Getwd()
	workInfo, _ := os.Stat(workdir)
	homeInfo, _ := os.Stat(os.Getenv("HOME"))
	tmpInfo, _ := os.Stat(os.Getenv("TMPDIR"))
	if workInfo == nil || homeInfo == nil || tmpInfo == nil || !os.SameFile(workInfo, homeInfo) || !os.SameFile(workInfo, tmpInfo) || os.Getenv("NODE_OPTIONS") != "" || os.Getenv("UNRELATED_API_KEY") != "" || os.Getenv("NANOBANANA_API_KEY") != "fixture-key" {
		return 3
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(script), "last-workdir"), []byte(workdir), 0o600)
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	initialized := false
	for {
		var msg struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if dec.Decode(&msg) != nil {
			return 0
		}
		result := any(map[string]any{})
		switch msg.Method {
		case "initialize":
			version := "2024-11-05"
			if mode == "bad-version" {
				version = "unknown"
			}
			result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "nanobanana-server", "version": "1.0.0"}}
		case "notifications/initialized":
			initialized = true
			continue
		case "tools/list":
			if !initialized {
				return 4
			}
			name := "generate_image"
			if mode == "missing-tool" {
				name = "edit_image"
			}
			result = map[string]any{"tools": []any{map[string]any{"name": name, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"prompt": map[string]string{"type": "string"}}, "required": []string{"prompt"}}}}}
		case "tools/call":
			if !initialized || mode == "probe" {
				return 5
			}
			var p struct {
				Name      string `json:"name"`
				Arguments struct {
					Prompt    string `json:"prompt"`
					Count     int    `json:"outputCount"`
					Preview   bool   `json:"preview"`
					NoPreview bool   `json:"noPreview"`
					Format    string `json:"format"`
				} `json:"arguments"`
			}
			if json.Unmarshal(msg.Params, &p) != nil || p.Name != "generate_image" || p.Arguments.Count != 1 || p.Arguments.Preview || !p.Arguments.NoPreview || p.Arguments.Format != "separate" || p.Arguments.Prompt == "" {
				return 6
			}
			_ = os.WriteFile(filepath.Join(filepath.Dir(script), "last-request"), msg.Params, 0o600)
			switch mode {
			case "wait":
				_ = os.WriteFile(filepath.Join(filepath.Dir(script), "ready"), []byte("ready"), 0o600)
				var ignored any
				_ = dec.Decode(&ignored)
				return 0
			case "rpc-error", "auth", "quota":
				message := "SECRET_PROMPT fixture-key /operator/private"
				if mode == "auth" {
					message += " authentication failed"
				}
				if mode == "quota" {
					message += " quota exceeded"
				}
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32603, "message": message}})
				continue
			case "server-request":
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "sampling/createMessage", "params": map[string]any{}})
				continue
			case "malformed":
				_, _ = os.Stdout.WriteString("SECRET_PROMPT\n")
				continue
			case "oversized-frame":
				_, _ = os.Stdout.WriteString(strings.Repeat("x", (1<<20)+2) + "\n")
				continue
			case "wrong-id":
				msg.ID++
			case "tool-error":
				result = map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": "SECRET_PROMPT"}}}
				_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
				continue
			}
			out := filepath.Join(workdir, "nanobanana-output")
			if mode == "symlink-directory" {
				_ = os.Symlink(filepath.Dir(script), out)
			} else if mode != "no-image" {
				_ = os.Mkdir(out, 0o700)
				image, _ := base64.StdEncoding.DecodeString(PNG)
				if mode == "large" {
					image = append(image, make([]byte, 600<<10)...)
				}
				if mode == "invalid-image" {
					image = []byte("SECRET_PROMPT")
				}
				file := filepath.Join(out, "result.png")
				if mode == "symlink-file" {
					_ = os.Symlink(script, file)
				} else {
					_ = os.WriteFile(file, image, 0o600)
				}
				if mode == "extra-image" {
					_ = os.WriteFile(filepath.Join(out, "extra.png"), image, 0o600)
				}
				if mode == "too-large" {
					_ = os.Truncate(file, (32<<20)+1)
				}
			}
			// Report an unrelated path: the adapter must inspect only its own output directory.
			// 报告一个无关路径：适配器必须只检查自己的输出目录。
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "Generated files:\n• /operator/private.png"}}}
		default:
			return 7
		}
		if enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result}) != nil {
			return 8
		}
	}
}

// Clock advances only when Fire is called.
// Clock 只在调用 Fire 时推进时间。
type Clock struct {
	mu     sync.Mutex
	timers map[chan time.Time]bool
}

// Now returns a stable timestamp.
// Now 返回固定时间戳。
func (c *Clock) Now() time.Time { return time.Unix(0, 0) }

// NewTimer registers a manually fired timer.
// NewTimer 注册手动触发的定时器。
func (c *Clock) NewTimer(time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timers == nil {
		c.timers = make(map[chan time.Time]bool)
	}
	ch := make(chan time.Time, 1)
	c.timers[ch] = true
	return ch, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		active := c.timers[ch]
		delete(c.timers, ch)
		return active
	}
}

// Fire expires every outstanding timer.
// Fire 使全部未结束的定时器到期。
func (c *Clock) Fire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for ch := range c.timers {
		ch <- c.Now()
		delete(c.timers, ch)
	}
}
