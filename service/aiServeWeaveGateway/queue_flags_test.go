package main

import (
	"log/slog"
	"testing"
	"time"
)

func TestValidateQueueFlagsRequiresRedis(t *testing.T) {
	if err := validateQueueFlags(time.Minute, 0, ""); err == nil {
		t.Error("a workflow queue without -redis-addr was accepted")
	}
	if err := validateQueueFlags(0, time.Second, ""); err == nil {
		t.Error("a chat queue without -redis-addr was accepted")
	}
	if err := validateQueueFlags(0, 0, ""); err != nil {
		t.Errorf("both queues off without Redis must start: %v", err)
	}
	if err := validateQueueFlags(time.Minute, time.Second, "127.0.0.1:6379"); err != nil {
		t.Errorf("queues with Redis rejected: %v", err)
	}
}

func TestQueuesOffBuildNothing(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	chat, err := buildChatQueue(nil, "r1", "aisw-chat", 0, 0, 0, 0, nil, logger)
	if err != nil || chat != nil {
		t.Fatalf("chat queue with zero max wait = %v %v, want none", chat, err)
	}
	wf, closeFn, err := buildWorkflowQueue("", nil, "r1", "aisw-workflow", 0, 0, 0, 0, nil, logger)
	if err != nil || wf != nil {
		t.Fatalf("workflow queue with zero max wait = %v %v, want none", wf, err)
	}
	closeFn()
}
