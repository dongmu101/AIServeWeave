package modelpull

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// patterned returns n deterministic, non-repeating-looking bytes so a hash
// over a wrong prefix or a wrong splice cannot accidentally match.
func patterned(n int) []byte {
	b := make([]byte, n)
	x := uint32(2463534242)
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

func rangeServer(t *testing.T, content []byte, requests *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			*requests++
		}
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Write(content)
			return
		}
		var n int
		fmt.Sscanf(rangeHeader, "bytes=%d-", &n)
		w.WriteHeader(http.StatusPartialContent)
		w.Write(content[n:])
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The digest computed while writing must equal a whole-file hash of what
// landed on disk, for a fresh download.
func TestPullOne_StreamingHashMatchesWholeFileHash_Fresh(t *testing.T) {
	content := patterned(3<<20 + 12345) // crosses several 1MiB copy buffers
	srv := rangeServer(t, content, nil)
	dir := t.TempDir()
	target := filepath.Join(dir, "m.bin")
	spec := Spec{Name: "m", SourceURL: srv.URL, SHA256: sha256Hex(content), TargetPath: target}

	if err := pullOne(context.Background(), http.DefaultClient, spec, pullOpts{}); err != nil {
		t.Fatalf("pullOne: %v", err)
	}
	whole, err := sha256File(target)
	if err != nil {
		t.Fatal(err)
	}
	if whole != spec.SHA256 {
		t.Fatalf("whole-file hash %s != spec %s", whole, spec.SHA256)
	}
	fi, _ := os.Stat(target)
	if cached, ok := readHashSidecar(target, fi); !ok || cached != whole {
		t.Fatalf("sidecar = %q (ok=%v), want the streaming digest %s", cached, ok, whole)
	}
}

// Resume: the existing prefix is hashed first, then the rest is streamed in;
// the combined digest must equal the whole-file digest.
func TestPullOne_StreamingHashMatchesWholeFileHash_Resume(t *testing.T) {
	content := patterned(3<<20 + 777)
	for _, prefix := range []int{1, 1 << 20, 2<<20 + 5, len(content) - 1} {
		t.Run(fmt.Sprintf("prefix=%d", prefix), func(t *testing.T) {
			var requests int
			srv := rangeServer(t, content, &requests)
			dir := t.TempDir()
			target := filepath.Join(dir, "m.bin")
			if err := os.WriteFile(target+".part", content[:prefix], 0o644); err != nil {
				t.Fatal(err)
			}
			spec := Spec{Name: "m", SourceURL: srv.URL, SHA256: sha256Hex(content), TargetPath: target}

			if err := pullOne(context.Background(), http.DefaultClient, spec, pullOpts{}); err != nil {
				t.Fatalf("pullOne: %v", err)
			}
			whole, err := sha256File(target)
			if err != nil {
				t.Fatal(err)
			}
			if whole != spec.SHA256 {
				t.Fatalf("whole-file hash %s != spec %s", whole, spec.SHA256)
			}
			fi, _ := os.Stat(target)
			if cached, ok := readHashSidecar(target, fi); !ok || cached != whole {
				t.Fatalf("sidecar = %q (ok=%v), want %s", cached, ok, whole)
			}
		})
	}
}

// The resumed prefix really is hashed from disk: a corrupted prefix must make
// the final digest wrong (and the artifact rejected), not be papered over.
func TestPullOne_ResumeWithCorruptPrefixIsRejected(t *testing.T) {
	content := patterned(2 << 20)
	srv := rangeServer(t, content, nil)
	dir := t.TempDir()
	target := filepath.Join(dir, "m.bin")
	bad := append([]byte(nil), content[:1<<20]...)
	bad[1000] ^= 0xff
	if err := os.WriteFile(target+".part", bad, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "m", SourceURL: srv.URL, SHA256: sha256Hex(content), TargetPath: target}

	err := pullOne(context.Background(), http.DefaultClient, spec, pullOpts{})
	if !errors.Is(err, errChecksumMismatch) {
		t.Fatalf("pullOne error = %v, want errChecksumMismatch", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt artifact must not be published, stat err = %v", statErr)
	}
	if _, statErr := os.Stat(target + ".part"); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt .part must be removed, stat err = %v", statErr)
	}
}

// When a server ignores Range and answers 200, the prefix hash is discarded
// and the full body is hashed from scratch.
func TestPullOne_ServerIgnoringRangeRestartsHash(t *testing.T) {
	content := patterned(2<<20 + 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(content) }))
	defer srv.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "m.bin")
	if err := os.WriteFile(target+".part", patterned(1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := Spec{Name: "m", SourceURL: srv.URL, SHA256: sha256Hex(content), TargetPath: target}
	if err := pullOne(context.Background(), http.DefaultClient, spec, pullOpts{}); err != nil {
		t.Fatalf("pullOne: %v", err)
	}
}

func TestPullOne_CanceledContextWhileHashingPrefix(t *testing.T) {
	content := patterned(1 << 20)
	srv := rangeServer(t, content, nil)
	dir := t.TempDir()
	target := filepath.Join(dir, "m.bin")
	if err := os.WriteFile(target+".part", content[:4096], 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	spec := Spec{Name: "m", SourceURL: srv.URL, SHA256: sha256Hex(content), TargetPath: target}
	if err := pullOne(ctx, http.DefaultClient, spec, pullOpts{}); !errors.Is(err, errFetchFailed) {
		t.Fatalf("pullOne error = %v, want errFetchFailed", err)
	}
	if _, err := os.Stat(target + ".part"); err != nil {
		t.Fatalf(".part must survive a canceled run for the next resume: %v", err)
	}
}

// The artifact is fsynced after its checksum verified and before the rename
// publishes it; a rejected artifact is never fsynced.
func TestPullOne_FsyncsAfterVerifyBeforeRename(t *testing.T) {
	content := patterned(1<<20 + 1)
	srv := rangeServer(t, content, nil)
	dir := t.TempDir()
	target := filepath.Join(dir, "m.bin")

	var calls int
	var partExisted, targetExisted bool
	orig := syncPartFile
	syncPartFile = func(f *os.File) error {
		calls++
		_, partErr := os.Stat(target + ".part")
		_, targetErr := os.Stat(target)
		partExisted, targetExisted = partErr == nil, targetErr == nil
		return f.Sync()
	}
	t.Cleanup(func() { syncPartFile = orig })

	good := Spec{Name: "m", SourceURL: srv.URL, SHA256: sha256Hex(content), TargetPath: target}
	if err := pullOne(context.Background(), http.DefaultClient, good, pullOpts{}); err != nil {
		t.Fatalf("pullOne: %v", err)
	}
	if calls != 1 || !partExisted || targetExisted {
		t.Fatalf("sync calls=%d partExisted=%v targetExisted=%v, want 1 call while only the .part exists", calls, partExisted, targetExisted)
	}

	calls = 0
	bad := Spec{Name: "m2", SourceURL: srv.URL, SHA256: sha256Hex([]byte("other")), TargetPath: filepath.Join(dir, "m2.bin")}
	if err := pullOne(context.Background(), http.DefaultClient, bad, pullOpts{}); !errors.Is(err, errChecksumMismatch) {
		t.Fatalf("pullOne error = %v, want errChecksumMismatch", err)
	}
	if calls != 0 {
		t.Fatalf("sync called %d times for a rejected artifact, want 0", calls)
	}
}

func TestPullOne_SyncFailureAbortsBeforeRename(t *testing.T) {
	content := patterned(1024)
	srv := rangeServer(t, content, nil)
	target := filepath.Join(t.TempDir(), "m.bin")
	orig := syncPartFile
	syncPartFile = func(*os.File) error { return errors.New("disk on fire") }
	t.Cleanup(func() { syncPartFile = orig })

	spec := Spec{Name: "m", SourceURL: srv.URL, SHA256: sha256Hex(content), TargetPath: target}
	if err := pullOne(context.Background(), http.DefaultClient, spec, pullOpts{}); !errors.Is(err, errStorageFailed) {
		t.Fatalf("pullOne error = %v, want errStorageFailed", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target must not be published after a failed fsync, stat err = %v", err)
	}
}

// seedTarget writes content to a fresh target file and returns its Spec.
func seedTarget(t *testing.T, content []byte) Spec {
	t.Helper()
	target := filepath.Join(t.TempDir(), "m.bin")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return Spec{Name: "m", SHA256: sha256Hex(content), TargetPath: target}
}

func TestAlreadySatisfied_WritesSidecarThenSkipsHashing(t *testing.T) {
	content := patterned(4096)
	spec := seedTarget(t, content)

	if !alreadySatisfied(spec) {
		t.Fatal("first check: want satisfied")
	}
	fi, _ := os.Stat(spec.TargetPath)
	if cached, ok := readHashSidecar(spec.TargetPath, fi); !ok || cached != spec.SHA256 {
		t.Fatalf("sidecar after a full hash = %q (ok=%v), want %s", cached, ok, spec.SHA256)
	}

	// Same length, same mtime, different bytes: only a skipped hash can still
	// say "satisfied". This pins the documented trust boundary of the cache.
	mtime := fi.ModTime()
	tampered := append([]byte(nil), content...)
	tampered[0] ^= 0xff
	if err := os.WriteFile(spec.TargetPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(spec.TargetPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if !alreadySatisfied(spec) {
		t.Fatal("sidecar hit: want satisfied without re-reading the file")
	}
}

func TestAlreadySatisfied_ModifiedFileIsRehashed(t *testing.T) {
	content := patterned(4096)
	for name, mutate := range map[string]func(spec Spec){
		"size changed": func(spec Spec) {
			if err := os.WriteFile(spec.TargetPath, append(append([]byte(nil), content...), 'x'), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"mtime changed": func(spec Spec) {
			tampered := append([]byte(nil), content...)
			tampered[10] ^= 0xff
			if err := os.WriteFile(spec.TargetPath, tampered, 0o644); err != nil {
				t.Fatal(err)
			}
			later := time.Now().Add(time.Hour)
			if err := os.Chtimes(spec.TargetPath, later, later); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := seedTarget(t, content)
			if !alreadySatisfied(spec) {
				t.Fatal("setup: want satisfied")
			}
			mutate(spec)
			if alreadySatisfied(spec) {
				t.Fatal("modified file: want not satisfied (sidecar must not be trusted)")
			}
		})
	}
}

// A file whose content changed back to the right bytes after an mtime bump is
// re-hashed, found valid, and re-cached for its new mtime.
func TestAlreadySatisfied_RehashRefreshesSidecar(t *testing.T) {
	content := patterned(2048)
	spec := seedTarget(t, content)
	if !alreadySatisfied(spec) {
		t.Fatal("setup")
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(spec.TargetPath, later, later); err != nil {
		t.Fatal(err)
	}
	if !alreadySatisfied(spec) {
		t.Fatal("same bytes, new mtime: want satisfied via full hash")
	}
	fi, _ := os.Stat(spec.TargetPath)
	if _, ok := readHashSidecar(spec.TargetPath, fi); !ok {
		t.Fatal("sidecar was not refreshed for the new mtime")
	}
}

func TestAlreadySatisfied_BadSidecarFallsBackToFullHash(t *testing.T) {
	content := patterned(2048)
	other := sha256Hex([]byte("some other file"))
	for name, sidecar := range map[string]func(fi os.FileInfo) string{
		"garbage": func(os.FileInfo) string { return "not json{" },
		"empty":   func(os.FileInfo) string { return "" },
		"wrong digest": func(fi os.FileInfo) string {
			return fmt.Sprintf(`{"size":%d,"mtime_unix_nano":%d,"sha256":%q}`, fi.Size(), fi.ModTime().UnixNano(), other)
		},
		"short digest": func(fi os.FileInfo) string {
			return fmt.Sprintf(`{"size":%d,"mtime_unix_nano":%d,"sha256":"abc"}`, fi.Size(), fi.ModTime().UnixNano())
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := seedTarget(t, content)
			fi, _ := os.Stat(spec.TargetPath)
			if err := os.WriteFile(spec.TargetPath+hashSidecarSuffix, []byte(sidecar(fi)), 0o644); err != nil {
				t.Fatal(err)
			}
			if !alreadySatisfied(spec) {
				t.Fatal("good file with an unusable sidecar: want satisfied via full hash")
			}
			// And a wrong file is still rejected regardless of what the sidecar says.
			bad := spec
			bad.SHA256 = other
			if alreadySatisfied(bad) {
				t.Fatal("digest differs from the file: want not satisfied")
			}
		})
	}
}

func TestAlreadySatisfied_MissingTargetOrDirectory(t *testing.T) {
	dir := t.TempDir()
	if alreadySatisfied(Spec{TargetPath: filepath.Join(dir, "absent.bin"), SHA256: sha256Hex(nil)}) {
		t.Fatal("missing target: want not satisfied")
	}
	if alreadySatisfied(Spec{TargetPath: dir, SHA256: sha256Hex(nil)}) {
		t.Fatal("directory target: want not satisfied")
	}
}

// pullOne replacing an existing artifact must not leave the old file's
// sidecar vouching for the new one.
func TestPullOne_ReplacingArtifactRefreshesSidecar(t *testing.T) {
	oldContent := patterned(4096)
	spec := seedTarget(t, oldContent)
	if !alreadySatisfied(spec) {
		t.Fatal("setup")
	}
	newContent := patterned(8192)
	srv := rangeServer(t, newContent, nil)
	spec.SourceURL, spec.SHA256 = srv.URL, sha256Hex(newContent)
	if err := pullOne(context.Background(), http.DefaultClient, spec, pullOpts{}); err != nil {
		t.Fatalf("pullOne: %v", err)
	}
	fi, _ := os.Stat(spec.TargetPath)
	if cached, ok := readHashSidecar(spec.TargetPath, fi); !ok || cached != spec.SHA256 {
		t.Fatalf("sidecar = %q (ok=%v), want the new digest", cached, ok)
	}
}
