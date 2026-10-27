package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// TestProbeClientReverseResults returns parallel tool results in reverse order.
// TestProbeClientReverseResults 将并行工具结果按相反顺序返回。
func TestProbeClientReverseResults(t *testing.T) {
	b := newBroker(principal{"t", "k"})
	defer b.close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan clientStats, 1)
	go func() { done <- serveProbeClient(ctx, b, "token", "parallel") }()
	results := make(chan error, 2)
	for _, label := range []string{"first", "second"} {
		go func() {
			args, _ := json.Marshal(map[string]string{"label": label})
			result, err := b.call(ctx, "probe_echo", args)
			if err == nil && (result.IsError || (label == "second" && result.Text != "token")) {
				t.Errorf("result = %+v, want successful %s result", result, label)
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	stats := <-done
	if stats.Calls != 2 || stats.First != 1 || stats.Second != 1 || !reflect.DeepEqual(stats.Order, []string{"second", "first"}) {
		t.Fatalf("stats = %+v, want two calls resolved second then first", stats)
	}
}
