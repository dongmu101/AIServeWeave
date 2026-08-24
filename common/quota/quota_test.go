package quota_test

import (
	"strings"
	"testing"

	"AIServeWeave/common/quota"
)

func TestUnlimited(t *testing.T) {
	tests := []struct {
		name   string
		limits quota.Limits
		want   bool
	}{
		{name: "nothing set", limits: quota.Limits{}, want: true},
		{name: "requests bounded", limits: quota.Limits{RequestsPerMinute: 60}, want: false},
		{name: "tokens bounded", limits: quota.Limits{TokensPerMinute: 1000}, want: false},
		{name: "concurrency bounded", limits: quota.Limits{MaxConcurrent: 4}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.limits.Unlimited(); got != tt.want {
				t.Errorf("Unlimited() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		limits  quota.Limits
		wantErr string
	}{
		{name: "all zero is unlimited, not invalid", limits: quota.Limits{}},
		{name: "positive values", limits: quota.Limits{RequestsPerMinute: 60, TokensPerMinute: 100000, MaxConcurrent: 8}},
		{name: "negative requests", limits: quota.Limits{RequestsPerMinute: -1}, wantErr: "requests_per_minute"},
		{name: "negative tokens", limits: quota.Limits{TokensPerMinute: -1}, wantErr: "tokens_per_minute"},
		{name: "negative concurrency", limits: quota.Limits{MaxConcurrent: -1}, wantErr: "max_concurrent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.limits.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() error = nil, want one mentioning %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
