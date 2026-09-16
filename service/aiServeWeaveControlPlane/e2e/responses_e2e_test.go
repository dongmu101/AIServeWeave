package e2e_test

import (
	"context"
	"encoding/json"
	"testing"

	"AIServeWeave/service/aiServeWeaveGateway/controlplaneclient"
)

// gatewayResponsesClient returns the Gateway's real Response-turn persistence
// client, pointed at this control plane — the same "use the real other
// side" approach gatewayJobsClient already takes.
//
// gatewayResponsesClient 返回 Gateway 真实的 Response 轮次持久化客户端，指向
// 本控制面——与 gatewayJobsClient 已经采用的是同一种「用真实的另一侧」做法。
func gatewayResponsesClient(h *harness) *controlplaneclient.ResponsesClient {
	h.t.Helper()
	client, err := controlplaneclient.NewResponsesClient(controlplaneclient.ResponsesClientConfig{
		Endpoint: h.base,
		Token:    internalToken,
	})
	if err != nil {
		h.t.Fatalf("controlplaneclient.NewResponsesClient: %v", err)
	}
	return client
}

// TestResponseTurnLifecycleThroughTheRealGatewayClient drives create and
// get through controlplaneclient.ResponsesClient against a real control
// plane — the closed loop STATUS.md's P2 "Responses 持久会话" exists for,
// the same way TestJobLifecycleThroughTheRealGatewayClient closes the loop
// for Job persistence.
//
// TestResponseTurnLifecycleThroughTheRealGatewayClient 用
// controlplaneclient.ResponsesClient 对着一个真实的控制面走完创建与读取——
// 这正是 STATUS.md P2「Responses 持久会话」存在的意义所在的那个闭环，与
// TestJobLifecycleThroughTheRealGatewayClient 为 Job 持久化闭合的是同一种
// 环路。
func TestResponseTurnLifecycleThroughTheRealGatewayClient(t *testing.T) {
	h := newHarness(t)
	client := gatewayResponsesClient(h)
	ctx := context.Background()

	messages := json.RawMessage(`[{"Role":"user","Content":"hi"},{"Role":"assistant","Content":"hello"}]`)
	created, err := client.CreateTurn(ctx, controlplaneclient.CreateTurnRequest{
		ResponseID: "resp_e2e_1", TenantID: "tenant-a", Model: "qwen3:8b", Messages: messages,
	})
	if err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}
	if created.ResponseID != "resp_e2e_1" || created.TenantID != "tenant-a" {
		t.Fatalf("CreateTurn returned %+v, want response_id=resp_e2e_1 tenant_id=tenant-a", created)
	}

	// A retried create for the same tenant is idempotent.
	//
	// 同一租户下重试一次创建是幂等的。
	again, err := client.CreateTurn(ctx, controlplaneclient.CreateTurnRequest{
		ResponseID: "resp_e2e_1", TenantID: "tenant-a", Model: "qwen3:8b", Messages: messages,
	})
	if err != nil || again.ResponseID != created.ResponseID {
		t.Fatalf("retried CreateTurn = %+v, %v, want the same turn back with no error", again, err)
	}

	got, err := client.GetTurn(ctx, "tenant-a", "resp_e2e_1")
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if string(got.Messages) != string(messages) {
		t.Errorf("GetTurn Messages = %s, want %s", got.Messages, messages)
	}

	// A second turn pointing back at the first, exercising the
	// previous_response_id link the Gateway walks to reconstruct a
	// conversation.
	//
	// 第二轮回指第一轮，练一遍 Gateway 用来重建对话的 previous_response_id
	// 链接。
	second, err := client.CreateTurn(ctx, controlplaneclient.CreateTurnRequest{
		ResponseID: "resp_e2e_2", TenantID: "tenant-a", PreviousResponseID: "resp_e2e_1", Model: "qwen3:8b",
		Messages: json.RawMessage(`[{"Role":"user","Content":"how are you"},{"Role":"assistant","Content":"good"}]`),
	})
	if err != nil || second.PreviousResponseID != "resp_e2e_1" {
		t.Fatalf("CreateTurn (second) = %+v, %v, want previous_response_id=resp_e2e_1", second, err)
	}

	// GetTurn under another tenant reads identically to "not found" — the
	// same tenant-isolation shape the Job endpoints already keep.
	//
	// 在另一个租户下 GetTurn，读起来与「未找到」完全一样——与 Job 端点已经
	// 保持的是同一种租户隔离形状。
	if _, err := client.GetTurn(ctx, "tenant-b", "resp_e2e_1"); err != controlplaneclient.ErrNotFound {
		t.Errorf("GetTurn(other tenant) = %v, want ErrNotFound", err)
	}
}
