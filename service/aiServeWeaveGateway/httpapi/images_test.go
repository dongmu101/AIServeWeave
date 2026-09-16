package httpapi

import (
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

func TestImagesRequestUnsupported(t *testing.T) {
	one := 1
	two := 2
	tests := []struct {
		name string
		req  imagesRequest
		want string
	}{
		{name: "plain prompt is supported", req: imagesRequest{Prompt: "a fox"}, want: ""},
		{name: "n omitted is supported", req: imagesRequest{Prompt: "a fox"}, want: ""},
		{name: "n=1 is supported", req: imagesRequest{Prompt: "a fox", N: &one}, want: ""},
		{name: "n=2 is rejected by name", req: imagesRequest{Prompt: "a fox", N: &two}, want: "n"},
		{name: "quality is rejected by name", req: imagesRequest{Prompt: "a fox", Quality: "hd"}, want: "quality"},
		{name: "style is rejected by name", req: imagesRequest{Prompt: "a fox", Style: "vivid"}, want: "style"},
		{name: "response_format b64_json is supported", req: imagesRequest{Prompt: "a fox", ResponseFormat: "b64_json"}, want: ""},
		{name: "response_format url is supported", req: imagesRequest{Prompt: "a fox", ResponseFormat: "url"}, want: ""},
		{name: "an unknown response_format is rejected by name", req: imagesRequest{Prompt: "a fox", ResponseFormat: "gif"}, want: "response_format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.req.unsupported(); got != tt.want {
				t.Errorf("unsupported() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseImageSize(t *testing.T) {
	tests := []struct {
		name       string
		size       string
		wantWidth  int
		wantHeight int
		wantOK     bool
		wantErr    bool
	}{
		{name: "empty size is not an error and not present", size: "", wantOK: false, wantErr: false},
		{name: "well-formed size", size: "768x512", wantWidth: 768, wantHeight: 512, wantOK: true},
		{name: "missing x separator", size: "768", wantErr: true},
		{name: "non-numeric width", size: "abcx512", wantErr: true},
		{name: "zero height is rejected", size: "768x0", wantErr: true},
		{name: "negative width is rejected", size: "-1x512", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			width, height, ok, err := parseImageSize(tt.size)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseImageSize(%q) error = %v, wantErr %v", tt.size, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if ok != tt.wantOK || width != tt.wantWidth || height != tt.wantHeight {
				t.Errorf("parseImageSize(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tt.size, width, height, ok, tt.wantWidth, tt.wantHeight, tt.wantOK)
			}
		})
	}
}

func TestTemplateDeclares(t *testing.T) {
	tpl := &workflow.Template{Inputs: []workflow.Input{
		{Name: "prompt", Type: workflow.InputString, Required: true},
		{Name: "width", Type: workflow.InputInteger},
	}}
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "a declared input", in: "width", want: true},
		{name: "an undeclared input", in: "height", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := templateDeclares(tpl, tt.in); got != tt.want {
				t.Errorf("templateDeclares(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestSelectImageArtifactsFiltersByBucketAndExtension is selectImageArtifacts'
// core contract: only "output"-bucket artifacts with a recognized image
// extension qualify — a "temp" preview, an "input" echo, and a non-image
// "output" file (e.g. a debug JSON dump some workflow authors save) must all
// be silently skipped rather than surfaced as images.
//
// TestSelectImageArtifactsFiltersByBucketAndExtension 是 selectImageArtifacts
// 的核心约定：只有 "output" 分区、且扩展名已识别为图像的产物才合格——一个
// "temp" 预览、一个 "input" 回显，以及一个非图像的 "output" 文件（例如某些
// 模板作者保存的调试 JSON 转储），都必须被静默跳过，而不是被当作图像呈现。
func TestSelectImageArtifactsFiltersByBucketAndExtension(t *testing.T) {
	refs := []runtime.ArtifactRef{
		{Filename: "ComfyUI_00001_.png", Type: "output"},
		{Filename: "preview.png", Type: "temp"},
		{Filename: "source.png", Type: "input"},
		{Filename: "debug.json", Type: "output"},
		{Filename: "ComfyUI_00002_.JPEG", Type: "output"},
	}
	got := selectImageArtifacts(refs)
	if len(got) != 2 {
		t.Fatalf("selectImageArtifacts() returned %d refs, want 2: %+v", len(got), got)
	}
	if got[0].Filename != "ComfyUI_00001_.png" || got[1].Filename != "ComfyUI_00002_.JPEG" {
		t.Errorf("selectImageArtifacts() = %+v, want the two output-bucket image files in order", got)
	}
}

func TestSelectImageArtifactsNoneQualify(t *testing.T) {
	refs := []runtime.ArtifactRef{{Filename: "debug.json", Type: "output"}, {Filename: "x.png", Type: "temp"}}
	if got := selectImageArtifacts(refs); len(got) != 0 {
		t.Errorf("selectImageArtifacts() = %+v, want none", got)
	}
}
