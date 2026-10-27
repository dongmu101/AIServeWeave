package claudecode

import "testing"

// TestParseAccountUsage validates only correlated, explicitly available subscription windows.
// TestParseAccountUsage 只接受请求匹配且明确可用的订阅窗口。
func TestParseAccountUsage(t *testing.T) {
	wrap := func(data string) string {
		return `{"type":"control_response","response":{"subtype":"success","request_id":"aisw_account_usage","response":` + data + `}}`
	}
	for _, tc := range []struct {
		name, data string
		wantCount  int
		wantUsed   float64
		wantError  bool
	}{
		{"real windows", wrap(`{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":25.5,"resets_at":"2027-01-15T12:00:00Z"},"seven_day":{"utilization":0,"resets_at":"2027-01-21T12:00:00Z"}}}`), 2, 25.5, false},
		{"not available", wrap(`{"rate_limits_available":false,"rate_limits":null}`), 0, 0, true},
		{"nullable optional windows and model scope", wrap(`{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":25,"resets_at":"2027-01-15T12:00:00Z"},"seven_day":null,"seven_day_opus":null,"model_scoped":[]}}`), 1, 25, false},
		{"over limit stays observable", wrap(`{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":109,"resets_at":"2027-01-15T12:00:00Z"}}}`), 1, 109, false},
		{"partial windows preserve known quota", wrap(`{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":25,"resets_at":"2027-01-15T12:00:00Z"},"seven_day":{"utilization":null,"resets_at":null}}}`), 1, 25, false},
		{"missing utilization", wrap(`{"rate_limits_available":true,"rate_limits":{"five_hour":{"resets_at":"2027-01-15T12:00:00Z"}}}`), 0, 0, true},
		{"invalid reset", wrap(`{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":20,"resets_at":"bad"}}}`), 0, 0, true},
		{"wrong request", `{"type":"control_response","response":{"subtype":"success","request_id":"other","response":{}}}`, 0, 0, true},
		{"raw error withheld", `{"type":"control_response","response":{"subtype":"error","request_id":"aisw_account_usage","error":"SECRET"}}`, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAccountUsage([]byte(tc.data))
			if (err != nil) != tc.wantError || len(got.Windows) != tc.wantCount {
				t.Fatalf("account=%+v error=%v, want %d windows and error=%t", got, err, tc.wantCount, tc.wantError)
			}
			if tc.wantCount > 0 && got.Windows[0].UsedPercent != tc.wantUsed {
				t.Fatalf("usage=%v, want %v", got.Windows[0].UsedPercent, tc.wantUsed)
			}
		})
	}
}
