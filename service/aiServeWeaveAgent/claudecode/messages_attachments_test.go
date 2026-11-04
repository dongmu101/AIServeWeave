package claudecode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/attachments"
)

func pdfTestBlock() messageBlock {
	return messageBlock{Type: "document", Title: "private-title", Source: &imageSource{Type: "base64", MediaType: "application/pdf", Data: base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 private-content"))}}
}

func TestAttachmentPreparationKeepsOriginalHistory(t *testing.T) {
	p, _ := attachments.New(attachments.Config{})
	block := pdfTestBlock()
	content, _ := json.Marshal([]messageBlock{block})
	req := testMessagesRequest()
	req.Messages = []bridgeMessage{{Role: "user", Content: content}}
	if err := req.validate(req.Model); err != nil {
		t.Fatal(err)
	}
	before := historyHash(req.Messages)
	s := &messagesSession{processor: p, request: req}
	prepared, err := s.prepareMessages(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input, structured := sessionInput(prepared)
	if !structured || !strings.Contains(input, `"type":"document"`) || !strings.Contains(input, block.Source.Data) {
		t.Fatal("PDF was not sent as a native document")
	}
	if historyHash(req.Messages) != before {
		t.Fatal("conversion mutated authenticated history")
	}
	altered := block
	copySource := *block.Source
	copySource.Data = base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 altered"))
	altered.Source = &copySource
	changed, _ := json.Marshal([]messageBlock{altered})
	if historyHash([]bridgeMessage{{Role: "user", Content: changed}}) == before {
		t.Fatal("changed document authenticated as original")
	}
	if len(s.attachmentCache) != 1 {
		t.Fatal("session did not retain prepared content")
	}
	_, err = s.prepareMessages(t.Context())
	if err != nil || len(s.attachmentCache) != 1 {
		t.Fatal("identical attachment was not reused")
	}
}

func TestOrphanedDocumentResultRestoresNativePDF(t *testing.T) {
	p, _ := attachments.New(attachments.Config{})
	req := testMessagesRequest()
	result, _ := json.Marshal([]messageBlock{pdfTestBlock()})
	toolResult, _ := json.Marshal([]messageBlock{{Type: "tool_result", ToolUseID: "lost", Content: result}})
	req.Messages = append(req.Messages, bridgeMessage{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"lost","name":"read_file","input":{"path":"lost.pdf"}}]`)}, bridgeMessage{Role: "user", Content: toolResult})
	if err := req.validate(req.Model); err != nil {
		t.Fatal(err)
	}
	s := &messagesSession{processor: p, request: req}
	prepared, err := s.prepareMessages(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input, structured := sessionInput(prepared)
	if !structured || !strings.Contains(input, "Do not repeat those tool calls") || !strings.Contains(input, pdfTestBlock().Source.Data) {
		t.Fatal("orphaned document was not restored with tool context")
	}
}

func TestMCPAttachmentFailureStopsRound(t *testing.T) {
	p, _ := attachments.New(attachments.Config{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan toolResult, 1)
	result <- toolResult{Content: []messageBlock{pdfTestBlock()}}
	s := &messagesSession{processor: p, ctx: ctx, cancel: cancel, request: testMessagesRequest(), events: make(chan bridgeEvent, 1), pending: map[string]*pendingTool{"call": {block: messageBlock{Name: "read_file", Input: json.RawMessage(`{}`)}, result: result}}, changed: make(chan struct{})}
	request := httptest.NewRequest("POST", "http://127.0.0.1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tool_0","arguments":{}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer fixture")
	response := httptest.NewRecorder()
	bridgeMCPHandler(s, "fixture", "127.0.0.1").ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), `"error"`) || s.ctx.Err() == nil {
		t.Fatal("failed attachment conversion was presented as successful tool content")
	}
	select {
	case event := <-s.events:
		if !errors.Is(event.err, attachments.ErrDependency) {
			t.Fatal(event.err)
		}
	default:
		t.Fatal("caller did not receive conversion failure")
	}
}

func TestAttachmentFailuresHaveStableCodes(t *testing.T) {
	rt, err := NewFactory("unused")(runtime.Config{ID: "local", Kind: runtime.KindClaude}, runtime.Dependencies{Clock: runtime.NewSystemClock()})
	if err != nil {
		t.Fatal(err)
	}
	r := rt.(*Runtime)
	defer r.Close()
	for _, tc := range []struct {
		cause error
		code  runtime.ErrorCode
	}{{attachments.ErrDependency, runtime.ErrorCapability}, {attachments.ErrLimit, runtime.ErrorResponseTooLarge}, {attachments.ErrBusy, runtime.ErrorBackpressure}, {attachments.ErrConversion, runtime.ErrorUpstream}, {context.DeadlineExceeded, runtime.ErrorTimeout}} {
		var failure *runtime.RuntimeError
		if !errors.As(r.roundError(&attachmentFailure{tc.cause}), &failure) || failure.Code != tc.code || failure.RuntimeID != "local" {
			t.Fatal("attachment failure classification lost")
		}
	}
}
