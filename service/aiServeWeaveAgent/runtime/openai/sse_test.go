package openai

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func readAllFrames(t *testing.T, input string, maxLine, maxEvent int) ([]sseFrame, error) {
	t.Helper()
	r := newSSEReader(strings.NewReader(input), maxLine, maxEvent)
	var frames []sseFrame
	for {
		f, err := r.readFrame()
		if err != nil {
			return frames, err
		}
		frames = append(frames, f)
	}
}

func TestSSEBasicEvent(t *testing.T) {
	frames, err := readAllFrames(t, "data: hello\n\n", 0, 0)
	if err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 1 || frames[0].Data != "hello" {
		t.Fatalf("unexpected frames: %+v", frames)
	}
}

func TestSSEMultiLineData(t *testing.T) {
	frames, err := readAllFrames(t, "data: line1\ndata: line2\n\n", 0, 0)
	if err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 1 || frames[0].Data != "line1\nline2" {
		t.Fatalf("unexpected frames: %+v", frames)
	}
}

func TestSSEEventIDRetryFields(t *testing.T) {
	frames, err := readAllFrames(t, "event: message\nid: 42\nretry: 3000\ndata: x\n\n", 0, 0)
	if err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	f := frames[0]
	if f.Event != "message" || f.ID != "42" || f.Retry != "3000" || f.Data != "x" {
		t.Fatalf("unexpected frame: %+v", f)
	}
}

func TestSSECommentLinesIgnored(t *testing.T) {
	frames, err := readAllFrames(t, ": keep-alive\ndata: x\n: another comment\ndata: y\n\n", 0, 0)
	if err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 1 || frames[0].Data != "x\ny" {
		t.Fatalf("unexpected frames: %+v", frames)
	}
}

func TestSSEEmptyDataField(t *testing.T) {
	frames, err := readAllFrames(t, "data\n\n", 0, 0)
	if err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 1 || frames[0].Data != "" {
		t.Fatalf("unexpected frames: %+v", frames)
	}
}

func TestSSEDoneSentinel(t *testing.T) {
	_, err := readAllFrames(t, "data: [DONE]\n\n", 0, 0)
	if !errors.Is(err, ErrSSEDone) {
		t.Fatalf("expected ErrSSEDone, got %v", err)
	}
}

func TestSSECRLFLineEndings(t *testing.T) {
	frames, err := readAllFrames(t, "data: hello\r\n\r\n", 0, 0)
	if err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 1 || frames[0].Data != "hello" {
		t.Fatalf("unexpected frames: %+v", frames)
	}
}

func TestSSEMultipleSequentialEvents(t *testing.T) {
	frames, err := readAllFrames(t, "data: a\n\ndata: b\n\ndata: c\n\n", 0, 0)
	if err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 3 || frames[0].Data != "a" || frames[1].Data != "b" || frames[2].Data != "c" {
		t.Fatalf("unexpected frames: %+v", frames)
	}
}

func TestSSELeadingBlankLinesIgnored(t *testing.T) {
	frames, err := readAllFrames(t, "\n\ndata: a\n\n", 0, 0)
	if err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 1 || frames[0].Data != "a" {
		t.Fatalf("unexpected frames: %+v", frames)
	}
}

func TestSSEOversizedLineErrors(t *testing.T) {
	_, err := readAllFrames(t, "data: "+strings.Repeat("a", 100)+"\n\n", 20, 0)
	if err == nil {
		t.Fatal("expected an error for an oversized line")
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("expected a size-limit error, not io.EOF")
	}
}

func TestSSEOversizedEventErrors(t *testing.T) {
	input := strings.Repeat("data: aaaaaaaaaa\n", 10) + "\n"
	_, err := readAllFrames(t, input, 0, 50)
	if err == nil {
		t.Fatal("expected an error for an oversized event")
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("expected a size-limit error, not io.EOF")
	}
}

func TestSSEAbruptDisconnectMidEventStillYieldsFrame(t *testing.T) {
	// No trailing blank line: the connection closed right after the last
	// data line. The partial event must still be delivered.
	frames, err := readAllFrames(t, "data: partial", 0, 0)
	if err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 1 || frames[0].Data != "partial" {
		t.Fatalf("unexpected frames: %+v", frames)
	}
}

func TestSSEEmptyStreamYieldsNoFrames(t *testing.T) {
	frames, err := readAllFrames(t, "", 0, 0)
	if err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("expected no frames, got %+v", frames)
	}
}
