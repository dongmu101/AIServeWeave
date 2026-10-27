package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func usageProcessFixture() {
	if os.Args[1] == "auth" {
		fmt.Println(`{"loggedIn":true,"authMethod":"claude.ai"}`)
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	for _, subtype := range []string{"initialize", "get_usage"} {
		var request struct {
			Type    string `json:"type"`
			ID      string `json:"request_id"`
			Request struct {
				Subtype string `json:"subtype"`
				Skip    bool   `json:"skip_behaviors"`
			} `json:"request"`
		}
		if decoder.Decode(&request) != nil || request.Type != "control_request" || request.Request.Subtype != subtype {
			os.Exit(3)
		}
		if subtype == "get_usage" {
			if !request.Request.Skip {
				os.Exit(3)
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "success", "request_id": request.ID, "response": map[string]any{
					"rate_limits_available": true, "rate_limits": map[string]any{"five_hour": map[string]any{"utilization": 25.5, "resets_at": "2027-01-15T12:00:00Z"}},
				},
			}})
		}
	}
}

// TestReadAccountUsage queries the real process boundary without sending a user prompt.
// TestReadAccountUsage 验证真实进程边界，不发送用户提示词。
func TestReadAccountUsage(t *testing.T) {
	account, err := readAccountUsage(t.Context(), os.Args[0], append(probeEnv(os.Environ()), "AISW_CLAUDE_USAGE_FIXTURE=1"))
	if err != nil || len(account.Windows) != 1 || account.Windows[0].UsedPercent != 25.5 {
		t.Fatalf("account=%+v error=%v, want five-hour window at 25.5%% and nil error", account, err)
	}
}
