package claudecode

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
)

// TestLiveMessagesImages verifies actual vision, history, and MCP image results.
// TestLiveMessagesImages 验证真实 CLI 看图、历史图片和 MCP 图片结果。
func TestLiveMessagesImages(t *testing.T) {
	model := os.Getenv("AISW_CLAUDE_MODEL")
	if model == "" {
		model = "claude-opus-5-5"
	}
	b := liveMessagesBridge(t, model)
	picture := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			picture.SetRGBA(x, y, color.RGBA{B: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	imageBlock := map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(encoded.Bytes())}}
	content := func(v any) json.RawMessage {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	assertBlue := func(req messagesRequest) []messageBlock {
		t.Helper()
		w := sendMessages(b, req, "live-first")
		var response struct{ Content []messageBlock }
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal("invalid JSON response")
		}
		var text string
		for _, block := range response.Content {
			text += block.Text
		}
		if w.Code != 200 || !strings.Contains(strings.ToLower(text), "blue") {
			t.Fatalf("status=%d, recognized blue=%t", w.Code, strings.Contains(strings.ToLower(text), "blue"))
		}
		return response.Content
	}
	req := messagesRequest{Model: model, MaxTokens: 256, Messages: []bridgeMessage{{Role: "user", Content: content([]any{map[string]string{"type": "text", "text": "What color is this image? Reply with just the English color name."}, imageBlock})}}}
	answer := assertBlue(req)
	req.Messages = append(req.Messages, bridgeMessage{Role: "assistant", Content: content(answer)}, bridgeMessage{Role: "user", Content: content("Check the original image again. What color is it? Reply with just the English color name.")})
	assertBlue(req)
	t.Log("direct image and follow-up history: recognized blue")
	req = testMessagesRequest()
	req.Model = model
	req.Messages[0].Content = content("Call read_file once with path caller-only.txt. Its result contains an image. Then identify the color of that image; reply with only the English color name.")
	req = continuation(t, req, sendMessages(b, req, "live-first"), "")
	last, _ := blocks(req.Messages[len(req.Messages)-1].Content)
	last[0].Content = content([]any{map[string]string{"type": "text", "text": "Here is the image."}, imageBlock})
	req.Messages[len(req.Messages)-1].Content = content(last)
	assertBlue(req)
	t.Log("caller-side tool image: recognized blue")
}
