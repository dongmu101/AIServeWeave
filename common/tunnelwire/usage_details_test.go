package tunnelwire

import (
	"testing"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/pricing"
	"AIServeWeave/common/runtime"
)

// TestUsageDetailsCrossTheTunnel checks that details and attributes survive
// the wire, that a usage carrying only them is still sent, and that a usage
// from an Agent that predates them restores with nil maps.
// TestUsageDetailsCrossTheTunnel 检查明细与属性能经过线路传递、只带有它们的用量仍会
// 发送，以及来自不支持它们的旧 Agent 的用量还原后 map 为 nil。
func TestUsageDetailsCrossTheTunnel(t *testing.T) {
	in := &runtime.Usage{PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050, ReasoningTokens: 20,
		Details: map[string]int64{pricing.MeterCacheRead: 600}, Attributes: map[string]string{"service_tier": "flex"}}
	out := usageFromProto(usageToProto(in))
	if out.PromptTokens != 1000 || out.ReasoningTokens != 20 || out.Details[pricing.MeterCacheRead] != 600 || out.Attributes["service_tier"] != "flex" {
		t.Fatalf("round trip = %+v, want the same usage", out)
	}
	in.Details[pricing.MeterCacheRead] = 1
	if out.Details[pricing.MeterCacheRead] != 600 {
		t.Fatal("the restored details must not alias the sender's map")
	}

	if usageToProto(&runtime.Usage{Details: map[string]int64{pricing.MeterCacheRead: 5}}) == nil {
		t.Fatal("a usage carrying only details must still be sent")
	}
	if usageToProto(&runtime.Usage{}) != nil {
		t.Fatal("an all-zero usage must stay nil on the wire")
	}

	old := usageFromProto(&tunnelv1.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5})
	if old.Details != nil || old.Attributes != nil {
		t.Fatalf("usage from an older Agent = %+v, want nil details and attributes", old)
	}
}
