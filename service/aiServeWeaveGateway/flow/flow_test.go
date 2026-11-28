package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AIServeWeave/common/flowdef"
)

// fakeClock is a manually advanced runtime.Clock, so timeouts are tested
// without real waiting.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at   time.Time
	ch   chan time.Time
	done bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		stopped := !t.done
		t.done = true
		return stopped
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, t := range c.timers {
		if !t.done && !t.at.After(c.now) {
			t.done = true
			t.ch <- c.now
		}
	}
}

// dispatcherFunc adapts a function to Dispatcher.
type dispatcherFunc func(ctx context.Context, call ChatCall, delta func(string)) (ChatReply, error)

func (f dispatcherFunc) Chat(ctx context.Context, call ChatCall, delta func(string)) (ChatReply, error) {
	return f(ctx, call, delta)
}

func (dispatcherFunc) GenerateImage(context.Context, ImageCall) (MediaReply, error) {
	return MediaReply{}, &Error{Code: "unsupported"}
}

func (dispatcherFunc) RunTemplate(context.Context, TemplateCall) (MediaReply, error) {
	return MediaReply{}, &Error{Code: "unsupported"}
}

func (dispatcherFunc) GenerateVideo(context.Context, VideoCall) (MediaReply, error) {
	return MediaReply{}, &Error{Code: "unsupported"}
}

// replies answers each call with a fixed text chosen by model.
func replies(texts map[string]string) Dispatcher {
	return dispatcherFunc(func(_ context.Context, call ChatCall, _ func(string)) (ChatReply, error) {
		return ChatReply{Text: texts[call.Model], Usage: Usage{PromptTokens: 10, CompletionTokens: 2}}, nil
	})
}

// blockUntilCanceled is a model call that only ends with its context,
// announcing itself on started first.
func blockUntilCanceled(started chan<- string) func(ctx context.Context, call ChatCall) (ChatReply, error) {
	return func(ctx context.Context, call ChatCall) (ChatReply, error) {
		started <- call.Model
		<-ctx.Done()
		return ChatReply{}, ctx.Err()
	}
}

func node(id string, t flowdef.NodeType, config string) flowdef.Node {
	n := flowdef.Node{ID: id, Type: t}
	if config != "" {
		n.Config = json.RawMessage(config)
	}
	return n
}

func llm(id, model, prompt string) flowdef.Node {
	return node(id, flowdef.NodeLLM, fmt.Sprintf(`{"model":%q,"prompt":%q}`, model, prompt))
}

func edge(from, to string, branch ...flowdef.Branch) flowdef.Edge {
	e := flowdef.Edge{From: from, To: to}
	if len(branch) > 0 {
		e.Branch = branch[0]
	}
	return e
}

var topicInput = []flowdef.Input{{Name: "topic", Type: flowdef.TypeText, Required: true}}

func topic(v string) map[string]json.RawMessage {
	raw, _ := json.Marshal(v)
	return map[string]json.RawMessage{"topic": raw}
}

type recorder struct{ events []Event }

func (r *recorder) emit(e Event) { r.events = append(r.events, e) }

func (r *recorder) types() []string {
	var out []string
	for _, e := range r.events {
		s := string(e.Type)
		if e.Node != "" {
			s += ":" + e.Node
		}
		out = append(out, s)
	}
	return out
}

func (r *recorder) step(node string) *Step {
	for _, e := range r.events {
		if e.Node == node && e.Step != nil {
			return e.Step
		}
	}
	return nil
}

func (r *recorder) started(node string) bool {
	for _, e := range r.events {
		if e.Type == EventNodeStarted && e.Node == node {
			return true
		}
	}
	return false
}

func mustPlan(t *testing.T, cfg Config, def flowdef.Definition, inputs map[string]json.RawMessage) *Plan {
	t.Helper()
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Plan(def, inputs)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	return p
}

// runAsync starts a run and returns a channel for its result; the recorder may
// be read once the result has arrived.
func runAsync(ctx context.Context, p *Plan) (<-chan Result, *recorder) {
	rec := &recorder{}
	out := make(chan Result, 1)
	go func() { out <- p.Run(ctx, rec.emit) }()
	return out, rec
}

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting")
		panic("unreachable")
	}
}

func TestRunLinear(t *testing.T) {
	var got ChatCall
	d := dispatcherFunc(func(_ context.Context, call ChatCall, delta func(string)) (ChatReply, error) {
		got = call
		delta("Hello ")
		delta("world")
		return ChatReply{Text: "Hello world", Usage: Usage{PromptTokens: 7, CompletionTokens: 2}}, nil
	})
	def := flowdef.Definition{
		Inputs: topicInput,
		Nodes: []flowdef.Node{
			node("start", flowdef.NodeInput, ""),
			node("draft", flowdef.NodeLLM, `{"model":"m","system":"Be brief.","prompt":"Write about {{inputs.topic}}","temperature":0.3}`),
			node("wrap", flowdef.NodeTemplate, `{"template":"<<{{draft.text}}>>"}`),
			node("end", flowdef.NodeOutput, ""),
		},
		Edges:   []flowdef.Edge{edge("start", "draft"), edge("draft", "wrap"), edge("wrap", "end")},
		Outputs: []flowdef.Output{{Name: "answer", Ref: "{{wrap.text}}"}},
	}
	p := mustPlan(t, Config{Dispatcher: d}, def, topic("cats"))
	rec := &recorder{}
	res := p.Run(context.Background(), rec.emit)

	if res.Status != RunSucceeded || res.Outputs["answer"] != "<<Hello world>>" {
		t.Fatalf("Run() = %+v", res)
	}
	if res.Usage != (Usage{PromptTokens: 7, CompletionTokens: 2}) {
		t.Fatalf("Usage = %+v", res.Usage)
	}
	if got.Model != "m" || got.System != "Be brief." || got.Prompt != "Write about cats" || got.Temperature == nil || *got.Temperature != 0.3 {
		t.Fatalf("ChatCall = %+v", got)
	}
	want := []string{
		"run_started", "node_started:start", "node_finished:start",
		"node_started:draft", "node_delta:draft", "node_delta:draft", "node_finished:draft",
		"node_started:wrap", "node_finished:wrap", "node_started:end", "node_finished:end", "run_finished",
	}
	if g := rec.types(); strings.Join(g, " ") != strings.Join(want, " ") {
		t.Fatalf("events =\n%v\nwant\n%v", g, want)
	}
	step := rec.step("draft")
	if step.Status != NodeSucceeded || step.Inputs["prompt"] != "Write about cats" || step.Model != "m" || step.Usage.PromptTokens != 7 {
		t.Fatalf("draft step = %+v", step)
	}
	if last := rec.events[len(rec.events)-1]; last.Result == nil || last.Result.Status != RunSucceeded {
		t.Fatalf("run_finished carries %+v", last.Result)
	}
}

// branching is start → draft → check ─true→ fix → end, with check ─false→ end
// joining directly.
func branching() flowdef.Definition {
	return flowdef.Definition{
		Inputs: topicInput,
		Nodes: []flowdef.Node{
			node("start", flowdef.NodeInput, ""),
			llm("draft", "m", "{{inputs.topic}}"),
			node("check", flowdef.NodeCondition, `{"left":"{{draft.text}}","op":"contains","right":"TODO"}`),
			node("fix", flowdef.NodeTemplate, `{"template":"fixed: {{draft.text}}"}`),
			node("end", flowdef.NodeOutput, ""),
		},
		Edges: []flowdef.Edge{
			edge("start", "draft"), edge("draft", "check"),
			edge("check", "fix", flowdef.BranchTrue), edge("check", "end", flowdef.BranchFalse), edge("fix", "end"),
		},
		Outputs: []flowdef.Output{{Name: "answer", Ref: "{{fix.text}}|{{draft.text}}"}},
	}
}

func TestRunConditionBranches(t *testing.T) {
	for _, tt := range []struct {
		reply, answer string
		fix           NodeStatus
	}{
		{"has TODO\n", "fixed: has TODO\n|has TODO\n", NodeSucceeded},
		{"done", "|done", NodeSkipped},
	} {
		t.Run(tt.reply, func(t *testing.T) {
			p := mustPlan(t, Config{Dispatcher: replies(map[string]string{"m": tt.reply})}, branching(), topic("x"))
			rec := &recorder{}
			res := p.Run(context.Background(), rec.emit)
			if res.Status != RunSucceeded || res.Outputs["answer"] != tt.answer {
				t.Fatalf("Run() = %+v, want answer %q", res, tt.answer)
			}
			if s := rec.step("fix"); s == nil || s.Status != tt.fix {
				t.Fatalf("fix step = %+v, want %s", s, tt.fix)
			}
			if s := rec.step("end"); s == nil || s.Status != NodeSucceeded {
				t.Fatalf("end step = %+v: the join must run when any branch reaches it", s)
			}
		})
	}
}

func TestRunSkippedOutputStillSucceeds(t *testing.T) {
	def := branching()
	def.Edges = def.Edges[:3] // drop check ─false→ end and fix → end
	def.Edges = append(def.Edges, edge("fix", "end"))
	p := mustPlan(t, Config{Dispatcher: replies(map[string]string{"m": "done"})}, def, topic("x"))
	rec := &recorder{}
	res := p.Run(context.Background(), rec.emit)
	if res.Status != RunSucceeded || res.Outputs != nil {
		t.Fatalf("Run() = %+v, want success without outputs", res)
	}
	for _, n := range []string{"fix", "end"} {
		if s := rec.step(n); s == nil || s.Status != NodeSkipped {
			t.Fatalf("%s step = %+v, want skipped", n, s)
		}
	}
}

// parallel is start → a, start → b, both → end.
func parallel() flowdef.Definition {
	return flowdef.Definition{
		Nodes: []flowdef.Node{
			node("start", flowdef.NodeInput, ""), llm("a", "a", "x"), llm("b", "b", "x"), node("end", flowdef.NodeOutput, ""),
		},
		Edges:   []flowdef.Edge{edge("start", "a"), edge("start", "b"), edge("a", "end"), edge("b", "end")},
		Outputs: []flowdef.Output{{Name: "both", Ref: "{{a.text}}+{{b.text}}"}},
	}
}

func TestRunParallelBranches(t *testing.T) {
	started, release := make(chan string, 2), make(chan struct{})
	d := dispatcherFunc(func(ctx context.Context, call ChatCall, _ func(string)) (ChatReply, error) {
		started <- call.Model
		<-release
		return ChatReply{Text: call.Model}, nil
	})
	done, _ := runAsync(context.Background(), mustPlan(t, Config{Dispatcher: d}, parallel(), nil))
	// Both calls must be in flight at once; a sequential engine would block
	// here with only one started.
	await(t, started)
	await(t, started)
	close(release)
	if res := await(t, done); res.Status != RunSucceeded || res.Outputs["both"] != "a+b" {
		t.Fatalf("Run() = %+v", res)
	}
}

func TestRunMaxParallel(t *testing.T) {
	var inFlight, peak atomic.Int32
	d := dispatcherFunc(func(context.Context, ChatCall, func(string)) (ChatReply, error) {
		n := inFlight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(10 * time.Millisecond)
		inFlight.Add(-1)
		return ChatReply{Text: "ok"}, nil
	})
	res := mustPlan(t, Config{Dispatcher: d, MaxParallel: 1}, parallel(), nil).Run(context.Background(), nil)
	if res.Status != RunSucceeded || peak.Load() != 1 {
		t.Fatalf("Run() = %+v with %d calls in flight at once, want 1", res, peak.Load())
	}
}

func TestRunJSONFields(t *testing.T) {
	def := branching()
	def.Nodes[1] = node("draft", flowdef.NodeLLM, `{"model":"m","prompt":"{{inputs.topic}}",
		"json_fields":[{"name":"score","type":"number","description":"0-10"},{"name":"ok","type":"boolean"},{"name":"why","type":"text"}]}`)
	def.Nodes[2] = node("check", flowdef.NodeCondition, `{"left":"{{draft.score}}","op":"gte","right":"7"}`)
	def.Outputs = []flowdef.Output{{Name: "summary", Ref: "{{draft.score}}/{{draft.ok}}/{{draft.why}}"}}

	var system string
	d := dispatcherFunc(func(_ context.Context, call ChatCall, _ func(string)) (ChatReply, error) {
		system = call.System
		return ChatReply{Text: "Sure:\n```json\n{\"score\": \"8.0\", \"ok\": \"TRUE\", \"why\": 3}\n```"}, nil
	})
	rec := &recorder{}
	res := mustPlan(t, Config{Dispatcher: d}, def, topic("x")).Run(context.Background(), rec.emit)
	if res.Status != RunSucceeded || res.Outputs["summary"] != "8/true/3" {
		t.Fatalf("Run() = %+v", res)
	}
	if s := rec.step("fix"); s.Status != NodeSucceeded {
		t.Fatalf("score 8 ≥ 7 should take the true branch, fix = %+v", s)
	}
	if !strings.Contains(system, `"score" (number): 0-10`) || !strings.Contains(system, `"why" (string)`) {
		t.Fatalf("system prompt lacks the field list:\n%s", system)
	}

	for _, reply := range []string{"no json here", `{"score": 8, "ok": true}`, `{"score": "high", "ok": true, "why": "x"}`} {
		t.Run(reply, func(t *testing.T) {
			p := mustPlan(t, Config{Dispatcher: replies(map[string]string{"m": reply})}, def, topic("x"))
			rec := &recorder{}
			res := p.Run(context.Background(), rec.emit)
			if res.Status != RunFailed || res.Error != CodeInvalidJSONOutput || res.FailedNode != "draft" {
				t.Fatalf("Run() = %+v, want %s at draft", res, CodeInvalidJSONOutput)
			}
			if s := rec.step("draft"); s.Outputs[flowdef.FieldText] != reply {
				t.Fatalf("a failed parse should still show the reply, got %+v", s.Outputs)
			}
		})
	}
}

func TestRunNodeFailureCancelsSiblings(t *testing.T) {
	started := make(chan string, 2)
	slow := blockUntilCanceled(started)
	d := dispatcherFunc(func(ctx context.Context, call ChatCall, _ func(string)) (ChatReply, error) {
		if call.Model == "b" {
			return slow(ctx, call)
		}
		<-started // fail only once the sibling is in flight
		return ChatReply{Usage: Usage{PromptTokens: 5}}, &Error{Code: "rate_limited"}
	})
	rec := &recorder{}
	res := mustPlan(t, Config{Dispatcher: d}, parallel(), nil).Run(context.Background(), rec.emit)
	if res.Status != RunFailed || res.Error != "rate_limited" || res.FailedNode != "a" {
		t.Fatalf("Run() = %+v, want rate_limited at a", res)
	}
	if s := rec.step("b"); s == nil || s.Status != NodeCanceled {
		t.Fatalf("b step = %+v, want canceled", s)
	}
	if rec.started("end") {
		t.Fatal("end started after a node failed")
	}
	if res.Usage.PromptTokens != 5 {
		t.Fatalf("usage of the failed call was dropped: %+v", res.Usage)
	}
}

func TestRunDispatcherErrors(t *testing.T) {
	for name, err := range map[string]error{
		"model_unavailable": &Error{Code: "model_unavailable"},
		CodeUpstream:        errors.New("connection reset"),
	} {
		t.Run(name, func(t *testing.T) {
			d := dispatcherFunc(func(context.Context, ChatCall, func(string)) (ChatReply, error) { return ChatReply{}, err })
			res := mustPlan(t, Config{Dispatcher: d}, branching(), topic("x")).Run(context.Background(), nil)
			if res.Status != RunFailed || res.Error != name || res.FailedNode != "draft" {
				t.Fatalf("Run() = %+v, want %s", res, name)
			}
		})
	}
}

func TestRunCancel(t *testing.T) {
	started := make(chan string, 1)
	block := blockUntilCanceled(started)
	d := dispatcherFunc(func(ctx context.Context, call ChatCall, _ func(string)) (ChatReply, error) { return block(ctx, call) })
	ctx, cancel := context.WithCancel(context.Background())
	done, rec := runAsync(ctx, mustPlan(t, Config{Dispatcher: d}, branching(), topic("x")))
	await(t, started)
	cancel()
	if res := await(t, done); res.Status != RunCanceled || res.Error != "" {
		t.Fatalf("Run() = %+v, want canceled", res)
	}
	if s := rec.step("draft"); s.Status != NodeCanceled {
		t.Fatalf("draft step = %+v, want canceled", s)
	}
}

func TestRunNodeTimeout(t *testing.T) {
	clock := newFakeClock()
	started := make(chan string, 1)
	block := blockUntilCanceled(started)
	d := dispatcherFunc(func(ctx context.Context, call ChatCall, _ func(string)) (ChatReply, error) { return block(ctx, call) })
	done, _ := runAsync(context.Background(), mustPlan(t, Config{Dispatcher: d, Clock: clock, NodeTimeout: time.Minute}, branching(), topic("x")))
	await(t, started)
	clock.Advance(time.Minute)
	if res := await(t, done); res.Status != RunFailed || res.Error != CodeNodeTimeout || res.FailedNode != "draft" {
		t.Fatalf("Run() = %+v, want %s at draft", res, CodeNodeTimeout)
	}
}

func TestRunDeadline(t *testing.T) {
	clock := newFakeClock()
	started := make(chan string, 1)
	block := blockUntilCanceled(started)
	d := dispatcherFunc(func(ctx context.Context, call ChatCall, _ func(string)) (ChatReply, error) { return block(ctx, call) })
	cfg := Config{Dispatcher: d, Clock: clock, RunTimeout: 10 * time.Minute, NodeTimeout: time.Hour}
	done, rec := runAsync(context.Background(), mustPlan(t, cfg, branching(), topic("x")))
	await(t, started)
	clock.Advance(10 * time.Minute)
	res := await(t, done)
	if res.Status != RunFailed || res.Error != CodeRunTimeout || res.FailedNode != "" {
		t.Fatalf("Run() = %+v, want %s", res, CodeRunTimeout)
	}
	if s := rec.step("draft"); s.Status != NodeCanceled {
		t.Fatalf("draft step = %+v, want canceled", s)
	}
	if res.FinishedAt.Sub(res.StartedAt) != 10*time.Minute {
		t.Fatalf("run lasted %v on the clock, want 10m", res.FinishedAt.Sub(res.StartedAt))
	}
}

func TestRunConditionNotANumber(t *testing.T) {
	def := branching()
	def.Nodes[2] = node("check", flowdef.NodeCondition, `{"left":"{{draft.text}}","op":"gt","right":"3"}`)
	res := mustPlan(t, Config{Dispatcher: replies(map[string]string{"m": "many"})}, def, topic("x")).Run(context.Background(), nil)
	if res.Status != RunFailed || res.Error != CodeNotANumber || res.FailedNode != "check" {
		t.Fatalf("Run() = %+v, want %s at check", res, CodeNotANumber)
	}
}

func TestRunBoundsValues(t *testing.T) {
	t.Run("long reply is cut", func(t *testing.T) {
		rec := &recorder{}
		p := mustPlan(t, Config{Dispatcher: replies(map[string]string{"m": "done" + strings.Repeat("长", 10)}), MaxValueBytes: 8}, branching(), topic("x"))
		if res := p.Run(context.Background(), rec.emit); res.Status != RunSucceeded {
			t.Fatalf("Run() = %+v", res)
		}
		if got := rec.step("draft").Outputs[flowdef.FieldText]; got != "done长" {
			t.Fatalf("draft text = %q, want it cut to 8 bytes on a rune boundary", got)
		}
	})
	t.Run("rendering past the bound fails the node", func(t *testing.T) {
		def := branching()
		def.Nodes[1] = llm("draft", "m", "{{inputs.topic}}{{inputs.topic}}")
		p := mustPlan(t, Config{Dispatcher: replies(nil), MaxValueBytes: 8}, def, topic("12345"))
		if res := p.Run(context.Background(), nil); res.Status != RunFailed || res.Error != CodeValueTooLarge || res.FailedNode != "draft" {
			t.Fatalf("Run() = %+v, want %s at draft", res, CodeValueTooLarge)
		}
	})
}

func TestPlanIsReusable(t *testing.T) {
	p := mustPlan(t, Config{Dispatcher: replies(map[string]string{"m": "done"})}, branching(), topic("x"))
	for i := range 2 {
		if res := p.Run(context.Background(), nil); res.Status != RunSucceeded || res.Outputs["answer"] != "|done" {
			t.Fatalf("run %d = %+v", i, res)
		}
	}
}

func TestPlanRejects(t *testing.T) {
	e, err := New(Config{Dispatcher: replies(nil)})
	if err != nil {
		t.Fatal(err)
	}
	bad := branching()
	bad.Edges = append(bad.Edges, edge("fix", "draft"))
	for name, call := range map[string]func() error{
		"invalid definition": func() error { _, err := e.Plan(bad, topic("x")); return err },
		"missing input":      func() error { _, err := e.Plan(branching(), nil); return err },
	} {
		t.Run(name, func(t *testing.T) {
			var verr *flowdef.ValidationError
			if err := call(); !errors.As(err, &verr) {
				t.Fatalf("Plan() = %v, want a *flowdef.ValidationError", err)
			}
		})
	}
}

func TestNewChecksConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New() accepted a Config without a Dispatcher")
	}
	if _, err := New(Config{Dispatcher: replies(nil), NodeTimeout: -1}); err == nil {
		t.Fatal("New() accepted a negative timeout")
	}
}

func TestCompare(t *testing.T) {
	for _, tt := range []struct {
		op          flowdef.Operator
		left, right string
		result, ok  bool
	}{
		{flowdef.OpEquals, " yes\n", "yes", true, true},
		{flowdef.OpEquals, "Yes", "yes", false, true},
		{flowdef.OpNotEquals, "a", "b", true, true},
		{flowdef.OpContains, "has TODO", "TODO", true, true},
		{flowdef.OpNotContains, "done", "TODO", true, true},
		{flowdef.OpEmpty, " \n", "", true, true},
		{flowdef.OpNotEmpty, "x", "", true, true},
		{flowdef.OpGT, "10", "9.5", true, true},
		{flowdef.OpGTE, "7", "7", true, true},
		{flowdef.OpLT, "-1", "0", true, true},
		{flowdef.OpLTE, "2", "1", false, true},
		{flowdef.OpGT, "10", "nine", false, false},
		{"bogus", "1", "1", false, false},
	} {
		if result, ok := compare(tt.op, tt.left, tt.right); result != tt.result || ok != tt.ok {
			t.Errorf("compare(%s, %q, %q) = %v, %v; want %v, %v", tt.op, tt.left, tt.right, result, ok, tt.result, tt.ok)
		}
	}
}
