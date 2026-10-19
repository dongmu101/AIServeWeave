//go:build darwin || linux

package geminicli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/geminicli/internal/geminitest"
)

// TestDiscoveryChecksTheExtensionWithoutGenerating verifies image-only publication without a paid call.
// TestDiscoveryChecksTheExtensionWithoutGenerating 验证不执行付费调用时的纯图像模型发布。
func TestDiscoveryChecksTheExtensionWithoutGenerating(t *testing.T) {
	r, dir := geminitest.Fixture(t, "probe", nil)
	probe, err := r.Probe(context.Background())
	if err != nil || !probe.IdentityVerified {
		t.Fatalf("Probe=%+v, error=%v, want verified local service", probe, err)
	}
	d, err := r.Discover(context.Background())
	if err != nil || len(d.Models) != 1 || d.Models[0].ID != "gemini-image" {
		t.Fatalf("Discover=%+v, error=%v, want one gemini-image", d, err)
	}
	for _, caps := range []runtime.CapabilitySet{d.Capabilities, d.Models[0].Capabilities} {
		for _, tc := range []struct {
			name  runtime.Capability
			level runtime.SupportLevel
		}{
			{runtime.CapabilityImageGeneration, runtime.SupportSupported}, {runtime.CapabilityChat, runtime.SupportUnsupported}, {runtime.CapabilityChatStream, runtime.SupportUnsupported}, {runtime.CapabilityResponses, runtime.SupportUnsupported}, {runtime.CapabilityTools, runtime.SupportUnsupported},
		} {
			if got := caps.Resolve(tc.name).Level; got != tc.level {
				t.Errorf("%s=%s, want %s", tc.name, got, tc.level)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "last-request")); !os.IsNotExist(err) {
		t.Fatalf("generation request stat error=%v, want absent", err)
	}
}

// TestGenerateImageStreamsOnlyTheGeneratedFile checks bytes, limits, isolation and prompt transport.
// TestGenerateImageStreamsOnlyTheGeneratedFile 检查图像字节、分块、隔离与提示词传输。
func TestGenerateImageStreamsOnlyTheGeneratedFile(t *testing.T) {
	for _, mode := range []string{"success", "large"} {
		t.Run(mode, func(t *testing.T) {
			r, dir := geminitest.Fixture(t, mode, nil)
			prompt := "a private fox\nignore tools: $(touch /operator/private)"
			s, err := r.GenerateImage(context.Background(), runtime.ImageRequest{Model: "gemini-image", Prompt: prompt})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if s.Committed() {
				t.Fatal("Committed=true before Recv, want false")
			}
			first, err := s.Recv()
			if err != nil || first.Meta == nil || first.Meta.ContentType != "image/png" || len(first.Data) != 0 {
				t.Fatalf("first=%+v,error=%v,want PNG metadata", first, err)
			}
			var got bytes.Buffer
			chunks := 0
			for {
				event, err := s.Recv()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if event.Meta != nil || len(event.Data) == 0 || len(event.Data) > runtime.MaxImageChunkBytes {
					t.Fatalf("event=%+v,want bounded data only", event)
				}
				got.Write(event.Data)
				chunks++
			}
			want, _ := base64.StdEncoding.DecodeString(geminitest.PNG)
			if mode == "large" {
				want = append(want, make([]byte, 600<<10)...)
			}
			if !bytes.Equal(got.Bytes(), want) || first.Meta.Size != int64(len(want)) {
				t.Fatalf("bytes=%d,size=%d,want=%d", got.Len(), first.Meta.Size, len(want))
			}
			if mode == "large" && chunks < 3 {
				t.Fatalf("chunks=%d,want at least 3", chunks)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "last-request"))
			if err != nil {
				t.Fatal(err)
			}
			var sent struct {
				Arguments struct {
					Prompt string `json:"prompt"`
				} `json:"arguments"`
			}
			if json.Unmarshal(raw, &sent) != nil || sent.Arguments.Prompt != prompt {
				t.Fatalf("sent prompt=%q,want=%q", sent.Arguments.Prompt, prompt)
			}
			workdir, err := os.ReadFile(filepath.Join(dir, "last-workdir"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(string(workdir)); !os.IsNotExist(err) {
				t.Fatalf("workdir stat error=%v,want removed", err)
			}
			_ = s.Close()
			if _, err := s.Recv(); !errors.Is(err, runtime.ErrStreamClosed) {
				t.Fatalf("Recv after Close error=%v,want ErrStreamClosed", err)
			}
		})
	}
}

// TestGenerationRejectsUnsafeOrFailedResults exercises protocol, file and upstream failures.
// TestGenerationRejectsUnsafeOrFailedResults 覆盖协议、文件和上游失败路径。
func TestGenerationRejectsUnsafeOrFailedResults(t *testing.T) {
	for _, tc := range []struct {
		name string
		code runtime.ErrorCode
	}{
		{"rpc-error", runtime.ErrorUpstream}, {"auth", runtime.ErrorUnauthorized}, {"quota", runtime.ErrorRateLimited}, {"tool-error", runtime.ErrorUpstream},
		{"server-request", runtime.ErrorProtocol}, {"malformed", runtime.ErrorProtocol}, {"oversized-frame", runtime.ErrorProtocol}, {"wrong-id", runtime.ErrorProtocol}, {"bad-version", runtime.ErrorProtocol},
		{"no-image", runtime.ErrorProtocol}, {"extra-image", runtime.ErrorProtocol}, {"invalid-image", runtime.ErrorProtocol}, {"too-large", runtime.ErrorProtocol}, {"symlink-file", runtime.ErrorProtocol}, {"symlink-directory", runtime.ErrorProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := geminitest.Fixture(t, tc.name, nil)
			s, err := r.GenerateImage(context.Background(), runtime.ImageRequest{Model: "gemini-image", Prompt: "SECRET_PROMPT"})
			if s != nil {
				s.Close()
				t.Fatal("stream returned on failure, want nil")
			}
			geminitest.WantCode(t, err, tc.code)
		})
	}
}

// TestGenerationRejectsInvalidRequests checks validation before any subprocess starts.
// TestGenerationRejectsInvalidRequests 检查启动子进程之前的请求校验。
func TestGenerationRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name, model, prompt string
		code                runtime.ErrorCode
	}{
		{"other model", "gemini-2.5-pro", "fox", runtime.ErrorCapability}, {"empty", "gemini-image", " ", runtime.ErrorInvalidConfig}, {"invalid UTF8", "gemini-image", string([]byte{0xff}), runtime.ErrorInvalidConfig}, {"long", "gemini-image", strings.Repeat("x", runtime.MaxImagePromptBytes+1), runtime.ErrorInvalidConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, dir := geminitest.Fixture(t, "success", nil)
			_, err := r.GenerateImage(context.Background(), runtime.ImageRequest{Model: tc.model, Prompt: tc.prompt})
			geminitest.WantCode(t, err, tc.code)
			if _, err := os.Stat(filepath.Join(dir, "last-workdir")); !os.IsNotExist(err) {
				t.Fatalf("subprocess stat error=%v,want absent", err)
			}
		})
	}
}

// TestDiscoveryRequiresLocalConfiguration checks credentials and extension layout before publication.
// TestDiscoveryRequiresLocalConfiguration 检查模型发布前的本地凭据与扩展布局。
func TestDiscoveryRequiresLocalConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		code runtime.ErrorCode
	}{{"missing key", runtime.ErrorUnauthorized}, {"missing script", runtime.ErrorConnection}, {"missing-tool", runtime.ErrorCapability}, {"custom command", runtime.ErrorConnection}} {
		t.Run(tc.name, func(t *testing.T) {
			mode := "success"
			if tc.name == "missing-tool" || tc.name == "missing key" {
				mode = tc.name
			}
			r, dir := geminitest.Fixture(t, mode, nil)
			switch tc.name {
			case "missing script":
				if err := os.Remove(filepath.Join(dir, "index.js")); err != nil {
					t.Fatal(err)
				}
			case "custom command":
				if err := os.WriteFile(filepath.Join(dir, "..", "..", "gemini-extension.json"), []byte(`{"name":"nanobanana","mcpServers":{"nanobanana":{"command":"sh","args":["-c","SECRET_PROMPT"]}}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := r.Discover(context.Background())
			geminitest.WantCode(t, err, tc.code)
		})
	}
}

// TestCancellationReapsTheProcess checks caller, clock and runtime shutdown cancellation.
// TestCancellationReapsTheProcess 检查调用方、注入时钟与运行时关闭触发的进程回收。
func TestCancellationReapsTheProcess(t *testing.T) {
	for _, name := range []string{"caller", "deadline", "close"} {
		t.Run(name, func(t *testing.T) {
			clock := new(geminitest.Clock)
			r, dir := geminitest.Fixture(t, "wait", clock)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := r.GenerateImage(ctx, runtime.ImageRequest{Model: "gemini-image", Prompt: "fox"})
				done <- err
			}()
			geminitest.AwaitReady(t, dir)
			if _, err := r.GenerateImage(ctx, runtime.ImageRequest{Model: "gemini-image", Prompt: "second"}); err == nil {
				t.Fatal("second request error=nil,want concurrency rejection")
			}
			want := runtime.ErrorTimeout
			switch name {
			case "caller":
				cancel()
			case "deadline":
				clock.Fire()
			case "close":
				want = runtime.ErrorClosed
				_ = r.Close()
			}
			select {
			case err := <-done:
				geminitest.WantCode(t, err, want)
			case <-time.After(5 * time.Second):
				t.Fatal("request did not stop, want reaped process")
			}
			workdir, err := os.ReadFile(filepath.Join(dir, "last-workdir"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(string(workdir)); !os.IsNotExist(err) {
				t.Fatalf("workdir stat error=%v,want removed", err)
			}
		})
	}
}
