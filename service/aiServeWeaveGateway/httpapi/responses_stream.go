package httpapi

import (
	"strings"

	"AIServeWeave/common/runtime"
)

// responseItems turns the flat run of deltas a backend streams into the nested
// item lifecycle a Responses client's state machine is built on. A reply can
// hold a text message and any number of tool calls, each its own output item
// with its own output_index, so what a delta belongs to has to be tracked
// rather than assumed.
//
// The rules that keep the sequence well-formed:
//
//   - An item is announced (output_item.added) before any delta that names it.
//   - A message item is closed as soon as a tool call opens, so a client never
//     sees a tool call announced inside an unfinished message. Text that arrives
//     after that opens a fresh message item.
//   - A tool call is announced once its name is known, because the item carries
//     it; arguments that arrive earlier are held and sent right after.
//   - Tool calls are closed together at the end of the stream. Backends
//     interleave the fragments of parallel calls, so one call is never known to
//     be finished before the stream is.
//
// responseItems 把后端流式给出的一串扁平 delta，转成 Responses 客户端状态机所依赖的那种
// 嵌套项生命周期。一个回复里可以既有文本消息、又有任意多个工具调用，每一个都是各自带有
// output_index 的输出项，因此一个 delta 属于谁必须被追踪，而不是被假定。
//
// 保证序列良构的规则：
//
//   - 项在任何点名它的 delta 之前先宣告（output_item.added）。
//   - 一旦有工具调用打开，message 项就立即关闭，客户端因而永远不会看到工具调用被宣告在
//     一条未结束的消息里面。此后再到达的文本会开一个新的 message 项。
//   - 工具调用在名字已知时才宣告，因为项里带着它；更早到达的参数先扣住，宣告之后立即发出。
//   - 工具调用在流结束时一并关闭。后端会交错发出并行调用的片段，因此在流结束之前，没有
//     哪个调用能被确知已经结束。
type responseItems struct {
	em      *responsesEmitter
	toolset responsesToolset

	// output is indexed by output_index. A slot holds a zero value from the
	// moment its item is announced until the item is closed.
	//
	// output 以 output_index 为下标。从项被宣告到被关闭之前，槽位里是零值。
	output []responseOutputRaw

	msgOpen  bool
	msgID    string
	msgIndex int
	msgText  strings.Builder
	allText  strings.Builder

	calls map[int]*streamedCall
	order []*streamedCall
}

// streamedCall is one tool call being assembled from deltas.
//
// streamedCall 是一个正在由 delta 拼装的工具调用。
type streamedCall struct {
	itemID      string
	outputIndex int
	callID      string
	name        string
	args        strings.Builder
	opened      bool
}

func newResponseItems(em *responsesEmitter, toolset responsesToolset) *responseItems {
	return &responseItems{em: em, toolset: toolset, calls: map[int]*streamedCall{}}
}

// text appends a text delta, opening a message item if none is open.
//
// text 追加一段文本 delta；没有打开的 message 项时先打开一个。
func (r *responseItems) text(delta string) {
	if delta == "" {
		return
	}
	if !r.msgOpen {
		r.msgOpen = true
		r.msgID = newItemID("msg")
		r.msgIndex = len(r.output)
		r.output = append(r.output, responseOutputRaw{})
		// The announced message carries an empty content array. It is spelled
		// out as a map because responseOutputRaw omits an empty Content, and a
		// client that cannot parse the announced item has no active item to
		// attach the deltas to (Codex logs "OutputTextDelta without active
		// item").
		//
		// 被宣告的消息带一个空的 content 数组。这里用 map 显式写出，因为
		// responseOutputRaw 会省略空的 Content，而无法解析所宣告项的客户端就没有可以挂靠
		// delta 的活动项（Codex 会记录 "OutputTextDelta without active item"）。
		r.em.event("response.output_item.added", map[string]any{
			"output_index": r.msgIndex,
			"item": map[string]any{
				"type": "message", "id": r.msgID, "status": "in_progress", "role": "assistant",
				"content": []any{},
			},
		})
		r.em.event("response.content_part.added", map[string]any{
			"item_id": r.msgID, "output_index": r.msgIndex, "content_index": 0,
			"part": responseContentPart{Type: "output_text", Text: "", Annotations: []any{}},
		})
	}
	r.msgText.WriteString(delta)
	r.allText.WriteString(delta)
	r.em.event("response.output_text.delta", map[string]any{
		"item_id": r.msgID, "output_index": r.msgIndex, "content_index": 0, "delta": delta,
	})
}

// closeMessage finishes the open message item, if any.
//
// closeMessage 结束当前打开的 message 项（如果有）。
func (r *responseItems) closeMessage() {
	if !r.msgOpen {
		return
	}
	r.msgOpen = false
	text := r.msgText.String()
	r.msgText.Reset()
	final := responseContentPart{Type: "output_text", Text: text, Annotations: []any{}}
	r.em.event("response.output_text.done", map[string]any{
		"item_id": r.msgID, "output_index": r.msgIndex, "content_index": 0, "text": text,
	})
	r.em.event("response.content_part.done", map[string]any{
		"item_id": r.msgID, "output_index": r.msgIndex, "content_index": 0, "part": final,
	})
	item := responseOutputRaw{
		Type: "message", ID: r.msgID, Status: "completed", Role: "assistant",
		Content: []responseContentPart{final},
	}
	r.em.event("response.output_item.done", map[string]any{"output_index": r.msgIndex, "item": item})
	r.output[r.msgIndex] = item
}

// toolCall applies one tool-call delta.
//
// toolCall 应用一个工具调用 delta。
func (r *responseItems) toolCall(d runtime.ToolCallDelta) {
	c := r.calls[d.Index]
	// A delta at a known index that carries a different call id starts a new
	// call: some backends number every call 0.
	//
	// 落在已知下标、却带着另一个 call id 的 delta 开启的是一个新调用：有些后端把每个
	// 调用都编号为 0。
	if c == nil || (d.ID != "" && c.callID != "" && d.ID != c.callID) {
		c = &streamedCall{}
		r.calls[d.Index] = c
		r.order = append(r.order, c)
	}
	if c.callID == "" {
		c.callID = d.ID
	}
	if c.name == "" {
		c.name = d.Function.Name
	}
	c.args.WriteString(d.Function.Arguments)

	if c.opened {
		if d.Function.Arguments != "" && r.toolset.wantsArgumentDeltas(c.name) {
			r.argumentsDelta(c, d.Function.Arguments)
		}
		return
	}
	if c.name != "" {
		r.open(c)
	}
}

// open announces a tool call's item.
//
// open 宣告一个工具调用的项。
func (r *responseItems) open(c *streamedCall) {
	r.closeMessage()
	if c.callID == "" {
		// Some backends never name a call. The client needs an id to pair the
		// result it sends back with this call, so one is made up.
		//
		// 有些后端从不给调用命名。客户端需要一个 id 来把它送回的结果与这次调用配对，
		// 因此这里编一个。
		c.callID = newItemID("call")
	}
	c.opened = true
	c.itemID = newItemID(r.toolset.itemPrefix(c.name))
	c.outputIndex = len(r.output)
	r.output = append(r.output, responseOutputRaw{})
	r.em.event("response.output_item.added", map[string]any{
		"output_index": c.outputIndex,
		"item":         r.toolset.callItem(c.itemID, c.callID, c.name, "", "in_progress"),
	})
	if c.args.Len() > 0 && r.toolset.wantsArgumentDeltas(c.name) {
		r.argumentsDelta(c, c.args.String())
	}
}

func (r *responseItems) argumentsDelta(c *streamedCall, delta string) {
	r.em.event("response.function_call_arguments.delta", map[string]any{
		"item_id": c.itemID, "output_index": c.outputIndex, "delta": delta,
	})
}

// finish closes every open item and returns the completed output array.
//
// finish 关闭所有打开的项，并返回完整的输出数组。
func (r *responseItems) finish() []responseOutputRaw {
	// A call whose name never arrived is still announced: the client should
	// see the malformed call and report it to the model, not have it vanish.
	//
	// 名字始终没有到达的调用依旧要宣告：客户端应当看到这个畸形调用并把它反馈给模型，
	// 而不是让它凭空消失。
	for _, c := range r.order {
		if !c.opened {
			r.open(c)
		}
	}
	r.closeMessage()
	for _, c := range r.order {
		item := r.toolset.callItem(c.itemID, c.callID, c.name, c.args.String(), "completed")
		if r.toolset.wantsArgumentDeltas(c.name) {
			r.em.event("response.function_call_arguments.done", map[string]any{
				"item_id": c.itemID, "output_index": c.outputIndex,
				"name": c.name, "arguments": *item.Arguments,
			})
		}
		r.em.event("response.output_item.done", map[string]any{"output_index": c.outputIndex, "item": item})
		r.output[c.outputIndex] = item
	}
	if len(r.output) == 0 {
		return []responseOutputRaw{}
	}
	return r.output
}

// assistantMessage is the streamed reply as one canonical assistant message,
// and whether the stream produced anything at all.
//
// assistantMessage 是以单条 canonical assistant 消息表示的流式回复，以及这条流是否产出了
// 任何东西。
func (r *responseItems) assistantMessage() (runtime.ChatMessage, bool) {
	msg := runtime.ChatMessage{Role: "assistant", Content: r.allText.String()}
	for _, c := range r.order {
		args := c.args.String()
		if args == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, runtime.ToolCall{
			ID: c.callID, Type: "function",
			Function: runtime.FunctionCall{Name: c.name, Arguments: args},
		})
	}
	return msg, msg.Content != "" || len(msg.ToolCalls) > 0
}
