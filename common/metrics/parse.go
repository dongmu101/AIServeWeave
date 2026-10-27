package metrics

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Sample is one physical series read back from a Prometheus text exposition:
// one line for a counter or gauge, and one of a histogram's
// _bucket/_sum/_count suffixed lines for a histogram. It carries no kind
// marker beyond the name — the caller already knows, from its own closed
// list of metric names it asked for, which of them are histograms.
//
// Sample 是从 Prometheus 文本导出中读回的一条物理序列：计数器或量表对应一行，
// 直方图对应其 _bucket/_sum/_count 后缀行中的一条。它不携带除名字外的类型标
// 记——调用方从自己关心的那份封闭指标名单里，本就知道哪些是直方图。
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// ParseExposition parses r as the exact text format Registry.Render writes:
// the inverse of exposition.go's writer, not a general third-party
// Prometheus parser. It rejects a data line it cannot parse rather than
// skipping it, because a scrape that silently drops half its samples is
// worse than one that fails loudly.
//
// ParseExposition 按 Registry.Render 写出的确切文本格式解析 r：是
// exposition.go 写入器的逆操作，不是一个通用的第三方 Prometheus 解析器。它对
// 无法解析的数据行报错而不是跳过——一次悄悄丢掉一半样本的抓取，比直接失败更糟。
func ParseExposition(r io.Reader) ([]Sample, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var samples []Sample
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s, err := parseSampleLine(line)
		if err != nil {
			return nil, fmt.Errorf("metrics: parse exposition line %q: %w", line, err)
		}
		samples = append(samples, s)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("metrics: read exposition: %w", err)
	}
	return samples, nil
}

// parseSampleLine parses one "name{labels} value" or "name value" line.
//
// parseSampleLine 解析一行 "name{labels} value" 或 "name value"。
func parseSampleLine(line string) (Sample, error) {
	name := line
	labels := map[string]string{}
	rest := ""

	if brace := strings.IndexByte(line, '{'); brace >= 0 {
		closeIdx := strings.LastIndexByte(line, '}')
		if closeIdx < brace {
			return Sample{}, fmt.Errorf("unbalanced braces")
		}
		name = line[:brace]
		var err error
		labels, err = parseLabels(line[brace+1 : closeIdx])
		if err != nil {
			return Sample{}, err
		}
		rest = strings.TrimSpace(line[closeIdx+1:])
	} else {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return Sample{}, fmt.Errorf("want \"name value\", got %d fields", len(fields))
		}
		name, rest = fields[0], fields[1]
	}

	value, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
	if err != nil {
		return Sample{}, fmt.Errorf("parse value: %w", err)
	}
	return Sample{Name: strings.TrimSpace(name), Labels: labels, Value: value}, nil
}

// parseLabels parses the comma-separated key="value" pairs between a series'
// braces, honoring quoted commas and the three escape sequences
// exposition.go's escapeLabelValue produces (\\, \", \n).
//
// parseLabels 解析一个序列花括号之间以逗号分隔的 key="value" 对，正确处理引号
// 内的逗号，以及 exposition.go 的 escapeLabelValue 产生的三种转义(\\、\"、\n)。
func parseLabels(body string) (map[string]string, error) {
	labels := map[string]string{}
	if strings.TrimSpace(body) == "" {
		return labels, nil
	}
	i := 0
	for i < len(body) {
		eq := strings.IndexByte(body[i:], '=')
		if eq < 0 {
			return nil, fmt.Errorf("label missing '='")
		}
		key := strings.TrimSpace(body[i : i+eq])
		i += eq + 1
		if i >= len(body) || body[i] != '"' {
			return nil, fmt.Errorf("label %q value not quoted", key)
		}
		i++
		var value strings.Builder
		for i < len(body) {
			c := body[i]
			if c == '\\' && i+1 < len(body) {
				switch body[i+1] {
				case '\\':
					value.WriteByte('\\')
				case '"':
					value.WriteByte('"')
				case 'n':
					value.WriteByte('\n')
				default:
					return nil, fmt.Errorf("label %q has an unknown escape", key)
				}
				i += 2
				continue
			}
			if c == '"' {
				i++
				break
			}
			value.WriteByte(c)
			i++
		}
		labels[key] = value.String()
		for i < len(body) && (body[i] == ',' || body[i] == ' ') {
			i++
		}
	}
	return labels, nil
}
