package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

const (
	maxPending    = 8
	maxCalls      = 64
	maxEventBytes = 1 << 20
)

var (
	errCapacity       = errors.New("tool capacity exceeded")
	errClosed         = errors.New("tool broker closed")
	errCallRejected   = errors.New("tool result rejected")
	errInvalidTool    = errors.New("invalid probe tool request")
	errResultTooLarge = errors.New("tool result exceeds size limit")
)

type principal struct{ tenant, key string }
type toolResult struct {
	Text    string
	IsError bool
}
type toolCall struct{ ID, Name, Label string }

type broker struct {
	owner   principal
	mu      sync.Mutex
	pending map[string]chan toolResult
	total   int
	closed  bool
	calls   chan toolCall
	done    chan struct{}
}

func newBroker(owner principal) *broker {
	return &broker{owner: owner, pending: make(map[string]chan toolResult), calls: make(chan toolCall, maxPending), done: make(chan struct{})}
}

func (b *broker) call(ctx context.Context, name string, args json.RawMessage) (toolResult, error) {
	var input struct {
		Label string `json:"label"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if name != "probe_echo" || len(args) > maxEventBytes || dec.Decode(&input) != nil || input.Label == "" || len(input.Label) > 128 || dec.Decode(new(any)) != io.EOF {
		return toolResult{}, errInvalidTool
	}
	if err := ctx.Err(); err != nil {
		return toolResult{}, err
	}
	id := randomID()
	result := make(chan toolResult, 1)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return toolResult{}, errClosed
	}
	if len(b.pending) >= maxPending || b.total >= maxCalls {
		b.mu.Unlock()
		return toolResult{}, errCapacity
	}
	b.pending[id] = result
	b.total++
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, id); b.mu.Unlock() }()
	select {
	case b.calls <- toolCall{ID: id, Name: name, Label: input.Label}:
	case <-ctx.Done():
		return toolResult{}, ctx.Err()
	case <-b.done:
		return toolResult{}, errClosed
	default:
		return toolResult{}, errCapacity
	}
	select {
	case r := <-result:
		return r, nil
	case <-ctx.Done():
		return toolResult{}, ctx.Err()
	case <-b.done:
		return toolResult{}, errClosed
	}
}

func (b *broker) resolve(owner principal, id string, result toolResult) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.pending[id]
	if b.closed || owner != b.owner || !ok {
		return errCallRejected
	}
	if len(result.Text) > maxEventBytes {
		return errResultTooLarge
	}
	delete(b.pending, id)
	ch <- result
	return nil
}

func (b *broker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.done)
		clear(b.pending)
	}
}

func randomID() string {
	var value [16]byte
	rand.Read(value[:])
	return hex.EncodeToString(value[:])
}
