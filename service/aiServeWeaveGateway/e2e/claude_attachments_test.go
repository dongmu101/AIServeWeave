//go:build darwin || linux

package e2e

import (
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/attachments"
)

func attachmentConverterFixture() {
	args := os.Args[1:]
	last := args[len(args)-1]
	value := func(flag string) string {
		for i, arg := range args {
			if arg == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	writeImage := func(path string) {
		file, err := os.Create(path)
		if err != nil {
			os.Exit(3)
		}
		_ = jpeg.Encode(file, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
		_ = file.Close()
	}
	switch {
	case strings.HasPrefix(args[0], "-env:"):
		_ = os.WriteFile(filepath.Join(filepath.Dir(last), "input.pdf"), []byte("%PDF-1.4 attachment fixture"), 0600)
	case args[0] == "-isodates":
		fmt.Println("Pages: 2")
	case args[0] == "-v":
		fmt.Println(`{"format":{"duration":"6"},"streams":[{"codec_type":"video"},{"codec_type":"audio"}]}`)
	case args[0] == "-nostdin":
		if strings.HasSuffix(last, ".wav") {
			_ = os.WriteFile(last, []byte("audio fixture"), 0600)
		} else {
			writeImage(last)
		}
	case args[0] == "-m":
		_ = os.WriteFile(value("-of")+".srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nspoken marker\n"), 0600)
	case args[0] == "-f":
		writeImage(last + "-1.jpg")
		writeImage(last + "-2.jpg")
	case args[0] == "-enc":
		_ = os.WriteFile(last, []byte("extracted table marker"), 0600)
	default:
		os.Exit(3)
	}
}

func attachmentFixtureProcessor(t *testing.T) *attachments.Processor {
	t.Helper()
	model := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(model, []byte("model fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := attachments.New(attachments.Config{LibreOfficePath: os.Args[0], PDFInfoPath: os.Args[0], PDFToPPMPath: os.Args[0], PDFToTextPath: os.Args[0], FFmpegPath: os.Args[0], FFprobePath: os.Args[0], WhisperPath: os.Args[0], WhisperModelPath: model})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
