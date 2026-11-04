package claudecode

import (
	"encoding/json"
	"errors"
	"strings"
)

// messages_stop.go emulates the Messages API's stop_sequences. The CLI has no
// stop sequence option, so the bridge watches the text it streams, releases
// nothing from the first matching sequence on, and ends the round with
// stop_reason "stop_sequence" as the API would. Claude Code's auto mode
// classifier depends on this: it stops at "</severity>".
//
// messages_stop.go 模拟 Messages API 的 stop_sequences。CLI 没有停止序列选项，因此桥接
// 监视 CLI 流出的文本，从第一个命中的序列起不再放出任何内容，并像 API 那样以
// stop_reason "stop_sequence" 结束回合。Claude Code auto 模式的安全判断依赖这一点：它在
// "</severity>" 处停止。

// errStopSequence ends bridgeEvents once a stop sequence has closed the
// round; runProcess then stops the CLI and delivers the final event. It never
// reaches a caller.
//
// errStopSequence 在停止序列结束回合后终止 bridgeEvents；runProcess 随后停止 CLI 并投递
// 最终事件。它从不传给调用方。
var errStopSequence = errors.New("stop sequence reached")

// stopMatcher scans one text block's deltas for stop sequences. It withholds
// the longest tail that could still begin a sequence, so a sequence split
// across deltas is caught and no part of it is ever released.
//
// stopMatcher 在一个文本块的增量中查找停止序列。它暂扣仍可能是某个序列开头的最长尾部，
// 因此跨增量拆开的序列也能被发现，且序列的任何部分都不会被放出。
type stopMatcher struct {
	sequences []string
	held      string
}

// feed adds text and returns what can be released. Once a sequence appears
// it returns the text before the earliest match and that sequence; the
// matcher must not be fed again for the block.
//
// feed 追加 text 并返回可以放出的文本。出现序列时，返回最早命中位置之前的文本以及该
// 序列；此后本块不得再调用 feed。
func (m *stopMatcher) feed(text string) (emit, matched string) {
	buf := m.held + text
	at := -1
	for _, sequence := range m.sequences {
		if i := strings.Index(buf, sequence); i >= 0 && (at < 0 || i < at) {
			at, matched = i, sequence
		}
	}
	if at >= 0 {
		m.held = ""
		return buf[:at], matched
	}
	keep := 0
	for _, sequence := range m.sequences {
		for n := min(len(sequence)-1, len(buf)); n > keep; n-- {
			if strings.HasSuffix(buf, sequence[:n]) {
				keep = n
				break
			}
		}
	}
	m.held = buf[len(buf)-keep:]
	return buf[:len(buf)-keep], ""
}

// flush returns the withheld tail when the block ends without a match.
// flush 在文本块未命中而结束时返回暂扣的尾部。
func (m *stopMatcher) flush() string {
	rest := m.held
	m.held = ""
	return rest
}

// textDeltaEvent renders a text_delta for the public block index.
// textDeltaEvent 为对外块索引渲染一个 text_delta。
func textDeltaEvent(index int, text string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": index, "delta": map[string]string{"type": "text_delta", "text": text}})
	return data
}

// blockStopEvent renders a content_block_stop for the public block index.
// blockStopEvent 为对外块索引渲染一个 content_block_stop。
func blockStopEvent(index int) json.RawMessage {
	data, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": index})
	return data
}

// stopSequenceDelta renders the message_delta that ends a round at sequence,
// carrying the CLI's usage when it has reported any.
// stopSequenceDelta 渲染在 sequence 处结束回合的 message_delta；CLI 已报告用量时一并携带。
func stopSequenceDelta(sequence string, usage json.RawMessage) json.RawMessage {
	event := map[string]any{"type": "message_delta", "delta": map[string]string{"stop_reason": "stop_sequence", "stop_sequence": sequence}}
	if len(usage) > 0 && string(usage) != "null" {
		event["usage"] = usage
	}
	data, _ := json.Marshal(event)
	return data
}
