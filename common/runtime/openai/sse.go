package openai

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrSSEDone signals that the stream sent the standard "[DONE]" sentinel as
// an event's data, which OpenAI-compatible backends use to mark a normal
// end of stream. It must not be treated as a JSON decode error.
var ErrSSEDone = errors.New("openai: sse [DONE]")

// sseFrame is one parsed Server-Sent Event, restricted to the fields
// OpenAI-compatible backends use.
type sseFrame struct {
	Event string
	Data  string
	ID    string
	Retry string
}

// sseReader parses an SSE byte stream: field lines ("field: value"),
// comment lines starting with ':', and blank lines that terminate an event.
// It does not know about chat/embedding semantics.
type sseReader struct {
	br            *bufio.Reader
	maxLineBytes  int
	maxEventBytes int
}

// newSSEReader wraps r. maxLineBytes bounds a single field line and
// maxEventBytes bounds the accumulated "data" payload of one event; either
// limit set to 0 disables that check.
func newSSEReader(r io.Reader, maxLineBytes, maxEventBytes int) *sseReader {
	return &sseReader{br: bufio.NewReaderSize(r, 4096), maxLineBytes: maxLineBytes, maxEventBytes: maxEventBytes}
}

// readFrame reads one event: a run of field lines up to a blank line, or up
// to EOF if the connection closes without a trailing blank line. It returns
// io.EOF once the stream is exhausted with no further event, and
// ErrSSEDone when the event's data is exactly "[DONE]".
func (s *sseReader) readFrame() (sseFrame, error) {
	var frame sseFrame
	var dataLines []string
	dataLen := 0
	sawAnyField := false

	for {
		line, err := s.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if sawAnyField {
					return s.finish(frame, dataLines)
				}
				return sseFrame{}, io.EOF
			}
			return sseFrame{}, err
		}

		if line == "" {
			if !sawAnyField {
				continue
			}
			return s.finish(frame, dataLines)
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		sawAnyField = true

		field, value := splitSSEField(line)
		switch field {
		case "event":
			frame.Event = value
		case "data":
			dataLines = append(dataLines, value)
			dataLen += len(value) + 1
			if s.maxEventBytes > 0 && dataLen > s.maxEventBytes {
				return sseFrame{}, fmt.Errorf("openai: sse event exceeds maximum size of %d bytes", s.maxEventBytes)
			}
		case "id":
			frame.ID = value
		case "retry":
			frame.Retry = value
		default:
			// Unknown field names are ignored per the SSE spec.
		}
	}
}

func (s *sseReader) finish(frame sseFrame, dataLines []string) (sseFrame, error) {
	frame.Data = strings.Join(dataLines, "\n")
	if frame.Data == "[DONE]" {
		return sseFrame{}, ErrSSEDone
	}
	return frame, nil
}

// readLine reads one line with its trailing "\n" (and "\r", if present)
// stripped. It enforces maxLineBytes so an unbounded line cannot grow
// memory without limit.
func (s *sseReader) readLine() (string, error) {
	var buf []byte
	for {
		chunk, err := s.br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if s.maxLineBytes > 0 && len(buf) > s.maxLineBytes {
			return "", fmt.Errorf("openai: sse line exceeds maximum size of %d bytes", s.maxLineBytes)
		}
		switch {
		case err == nil:
			line := strings.TrimSuffix(string(buf), "\n")
			line = strings.TrimSuffix(line, "\r")
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(buf) == 0 {
				return "", io.EOF
			}
			return string(buf), nil
		default:
			return "", err
		}
	}
}

func splitSSEField(line string) (field, value string) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return line, ""
	}
	field = line[:i]
	value = strings.TrimPrefix(line[i+1:], " ")
	return field, value
}

// SSEDecoder exposes the bounded SSE parser to adapters whose streams are
// not OpenAI chat chunks (Anthropic Messages, for one).
//
// SSEDecoder 把有界 SSE 解析器提供给流格式不是 OpenAI 聊天分片的适配器（例如
// Anthropic Messages）。
type SSEDecoder struct{ r *sseReader }

// NewSSEDecoder wraps r; maxLineBytes and maxEventBytes bound one field line
// and one event's data, and 0 disables that check.
//
// NewSSEDecoder 包装 r；maxLineBytes 与 maxEventBytes 分别限制单行字段与单个
// 事件的数据，0 表示不检查。
func NewSSEDecoder(r io.Reader, maxLineBytes, maxEventBytes int) *SSEDecoder {
	return &SSEDecoder{r: newSSEReader(r, maxLineBytes, maxEventBytes)}
}

// Next returns the next event's name and data. It returns io.EOF at the end
// of the stream and ErrSSEDone for a "[DONE]" sentinel.
//
// Next 返回下一个事件的名称与数据。流结束时返回 io.EOF，遇到 "[DONE]" 哨兵时
// 返回 ErrSSEDone。
func (d *SSEDecoder) Next() (event, data string, err error) {
	frame, err := d.r.readFrame()
	return frame.Event, frame.Data, err
}
