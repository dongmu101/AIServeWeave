package flow

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/common/flowdef"
	"AIServeWeave/common/runtime"
)

// Cancellation causes the engine sets, so it can tell its own reasons for
// stopping apart from the caller's.
var (
	errRunTimeout  = errors.New("flow: run deadline reached")
	errNodeTimeout = errors.New("flow: node timeout reached")
	errNodeFailed  = errors.New("flow: a node failed")
)

// run is one execution of a Plan. Every field below msgs is owned by the
// coordinator goroutine, the one inside execute: model calls run on their own
// goroutines but only ever talk to it through msgs, so node values need no
// lock.
//
// run 是 Plan 的一次执行。msgs 之后的每个字段都归协调者 goroutine（execute 所在的
// goroutine）所有：模型调用在各自的 goroutine 上进行，但只通过 msgs 与它通信，因此节点
// 取值不需要加锁。
type run struct {
	p      *Plan
	clock  runtime.Clock
	emit   func(Event)
	ctx    context.Context
	cancel context.CancelCauseFunc
	msgs   chan message
	done   chan struct{} // closed when execute returns

	remaining []int               // unsettled incoming edges, by node index
	status    []NodeStatus        // "" until the node finishes or is skipped
	values    []map[string]string // outputs of nodes that succeeded
	ready     []*planNode         // decided to run, not started yet
	running   int                 // calls in flight
	failure   *Step               // the first node failure
	outputs   map[string]string   // set when the output node ran
	usage     Usage
}

// message is what a call's goroutine sends the coordinator: a piece of reply
// text, or, once, the final outcome.
type message struct {
	node  *planNode
	delta string
	final bool
	step  Step
	reply ChatReply
	media MediaReply
	err   error
	cause error // context.Cause of the call's context when the call returned
}

func (r *run) execute(parent context.Context) Result {
	started := r.clock.Now()
	r.ctx, r.cancel = context.WithCancelCause(parent)
	defer r.cancel(nil)
	defer close(r.done)
	after(r.clock, r.ctx, r.p.runTimeout, func() { r.cancel(errRunTimeout) })

	r.send(Event{Type: EventRunStarted})
	for i, n := range r.p.nodes {
		r.remaining[i] = len(n.in)
	}
	r.ready = append(r.ready, r.p.start)
	for {
		for len(r.ready) > 0 && r.running < r.p.cfg.MaxParallel && r.ctx.Err() == nil {
			n := r.ready[0]
			r.ready = r.ready[1:]
			r.start(n)
		}
		if r.running == 0 {
			break
		}
		m := <-r.msgs
		if !m.final {
			r.send(Event{Type: EventNodeDelta, Node: m.node.ID, Delta: m.delta})
			continue
		}
		r.running--
		r.finishCall(m)
	}

	res := r.result(started)
	r.send(Event{Type: EventRunFinished, Result: &res})
	return res
}

func (r *run) send(e Event) {
	e.Time = r.clock.Now()
	r.emit(e)
}

// post hands a message to the coordinator. It gives up once the run is over,
// so a Dispatcher that breaks its contract cannot strand the goroutine.
func (r *run) post(m message) {
	select {
	case r.msgs <- m:
	case <-r.done:
	}
}

// after calls fire once d elapses on clock, unless ctx ends first.
func after(clock runtime.Clock, ctx context.Context, d time.Duration, fire func()) {
	ch, stop := clock.NewTimer(d)
	go func() {
		select {
		case <-ch:
			fire()
		case <-ctx.Done():
			stop()
		}
	}()
}

// start runs a node that was decided to run. Every node but those that make a
// call — llm, image_generation, comfyui_template and video_generation —
// finishes on the spot.
func (r *run) start(n *planNode) {
	r.send(Event{Type: EventNodeStarted, Node: n.ID})
	step := Step{Node: n.ID, Type: n.Type, StartedAt: r.clock.Now()}
	switch n.Type {
	case flowdef.NodeInput:
		r.finish(n, step, NodeSucceeded, "")
	case flowdef.NodeOutput:
		outs := make(map[string]string, len(r.p.outputs))
		for _, o := range r.p.outputs {
			v, ok := r.render(o.tmpl)
			if !ok {
				r.finish(n, step, NodeFailed, CodeValueTooLarge)
				return
			}
			outs[o.name] = v
		}
		step.Outputs = outs
		r.outputs = outs
		r.finish(n, step, NodeSucceeded, "")
	case flowdef.NodeTemplate:
		text, ok := r.render(n.tmpl["template"])
		if !ok {
			r.finish(n, step, NodeFailed, CodeValueTooLarge)
			return
		}
		step.Outputs = map[string]string{flowdef.FieldText: text}
		r.finish(n, step, NodeSucceeded, "")
	case flowdef.NodeCondition:
		left, okL := r.render(n.tmpl["left"])
		right, okR := r.render(n.tmpl["right"])
		if !okL || !okR {
			r.finish(n, step, NodeFailed, CodeValueTooLarge)
			return
		}
		step.Inputs = map[string]string{"left": left}
		if n.cond.Op != flowdef.OpEmpty && n.cond.Op != flowdef.OpNotEmpty {
			step.Inputs["right"] = right
		}
		result, ok := compare(n.cond.Op, left, right)
		if !ok {
			r.finish(n, step, NodeFailed, CodeNotANumber)
			return
		}
		step.Outputs = map[string]string{flowdef.FieldResult: string(branchOf(result))}
		r.finish(n, step, NodeSucceeded, "")
	case flowdef.NodeLLM:
		r.startLLM(n, step)
	case flowdef.NodeImage:
		r.startImage(n, step)
	case flowdef.NodeComfyUI:
		r.startTemplate(n, step)
	case flowdef.NodeVideo:
		r.startVideo(n, step)
	}
}

func (r *run) startLLM(n *planNode, step Step) {
	system, okS := r.render(n.tmpl["system"])
	prompt, okP := r.render(n.tmpl["prompt"])
	if !okS || !okP {
		r.finish(n, step, NodeFailed, CodeValueTooLarge)
		return
	}
	if len(n.llm.JSONFields) > 0 {
		system = withJSONInstruction(system, n.llm.JSONFields)
	}
	call := ChatCall{Model: n.llm.Model, System: system, Prompt: prompt, Temperature: n.llm.Temperature, MaxTokens: n.llm.MaxTokens}
	step.Model = call.Model
	step.Inputs = map[string]string{"system": system, "prompt": prompt}
	r.call(n, step, func(ctx context.Context, m *message) error {
		var err error
		m.reply, err = r.p.cfg.Dispatcher.Chat(ctx, call, func(text string) {
			if text != "" {
				r.post(message{node: n, delta: text})
			}
		})
		return err
	})
}

func (r *run) startImage(n *planNode, step Step) {
	prompt, ok := r.render(n.tmpl["prompt"])
	if !ok {
		r.finish(n, step, NodeFailed, CodeValueTooLarge)
		return
	}
	call := ImageCall{Model: n.image.Model, Prompt: prompt, Size: n.image.Size}
	step.Model = call.Model
	step.Inputs = map[string]string{"prompt": prompt}
	if call.Size != "" {
		step.Inputs["size"] = call.Size
	}
	r.call(n, step, func(ctx context.Context, m *message) error {
		var err error
		m.media, err = r.p.cfg.Dispatcher.GenerateImage(ctx, call)
		return err
	})
}

func (r *run) startTemplate(n *planNode, step Step) {
	call := TemplateCall{Template: n.comfy.Template, Inputs: make(map[string]string, len(n.comfy.Inputs))}
	total := 0
	for name := range n.comfy.Inputs {
		v, ok := r.render(n.tmpl["inputs."+name])
		if total += len(v); !ok || total > r.p.cfg.MaxValueBytes {
			r.finish(n, step, NodeFailed, CodeValueTooLarge)
			return
		}
		if !n.files[name] {
			call.Inputs[name] = v
			continue
		}
		if v = strings.TrimSpace(v); v != "" {
			if call.Files == nil {
				call.Files = map[string]string{}
			}
			call.Files[name] = v
		}
	}
	step.Model = call.Template
	step.Inputs = make(map[string]string, len(call.Inputs)+len(call.Files))
	for name, v := range call.Inputs {
		step.Inputs[name] = v
	}
	for name, v := range call.Files {
		step.Inputs[name] = v
	}
	r.call(n, step, func(ctx context.Context, m *message) error {
		var err error
		m.media, err = r.p.cfg.Dispatcher.RunTemplate(ctx, call)
		return err
	})
}

func (r *run) startVideo(n *planNode, step Step) {
	prompt, ok := r.render(n.tmpl["prompt"])
	if !ok {
		r.finish(n, step, NodeFailed, CodeValueTooLarge)
		return
	}
	// The image slots are single references to images of the run (flowdef
	// checked that), so what they render to is an artifact id or "".
	// 图片槽位是对本次运行中图片的单个引用（flowdef 已检查），因此渲染结果是产物 id 或空。
	image := func(key string) (string, bool) {
		v, ok := r.render(n.tmpl[key])
		return strings.TrimSpace(v), ok
	}
	call := VideoCall{
		Model:         n.video.Model,
		Prompt:        prompt,
		Resolution:    n.video.Resolution,
		Ratio:         n.video.Ratio,
		Duration:      n.video.Duration,
		GenerateAudio: n.video.GenerateAudio,
		Watermark:     n.video.Watermark,
	}
	step.Model = call.Model
	step.Inputs = map[string]string{"prompt": prompt}
	var okF, okL bool
	if call.FirstFrame, okF = image("first_frame"); call.FirstFrame != "" {
		step.Inputs["first_frame"] = call.FirstFrame
	}
	if call.LastFrame, okL = image("last_frame"); call.LastFrame != "" {
		step.Inputs["last_frame"] = call.LastFrame
	}
	if !okF || !okL {
		r.finish(n, step, NodeFailed, CodeValueTooLarge)
		return
	}
	for i := range n.video.ReferenceImages {
		key := videoReferenceField(i)
		v, ok := image(key)
		if !ok {
			r.finish(n, step, NodeFailed, CodeValueTooLarge)
			return
		}
		if v != "" {
			call.ReferenceImages = append(call.ReferenceImages, v)
			step.Inputs[key] = v
		}
	}
	for k, v := range map[string]string{"resolution": call.Resolution, "ratio": call.Ratio} {
		if v != "" {
			step.Inputs[k] = v
		}
	}
	if call.Duration != 0 {
		step.Inputs["duration"] = strconv.Itoa(call.Duration)
	}
	r.call(n, step, func(ctx context.Context, m *message) error {
		var err error
		m.media, err = r.p.cfg.Dispatcher.GenerateVideo(ctx, call)
		return err
	})
}

// call runs do on its own goroutine under the node timeout, as one of the
// run's calls in flight, and hands its outcome to the coordinator. do fills
// in the reply on the message it is given.
func (r *run) call(n *planNode, step Step, do func(ctx context.Context, m *message) error) {
	ctx, cancel := context.WithCancelCause(r.ctx)
	timeout := r.p.cfg.NodeTimeout
	if n.Type == flowdef.NodeVideo {
		timeout = r.p.cfg.VideoNodeTimeout
	}
	after(r.clock, ctx, timeout, func() { cancel(errNodeTimeout) })
	r.running++
	go func() {
		defer cancel(nil)
		m := message{node: n, final: true, step: step}
		m.err = do(ctx, &m)
		m.cause = context.Cause(ctx)
		r.post(m)
	}()
}

// finishCall records a call's outcome. A call that failed because the run was
// stopping is cancelled, not failed, so the run's own reason wins.
func (r *run) finishCall(m message) {
	n, step := m.node, m.step
	step.Usage = m.reply.Usage
	step.Usage.add(m.media.Usage)
	r.usage.add(step.Usage)
	switch {
	case m.err == nil:
		outputs, code := r.callOutputs(n, m)
		step.Outputs = outputs
		if code != "" {
			r.finish(n, step, NodeFailed, code)
			return
		}
		r.finish(n, step, NodeSucceeded, "")
	case r.ctx.Err() != nil:
		r.finish(n, step, NodeCanceled, "")
	case errors.Is(m.cause, errNodeTimeout):
		r.finish(n, step, NodeFailed, CodeNodeTimeout)
	default:
		code := CodeUpstream
		var fe *Error
		if errors.As(m.err, &fe) && fe.Code != "" {
			code = fe.Code
		}
		r.finish(n, step, NodeFailed, code)
	}
}

// callOutputs turns a successful call's reply into the node's outputs, or
// names why the reply does not do.
func (r *run) callOutputs(n *planNode, m message) (map[string]string, string) {
	switch n.Type {
	case flowdef.NodeLLM:
		text := truncate(m.reply.Text, r.p.cfg.MaxValueBytes)
		outputs := map[string]string{flowdef.FieldText: text}
		if fields := n.llm.JSONFields; len(fields) > 0 {
			values, ok := parseJSONFields(text, fields, r.p.cfg.MaxValueBytes)
			if !ok {
				return outputs, CodeInvalidJSONOutput
			}
			for k, v := range values {
				outputs[k] = v
			}
		}
		return outputs, ""
	case flowdef.NodeImage:
		image := firstImage(m.media.Artifacts)
		if image == "" {
			return nil, CodeNoImage
		}
		return map[string]string{flowdef.FieldImage: image}, ""
	case flowdef.NodeVideo:
		var video, lastFrame string
		for _, a := range m.media.Artifacts {
			switch {
			case a.ID == "":
			case a.Kind == ArtifactVideo && video == "":
				video = a.ID
			case a.Kind == ArtifactImage && lastFrame == "":
				lastFrame = a.ID
			}
		}
		if video == "" {
			return nil, CodeNoVideo
		}
		return map[string]string{flowdef.FieldVideo: video, flowdef.FieldLastFrame: lastFrame}, ""
	default: // flowdef.NodeComfyUI
		list := m.media.Artifacts
		if list == nil {
			list = []Artifact{}
		}
		encoded, err := json.Marshal(list)
		if err != nil || len(encoded) > r.p.cfg.MaxValueBytes {
			return nil, CodeValueTooLarge
		}
		return map[string]string{flowdef.FieldImage: firstImage(list), flowdef.FieldArtifacts: string(encoded)}, ""
	}
}

func firstImage(list []Artifact) string {
	for _, a := range list {
		if a.Kind == ArtifactImage && a.ID != "" {
			return a.ID
		}
	}
	return ""
}

// finish records a node's end and, if it succeeded, settles its outgoing
// edges. The first failure stops the run.
func (r *run) finish(n *planNode, step Step, status NodeStatus, code string) {
	step.Status, step.Error, step.FinishedAt = status, code, r.clock.Now()
	r.status[n.index] = status
	if status == NodeSucceeded {
		r.values[n.index] = step.Outputs
	}
	r.send(Event{Type: EventNodeFinished, Node: n.ID, Step: &step})
	switch status {
	case NodeSucceeded:
		r.settle(n)
	case NodeFailed:
		if r.failure == nil {
			r.failure = &step
			r.cancel(errNodeFailed)
		}
	}
}

// settle marks a finished or skipped node's outgoing edges settled and decides
// every child whose incoming edges are now all settled.
func (r *run) settle(n *planNode) {
	for _, e := range n.out {
		child := r.p.byID[e.To]
		if r.remaining[child.index]--; r.remaining[child.index] == 0 {
			r.decide(child)
		}
	}
}

func (r *run) decide(n *planNode) {
	for _, e := range n.in {
		if r.active(e) {
			r.ready = append(r.ready, n)
			return
		}
	}
	now := r.clock.Now()
	r.status[n.index] = NodeSkipped
	r.send(Event{Type: EventNodeSkipped, Node: n.ID, Step: &Step{
		Node: n.ID, Type: n.Type, Status: NodeSkipped, StartedAt: now, FinishedAt: now,
	}})
	r.settle(n)
}

// active reports whether a settled edge carries the flow on: its source
// succeeded and, leaving a condition, the branch is the one taken.
func (r *run) active(e flowdef.Edge) bool {
	from := r.p.byID[e.From]
	if r.status[from.index] != NodeSucceeded {
		return false
	}
	return e.Branch == "" || r.values[from.index][flowdef.FieldResult] == string(e.Branch)
}

// render substitutes a template. It reports false when the substituted values
// together exceed MaxValueBytes, so a template that repeats a large value
// cannot blow up memory.
func (r *run) render(t flowdef.Template) (string, bool) {
	total, over := 0, false
	s := t.Render(func(ref flowdef.Ref) string {
		v := r.value(ref)
		if total += len(v); total > r.p.cfg.MaxValueBytes {
			over = true
			return ""
		}
		return v
	})
	return s, !over
}

// value resolves one reference. A node that was skipped, or has not run, has
// no values and resolves to "".
func (r *run) value(ref flowdef.Ref) string {
	if ref.Scope == flowdef.InputsScope {
		return r.p.inputs[ref.Field]
	}
	return r.values[r.p.byID[ref.Scope].index][ref.Field]
}

func (r *run) result(started time.Time) Result {
	res := Result{Outputs: r.outputs, Usage: r.usage, StartedAt: started, FinishedAt: r.clock.Now()}
	complete := true
	for _, s := range r.status {
		if s != NodeSucceeded && s != NodeSkipped {
			complete = false
			break
		}
	}
	cause := context.Cause(r.ctx)
	switch {
	case r.failure != nil:
		res.Status, res.Error, res.FailedNode = RunFailed, r.failure.Error, r.failure.Node
	case complete:
		res.Status = RunSucceeded
	case errors.Is(cause, errRunTimeout) || errors.Is(cause, context.DeadlineExceeded):
		res.Status, res.Error = RunFailed, CodeRunTimeout
	default:
		res.Status = RunCanceled
	}
	return res
}
