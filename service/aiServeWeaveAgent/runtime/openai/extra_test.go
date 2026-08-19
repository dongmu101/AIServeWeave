package openai

import (
	"encoding/json"
	"errors"
	"testing"

	"AIServeWeave/service/aiServeWeaveAgent/runtime"
)

func TestMergeExtraFieldsAddsUnmodeledKeys(t *testing.T) {
	c, err := NewClient(ClientConfig{BaseURL: "http://example.com", Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	dto := struct {
		Model string `json:"model"`
	}{Model: "m1"}
	extra := map[string]json.RawMessage{"top_k": json.RawMessage(`40`)}
	modeled := map[string]bool{"model": true}

	merged, err := c.mergeExtraFields("op", dto, extra, modeled)
	if err != nil {
		t.Fatal(err)
	}
	if string(merged["model"]) != `"m1"` {
		t.Errorf("model = %s, want \"m1\"", merged["model"])
	}
	if string(merged["top_k"]) != "40" {
		t.Errorf("top_k = %s, want 40", merged["top_k"])
	}
}

func TestMergeExtraFieldsRejectsCollisionWithModeledField(t *testing.T) {
	c, err := NewClient(ClientConfig{BaseURL: "http://example.com", Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	dto := struct {
		Model string `json:"model"`
	}{Model: "m1"}
	// "temperature" is a modeled field name even though dto has no such
	// field currently set — the check is against the field name set, not
	// against what happens to be present in this particular marshal.
	extra := map[string]json.RawMessage{"temperature": json.RawMessage(`0.7`)}
	modeled := map[string]bool{"model": true, "temperature": true}

	_, err = c.mergeExtraFields("op", dto, extra, modeled)
	var rtErr *runtime.RuntimeError
	if !errors.As(err, &rtErr) || rtErr.Code != runtime.ErrorInvalidConfig {
		t.Fatalf("error = %v, want a RuntimeError with Code %s", err, runtime.ErrorInvalidConfig)
	}
}

func TestMergeExtraFieldsNoExtraReturnsBaseUnchanged(t *testing.T) {
	c, err := NewClient(ClientConfig{BaseURL: "http://example.com", Kind: runtime.KindVLLM, RuntimeID: "r1"})
	if err != nil {
		t.Fatal(err)
	}

	dto := struct {
		Model string `json:"model"`
	}{Model: "m1"}

	merged, err := c.mergeExtraFields("op", dto, nil, map[string]bool{"model": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 1 || string(merged["model"]) != `"m1"` {
		t.Fatalf("unexpected merged result: %v", merged)
	}
}
