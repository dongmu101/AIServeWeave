package tunnelwire

import (
	"bytes"
	"encoding/json"
	"testing"

	"AIServeWeave/common/runtime"
)

// TestMessagesWireRoundTrip preserves raw block order and authenticated attribution.
// TestMessagesWireRoundTrip 保留原始块顺序与已认证归属。
func TestMessagesWireRoundTrip(t *testing.T) {
	want := runtime.MessagesRequest{Model: "sonnet", TenantID: "tenant", KeyID: "key", JSON: json.RawMessage(`{"messages":[{"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}]}`)}
	data, err := MarshalMessagesRequest(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalMessagesRequest(data)
	if err != nil || got.Model != want.Model || got.TenantID != want.TenantID || got.KeyID != want.KeyID || !bytes.Equal(got.JSON, want.JSON) {
		t.Fatalf("wire round trip matched=%t,error=%v,want true/nil", bytes.Equal(got.JSON, want.JSON), err)
	}
	event := runtime.MessagesEvent{JSON: json.RawMessage(`{"type":"message_stop"}`)}
	data, err = MarshalMessagesEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalMessagesEvent(data)
	if err != nil || !bytes.Equal(decoded.JSON, event.JSON) {
		t.Fatal("event round trip failed")
	}
}

// TestMessagesWireBounds rejects invalid and oversized documents without reflecting them.
// TestMessagesWireBounds 拒绝非法及超限文档，不反射文档内容。
func TestMessagesWireBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"invalid JSON", []byte("private-invalid-input")}, {"oversize", bytes.Repeat([]byte(" "), 1<<20+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := MarshalMessagesRequest(runtime.MessagesRequest{TenantID: "tenant", KeyID: "key", JSON: tc.data}); err == nil {
				t.Fatal("request error=nil,want rejected")
			}
			if _, err := MarshalMessagesEvent(runtime.MessagesEvent{JSON: tc.data}); err == nil {
				t.Fatal("event error=nil,want rejected")
			}
		})
	}
}
