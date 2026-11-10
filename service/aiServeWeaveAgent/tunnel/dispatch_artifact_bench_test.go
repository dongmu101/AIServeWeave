package tunnel

import (
	"bytes"
	"context"
	"io"
	"testing"

	"AIServeWeave/common/runtime"
)

// discardSink accepts every frame without keeping it.
type discardSink struct{ frames int }

func (s *discardSink) Headers(string, int64) error { return nil }
func (s *discardSink) Data([]byte) error           { s.frames++; return nil }

// BenchmarkForwardArtifactSmall forwards a 32KiB image with the default 4MiB
// frame limit, the case that used to allocate a full 4MiB buffer per request.
//
// BenchmarkForwardArtifactSmall 在默认 4MiB 帧上限下转发一张 32KiB 图片，
// 这正是过去每次请求都分配完整 4MiB 缓冲的场景。
func BenchmarkForwardArtifactSmall(b *testing.B) {
	d := &Dispatcher{cfg: DispatchConfig{MaxFrameBytes: 4 << 20}}
	payload := bytes.Repeat([]byte{0x5a}, 32<<10)
	sink := &discardSink{}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		art := runtime.Artifact{ContentType: "image/png", Size: int64(len(payload)), Body: io.NopCloser(bytes.NewReader(payload))}
		if err := d.forwardArtifact(context.Background(), sink, "r", art); err != nil {
			b.Fatal(err)
		}
	}
}
