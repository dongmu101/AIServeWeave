package comfyui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"AIServeWeave/common/runtime"
)

const (
	// wsMessageText and wsMessageBinary mirror the RFC 6455 opcodes the
	// WSConn implementation reports. Binary frames carry live previews,
	// which this adapter counts and discards.
	wsMessageText   = 1
	wsMessageBinary = 2

	// maxRawEventBytes bounds the unparsed payload kept on a WorkflowEvent.
	maxRawEventBytes = 64 << 10 // 64 KiB

	// subscriberBuffer is how many events one subscriber may fall behind
	// before the multiplexer starts dropping its events. Dropping is safe
	// by design: the final state always comes from History, never from
	// having seen every event.
	subscriberBuffer = 128

	reconnectBaseDelay = 500 * time.Millisecond
	reconnectMaxDelay  = 30 * time.Second
)

// eventMux owns the single WebSocket connection an instance of ComfyUI
// exposes and fans its events out to per-run subscribers.
//
// ComfyUI's WebSocket is instance-wide, not per-run: every submission from
// this client id shares one stream, and each frame names the run it belongs
// to. One connection per Runtime with routing by prompt id is therefore the
// only shape that does not open a connection per workflow.
type eventMux struct {
	client   *Client
	dialer   runtime.WSDialer
	clock    runtime.Clock
	logger   *slog.Logger
	clientID string

	mu          sync.Mutex
	conn        runtime.WSConn
	subscribers map[string]map[uint64]*eventStream
	nextSubID   uint64
	loopCancel  context.CancelFunc
	loopDone    chan struct{}
	closed      bool

	binaryFrames atomic.Int64
	droppedEvent atomic.Int64
	reconnects   atomic.Int64
}

func newEventMux(client *Client, dialer runtime.WSDialer, clock runtime.Clock, logger *slog.Logger, clientID string) *eventMux {
	return &eventMux{
		client:      client,
		dialer:      dialer,
		clock:       clock,
		logger:      logger,
		clientID:    clientID,
		subscribers: make(map[string]map[uint64]*eventStream),
	}
}

// newClientID returns the identity this Runtime uses on /ws and on every
// submission, so the server only streams back events for work this instance
// submitted.
func newClientID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is not recoverable here, and a fixed id would
		// silently mix two Agents' event streams together; the timestamp
		// keeps them distinct without pretending to be random.
		return "aiserveweave-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(buf[:])
}

// ensureConnected dials the event stream if it is not already running. It
// is called before every submission, because a workflow that finishes
// before the connection is up would produce no events at all.
func (m *eventMux) ensureConnected(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return m.closedError("subscribe")
	}
	if m.conn != nil {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	conn, err := m.dial(ctx)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		_ = conn.Close()
		return m.closedError("subscribe")
	}
	if m.conn != nil {
		// Another caller won the race; keep theirs and drop ours rather
		// than leaving two read loops on one instance.
		_ = conn.Close()
		return nil
	}

	m.conn = conn
	loopCtx, cancel := context.WithCancel(context.Background())
	m.loopCancel = cancel
	m.loopDone = make(chan struct{})
	go m.readLoop(loopCtx, conn, m.loopDone)
	return nil
}

func (m *eventMux) dial(ctx context.Context) (runtime.WSConn, error) {
	conn, err := m.dialer.Dial(ctx, m.client.WebSocketURL(m.clientID), m.client.Header())
	if err != nil {
		code := runtime.ClassifyTransportError(err)
		if code == "" {
			code = runtime.ErrorConnection
		}
		return nil, &runtime.RuntimeError{
			Code:      code,
			RuntimeID: m.client.runtimeID,
			Kind:      runtime.KindComfyUI,
			Operation: "subscribe",
			Message:   runtime.Redact("connect to the event stream: "+err.Error(), m.client.apiKey),
			Cause:     err,
			Retryable: true,
		}
	}
	return conn, nil
}

// subscribe registers a stream for one run's events. Events that arrive
// before subscribe are not replayed — Status reconciles against History,
// which is the authority on what actually happened.
func (m *eventMux) subscribe(promptID string) (*eventStream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, m.closedError("subscribe")
	}

	m.nextSubID++
	sub := newEventStream(m, promptID, m.nextSubID)
	if m.subscribers[promptID] == nil {
		m.subscribers[promptID] = make(map[uint64]*eventStream)
	}
	m.subscribers[promptID][sub.id] = sub
	return sub, nil
}

func (m *eventMux) unsubscribe(promptID string, id uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if subs, ok := m.subscribers[promptID]; ok {
		delete(subs, id)
		if len(subs) == 0 {
			delete(m.subscribers, promptID)
		}
	}
}

// readLoop consumes frames until the mux is closed, reconnecting on its own
// when the connection drops. The first reconnect is immediate, since the
// common case is a single dropped connection and waiting would delay every
// subscriber for no reason; repeated failures back off.
func (m *eventMux) readLoop(ctx context.Context, conn runtime.WSConn, done chan struct{}) {
	defer close(done)

	attempt := 0
	for {
		msgType, data, err := conn.Read(ctx)
		if err == nil {
			attempt = 0
			m.handleFrame(msgType, data)
			continue
		}

		_ = conn.Close()
		if ctx.Err() != nil || m.isClosed() {
			return
		}

		m.logger.Warn("comfyui event stream dropped; reconnecting",
			"runtime_id", m.client.runtimeID, "error", err)
		attempt++
		if !m.waitBeforeReconnect(ctx, attempt) {
			return
		}

		next, dialErr := m.dial(ctx)
		if dialErr != nil {
			m.logger.Warn("comfyui event stream reconnect failed",
				"runtime_id", m.client.runtimeID, "attempt", attempt, "error", dialErr)
			conn = deadConn{err: dialErr}
			continue
		}

		m.reconnects.Add(1)
		conn = next
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			_ = conn.Close()
			return
		}
		m.conn = conn
		m.mu.Unlock()
	}
}

// waitBeforeReconnect sleeps out the backoff for this attempt and reports
// whether the loop should continue. Attempt 1 does not wait at all.
func (m *eventMux) waitBeforeReconnect(ctx context.Context, attempt int) bool {
	if attempt <= 1 {
		return true
	}
	delay := reconnectBaseDelay << (attempt - 2)
	if delay > reconnectMaxDelay || delay <= 0 {
		delay = reconnectMaxDelay
	}
	timer, stop := m.clock.NewTimer(delay)
	defer stop()
	select {
	case <-timer:
		return true
	case <-ctx.Done():
		return false
	}
}

// deadConn stands in for a connection that could not be re-established, so
// the read loop's next iteration fails immediately and re-enters the
// backoff path instead of the loop needing a second retry structure.
type deadConn struct{ err error }

func (c deadConn) Read(context.Context) (int, []byte, error) { return 0, nil, c.err }
func (c deadConn) Close() error                              { return nil }

// wsFrame is the envelope of every text frame ComfyUI sends.
type wsFrame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// wsFrameData holds the fields this adapter reads out of a frame's data
// object. Every field is optional: which ones are present depends on the
// event type, and unknown ones are preserved through WorkflowEvent.Raw.
type wsFrameData struct {
	PromptID string          `json:"prompt_id"`
	Node     json.RawMessage `json:"node"`
}

// handleFrame parses one frame and routes it. A frame that cannot be parsed
// is counted and dropped: a malformed or unrecognized message must never
// tear down the connection that every other run depends on.
func (m *eventMux) handleFrame(msgType int, data []byte) {
	if msgType == wsMessageBinary {
		// Binary frames are live previews. The real outputs come from
		// History and /view, so they are counted and discarded.
		m.binaryFrames.Add(1)
		return
	}

	var frame wsFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		m.logger.Debug("comfyui sent an unparsable event frame",
			"runtime_id", m.client.runtimeID, "error", err)
		return
	}
	var payload wsFrameData
	if len(frame.Data) > 0 {
		// A data field that is not an object (or is missing fields) is
		// normal for some event types; the zero value is the right result.
		_ = json.Unmarshal(frame.Data, &payload)
	}

	eventType, nodeID := normalizeEvent(frame.Type, payload)
	event := runtime.WorkflowEvent{
		Type:       eventType,
		RunID:      payload.PromptID,
		NodeID:     nodeID,
		Raw:        truncateRaw(data),
		ReceivedAt: m.clock.Now(),
	}

	if payload.PromptID == "" {
		// Instance-wide events such as queue status name no run; every
		// subscriber needs them, since they describe the queue each one is
		// waiting in.
		m.broadcast(event)
		return
	}
	m.deliver(payload.PromptID, event)
}

// normalizeEvent maps ComfyUI's event vocabulary onto the runtime's. The
// "executing" event is the one that carries two meanings: a node id means
// that node started, and a null node means the run finished executing.
func normalizeEvent(name string, data wsFrameData) (runtime.WorkflowEventType, string) {
	switch name {
	case "status":
		return runtime.WorkflowEventQueueChanged, ""
	case "execution_start":
		return runtime.WorkflowEventStarted, ""
	case "execution_cached":
		return runtime.WorkflowEventCached, ""
	case "executing":
		if nodeID, ok := decodeNodeID(data.Node); ok {
			return runtime.WorkflowEventNodeStarted, nodeID
		}
		return runtime.WorkflowEventCompleted, ""
	case "progress":
		nodeID, _ := decodeNodeID(data.Node)
		return runtime.WorkflowEventProgress, nodeID
	case "executed":
		nodeID, _ := decodeNodeID(data.Node)
		return runtime.WorkflowEventNodeOutput, nodeID
	case "execution_success":
		return runtime.WorkflowEventSucceeded, ""
	case "execution_error":
		return runtime.WorkflowEventFailed, ""
	case "execution_interrupted":
		return runtime.WorkflowEventCancelled, ""
	default:
		return runtime.WorkflowEventUnknown, ""
	}
}

// decodeNodeID reads a node id that ComfyUI sends as a string, sometimes as
// a number, and as null to mean "no node".
func decodeNodeID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, s != ""
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String(), true
	}
	return "", false
}

func truncateRaw(data []byte) json.RawMessage {
	if len(data) <= maxRawEventBytes {
		return json.RawMessage(append([]byte(nil), data...))
	}
	// Keep the payload valid JSON rather than handing callers a truncated
	// fragment they cannot parse.
	return json.RawMessage(`{"truncated":true}`)
}

func (m *eventMux) deliver(promptID string, event runtime.WorkflowEvent) {
	m.mu.Lock()
	subs := make([]*eventStream, 0, len(m.subscribers[promptID]))
	for _, sub := range m.subscribers[promptID] {
		subs = append(subs, sub)
	}
	m.mu.Unlock()

	for _, sub := range subs {
		sub.deliver(event)
	}
}

func (m *eventMux) broadcast(event runtime.WorkflowEvent) {
	m.mu.Lock()
	var subs []*eventStream
	for _, byID := range m.subscribers {
		for _, sub := range byID {
			subs = append(subs, sub)
		}
	}
	m.mu.Unlock()

	for _, sub := range subs {
		sub.deliver(event)
	}
}

func (m *eventMux) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *eventMux) closedError(operation string) error {
	return &runtime.RuntimeError{
		Code:      runtime.ErrorClosed,
		RuntimeID: m.client.runtimeID,
		Kind:      runtime.KindComfyUI,
		Operation: operation,
		Message:   "runtime is closed",
		Cause:     runtime.ErrRuntimeClosed,
	}
}

// close stops the read loop, closes the connection and ends every
// subscriber stream. It is idempotent and waits for the read loop to exit,
// so a closed Runtime leaves no goroutine behind.
func (m *eventMux) close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	conn, cancel, done := m.conn, m.loopCancel, m.loopDone
	subs := make([]*eventStream, 0)
	for _, byID := range m.subscribers {
		for _, sub := range byID {
			subs = append(subs, sub)
		}
	}
	m.subscribers = make(map[string]map[uint64]*eventStream)
	m.conn = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	var err error
	if conn != nil {
		err = conn.Close()
	}
	if done != nil {
		<-done
	}
	for _, sub := range subs {
		sub.finish()
	}
	return err
}

// eventStream is one subscriber's view of a run's events.
//
// The multiplexer must never block on a slow consumer — one stalled caller
// would stall every other run's events and the connection itself — so
// events land in a bounded buffer that a pump goroutine drains into the
// stream, and a subscriber that falls too far behind loses events rather
// than stalling the instance.
type eventStream struct {
	*runtime.ChanStream[runtime.WorkflowEvent]

	mux      *eventMux
	promptID string
	id       uint64

	buf        chan runtime.WorkflowEvent
	finishOnce sync.Once
	dead       atomic.Bool
	dropped    atomic.Int64
}

func newEventStream(mux *eventMux, promptID string, id uint64) *eventStream {
	s := &eventStream{
		ChanStream: runtime.NewChanStream[runtime.WorkflowEvent](0),
		mux:        mux,
		promptID:   promptID,
		id:         id,
		buf:        make(chan runtime.WorkflowEvent, subscriberBuffer),
	}
	go s.pump()
	return s
}

// deliver queues one event without ever blocking the multiplexer.
func (s *eventStream) deliver(event runtime.WorkflowEvent) {
	if s.dead.Load() {
		return
	}
	select {
	case s.buf <- event:
	default:
		s.dropped.Add(1)
		s.mux.droppedEvent.Add(1)
		s.mux.logger.Warn("comfyui subscriber fell behind; dropping an event",
			"runtime_id", s.mux.client.runtimeID, "run_id", s.promptID, "type", event.Type)
	}

	// A run only ends once, so the stream is closed after its terminal
	// event instead of leaving callers to guess when to stop reading.
	switch event.Type {
	case runtime.WorkflowEventSucceeded, runtime.WorkflowEventFailed, runtime.WorkflowEventCancelled:
		s.finish()
	}
}

// finish signals that no further events will be queued. The pump drains
// what is already buffered and then ends the stream normally.
func (s *eventStream) finish() {
	s.finishOnce.Do(func() { close(s.buf) })
}

func (s *eventStream) pump() {
	for event := range s.buf {
		if !s.ChanStream.Send(event) {
			s.dead.Store(true)
			return // the consumer closed the stream
		}
	}
	s.ChanStream.CloseWithError(nil)
}

// Close unregisters the subscriber and releases the stream. It is safe to
// call more than once, and callers must call it even after the stream has
// ended on its own.
func (s *eventStream) Close() error {
	s.dead.Store(true)
	s.mux.unsubscribe(s.promptID, s.id)
	err := s.ChanStream.Close()
	s.finish()
	return err
}

// Dropped reports how many events were discarded because this subscriber
// could not keep up.
func (s *eventStream) Dropped() int64 { return s.dropped.Load() }

var (
	_ runtime.Stream[runtime.WorkflowEvent] = (*eventStream)(nil)
	_ runtime.WSConn                        = deadConn{}
)
