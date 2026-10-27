package alertengine_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/alertengine"
)

func TestSenderNotifyPostsThePayloadAndSucceedsOnFirstTry(t *testing.T) {
	var gotBody alertengine.WebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := alertengine.NewSender(nil, nil)
	attempts, err := sender.Notify(context.Background(), server.URL, alertengine.WebhookPayload{AlertID: "ali_1", Status: "firing"})
	if err != nil {
		t.Fatalf("Notify() error = %v, want nil", err)
	}
	if attempts != 1 {
		t.Fatalf("Notify() attempts = %d, want 1", attempts)
	}
	if gotBody.AlertID != "ali_1" {
		t.Fatalf("posted body = %+v, want AlertID=ali_1", gotBody)
	}
}

func TestSenderNotifyRetriesUpToThreeTimesThenGivesUp(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	clock := &fakeClock{now: time.Now()}
	sender := alertengine.NewSender(clock, nil)
	attempts, err := sender.Notify(context.Background(), server.URL, alertengine.WebhookPayload{})
	if err == nil {
		t.Fatal("Notify() error = nil, want an error after exhausting retries")
	}
	if attempts != 3 {
		t.Fatalf("Notify() attempts = %d, want 3", attempts)
	}
	if calls != 3 {
		t.Fatalf("server received %d calls, want 3", calls)
	}
}

func TestSenderNotifyRejectsAnInvalidURL(t *testing.T) {
	sender := alertengine.NewSender(nil, nil)
	tests := []struct {
		name string
		url  string
	}{
		{name: "empty scheme", url: "example.test/hook"},
		{name: "not http(s)", url: "ftp://example.test/hook"},
		{name: "contains userinfo", url: "https://user:pass@example.test/hook"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := sender.Notify(context.Background(), tt.url, alertengine.WebhookPayload{}); err == nil {
				t.Fatalf("Notify(%q) error = nil, want a validation error", tt.url)
			}
		})
	}
}
