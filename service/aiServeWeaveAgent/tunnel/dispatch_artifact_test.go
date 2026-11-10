package tunnel

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"sync"
	"testing"

	"AIServeWeave/common/runtime"
)

func TestArtifactBufferSize(t *testing.T) {
	const maxFrame = 4 << 20
	tests := []struct {
		name     string
		size     int64
		maxFrame int
		want     int
	}{
		{"small known size", 30 << 10, maxFrame, 30 << 10},
		{"known size at the frame limit", maxFrame, maxFrame, maxFrame},
		{"known size over the frame limit", 500 << 20, maxFrame, maxFrame},
		{"unknown size", -1, maxFrame, artifactUnknownSizeBuffer},
		{"zero size is treated as unknown", 0, maxFrame, artifactUnknownSizeBuffer},
		{"unknown size under a tiny frame limit", -1, 4, 4},
		{"known size under a tiny frame limit", 10, 4, 4},
		{"one byte", 1, maxFrame, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := artifactBufferSize(tc.size, tc.maxFrame); got != tc.want {
				t.Errorf("artifactBufferSize(%d, %d) = %d, want %d", tc.size, tc.maxFrame, got, tc.want)
			}
		})
	}
}

func TestArtifactBufferPoolReturnsTheExactLengthAskedFor(t *testing.T) {
	// A buffer recycled from a big request must not lengthen a later small one
	// nor shorten a later big one: the length is the caller's, the class is
	// only where the memory comes from.
	for _, n := range []int{1, 100, 16 << 10, 16<<10 + 1, 30 << 10, 256 << 10, 4 << 20, 64 << 20, 64<<20 + 1} {
		pooled, buf := getArtifactBuffer(n)
		if len(buf) != n {
			t.Errorf("getArtifactBuffer(%d) has length %d", n, len(buf))
		}
		if pooled != nil && cap(*pooled) < n {
			t.Errorf("getArtifactBuffer(%d) drew from a class of only %d bytes", n, cap(*pooled))
		}
		if pooled != nil && cap(*pooled) >= 4*n && cap(*pooled) > 16<<10 {
			t.Errorf("getArtifactBuffer(%d) drew from a class of %d bytes: a small request must not pin a much larger buffer", n, cap(*pooled))
		}
		putArtifactBuffer(pooled)
	}
	if k := artifactClass(64<<20 + 1); k != -1 {
		t.Errorf("a request over the top class got class %d, want -1 (not pooled)", k)
	}
}

// lengthReader records the largest buffer the forwarder offers to Read.
type lengthReader struct {
	r      io.Reader
	maxLen int
}

func (l *lengthReader) Read(p []byte) (int, error) {
	l.maxLen = max(l.maxLen, len(p))
	return l.r.Read(p)
}

func (l *lengthReader) Close() error { return nil }

// collectSink reassembles the frames and remembers the largest one.
type collectSink struct {
	mu       sync.Mutex
	data     bytes.Buffer
	frames   int
	maxFrame int
}

func (s *collectSink) Headers(string, int64) error { return nil }
func (s *collectSink) Data(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames++
	s.maxFrame = max(s.maxFrame, len(p))
	s.data.Write(p)
	return nil
}

func TestForwardArtifactReadBufferFollowsTheDeclaredSize(t *testing.T) {
	tests := []struct {
		name       string
		size       int64
		bodyLen    int
		maxFrame   int
		wantRead   int
		wantFrames int
	}{
		{"small known", 30 << 10, 30 << 10, 4 << 20, 30 << 10, 1},
		{"unknown size", -1, 1 << 20, 4 << 20, artifactUnknownSizeBuffer, 4},
		{"big known is bounded by the frame limit", 10 << 20, 10 << 20, 1 << 20, 1 << 20, 10},
		{"tiny frame limit keeps its semantics", -1, 10, 4, 4, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dispatcher{cfg: DispatchConfig{MaxFrameBytes: tc.maxFrame}}
			want := make([]byte, tc.bodyLen)
			rand.New(rand.NewSource(1)).Read(want)
			body := &lengthReader{r: bytes.NewReader(want)}
			sink := &collectSink{}
			art := runtime.Artifact{Size: tc.size, Body: body}
			if err := d.forwardArtifact(context.Background(), sink, "r", art); err != nil {
				t.Fatal(err)
			}
			if body.maxLen != tc.wantRead {
				t.Errorf("read buffer = %d bytes, want %d", body.maxLen, tc.wantRead)
			}
			if sink.maxFrame > tc.maxFrame {
				t.Errorf("a frame of %d bytes exceeds MaxFrameBytes %d", sink.maxFrame, tc.maxFrame)
			}
			if sink.frames != tc.wantFrames {
				t.Errorf("frames = %d, want %d", sink.frames, tc.wantFrames)
			}
			if !bytes.Equal(sink.data.Bytes(), want) {
				t.Error("the reassembled artifact differs from the source")
			}
		})
	}
}

// TestForwardArtifactPooledBuffersNeverCrossTalk runs many transfers of mixed
// sizes at once, so buffers are constantly recycled between them, and checks
// every artifact arrives byte for byte (run with -race).
func TestForwardArtifactPooledBuffersNeverCrossTalk(t *testing.T) {
	d := &Dispatcher{cfg: DispatchConfig{MaxFrameBytes: 64 << 10}}
	sizes := []int{1, 100, 20 << 10, 64 << 10, 200 << 10, 1 << 20}

	var wg sync.WaitGroup
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(g)))
			for i := range 20 {
				n := sizes[rng.Intn(len(sizes))]
				want := make([]byte, n)
				rng.Read(want)
				size := int64(n)
				if i%3 == 0 {
					size = -1
				}
				sink := &collectSink{}
				art := runtime.Artifact{Size: size, Body: io.NopCloser(bytes.NewReader(want))}
				if err := d.forwardArtifact(context.Background(), sink, "r", art); err != nil {
					t.Error(err)
					return
				}
				if sink.maxFrame > d.cfg.MaxFrameBytes {
					t.Errorf("frame of %d bytes over the %d limit", sink.maxFrame, d.cfg.MaxFrameBytes)
				}
				if !bytes.Equal(sink.data.Bytes(), want) {
					t.Errorf("goroutine %d iteration %d: artifact of %d bytes (declared %d) arrived corrupted", g, i, n, size)
					return
				}
			}
		}()
	}
	wg.Wait()
}
