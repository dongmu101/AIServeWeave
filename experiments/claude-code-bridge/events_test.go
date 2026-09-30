package main

import (
	"errors"
	"strings"
	"testing"
)

// TestInspectEvents distinguishes protocol evidence from success text.
// TestInspectEvents 区分协议证据与仅有成功文本。
func TestInspectEvents(t *testing.T) {
	good := `{"type":"system","subtype":"init","tools":["mcp__aisw_probe__probe_echo"]}
{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use"}}}
{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"}}}
{"type":"stream_event","event":{"type":"message_stop"}}
{"type":"result","subtype":"success","is_error":false,"result":"secret-sentinel"}`
	s, err := inspectEvents(strings.NewReader(good), "secret-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	if s.Events != 5 || s.ToolStarts != 1 || s.ToolTurns != 1 || s.MessageStops != 1 || !s.SentinelSeen {
		t.Fatalf("summary = %+v, want 5 events, 1 tool start/turn/stop and sentinel", s)
	}
	for _, tc := range []struct {
		name, input string
		want        error
	}{
		{"no result", `{"type":"system"}`, errMissingResult},
		{"result failure", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"SECRET"}`, errCLIResult},
		{"duplicate result", `{"type":"result","subtype":"success"}` + "\n" + `{"type":"result","subtype":"success"}`, errCLIProtocol},
		{"bad JSON", `SECRET`, errCLIProtocol},
		{"null", `null`, errCLIProtocol},
		{"oversized", strings.Repeat("x", maxEventBytes+1), errCLIProtocol},
		{"native tool", `{"type":"system","subtype":"init","tools":["Bash"]}`, errUnsafeTools},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inspectEvents(strings.NewReader(tc.input), "unused")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("error leaked input, want constant error")
			}
		})
	}
}
