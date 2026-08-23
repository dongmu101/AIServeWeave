package openai

import "testing"

func TestParseErrorMessageFromEnvelope(t *testing.T) {
	body := []byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`)
	if got := parseErrorMessage(body); got != "bad request" {
		t.Fatalf("parseErrorMessage() = %q, want %q", got, "bad request")
	}
}

func TestParseErrorMessageFallsBackToRawBody(t *testing.T) {
	body := []byte(`not json`)
	if got := parseErrorMessage(body); got != "not json" {
		t.Fatalf("parseErrorMessage() = %q, want %q", got, "not json")
	}
}

func TestParseErrorMessageEmptyBody(t *testing.T) {
	if got := parseErrorMessage(nil); got == "" {
		t.Fatal("expected a non-empty fallback message for an empty body")
	}
}

func TestParseErrorMessageEnvelopeWithoutMessage(t *testing.T) {
	body := []byte(`{"error":{"type":"invalid_request_error"}}`)
	got := parseErrorMessage(body)
	if got == "" {
		t.Fatal("expected fallback to raw body when error.message is empty")
	}
}

func TestParseErrorMessageTruncatesLongBody(t *testing.T) {
	long := make([]byte, maxErrorMessageLen+500)
	for i := range long {
		long[i] = 'a'
	}
	got := parseErrorMessage(long)
	if len(got) > maxErrorMessageLen+20 {
		t.Fatalf("message was not truncated, len=%d", len(got))
	}
}
