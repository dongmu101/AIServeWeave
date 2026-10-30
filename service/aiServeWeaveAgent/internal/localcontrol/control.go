// Package localcontrol exposes bounded, user-private local maintenance actions.
// Package localcontrol 提供有界且仅本用户可访问的本地维护动作。
package localcontrol

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Handlers contains the closed set of local maintenance operations.
// Handlers 包含封闭集合中的本地维护操作。
type Handlers struct {
	Status func() any
	Reload func(context.Context) error
	Stop   func()
}

type connection interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
}
type listener interface {
	Accept() (connection, error)
	Close() error
}

func endpointKey(key string) (string, error) {
	key, err := filepath.Abs(key)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:12]), nil
}

// Serve starts a private endpoint and returns an idempotent, joining close.
// Serve 启动私有端点，返回幂等且等待服务结束的关闭函数。
func Serve(ctx context.Context, key string, h Handlers) (func(), error) {
	ln, err := listen(key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	var active connection
	reloadSlot := make(chan struct{}, 1)
	done := make(chan struct{})
	after := context.AfterFunc(ctx, func() {
		_ = ln.Close()
		mu.Lock()
		if active != nil {
			_ = active.Close()
		}
		mu.Unlock()
	})
	go func() {
		defer close(done)
		defer ln.Close()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			active = c
			mu.Unlock()
			if ctx.Err() != nil {
				_ = c.Close()
				return
			}
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			line, err := bufio.NewReader(io.LimitReader(c, 64)).ReadString('\n')
			action := strings.TrimSuffix(line, "\n")
			ok := err == nil
			var result any
			if ok {
				switch action {
				case "status":
					if h.Status != nil {
						result = h.Status()
					}
				case "reload":
					if h.Reload == nil {
						ok = false
					} else {
						select {
						case reloadSlot <- struct{}{}:
							rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
							result := make(chan error, 1)
							go func() { defer func() { <-reloadSlot }(); result <- h.Reload(rctx) }()
							select {
							case err := <-result:
								ok = err == nil
							case <-rctx.Done():
								ok = false
							}
							rcancel()
						default:
							ok = false
						}
					}
				case "stop":
					ok = h.Stop != nil
				default:
					ok = false
				}
			}
			data, _ := json.Marshal(map[string]any{"ok": ok, "status": result})
			if len(data) > 64*1024 {
				data = []byte(`{"ok":false}`)
			}
			if _, err := c.Write(append(data, '\n')); err == nil {
				// Keep named-pipe buffers alive until the client confirms receipt.
				// 在客户端确认收到响应前保留 named pipe 缓冲。
				var ack [1]byte
				_, _ = io.ReadFull(c, ack[:])
			}
			_ = c.Close()
			mu.Lock()
			active = nil
			mu.Unlock()
			if action == "stop" && ok {
				h.Stop()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { cancel(); <-done; after() }) }, nil
}

// Request invokes one known action and reads at most 64 KiB of redacted status.
// Request 调用一个已知动作，读取至多 64 KiB 的脱敏状态。
func Request(ctx context.Context, key, action string) ([]byte, error) {
	if action != "status" && action != "stop" && action != "reload" {
		return nil, errors.New("unknown local action")
	}
	c, err := dial(ctx, key)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	deadline := time.Now().Add(6 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(c, action+"\n"); err != nil {
		return nil, err
	}
	data, err := bufio.NewReader(io.LimitReader(c, 65537)).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if len(data) > 65536 || json.Unmarshal(data, &result) != nil || !result.OK {
		return nil, errors.New("local action failed")
	}
	if _, err := c.Write([]byte{1}); err != nil {
		return nil, err
	}
	return data, nil
}
