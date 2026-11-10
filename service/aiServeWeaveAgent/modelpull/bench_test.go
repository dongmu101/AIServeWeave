package modelpull

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// benchSource serves size bytes of a repeating 1MiB pattern without ever
// holding the whole body in memory, and returns the body's SHA-256.
//
// benchSource 以重复的 1MiB 模式提供 size 字节的响应体，而不必把整个 body
// 放进内存，并返回该 body 的 SHA-256。
func benchSource(tb testing.TB, size int64) (*httptest.Server, string) {
	tb.Helper()
	block := make([]byte, 1<<20)
	for i := range block {
		block[i] = byte(i * 7)
	}
	h := sha256.New()
	for written := int64(0); written < size; written += int64(len(block)) {
		h.Write(block[:min(int64(len(block)), size-written)])
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		for written := int64(0); written < size; written += int64(len(block)) {
			if _, err := w.Write(block[:min(int64(len(block)), size-written)]); err != nil {
				return
			}
		}
	}))
	tb.Cleanup(srv.Close)
	return srv, hex.EncodeToString(h.Sum(nil))
}

const benchSize = 256 << 20

func benchmarkPullOne(b *testing.B, withLedger bool) {
	srv, digest := benchSource(b, benchSize)
	dir := b.TempDir()
	b.SetBytes(benchSize)
	b.ReportAllocs()
	var saves int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		target := filepath.Join(dir, "model"+strconv.Itoa(i)+".bin")
		spec := Spec{Name: "bench", SourceURL: srv.URL, SHA256: digest, SizeBytes: benchSize, TargetPath: target}
		opts := pullOpts{}
		var ledger *Ledger
		if withLedger {
			ledger = &Ledger{Path: filepath.Join(dir, "ledger"+strconv.Itoa(i)+".json")}
			opts.Ledger = ledger
			opts.LedgerQuotaBytes = 1 << 40
		}
		if err := pullOne(context.Background(), http.DefaultClient, spec, opts); err != nil {
			b.Fatal(err)
		}
		if ledger != nil {
			saves += ledger.saves.Load()
		}
		b.StopTimer()
		os.Remove(target)
		b.StartTimer()
	}
	if withLedger {
		b.ReportMetric(float64(saves)/float64(b.N), "ledger-saves/op")
	}
}

// BenchmarkPullOne_256MiB downloads and verifies a 256MiB artifact from a
// local httptest source with no quota bookkeeping.
//
// BenchmarkPullOne_256MiB 从本地 httptest 来源下载并校验一个 256MiB 制品，
// 不带任何配额记账。
func BenchmarkPullOne_256MiB(b *testing.B) { benchmarkPullOne(b, false) }

// BenchmarkPullOne_256MiB_Ledger is the same download with a persistent
// Ledger attached; ledger-saves/op is the number of ledger file rewrites.
//
// BenchmarkPullOne_256MiB_Ledger 是同样的下载，但挂了持久化 Ledger；
// ledger-saves/op 是账本文件被重写的次数。
func BenchmarkPullOne_256MiB_Ledger(b *testing.B) { benchmarkPullOne(b, true) }

func benchmarkAlreadySatisfied(b *testing.B, dropSidecar bool) {
	dir := b.TempDir()
	target := filepath.Join(dir, "model.bin")
	block := bytes.Repeat([]byte{0x5a}, 1<<20)
	f, err := os.Create(target)
	if err != nil {
		b.Fatal(err)
	}
	h := sha256.New()
	for i := 0; i < benchSize>>20; i++ {
		f.Write(block)
		h.Write(block)
	}
	f.Close()
	spec := Spec{Name: "bench", SHA256: hex.EncodeToString(h.Sum(nil)), TargetPath: target}
	if !alreadySatisfied(spec) { // also primes the sidecar
		b.Fatal("not satisfied")
	}
	b.SetBytes(benchSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if dropSidecar {
			b.StopTimer()
			os.Remove(target + hashSidecarSuffix)
			b.StartTimer()
		}
		if !alreadySatisfied(spec) {
			b.Fatal("not satisfied")
		}
	}
}

// BenchmarkAlreadySatisfied_256MiB measures the "target is already in place"
// check that runs at every startup and every Gateway trigger, with the hash
// sidecar present (steady state).
//
// BenchmarkAlreadySatisfied_256MiB 度量每次启动与每次 Gateway 触发时都会执行
// 的"目标已就位"检查，sidecar 存在（稳态）。
func BenchmarkAlreadySatisfied_256MiB(b *testing.B) { benchmarkAlreadySatisfied(b, false) }

// BenchmarkAlreadySatisfied_256MiB_NoSidecar is the same check when the
// sidecar is missing or stale, which is the only behavior before sidecars.
//
// BenchmarkAlreadySatisfied_256MiB_NoSidecar 是 sidecar 缺失或过期时的同一项
// 检查，也是引入 sidecar 之前的唯一行为。
func BenchmarkAlreadySatisfied_256MiB_NoSidecar(b *testing.B) { benchmarkAlreadySatisfied(b, true) }
