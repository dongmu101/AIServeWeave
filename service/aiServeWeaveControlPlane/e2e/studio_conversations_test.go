package e2e_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
	"AIServeWeave/service/aiServeWeaveGateway/httpapi"
)

func studioClient(h *harness) *controlplaneclient.StudioClient {
	h.t.Helper()
	client, err := controlplaneclient.NewStudioClient(controlplaneclient.StudioClientConfig{Endpoint: h.base, Token: internalToken})
	if err != nil {
		h.t.Fatal(err)
	}
	return client
}

// TestStudioConversationsRoundTripThroughTheGatewayClient drives the
// internal Studio API with the Gateway's real client: a conversation is
// created, a turn begins and is answered, regenerated and edited, and every
// read stays within its owner.
//
// TestStudioConversationsRoundTripThroughTheGatewayClient 用 Gateway 真实的客户端驱动
// 内部 Studio API：创建对话、开始一轮并回答、重新生成与编辑，所有读取都限定在所有者内。
func TestStudioConversationsRoundTripThroughTheGatewayClient(t *testing.T) {
	h := newHarness(t)
	client := studioClient(h)
	ctx := context.Background()
	owner := httpapi.StudioOwner{TenantID: "tnt_studio", UserID: "usr_alice"}
	other := httpapi.StudioOwner{TenantID: "tnt_studio", UserID: "usr_bob"}

	c, err := client.CreateConversation(ctx, owner, "", "qwen3:8b", "")
	if err != nil || !strings.HasPrefix(c.ID, "cnv_") || c.Model != "qwen3:8b" {
		t.Fatalf("CreateConversation = %+v, %v", c, err)
	}
	if _, err := client.GetConversation(ctx, other, c.ID); !errors.Is(err, httpapi.ErrStudioNotFound) {
		t.Errorf("another user's GetConversation = %v, want ErrStudioNotFound", err)
	}

	c, history, err := client.BeginTurn(ctx, owner, c.ID, httpapi.StudioTurn{Content: "\n  如何写一个 HTTP 服务？\n用 Go", Model: "llama3"})
	if err != nil || len(history) != 1 || history[0].Role != "user" || history[0].Seq != 1 {
		t.Fatalf("BeginTurn = %+v, %v", history, err)
	}
	if c.Title != "如何写一个 HTTP 服务？" || c.Model != "llama3" {
		t.Errorf("conversation after the first turn = %+v, want the first line as title and the turn's model", c)
	}
	reply := httpapi.StudioReply{ReplyTo: 1, ID: "msg_reply_1", Content: "先导入 net/http", Reasoning: "想一想", Status: "complete", Model: "llama3", PromptTokens: 7, CompletionTokens: 9}
	if err := client.AppendReply(ctx, owner, c.ID, reply); err != nil {
		t.Fatalf("AppendReply = %v", err)
	}
	if err := client.AppendReply(ctx, owner, c.ID, reply); err != nil {
		t.Errorf("retried AppendReply = %v, want nil", err)
	}
	late := reply
	late.ID = "msg_reply_late"
	if err := client.AppendReply(ctx, owner, c.ID, late); !errors.Is(err, httpapi.ErrStudioStaleTurn) {
		t.Errorf("stale AppendReply = %v, want ErrStudioStaleTurn", err)
	}

	if _, _, err := client.BeginTurn(ctx, owner, c.ID, httpapi.StudioTurn{Regenerate: true}); err != nil {
		t.Fatalf("regenerate = %v", err)
	}
	messages, err := client.ListMessages(ctx, owner, c.ID, 0, 0)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages after regenerate = %+v, %v, want only the question", messages, err)
	}
	if _, history, err = client.BeginTurn(ctx, owner, c.ID, httpapi.StudioTurn{ReplaceFromMessageID: messages[0].ID, Content: "换个问题"}); err != nil || len(history) != 1 || history[0].Content != "换个问题" {
		t.Fatalf("edit = %+v, %v", history, err)
	}
	if _, _, err := client.BeginTurn(ctx, owner, c.ID, httpapi.StudioTurn{Regenerate: true, Content: "both"}); !errors.Is(err, httpapi.ErrStudioInvalid) {
		t.Errorf("regenerate with content = %v, want ErrStudioInvalid", err)
	}
	if _, _, err := client.BeginTurn(ctx, owner, c.ID, httpapi.StudioTurn{ReplaceFromMessageID: "msg_missing", Content: "x"}); !errors.Is(err, httpapi.ErrStudioConflict) {
		t.Errorf("edit of a missing message = %v, want ErrStudioConflict", err)
	}

	title := "HTTP 笔记"
	if c, err = client.UpdateConversation(ctx, owner, c.ID, httpapi.StudioConversationPatch{Title: &title}); err != nil || c.Title != title {
		t.Fatalf("UpdateConversation = %+v, %v", c, err)
	}
	second, _ := client.CreateConversation(ctx, owner, "另一个", "", "")
	page, err := client.ListConversations(ctx, owner, "", "", 1)
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].ID != second.ID || page.Next == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page, err = client.ListConversations(ctx, owner, "", page.Next, 1)
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].ID != c.ID || page.Next != "" {
		t.Fatalf("second page = %+v, %v", page, err)
	}
	if found, _ := client.ListConversations(ctx, owner, "笔记", "", 0); len(found.Conversations) != 1 || found.Conversations[0].ID != c.ID {
		t.Errorf("search = %+v", found)
	}
	if _, err := client.ListConversations(ctx, owner, "", "forged", 0); !errors.Is(err, httpapi.ErrStudioInvalid) {
		t.Errorf("forged cursor = %v, want ErrStudioInvalid", err)
	}
	if theirs, _ := client.ListConversations(ctx, other, "", "", 0); len(theirs.Conversations) != 0 {
		t.Errorf("another user's list = %+v, want empty", theirs)
	}

	if err := client.DeleteConversation(ctx, other, c.ID); !errors.Is(err, httpapi.ErrStudioNotFound) {
		t.Errorf("another user's delete = %v, want ErrStudioNotFound", err)
	}
	if err := client.DeleteConversation(ctx, owner, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListMessages(ctx, owner, c.ID, 0, 0); !errors.Is(err, httpapi.ErrStudioNotFound) {
		t.Errorf("messages of a deleted conversation = %v, want ErrStudioNotFound", err)
	}
}

// TestStudioInternalAPIRequiresTheInternalToken: a tenant session cannot
// reach another member's conversations by calling the internal API itself.
//
// TestStudioInternalAPIRequiresTheInternalToken：租户会话无法自行调用内部 API 来读取其他成员的对话。
func TestStudioInternalAPIRequiresTheInternalToken(t *testing.T) {
	h := newHarness(t)
	_, session := bootstrap(h, "Acme", "studio-internal@example.com")
	if status := h.call(http.MethodGet, "/internal/v1/studio/conversations?tenant_id=t&user_id=u", session, nil, nil); status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
}
