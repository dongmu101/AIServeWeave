package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// StudioConversations checks the Studio conversation contract: owner
// scoping, list order, paging and search, the turn rules (append,
// regenerate, replace), idempotent and stale replies, limits and deletion.
//
// StudioConversations 检查 Studio 对话契约：按所有者限定、列表顺序、分页与搜索、轮次规则
// （追加、重新生成、替换）、幂等与过期的回复、上限以及删除。
func StudioConversations(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	tenant := model.NewID(model.PrefixTenant)
	alice := store.StudioOwner{TenantID: tenant, UserID: model.NewID(model.PrefixUser)}
	bob := store.StudioOwner{TenantID: tenant, UserID: model.NewID(model.PrefixUser)}

	create := func(owner store.StudioOwner, title string, updated time.Time) model.StudioConversation {
		t.Helper()
		c := model.StudioConversation{ID: model.NewID(model.PrefixConversation), TenantID: owner.TenantID, UserID: owner.UserID, Title: title, Model: "qwen3:8b", CreatedAt: updated, UpdatedAt: updated}
		if err := st.CreateStudioConversation(ctx, &c, 3); err != nil {
			t.Fatalf("CreateStudioConversation(%q) = %v", title, err)
		}
		return c
	}
	first := create(alice, "Go 泛型", at)
	second := create(alice, "50% 折扣", at.Add(time.Minute))
	third := create(alice, "", at.Add(2*time.Minute))
	extra := model.StudioConversation{ID: model.NewID(model.PrefixConversation), TenantID: alice.TenantID, UserID: alice.UserID, CreatedAt: at, UpdatedAt: at}
	if err := st.CreateStudioConversation(ctx, &extra, 3); !errors.Is(err, store.ErrStudioLimit) {
		t.Fatalf("fourth conversation = %v, want ErrStudioLimit", err)
	}
	create(bob, "bob's", at)

	if _, err := st.GetStudioConversation(ctx, bob, first.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's GetStudioConversation = %v, want ErrNotFound", err)
	}
	if _, err := st.ListStudioMessages(ctx, bob, first.ID, 0, 0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's ListStudioMessages = %v, want ErrNotFound", err)
	}

	page, err := st.ListStudioConversations(ctx, store.StudioConversationFilter{Owner: alice, Limit: 2})
	if err != nil || len(page) != 2 || page[0].ID != third.ID || page[1].ID != second.ID {
		t.Fatalf("first page = %v, %v, want [third second]", ids(page), err)
	}
	rest, err := st.ListStudioConversations(ctx, store.StudioConversationFilter{Owner: alice, Limit: 2, Before: &store.StudioConversationCursor{UpdatedAt: page[1].UpdatedAt, ID: page[1].ID}})
	if err != nil || len(rest) != 1 || rest[0].ID != first.ID {
		t.Fatalf("second page = %v, %v, want [first]", ids(rest), err)
	}
	found, err := st.ListStudioConversations(ctx, store.StudioConversationFilter{Owner: alice, Query: "50%"})
	if err != nil || len(found) != 1 || found[0].ID != second.ID {
		t.Fatalf("search 50%% = %v, %v, want only the literal match", ids(found), err)
	}
	if found, _ := st.ListStudioConversations(ctx, store.StudioConversationFilter{Owner: alice, Query: "go"}); len(found) != 1 || found[0].ID != first.ID {
		t.Errorf("case-insensitive search = %v, want [first]", ids(found))
	}

	user := func(text string) *model.StudioMessage {
		return &model.StudioMessage{ID: model.NewID(model.PrefixMessage), ConversationID: third.ID, TenantID: alice.TenantID, UserID: alice.UserID, Role: model.StudioRoleUser, Content: text, Status: model.StudioMessageComplete, CreatedAt: at}
	}
	reply := func(text string) *model.StudioMessage {
		return &model.StudioMessage{ID: model.NewID(model.PrefixMessage), ConversationID: third.ID, TenantID: alice.TenantID, UserID: alice.UserID, Role: model.StudioRoleAssistant, Content: text, Reasoning: "thought", Status: model.StudioMessageComplete, Model: "qwen3:8b", PromptTokens: 3, CompletionTokens: 5, CreatedAt: at}
	}
	begin := func(turn store.StudioTurn) (model.StudioConversation, []model.StudioMessage) {
		t.Helper()
		turn.MaxMessages = 6
		turn.At = at.Add(time.Hour)
		c, history, err := st.BeginStudioTurn(ctx, alice, third.ID, turn)
		if err != nil {
			t.Fatalf("BeginStudioTurn(%+v) = %v", turn, err)
		}
		return c, history
	}

	c, history := begin(store.StudioTurn{User: user("你好"), Title: "你好", Model: "llama3"})
	if c.Title != "你好" || c.Model != "llama3" || len(history) != 1 || history[0].Seq != 1 {
		t.Fatalf("first turn = %+v, %v, want the title set, the model switched and one message at seq 1", c, contents(history))
	}
	answer := reply("嗨")
	if err := st.AppendStudioReply(ctx, alice, third.ID, 1, answer); err != nil {
		t.Fatalf("AppendStudioReply = %v", err)
	}
	if err := st.AppendStudioReply(ctx, alice, third.ID, 1, answer); err != nil {
		t.Fatalf("retried AppendStudioReply = %v, want nil", err)
	}
	if err := st.AppendStudioReply(ctx, alice, third.ID, 1, reply("late")); !errors.Is(err, store.ErrStudioStaleTurn) {
		t.Fatalf("reply to a message that is no longer last = %v, want ErrStudioStaleTurn", err)
	}
	if err := st.AppendStudioReply(ctx, bob, third.ID, 2, reply("x")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's AppendStudioReply = %v, want ErrNotFound", err)
	}

	second2 := user("再说一次")
	c, history = begin(store.StudioTurn{User: second2, Title: "ignored"})
	if c.Title != "你好" || len(history) != 3 || history[2].Seq != 3 {
		t.Fatalf("second turn = %q, %v, want the title kept and the user message at seq 3", c.Title, contents(history))
	}
	if err := st.AppendStudioReply(ctx, alice, third.ID, 3, reply("第二个回答")); err != nil {
		t.Fatal(err)
	}

	_, history = begin(store.StudioTurn{Regenerate: true})
	if len(history) != 3 || history[2].ID != second2.ID {
		t.Fatalf("regenerate history = %v, want the last reply dropped", contents(history))
	}
	if err := st.AppendStudioReply(ctx, alice, third.ID, 3, reply("重新生成的回答")); err != nil {
		t.Fatal(err)
	}

	_, history = begin(store.StudioTurn{ReplaceFromID: second2.ID, User: user("改过的问题")})
	if len(history) != 3 || history[2].Content != "改过的问题" || history[2].Seq != 3 {
		t.Fatalf("replace history = %v, want the edited question in place of the old one", contents(history))
	}
	if _, _, err := st.BeginStudioTurn(ctx, alice, third.ID, store.StudioTurn{ReplaceFromID: answer.ID, User: user("x"), At: at}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("replacing an assistant message = %v, want ErrConflict", err)
	}
	if _, _, err := st.BeginStudioTurn(ctx, alice, third.ID, store.StudioTurn{User: user("x"), MaxMessages: 4, At: at}); !errors.Is(err, store.ErrStudioLimit) {
		t.Errorf("turn past MaxMessages = %v, want ErrStudioLimit", err)
	}
	stored, err := st.ListStudioMessages(ctx, alice, third.ID, 0, 0)
	if err != nil || len(stored) != 3 {
		t.Fatalf("stored messages = %v, %v, want the three of the replaced turn", contents(stored), err)
	}
	if stored[1].Reasoning != "thought" || stored[1].PromptTokens != 3 || stored[1].Model != "qwen3:8b" {
		t.Errorf("stored reply = %+v, want its reasoning, usage and model kept", stored[1])
	}
	if after, _ := st.ListStudioMessages(ctx, alice, third.ID, 1, 1); len(after) != 1 || after[0].Seq != 2 {
		t.Errorf("ListStudioMessages(after 1, limit 1) = %v, want seq 2", contents(after))
	}

	title, prompt := "改名", "你是一位助手"
	if c, err := st.UpdateStudioConversation(ctx, alice, third.ID, store.StudioConversationPatch{Title: &title, SystemPrompt: &prompt}, at.Add(2*time.Hour)); err != nil || c.Title != title || c.SystemPrompt != prompt || c.Model != "llama3" {
		t.Fatalf("UpdateStudioConversation = %+v, %v", c, err)
	}
	if _, err := st.UpdateStudioConversation(ctx, bob, third.ID, store.StudioConversationPatch{Title: &title}, at); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's UpdateStudioConversation = %v, want ErrNotFound", err)
	}

	if err := st.DeleteStudioConversation(ctx, bob, third.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's DeleteStudioConversation = %v, want ErrNotFound", err)
	}
	if err := st.DeleteStudioConversation(ctx, alice, third.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListStudioMessages(ctx, alice, third.ID, 0, 0); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("messages of a deleted conversation = %v, want ErrNotFound", err)
	}
	if err := st.CreateStudioConversation(ctx, &extra, 3); err != nil {
		t.Errorf("creating after a delete freed room = %v", err)
	}
}

func ids(list []model.StudioConversation) []string {
	out := make([]string, len(list))
	for i, c := range list {
		out[i] = c.Title + "#" + c.ID
	}
	return out
}

func contents(list []model.StudioMessage) []string {
	out := make([]string, len(list))
	for i, m := range list {
		out[i] = m.Role + ":" + m.Content
	}
	return out
}
