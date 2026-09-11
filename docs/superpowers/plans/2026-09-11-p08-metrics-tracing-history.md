# P08 指标、追踪与历史监控 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Registry 与控制面接入 `common/metrics`；新增按 request_id 关联的跨服务结构化日志；控制面新增历史指标采集、存储与查询；Console 交付 `/operator/metrics`(C27)。

**Architecture:** Registry 与控制面复用 Gateway 已有的 `common/metrics.Registry` + `-metrics-addr` Prometheus 文本导出模式。控制面新增只读的 `common/metrics.ParseExposition`(现有导出器的逆操作，零新依赖)，定时抓取 Gateway/Registry 的 `/metrics`，聚合为 5 分钟粒度写入新表(走 P07 的固定版本 SQL 迁移账本)，通过 `GET /operator/v1/metrics/history` 供 Console 查询。跨服务 trace 只在 Scheduler/Tunnel/Agent 三个既有调用点补结构化日志，复用一个新的共享 `common/reqid` 包把 Gateway HTTP 前门已经在铸造的 request_id 一路带到 Agent 的后端调用。

**Tech Stack:** Go 1.27、`common/metrics`、`common/runtime`、gorm(PostgreSQL/MySQL)、go-zero；前端 Next.js 16 + `echarts-for-react`。

**Spec:** [docs/superpowers/specs/2026-09-11-p08-metrics-tracing-history-design.md](../specs/2026-09-11-p08-metrics-tracing-history-design.md)

## Global Constraints

- 不引入 Prometheus/VictoriaMetrics/InfluxDB 等新部署组件，不引入第三方 Prometheus 解析库。
- 标签基数纪律：不放 `tenant_id`、`user_id`、Registry 侧 `node_id`、自由文本错误信息、模型名、请求路径。
- 新写与修改的注释英文在前、中文紧随；导出标识符有 doc comment。
- 不用真实 `time.Sleep` 推进测试时间；不记录凭据、哈希、Prompt 或工作流 JSON。
- 新表走固定版本 SQL 迁移账本(P07 模式)，不用 gorm AutoMigrate。
- 每个任务完成后运行该任务触及的包的 `go test`(或 Console 的 `pnpm test`)，全部通过再进入下一个任务；全部任务完成后跑 AGENTS.md 的完整门禁。

---

## Task 1: `common/metrics.ParseExposition`

**Files:**
- Create: `common/metrics/parse.go`
- Test: `common/metrics/parse_test.go`

**Interfaces:**
- Produces: `type Sample struct { Name string; Labels map[string]string; Value float64 }`，`func ParseExposition(r io.Reader) ([]Sample, error)`。后续任务(采集器)据此按 metric 名后缀(`_bucket`/`_sum`/`_count`)自行判断是否为直方图分量，`ParseExposition` 本身不解释 HELP/TYPE 注释。

- [ ] **Step 1: 写失败测试**

```go
// common/metrics/parse_test.go
package metrics_test

import (
	"strings"
	"testing"

	"AIServeWeave/common/metrics"
)

func TestParseExpositionRoundTripsCounterAndGauge(t *testing.T) {
	text := `# HELP demo_requests_total a counter
# TYPE demo_requests_total counter
demo_requests_total{endpoint="chat",status="200"} 3
# HELP demo_inflight a gauge
# TYPE demo_inflight gauge
demo_inflight 2
`
	samples, err := metrics.ParseExposition(strings.NewReader(text))
	if err != nil {
		t.Fatalf("ParseExposition() error = %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("len(samples) = %d, want 2", len(samples))
	}
	if samples[0].Name != "demo_requests_total" || samples[0].Value != 3 {
		t.Errorf("samples[0] = %+v, want name=demo_requests_total value=3", samples[0])
	}
	if got, want := samples[0].Labels["endpoint"], "chat"; got != want {
		t.Errorf("labels[endpoint] = %q, want %q", got, want)
	}
	if samples[1].Name != "demo_inflight" || samples[1].Value != 2 || len(samples[1].Labels) != 0 {
		t.Errorf("samples[1] = %+v, want name=demo_inflight value=2 no labels", samples[1])
	}
}

func TestParseExpositionRoundTripsHistogram(t *testing.T) {
	text := `# HELP demo_duration_seconds a histogram
# TYPE demo_duration_seconds histogram
demo_duration_seconds_bucket{endpoint="chat",le="0.1"} 1
demo_duration_seconds_bucket{endpoint="chat",le="+Inf"} 4
demo_duration_seconds_sum{endpoint="chat"} 1.5
demo_duration_seconds_count{endpoint="chat"} 4
`
	samples, err := metrics.ParseExposition(strings.NewReader(text))
	if err != nil {
		t.Fatalf("ParseExposition() error = %v", err)
	}
	if len(samples) != 4 {
		t.Fatalf("len(samples) = %d, want 4", len(samples))
	}
	if samples[1].Name != "demo_duration_seconds_bucket" || samples[1].Labels["le"] != "+Inf" || samples[1].Value != 4 {
		t.Errorf("samples[1] = %+v, want +Inf bucket with value 4", samples[1])
	}
	if samples[3].Name != "demo_duration_seconds_count" || samples[3].Value != 4 {
		t.Errorf("samples[3] = %+v, want demo_duration_seconds_count value 4", samples[3])
	}
}

func TestParseExpositionRejectsMalformedLine(t *testing.T) {
	if _, err := metrics.ParseExposition(strings.NewReader("not_a_valid_line\n")); err == nil {
		t.Fatal("ParseExposition() error = nil, want error for a line with no value")
	}
}

func TestParseExpositionRoundTripsRegistryRender(t *testing.T) {
	catalogue := metrics.Descriptions{
		"demo_dispatch_total": {Kind: metrics.KindCounter, Help: "dispatch attempts"},
	}
	reg := metrics.New(catalogue)
	reg.Counter("demo_dispatch_total", map[string]string{"result": "success"}).Add(2)

	var buf strings.Builder
	if err := reg.Render(&buf); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	samples, err := metrics.ParseExposition(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("ParseExposition() error = %v", err)
	}
	found := false
	for _, s := range samples {
		if s.Name == "demo_dispatch_total" && s.Labels["result"] == "success" && s.Value == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("samples = %+v, want a demo_dispatch_total{result=success} = 2 sample", samples)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./common/metrics/... -run TestParseExposition -v`
Expected: FAIL，`undefined: metrics.ParseExposition` / `undefined: metrics.Sample`

- [ ] **Step 3: 实现 `ParseExposition`**

```go
// common/metrics/parse.go
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
		close := strings.LastIndexByte(line, '}')
		if close < brace {
			return Sample{}, fmt.Errorf("unbalanced braces")
		}
		name = line[:brace]
		var err error
		labels, err = parseLabels(line[brace+1 : close])
		if err != nil {
			return Sample{}, err
		}
		rest = strings.TrimSpace(line[close+1:])
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
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./common/metrics/... -v`
Expected: PASS，全部含现有 `exposition_test.go`/`registry_test.go` 在内的用例

- [ ] **Step 5: 提交**

```bash
git add common/metrics/parse.go common/metrics/parse_test.go
git commit -m "$(cat <<'EOF'
feat(metrics): add ParseExposition as the inverse of the Prometheus text writer

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `common/reqid` 共享 request_id 上下文包

**Files:**
- Create: `common/reqid/reqid.go`
- Test: `common/reqid/reqid_test.go`

**Interfaces:**
- Produces: `func New() string`、`func WithValue(ctx context.Context, id string) context.Context`、`func FromContext(ctx context.Context) string`。Task 3 用它替换 Gateway httpapi 私有的 context key；Task 4/5/6 用它在 Scheduler/Tunnel 读取同一个 id。

- [ ] **Step 1: 写失败测试**

```go
// common/reqid/reqid_test.go
package reqid_test

import (
	"context"
	"testing"

	"AIServeWeave/common/reqid"
)

func TestWithValueAndFromContext(t *testing.T) {
	ctx := reqid.WithValue(context.Background(), "abc123")
	if got := reqid.FromContext(ctx); got != "abc123" {
		t.Errorf("FromContext() = %q, want %q", got, "abc123")
	}
}

func TestFromContextEmptyWhenUnset(t *testing.T) {
	if got := reqid.FromContext(context.Background()); got != "" {
		t.Errorf("FromContext() = %q, want empty", got)
	}
}

func TestNewProducesDistinctHexIDs(t *testing.T) {
	a, b := reqid.New(), reqid.New()
	if a == b {
		t.Fatalf("New() produced the same id twice: %q", a)
	}
	if len(a) != 32 {
		t.Errorf("len(New()) = %d, want 32 (16 bytes hex-encoded)", len(a))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./common/reqid/... -v`
Expected: FAIL，`no such package` / `undefined: reqid.New`

- [ ] **Step 3: 实现**

```go
// common/reqid/reqid.go

// Package reqid is the one place a request-correlation id is minted and
// carried on a context.Context, shared by Gateway's httpapi (which mints it)
// and tunnelserver (which must read the same value httpapi already
// generated, not mint a second, disconnected one) — see the P08 design doc
// for why two independently-minted ids made request_id useless for
// cross-service correlation before this package existed.
//
// reqid 包是请求关联 id 被铸造、并挂在 context.Context 上传递的唯一地方，由
// Gateway 的 httpapi(铸造方)与 tunnelserver(必须读到 httpapi 已经生成的同一个
// 值，而不是再铸造一个互不相干的第二个)共用——两个各自独立铸造的 id 为什么会让
// request_id 在跨服务关联上形同虚设，见 P08 设计文档。
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type contextKey struct{}

// New returns a fresh 16-byte hex-encoded id. A broken OS entropy source is
// not a condition worth crashing a request over, so it falls back to a fixed
// sentinel instead of panicking — degraded correlation for one request beats
// a downed handler.
//
// New 返回一个新的 16 字节十六进制 id。操作系统熵源损坏不值得让一次请求崩溃，
// 因此退化为一个固定哨兵值而不是 panic——一次请求关联能力下降，好过 handler 被
// 打挂。
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}

// WithValue attaches id to ctx.
//
// WithValue 把 id 挂到 ctx 上。
func WithValue(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext reads back the id attached by WithValue, or "" if none.
//
// FromContext 读回由 WithValue 挂上的 id，未挂载时返回空串。
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./common/reqid/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add common/reqid/
git commit -m "$(cat <<'EOF'
feat(reqid): add shared request-id context package

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Gateway httpapi 改用共享 `common/reqid`

**Files:**
- Modify: `service/aiServeWeaveGateway/httpapi/context.go`
- Test: `service/aiServeWeaveGateway/httpapi/context_test.go`

**Interfaces:**
- Consumes: `common/reqid.WithValue`、`common/reqid.FromContext`(Task 2)。
- Produces: `withRequestID`/`requestIDFrom` 签名不变，`chat.go:449`、`jobs.go:218`、`jobcancel.go:71`、`ratelimit.go:96` 等既有调用方零改动；但底层 context key 从 httpapi 私有类型换成 `common/reqid` 的类型，供 Task 4 的 tunnelserver 用同一个 key 读到同一个 id。

- [ ] **Step 1: 写失败测试**

```go
// service/aiServeWeaveGateway/httpapi/context_test.go
package httpapi

import (
	"context"
	"testing"

	"AIServeWeave/common/reqid"
)

func TestWithRequestIDIsReadableViaSharedPackage(t *testing.T) {
	ctx := withRequestID(context.Background(), "req-1")
	if got := reqid.FromContext(ctx); got != "req-1" {
		t.Errorf("reqid.FromContext() = %q, want %q — httpapi must use the shared context key so tunnelserver can read the same id", got, "req-1")
	}
	if got := requestIDFrom(ctx); got != "req-1" {
		t.Errorf("requestIDFrom() = %q, want %q", got, "req-1")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveGateway/httpapi/... -run TestWithRequestIDIsReadableViaSharedPackage -v`
Expected: FAIL，`reqid.FromContext(ctx)` 返回空串(私有 key 与共享包的 key 是两个不同类型)

- [ ] **Step 3: 实现**

```go
// service/aiServeWeaveGateway/httpapi/context.go
package httpapi

import (
	"context"

	"AIServeWeave/common/reqid"
)

func withRequestID(ctx context.Context, id string) context.Context {
	return reqid.WithValue(ctx, id)
}

func requestIDFrom(ctx context.Context) string {
	return reqid.FromContext(ctx)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveGateway/httpapi/... -v`
Expected: PASS，全部既有 httpapi 用例保持通过(`withRequestID`/`requestIDFrom` 的调用方完全不用改)

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveGateway/httpapi/context.go service/aiServeWeaveGateway/httpapi/context_test.go
git commit -m "$(cat <<'EOF'
refactor(gateway): httpapi request id uses the shared reqid context key

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: `tunnelserver.Dispatch` 优先复用 ctx 里的 request_id，并加起止结构化日志

**Files:**
- Modify: `service/aiServeWeaveGateway/tunnelserver/call.go`
- Test: `service/aiServeWeaveGateway/tunnelserver/call_test.go`(在既有文件中新增用例)

**Interfaces:**
- Consumes: `common/reqid.FromContext`(Task 2)；`s.logger *slog.Logger`(已存在，见 `server.go:104`)。
- Produces: `Response` 新增未导出字段 `nodeID`、`runtimeID`、`logger string`/`*slog.Logger`，供本任务内 `Close()` 使用(仅包内使用，不改变导出 API)。

- [ ] **Step 1: 写失败测试**

```go
// 追加到 service/aiServeWeaveGateway/tunnelserver/call_test.go
// (沿用该文件已有的测试夹具，例如起一个 Server 并接入一个 fake 节点连接的辅助函数；
// 具体函数名以该文件已有的为准，语义等价即可)

func TestDispatchPrefersRequestIDFromContext(t *testing.T) {
	srv, node, frames := newDispatchTestFixture(t) // 复用本文件已有的夹具：Server + 一个已连接的 fake 节点 + 能读到该节点收到的帧的 channel
	ctx := reqid.WithValue(context.Background(), "ctx-req-id")

	resp, err := srv.Dispatch(ctx, Request{NodeID: node, RuntimeID: "ollama", Operation: tunnelv1.Operation_OPERATION_CHAT, Payload: []byte("{}")})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	defer resp.Close()

	headers := <-frames // 该 fake 节点收到的第一帧：GatewayFrame_Headers
	if got := headers.GetRequestId(); got != "ctx-req-id" {
		t.Errorf("dispatched request_id = %q, want the id carried on ctx", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveGateway/tunnelserver/... -run TestDispatchPrefersRequestIDFromContext -v`
Expected: FAIL，`Dispatch` 目前只看 `req.Trace["request_id"]`，忽略 ctx，因此帧上的 request_id 是随机生成的，不等于 `"ctx-req-id"`

- [ ] **Step 3: 实现**

```go
// service/aiServeWeaveGateway/tunnelserver/call.go
// 新增 import：
	"log/slog"

	"AIServeWeave/common/reqid"

// 替换 Dispatch 内 call.go:166-169 这四行：
	requestID := reqid.FromContext(ctx)
	if requestID == "" {
		requestID = req.Trace["request_id"]
	}
	if requestID == "" {
		requestID = newRequestID()
	}

	s.logger.Info("tunnel dispatch started",
		slog.String("request_id", requestID),
		slog.String("node_id", req.NodeID),
		slog.String("runtime_id", req.RuntimeID),
		slog.String("operation", req.Operation.String()))

// Response 结构体(call.go:52-74)新增三个未导出字段：
	nodeID    string
	runtimeID string
	logger    *slog.Logger

// Dispatch 末尾构造 Response 的字面量(call.go:227-245)新增三行赋值：
		nodeID:    req.NodeID,
		runtimeID: req.RuntimeID,
		logger:    s.logger,

// Response.Close() 内 r.recordOnce.Do(...) 里，记录指标那行之后追加一行日志：
	r.recordOnce.Do(func() {
		d := r.clock.Now().Sub(r.started)
		result := tunnelwire.ResultFor(r.outcome)
		r.metrics.Dispatch(r.operation, result, d)
		r.logger.Info("tunnel dispatch completed",
			slog.String("request_id", r.call.id),
			slog.String("node_id", r.nodeID),
			slog.String("runtime_id", r.runtimeID),
			slog.String("operation", r.operation.String()),
			slog.String("result", string(result)),
			slog.Duration("duration", d))
	})
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveGateway/tunnelserver/... -v`
Expected: PASS，含新用例与全部既有用例(尤其是既有的指标用例不应因新增日志字段而改变指标行为)

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveGateway/tunnelserver/call.go service/aiServeWeaveGateway/tunnelserver/call_test.go
git commit -m "$(cat <<'EOF'
feat(gateway): tunnelserver dispatch reuses ctx request_id and logs start/completion

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Gateway `scheduler` 增加派发决策结构化日志

**Files:**
- Modify: `service/aiServeWeaveGateway/scheduler/scheduler.go`
- Test: `service/aiServeWeaveGateway/scheduler/scheduler_test.go`(在既有文件中新增用例)

**Interfaces:**
- Consumes: `common/reqid.FromContext`(Task 2)。
- Produces: `Scheduler` 新增未导出字段 `logger *slog.Logger`；`Config`(scheduler 包已有的配置结构体)新增可选字段 `Logger *slog.Logger`，nil 时退化为 `slog.New(slog.DiscardHandler)`，不影响既有调用方(`New(server, scheduler.Config{Metrics: registry})` 这类字面量零改动)。

`scheduler` 包此前完全没有日志(调研已确认)，因此本任务先补 `New` 里的 nil 兜底，再在 `Chat`/`Embed`/`ChatStream` 里各加一行。

- [ ] **Step 1: 写失败测试**

```go
// 追加到 service/aiServeWeaveGateway/scheduler/scheduler_test.go
// 用一个把日志写进 bytes.Buffer 的 slog.Handler 断言日志内容，而不是断言具体格式串，
// 避免测试与文案耦合过紧。

func TestChatLogsDispatchDecisionWithRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	s := newTestScheduler(t, scheduler.Config{Metrics: metricstest.New(), Logger: logger}) // 复用本文件已有的"起一个带 fake 节点的 Scheduler"辅助函数，追加 Logger 字段
	ctx := reqid.WithValue(context.Background(), "sched-req-id")

	if _, _, err := s.Chat(ctx, runtime.ChatRequest{Model: "demo"}); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if !strings.Contains(buf.String(), "sched-req-id") {
		t.Errorf("scheduler log = %q, want it to contain the ctx request_id", buf.String())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveGateway/scheduler/... -run TestChatLogsDispatchDecisionWithRequestID -v`
Expected: FAIL，`scheduler.Config` 目前没有 `Logger` 字段(编译失败)，或即使加了字段也读不到任何日志输出

- [ ] **Step 3: 实现**

```go
// service/aiServeWeaveGateway/scheduler/scheduler.go
// 新增 import："log/slog"、"AIServeWeave/common/reqid"

// Config 结构体新增字段(与既有的 Metrics runtime.Metrics 并列)：
	// Logger receives per-attempt dispatch-decision events. Nil discards them.
	// Logger 接收逐次派发决策事件。为 nil 时丢弃。
	Logger *slog.Logger

// Scheduler 结构体新增未导出字段：
	logger *slog.Logger

// New(...) 构造函数内，紧邻既有的 metrics 兜底逻辑之后：
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	// ...(既有构造逻辑不变，最终赋值处加上：)
	// &Scheduler{ ..., logger: logger }

// Chat 方法(scheduler.go:119-140)循环体内，s.metrics.Dispatch(c, err) 之后追加：
		s.logger.Info("scheduler dispatch decided",
			slog.String("request_id", reqid.FromContext(ctx)),
			slog.String("node_id", c.NodeID),
			slog.String("runtime_id", c.RuntimeID),
			slog.Bool("success", err == nil))
```

`Embed`(scheduler.go:143-164)与 `ChatStream`(scheduler.go:173-221)在各自循环体内、指标记录调用之后加同样一行(把 `slog.String` 的固定文案分别改成 `"scheduler embed dispatch decided"`/`"scheduler chat stream dispatch decided"`，或统一成一个带 `operation` 标签的私有 helper 方法 `func (s *Scheduler) logDispatchDecision(ctx context.Context, op, capability string, c Candidate, err error)`，供三处复用——后者更贴合仓库"避免重复"的约定，采用后者)。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveGateway/scheduler/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveGateway/scheduler/scheduler.go service/aiServeWeaveGateway/scheduler/scheduler_test.go
git commit -m "$(cat <<'EOF'
feat(gateway): scheduler logs each dispatch decision with the request id

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Gateway `main.go` 装配 Scheduler 的 Logger

**Files:**
- Modify: `service/aiServeWeaveGateway/main.go`

**Interfaces:**
- Consumes: Task 5 新增的 `scheduler.Config.Logger`。

- [ ] **Step 1: 修改**

`main.go:248` 附近现有的 `sched := scheduler.New(server, scheduler.Config{Metrics: registry})` 改为：

```go
sched := scheduler.New(server, scheduler.Config{Metrics: registry, Logger: logger})
```

(`logger` 是 `main.go` 里已经构造好的 `*slog.Logger`，供 tunnelserver/httpapi 等复用的同一个实例。)

- [ ] **Step 2: 跑编译确认无破坏**

Run: `go build ./service/aiServeWeaveGateway/...`
Expected: 无输出，构建成功

- [ ] **Step 3: 提交**

```bash
git add service/aiServeWeaveGateway/main.go
git commit -m "$(cat <<'EOF'
feat(gateway): wire the shared logger into the scheduler

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Agent `tunnel/dispatch.go` 增加后端调用起止结构化日志

**Files:**
- Modify: `service/aiServeWeaveAgent/tunnel/dispatch.go`
- Test: `service/aiServeWeaveAgent/tunnel/dispatch_test.go`(在既有文件中新增用例)

**Interfaces:**
- Consumes: 无新依赖——`req.ID`(已存在)、`d.logger`(已存在，`dispatch.go:85`)。

`run(ctx, rt, spec, req, sink)`(`dispatch.go:185`)是所有后端调用(`ir.Chat`/`ir.ChatStream`/`ir.Embed`/`wr.Submit` 等)的统一入口，因此在函数入口加一条"开始"日志、在函数返回前(用 `defer` 结合具名返回值捕获 err)加一条"结束"日志，覆盖全部操作类型，不需要在每个 `case` 分支里分别插入。

- [ ] **Step 1: 写失败测试**

```go
// 追加到 service/aiServeWeaveAgent/tunnel/dispatch_test.go
// 用一个写进 bytes.Buffer 的 slog.Handler 断言日志内容

func TestRunLogsBackendCallStartAndEnd(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	d := newTestDispatcher(t, DispatchConfig{Logger: logger /* ...其余字段沿用本文件已有的夹具默认值... */})
	req := &Request{ID: "backend-req-id", Headers: &tunnelv1.RequestHeaders{RuntimeId: "ollama", Operation: tunnelv1.Operation_OPERATION_CHAT}}

	_ = d.Handle(context.Background(), req, newDiscardSink()) // newDiscardSink：本文件已有或需新增的一个丢弃响应的 ResponseSink 桩

	log := buf.String()
	if !strings.Contains(log, "backend-req-id") {
		t.Errorf("dispatcher log = %q, want it to contain the request id", log)
	}
	if !strings.Contains(log, "backend call started") || !strings.Contains(log, "backend call finished") {
		t.Errorf("dispatcher log = %q, want both a start and a finish line", log)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveAgent/tunnel/... -run TestRunLogsBackendCallStartAndEnd -v`
Expected: FAIL，当前 `run` 不写任何日志

- [ ] **Step 3: 实现**

```go
// service/aiServeWeaveAgent/tunnel/dispatch.go
// run 方法签名不变；在函数体最开头插入：

func (d *Dispatcher) run(ctx context.Context, rt runtime.Runtime, spec tunnelwire.OperationSpec, req *Request, sink ResponseSink) (err error) {
	started := d.clock.Now() // Dispatcher 若已有注入的 clock 字段则复用；否则用 time.Now()，与本文件其余计时方式保持一致
	runtimeID := req.Headers.GetRuntimeId()
	d.logger.Info("backend call started",
		slog.String("request_id", req.ID),
		slog.String("runtime_id", runtimeID),
		slog.String("operation", spec.Operation.String()))
	defer func() {
		d.logger.Info("backend call finished",
			slog.String("request_id", req.ID),
			slog.String("runtime_id", runtimeID),
			slog.String("operation", spec.Operation.String()),
			slog.Bool("success", err == nil),
			slog.Duration("duration", d.clock.Now().Sub(started)))
	}()

	// ...原有函数体不变...
```

若 `run` 当前签名的返回值不是具名的 `err`，需要改成具名返回值(`) (err error) {`)以便 `defer` 捕获最终错误；原有的每个 `return ...err...` 分支不需要改动，仍然正常工作。若 `Dispatcher` 目前没有注入的 `runtime.Clock`，改用 `time.Now()` 并在测试里放宽耗时断言，不为本任务新增 Clock 依赖(不属于本任务范围)。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveAgent/tunnel/... -v`
Expected: PASS，含新用例与全部既有用例

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveAgent/tunnel/dispatch.go service/aiServeWeaveAgent/tunnel/dispatch_test.go
git commit -m "$(cat <<'EOF'
feat(agent): log backend call start/finish keyed by request_id

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Registry `registryserver.Descriptions()` 与 `recorder`

**Files:**
- Create: `service/aiServeWeaveRegistry/internal/registryserver/metrics.go`
- Test: `service/aiServeWeaveRegistry/internal/registryserver/metrics_test.go`

**Interfaces:**
- Produces: `func Descriptions() metrics.Descriptions`；`type recorder struct{ sink runtime.Metrics }`、`func newRecorder(sink runtime.Metrics) *recorder`，以及下列方法(Task 9 在各 RPC 里调用)：
  - `func (r *recorder) Register(result string)`
  - `func (r *recorder) CertRenewal(result string)`
  - `func (r *recorder) GatewayJoined()` / `func (r *recorder) GatewayLeft()`(维护 `registry_gateway_replicas_connected` gauge)
  - `func (r *recorder) TokenOp(op, result string)`
  - `func (r *recorder) NodeStateChange(action, result string)`
  - `func (r *recorder) ListNodeStates(result string)`
- 标签值全部来自本文件内定义的封闭字符串常量，不接受调用方传入自由文本——这是 Task 11 基数测试要断言的东西。

- [ ] **Step 1: 写失败测试**

```go
// service/aiServeWeaveRegistry/internal/registryserver/metrics_test.go
package registryserver

import (
	"testing"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/metrics/metricstest"
)

func TestDescriptionsCoverEveryMetric(t *testing.T) {
	descs := Descriptions()
	for _, name := range []string{
		MetricRegisterTotal, MetricCertRenewalTotal, MetricGatewayReplicasConnected,
		MetricTokenOpsTotal, MetricNodeStateChangesTotal, MetricListNodeStatesTotal,
	} {
		desc, ok := descs[name]
		if !ok {
			t.Errorf("Descriptions() missing %s", name)
			continue
		}
		if desc.Help == "" {
			t.Errorf("%s has no Help text", name)
		}
	}
}

func TestRecorderTracksRegisterOutcomes(t *testing.T) {
	mx := metricstest.New()
	rec := newRecorder(mx)

	rec.Register(ResultSuccess)
	rec.Register(ResultConflict)

	if got := mx.Sum(MetricRegisterTotal, map[string]string{"result": ResultSuccess}); got != 1 {
		t.Errorf("success count = %v, want 1", got)
	}
	if got := mx.Sum(MetricRegisterTotal, map[string]string{"result": ResultConflict}); got != 1 {
		t.Errorf("conflict count = %v, want 1", got)
	}
}

func TestRecorderTracksGatewayReplicaGauge(t *testing.T) {
	mx := metricstest.New()
	rec := newRecorder(mx)

	rec.GatewayJoined()
	rec.GatewayJoined()
	rec.GatewayLeft()

	if got := mx.Sum(MetricGatewayReplicasConnected, nil); got != 1 {
		t.Errorf("connected replicas = %v, want 1", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveRegistry/internal/registryserver/... -run TestDescriptions -v`
Expected: FAIL，`metrics.go` 尚不存在

- [ ] **Step 3: 实现**

```go
// service/aiServeWeaveRegistry/internal/registryserver/metrics.go
package registryserver

import (
	"sync"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
)

// Metric name constants — mirrors tunnelserver/metrics.go's convention: one
// exported constant per name so a rename shows up as a compile error at every
// call site instead of a silently orphaned series.
//
// 指标名常量——照抄 tunnelserver/metrics.go 的约定：每个名字一个导出常量，改名
// 会在每个调用点变成编译错误，而不是留下一条悄悄失联的序列。
const (
	MetricRegisterTotal            = "registry_register_total"
	MetricCertRenewalTotal         = "registry_cert_renewal_total"
	MetricGatewayReplicasConnected = "registry_gateway_replicas_connected"
	MetricTokenOpsTotal            = "registry_token_ops_total"
	MetricNodeStateChangesTotal    = "registry_node_state_changes_total"
	MetricListNodeStatesTotal      = "registry_list_node_states_total"
)

// Closed result/operation/action vocabularies. No RPC method may pass a
// string here that is not one of these constants — that is what keeps every
// label value bounded regardless of how many distinct gRPC error messages
// exist.
//
// 封闭的 result/operation/action 取值集合。任何 RPC 方法都不能在这里传入不属于
// 这些常量的字符串——这正是不论底层有多少种 gRPC 错误消息，标签值始终有界的
// 保证方式。
const (
	ResultSuccess         = "success"
	ResultReconnect       = "reconnect"
	ResultConflict        = "conflict"
	ResultPendingApproval = "pending_approval"
	ResultInvalid         = "invalid"
	ResultUnauthorized    = "unauthorized"
	ResultNotFound        = "not_found"
	ResultInternal        = "internal"
)

const (
	TokenOpMint   = "mint"
	TokenOpRevoke = "revoke"
)

const (
	NodeActionApprove          = "approve"
	NodeActionDisable          = "disable"
	NodeActionEnable           = "enable"
	NodeActionMaintenanceSet   = "maintenance_set"
	NodeActionMaintenanceClear = "maintenance_clear"
)

// Descriptions returns this package's metric catalogue.
//
// Descriptions 返回本包的指标目录。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricRegisterTotal:            {Kind: metrics.KindCounter, Help: "Node identity registration attempts by outcome. / 按结果分类的节点身份注册尝试次数。"},
		MetricCertRenewalTotal:         {Kind: metrics.KindCounter, Help: "Certificate renewal attempts by outcome. / 按结果分类的证书续期尝试次数。"},
		MetricGatewayReplicasConnected: {Kind: metrics.KindGauge, Help: "Gateway replicas currently joined to this Registry. / 当前加入本 Registry 的 Gateway 副本数。"},
		MetricTokenOpsTotal:            {Kind: metrics.KindCounter, Help: "TokenAdmin token mint/revoke calls by outcome. / TokenAdmin 铸造/撤销 token 调用，按结果分类。"},
		MetricNodeStateChangesTotal:    {Kind: metrics.KindCounter, Help: "Node approve/disable/enable/maintenance calls by outcome. / 节点审批/禁用/启用/维护调用，按结果分类。"},
		MetricListNodeStatesTotal:      {Kind: metrics.KindCounter, Help: "ListNodeStates calls by outcome. / ListNodeStates 调用次数，按结果分类。"},
	}
}

// recorder is registryserver's typed wrapper over runtime.Metrics, mirroring
// tunnelserver/metrics.go's recorder — the caller passes only a closed-set
// result string, never a raw error, so a label value cannot become free text
// by accident.
//
// recorder 是 registryserver 对 runtime.Metrics 的类型化包装，照抄
// tunnelserver/metrics.go 的 recorder——调用方只能传入一个封闭集合里的 result
// 字符串，从不传原始 error，标签值因此不会意外变成自由文本。
type recorder struct {
	sink runtime.Metrics

	mu        sync.Mutex
	connected int
}

func newRecorder(sink runtime.Metrics) *recorder {
	if sink == nil {
		sink = discardMetrics{}
	}
	return &recorder{sink: sink}
}

func (r *recorder) Register(result string) {
	r.sink.Counter(MetricRegisterTotal, map[string]string{"result": result}).Add(1)
}

func (r *recorder) CertRenewal(result string) {
	r.sink.Counter(MetricCertRenewalTotal, map[string]string{"result": result}).Add(1)
}

func (r *recorder) GatewayJoined() {
	r.mu.Lock()
	r.connected++
	n := r.connected
	r.mu.Unlock()
	r.sink.Gauge(MetricGatewayReplicasConnected, nil).Set(float64(n))
}

func (r *recorder) GatewayLeft() {
	r.mu.Lock()
	if r.connected > 0 {
		r.connected--
	}
	n := r.connected
	r.mu.Unlock()
	r.sink.Gauge(MetricGatewayReplicasConnected, nil).Set(float64(n))
}

func (r *recorder) TokenOp(op, result string) {
	r.sink.Counter(MetricTokenOpsTotal, map[string]string{"operation": op, "result": result}).Add(1)
}

func (r *recorder) NodeStateChange(action, result string) {
	r.sink.Counter(MetricNodeStateChangesTotal, map[string]string{"action": action, "result": result}).Add(1)
}

func (r *recorder) ListNodeStates(result string) {
	r.sink.Counter(MetricListNodeStatesTotal, map[string]string{"result": result}).Add(1)
}

// discardMetrics is the zero-cost sink used when Config.Metrics is nil,
// mirroring tunnelserver's discardMetrics.
//
// discardMetrics 是 Config.Metrics 为 nil 时使用的零开销汇点，照抄
// tunnelserver 的 discardMetrics。
type discardMetrics struct{}

func (discardMetrics) Counter(string, map[string]string) runtime.Counter     { return discardInstrument{} }
func (discardMetrics) Gauge(string, map[string]string) runtime.Gauge         { return discardInstrument{} }
func (discardMetrics) Histogram(string, map[string]string) runtime.Histogram { return discardInstrument{} }

type discardInstrument struct{}

func (discardInstrument) Add(float64)     {}
func (discardInstrument) Set(float64)     {}
func (discardInstrument) Observe(float64) {}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveRegistry/internal/registryserver/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveRegistry/internal/registryserver/metrics.go service/aiServeWeaveRegistry/internal/registryserver/metrics_test.go
git commit -m "$(cat <<'EOF'
feat(registry): add metrics catalogue and recorder

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: 把 `recorder` 接入 `Server` 并在每个 RPC 里打点

**Files:**
- Modify: `service/aiServeWeaveRegistry/internal/registryserver/server.go`
- Modify: `service/aiServeWeaveRegistry/internal/registryserver/identity.go`
- Modify: `service/aiServeWeaveRegistry/internal/registryserver/roster.go`
- Modify: `service/aiServeWeaveRegistry/internal/registryserver/token_admin.go`
- Test: 在 `identity_test.go`/`roster_test.go`/`node_admin_test.go`/`approval_test.go` 中各加一个断言

**Interfaces:**
- Consumes: Task 8 的 `newRecorder`/`recorder` 方法。
- Produces: `Config` 新增可选字段 `Metrics runtime.Metrics`，`Server` 新增未导出字段 `metrics *recorder`。

- [ ] **Step 1: 写失败测试**(以 `Register` 为代表，其余方法在 Step 3 里逐条对照实现，验证方式相同：用 `metricstest.New()` 建一个 `Server`，调用 RPC，断言对应 counter/gauge 增加)

```go
// 追加到 service/aiServeWeaveRegistry/internal/registryserver/identity_test.go
func TestRegisterRecordsMetrics(t *testing.T) {
	mx := metricstest.New()
	server, err := registryserver.New(registryserver.Config{
		CA: root, Tokens: tokens, Identities: identities, Metrics: mx,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// ...沿用本文件已有的、构造一个合法 RegisterRequest 并调用 server.Register 的方式...
	if _, err := server.Register(ctx, validRegisterRequest(t)); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if got := mx.Sum(registryserver.MetricRegisterTotal, nil); got == 0 {
		t.Error("expected Register to record a metric, got none")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveRegistry/internal/registryserver/... -run TestRegisterRecordsMetrics -v`
Expected: FAIL，`Config.Metrics` 字段不存在(编译失败)

- [ ] **Step 3: 实现**

`server.go`：

```go
// Config 结构体新增字段(与既有 Clock/Logger 并列)：
	// Metrics receives this server's counters and gauges. Nil records nothing.
	// Metrics 接收本服务的计数器与量表。为 nil 时不记录任何东西。
	Metrics runtime.Metrics

// Server 结构体新增未导出字段：
	metrics *recorder

// New(cfg Config) 内，紧邻既有的 clock/logger 兜底逻辑之后：
	metrics := newRecorder(cfg.Metrics)
	// ...(最终构造 &Server{..., metrics: metrics} 时带上)
```

`identity.go` 的 `Register`/`RenewCertificate` 在各自每一条返回路径前插入对应记录调用(不改变任何返回值/错误，只在 `return` 之前多一行)：

| 方法 | 分支 | 插入的记录调用 |
| --- | --- | --- |
| `Register` | bootstrap token 无效/过期 → `Unauthenticated` | `s.metrics.Register(ResultUnauthorized)` |
| `Register` | 节点被禁用 → `PermissionDenied` | `s.metrics.Register(ResultUnauthorized)` |
| `Register` | 空 node_id / CSR 解析失败 → `InvalidArgument` | `s.metrics.Register(ResultInvalid)` |
| `Register` | 待运维审批 → `PermissionDenied` | `s.metrics.Register(ResultPendingApproval)` |
| `Register` | CA 签发失败 → `InvalidArgument` | `s.metrics.Register(ResultInvalid)` |
| `Register` | token store / identity store 失败 → `Internal` | `s.metrics.Register(ResultInternal)` |
| `Register` | `identitystore.OutcomeConflict` → `AlreadyExists` | `s.metrics.Register(ResultConflict)` |
| `Register` | 成功，`first_registration=true` | `s.metrics.Register(ResultSuccess)` |
| `Register` | 成功，重连(既有节点、未换 key) | `s.metrics.Register(ResultReconnect)` |
| `RenewCertificate` | 任意失败分支(peer 提取/mTLS 不符/禁用/CSR/CA/store) | `s.metrics.CertRenewal(ResultXxx)`(按失败性质选 `ResultUnauthorized`/`ResultInvalid`/`ResultInternal`，与上表 `Register` 同一套映射原则) |
| `RenewCertificate` | 成功 | `s.metrics.CertRenewal(ResultSuccess)` |

`roster.go` 的 `Join`：在 `s.roster.join(js)` 调用成功之后插入 `s.metrics.GatewayJoined()`；在 `defer s.roster.leave(js)` 所在的 defer 闭包里插入 `s.metrics.GatewayLeft()`(与 `leave` 调用同一处，保证 join/leave 严格配对)。`requireGatewayToken` 失败与首次 `Recv` 失败不计入连接数变化，不需要打点(尚未真正建立一次连接)。

`token_admin.go` 的 `MintToken`/`RevokeToken`：`requireAdmin` 失败 → `s.metrics.TokenOp(TokenOpMint 或 TokenOpRevoke, ResultUnauthorized)`；`MintToken` 的 `InvalidArgument`(ttl<=0) → `ResultInvalid`；store 失败 → `ResultInternal`；成功 → `ResultSuccess`。`RevokeToken` 的 `NotFound` → `ResultNotFound`。

`DisableNode`/`EnableNode`/`ApproveNode`/`SetMaintenance`/`ClearMaintenance`：`requireAdmin` 失败 → `s.metrics.NodeStateChange(NodeActionXxx, ResultUnauthorized)`；空 node_id → `ResultInvalid`；store 失败 → `ResultInternal`；成功 → `ResultSuccess`(`NodeActionXxx` 按方法名对应 `NodeActionDisable`/`NodeActionEnable`/`NodeActionApprove`/`NodeActionMaintenanceSet`/`NodeActionMaintenanceClear`)。

`ListNodeStates`：`requireAdmin` 失败 → `s.metrics.ListNodeStates(ResultUnauthorized)`；成功 → `s.metrics.ListNodeStates(ResultSuccess)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveRegistry/... -v`
Expected: PASS，含新增用例与全部既有用例(既有测试构造 `Config` 字面量时未设置 `Metrics`，`newRecorder(nil)` 走 `discardMetrics` 兜底，行为不变)

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveRegistry/internal/registryserver/
git commit -m "$(cat <<'EOF'
feat(registry): record metrics at every RPC outcome

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Registry `main.go` 新增 `-metrics-addr`

**Files:**
- Modify: `service/aiServeWeaveRegistry/main.go`

**Interfaces:**
- Consumes: Task 8/9 的 `registryserver.Descriptions()`、`registryserver.Config.Metrics`；`common/metrics.New`/`Registry.Server`(已存在，Gateway 已在用)。

- [ ] **Step 1: 实现**(本任务是纯装配，跟 Gateway `main.go` 的既有写法逐字对照，不新增测试文件；用 Step 2 的手工验证代替)

```go
// service/aiServeWeaveRegistry/main.go
// 新增 import："net/http"、"AIServeWeave/common/metrics"

// 在既有 flag 声明块(main.go:65-85)里，紧邻 addr 之后新增：
	metricsAddr := flag.String("metrics-addr", "127.0.0.1:9090",
		"address the Prometheus /metrics listener binds; loopback by default, empty disables it")

// 在构造 registryserver.Server 之前(main.go:106 附近，server, err := registryserver.New(...) 之前)：
	registry := metrics.New(registryserver.Descriptions())

// registryserver.Config 字面量新增一行：
	server, err := registryserver.New(registryserver.Config{
		CA: root, Tokens: tokens, Identities: identities, Logger: logger,
		AdminToken: adminToken, GatewayToken: gatewayToken,
		Metrics: registry,
	})

// 在 grpcServer.Serve 启动的 goroutine 之前(main.go:159 附近)，照抄 Gateway main.go:371-388 的形状：
	var metricsServer *http.Server
	if *metricsAddr == "" {
		logger.Warn("no -metrics-addr; this replica exports no metrics")
	} else {
		metricsServer = registry.Server(*metricsAddr)
		go func() {
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics listener stopped", slog.Any("error", err))
			}
		}()
		logger.Info("metrics listening", slog.String("metrics_addr", *metricsAddr))
	}

// 在 grpcServer.GracefulStop() 之后(main.go:172 附近)，关闭顺序与 Gateway 一致——先排空
// gRPC 连接，最后关指标监听：
	if metricsServer != nil {
		_ = metricsServer.Close()
	}
```

只在真正起服务器的路径里启动这个监听器；`-mint-token`/`-revoke-token`/`-disable-node`/`-enable-node`/`-issue-server-cert` 这几条在 `run()` 里提前 `return` 的 CLI 分支不触碰这段代码，本就不会执行到这里。

- [ ] **Step 2: 手工验证**

Run:
```bash
go build ./service/aiServeWeaveRegistry/...
go run ./service/aiServeWeaveRegistry -data-dir /tmp/aisw-registry-metrics-check -metrics-addr 127.0.0.1:19090 &
sleep 1
curl -sf http://127.0.0.1:19090/metrics | grep registry_
kill %1
```
Expected: `curl` 输出包含 `registry_gateway_replicas_connected`、`aisw_metric_conflicts_total` 等行；进程无 panic

- [ ] **Step 3: 提交**

```bash
git add service/aiServeWeaveRegistry/main.go
git commit -m "$(cat <<'EOF'
feat(registry): add -metrics-addr Prometheus listener

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Registry 标签基数测试

**Files:**
- Create: `service/aiServeWeaveRegistry/internal/registryserver/metrics_cardinality_test.go`

**Interfaces:**
- Consumes: Task 8/9 的全部导出常量。

- [ ] **Step 1: 写测试**(本任务本身就是测试，没有独立于测试之外的实现代码；照抄 `tunnelserver/metrics_test.go` 的 `TestMetricLabelValuesAreBounded` 思路)

```go
// service/aiServeWeaveRegistry/internal/registryserver/metrics_cardinality_test.go
package registryserver_test

import (
	"testing"

	"AIServeWeave/common/metrics/metricstest"
	"AIServeWeave/service/aiServeWeaveRegistry/internal/registryserver"
)

func TestMetricLabelValuesAreBounded(t *testing.T) {
	allowed := map[string]map[string]bool{
		"result": {
			registryserver.ResultSuccess: true, registryserver.ResultReconnect: true,
			registryserver.ResultConflict: true, registryserver.ResultPendingApproval: true,
			registryserver.ResultInvalid: true, registryserver.ResultUnauthorized: true,
			registryserver.ResultNotFound: true, registryserver.ResultInternal: true,
		},
		"operation": {registryserver.TokenOpMint: true, registryserver.TokenOpRevoke: true},
		"action": {
			registryserver.NodeActionApprove: true, registryserver.NodeActionDisable: true,
			registryserver.NodeActionEnable: true, registryserver.NodeActionMaintenanceSet: true,
			registryserver.NodeActionMaintenanceClear: true,
		},
	}

	mx := metricstest.New()
	// 驱动一个完整的注册表实例，行使每个已知分支——复用本包 identity_test.go/
	// roster_test.go/node_admin_test.go/approval_test.go 里已有的夹具与请求构造函数
	// 逐一调用 Register/RenewCertificate/Join/MintToken/RevokeToken/DisableNode/
	// EnableNode/ApproveNode/SetMaintenance/ClearMaintenance/ListNodeStates 的成功与
	// 失败分支，全部指向同一个 mx。

	for _, s := range mx.All() {
		for key, value := range s.Labels {
			set, checked := allowed[key]
			if !checked {
				t.Errorf("metric %s has an unexpected label key %q", s.Name, key)
				continue
			}
			if !set[value] {
				t.Errorf("metric %s label %s = %q, which is not a bounded value", s.Name, key, value)
			}
		}
	}
}
```

- [ ] **Step 2: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveRegistry/internal/registryserver/... -run TestMetricLabelValuesAreBounded -v`
Expected: PASS(若失败，说明 Task 9 某个分支传入了不属于封闭常量集合的字符串，回 Task 9 修正)

- [ ] **Step 3: 提交**

```bash
git add service/aiServeWeaveRegistry/internal/registryserver/metrics_cardinality_test.go
git commit -m "$(cat <<'EOF'
test(registry): assert every recorded metric label value is bounded

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Registry README 补「指标」一节

**Files:**
- Modify: `service/aiServeWeaveRegistry/README.md`

- [ ] **Step 1: 编写文档**

在 README 追加一节「## 指标」，格式参照 Gateway README 的「## 指标」一节：`-metrics-addr` 的默认值与关闭方式、一张 `指标|标签|说明` 表(把 Task 8 六个指标常量、Task 9 表格里的封闭标签取值抄进去)、以及标签基数纪律的同一句话("模型名不进任何标签……" 对 Registry 场景改写为 "node_id 不进任何标签，按节点排查走结构化日志")。同时把 Gateway README「下一步」第 1 条("Registry 侧指标……")标记为已完成或删除该条。

- [ ] **Step 2: 提交**

```bash
git add service/aiServeWeaveRegistry/README.md service/aiServeWeaveGateway/README.md
git commit -m "$(cat <<'EOF'
docs(registry): document the new metrics endpoint and catalogue

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: 控制面 `internal/metrics` 目录 + HTTP 请求量/耗时中间件

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/metrics/metrics.go`
- Create: `service/aiServeWeaveControlPlane/internal/handler/instrumented.go`
- Test: `service/aiServeWeaveControlPlane/internal/handler/instrumented_test.go`

**Interfaces:**
- Produces: `metrics.Descriptions()`；`func instrumented(reg *commonmetrics.Registry, routeName string, h http.HandlerFunc) http.HandlerFunc`。`routeName` 取 go-zero `rest.Route.Path` 本身的模板字符串(如 `/operator/v1/nodes`、`/admin/v1/jobs/history/:id`)——这是路由注册时就静态已知的模板，不是运行时拼出来的原始路径，因此天然是封闭标签，不需要像 Gateway httpapi 那样做形状匹配。

- [ ] **Step 1: 写失败测试**

```go
// service/aiServeWeaveControlPlane/internal/handler/instrumented_test.go
package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	commonmetrics "AIServeWeave/common/metrics"
	"AIServeWeave/common/metrics/metricstest"
	cpmetrics "AIServeWeave/service/aiServeWeaveControlPlane/internal/metrics"
)

func TestInstrumentedRecordsRequestsByRouteTemplate(t *testing.T) {
	mx := metricstest.New()
	h := instrumented(mx, "/admin/v1/jobs/history/:id", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/admin/v1/jobs/history/job-123", nil))

	if got := mx.Sum(cpmetrics.MetricHTTPRequestsTotal, map[string]string{"route": "/admin/v1/jobs/history/:id", "status": "200"}); got != 1 {
		t.Errorf("request count = %v, want 1", got)
	}
}
```

`mx`(`metricstest.New()`)满足 `runtime.Metrics`，可直接作为 `*commonmetrics.Registry` 的替身传给 `instrumented`——`instrumented` 的第一个参数类型应为接口 `runtime.Metrics`，不是具体的 `*commonmetrics.Registry`，这样测试不需要真的建一个 Registry。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveControlPlane/internal/handler/... -run TestInstrumentedRecordsRequestsByRouteTemplate -v`
Expected: FAIL，`instrumented`/`cpmetrics` 均不存在

- [ ] **Step 3: 实现**

```go
// service/aiServeWeaveControlPlane/internal/metrics/metrics.go

// Package metrics is aiServeWeaveControlPlane's common/metrics catalogue —
// this service's first use of common/metrics, confirmed empty before this
// package (see the P08 design doc and this repo's own README known-gaps
// list).
//
// metrics 包是控制面在 common/metrics 上的指标目录——本服务首次使用
// common/metrics(此前确认为空，见 P08 设计文档与本仓库自己 README 的已知缺口
// 列表)。
package metrics

import "AIServeWeave/common/metrics"

const (
	MetricHTTPRequestsTotal        = "controlplane_http_requests_total"
	MetricHTTPRequestDurationSecs  = "controlplane_http_request_duration_seconds"
	MetricHTTPInflightRequests     = "controlplane_http_inflight_requests"
	MetricOutboxLagGeneration      = "controlplane_revocation_outbox_lag"
	MetricFleetCallsTotal          = "controlplane_fleet_calls_total"
	MetricRegistryClientCallsTotal = "controlplane_registry_client_calls_total"
)

// Descriptions returns this service's metric catalogue.
//
// Descriptions 返回本服务的指标目录。
func Descriptions() metrics.Descriptions {
	return metrics.Descriptions{
		MetricHTTPRequestsTotal:        {Kind: metrics.KindCounter, Help: "HTTP requests by route template and status. / 按路由模板与状态码分类的 HTTP 请求数。"},
		MetricHTTPRequestDurationSecs:  {Kind: metrics.KindHistogram, Help: "HTTP request duration. / HTTP 请求耗时。", Buckets: metrics.SecondsBuckets()},
		MetricHTTPInflightRequests:     {Kind: metrics.KindGauge, Help: "In-flight HTTP requests by route template. / 按路由模板分类的在途 HTTP 请求数。"},
		MetricOutboxLagGeneration:      {Kind: metrics.KindGauge, Help: "generation - delivered_generation for the revocation outbox. / 吊销 outbox 的 generation 与 delivered_generation 之差。"},
		MetricFleetCallsTotal:          {Kind: metrics.KindCounter, Help: "Fleet aggregation calls to Gateway replicas by result. / 控制面向 Gateway 副本发起的机群聚合调用，按结果分类。"},
		MetricRegistryClientCallsTotal: {Kind: metrics.KindCounter, Help: "Calls to the Registry TokenAdmin client by result. / 控制面对 Registry TokenAdmin 客户端的调用，按结果分类。"},
	}
}
```

```go
// service/aiServeWeaveControlPlane/internal/handler/instrumented.go
package handler

import (
	"net/http"
	"strconv"
	"time"

	"AIServeWeave/common/runtime"
	cpmetrics "AIServeWeave/service/aiServeWeaveControlPlane/internal/metrics"
)

// instrumented wraps h to record request count, duration and in-flight gauge
// under routeName — the route's static path template (e.g.
// "/admin/v1/jobs/history/:id"), never the raw request path, so the label
// stays closed regardless of how many distinct ids are ever requested.
//
// instrumented 包装 h，在 routeName(路由自身静态的路径模板，例如
// "/admin/v1/jobs/history/:id"，绝不是原始请求路径)下记录请求数、耗时与在途
// 量表——无论实际请求过多少个不同的 id，标签始终保持封闭。
func instrumented(reg runtime.Metrics, routeName string, h http.HandlerFunc) http.HandlerFunc {
	if reg == nil {
		return h
	}
	inflight := reg.Gauge(cpmetrics.MetricHTTPInflightRequests, map[string]string{"route": routeName})
	return func(w http.ResponseWriter, r *http.Request) {
		inflight.Set(1) // 简化实现：并发同路由请求会互相覆盖为 1，足以回答"这条路由此刻是否有在途请求"；
		defer inflight.Set(0) // 精确并发计数留给后续需要时再加

		started := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(sw, r)

		reg.Counter(cpmetrics.MetricHTTPRequestsTotal, map[string]string{"route": routeName, "status": strconv.Itoa(sw.status)}).Add(1)
		reg.Histogram(cpmetrics.MetricHTTPRequestDurationSecs, map[string]string{"route": routeName}).Observe(time.Since(started).Seconds())
	}
}

// statusRecorder captures the status code a handler writes, mirroring
// Gateway httpapi's statusWriter.
//
// statusRecorder 捕获 handler 写出的状态码，照抄 Gateway httpapi 的
// statusWriter。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}
```

在 `routes.go` 里，每一条 `rest.Route{Method: ..., Path: "<template>", Handler: <既有 handler 表达式>}` 的 `Handler` 字段外面套一层 `instrumented(ctx.MetricsRegistry, "<同一个 template>", <原表达式>)`，例如第 3 节「机群清单」那三行：

```go
{Method: http.MethodGet, Path: "/operator/v1/nodes", Handler: instrumented(ctx.MetricsRegistry, "/operator/v1/nodes", requirePlatformSession(ctx, listFleetNodes(ctx)))},
{Method: http.MethodGet, Path: "/operator/v1/models", Handler: instrumented(ctx.MetricsRegistry, "/operator/v1/models", requirePlatformSession(ctx, listFleetModels(ctx)))},
{Method: http.MethodGet, Path: "/operator/v1/workflows", Handler: instrumented(ctx.MetricsRegistry, "/operator/v1/workflows", requirePlatformSession(ctx, listOperatorWorkflows(ctx)))},
```

对 `routes.go` 里其余的每一条路由(含 `/admin/v1/*`、`/operator/v1/audit`、`/internal/v1/*` 等)做同样的机械包裹——`Path` 字段的值本身就是要传给 `instrumented` 的 `routeName`，一一对应，不需要另外设计标签。`ctx.MetricsRegistry` 由 Task 14 加到 `ServiceContext`；在 Task 14 完成前本任务先让它编译不过是预期的(下一任务补上该字段)，或者本任务里把 `instrumented` 的调用暂时留到 Task 14 一并做——**选择后者**：本任务只交付 `metrics.go`/`instrumented.go`/`instrumented_test.go`，routes.go 的改动挪到 Task 14 与 `ServiceContext` 改动一起做，避免中间状态编译不过。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveControlPlane/internal/metrics/... ./service/aiServeWeaveControlPlane/internal/handler/... -run TestInstrumented -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/metrics/ service/aiServeWeaveControlPlane/internal/handler/instrumented.go service/aiServeWeaveControlPlane/internal/handler/instrumented_test.go
git commit -m "$(cat <<'EOF'
feat(controlplane): add metrics catalogue and an HTTP instrumentation wrapper

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 14: 装配 `ServiceContext.MetricsRegistry`、`-metrics-addr`，并给全部路由套上 `instrumented`

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/svc/servicecontext.go`
- Modify: `service/aiServeWeaveControlPlane/internal/handler/routes.go`(每条路由的 `Handler` 字段外套 `instrumented(...)`)
- Modify: `service/aiServeWeaveControlPlane/main.go`

**Interfaces:**
- Consumes: Task 13 的 `cpmetrics.Descriptions()`、`instrumented`。
- Produces: `ServiceContext.MetricsRegistry *commonmetrics.Registry`(导出字段，Task 15/17/22 都要用它)。

- [ ] **Step 1: 实现 `ServiceContext` 改动**

```go
// service/aiServeWeaveControlPlane/internal/svc/servicecontext.go
// 新增 import："AIServeWeave/common/metrics" as commonmetrics（避免与本服务自己的
// internal/metrics 包重名）、本服务的 "AIServeWeave/service/aiServeWeaveControlPlane/internal/metrics" as cpmetrics

// ServiceContext 结构体新增导出字段(与既有 Fleet/RegistryClient 并列)：
	MetricsRegistry *commonmetrics.Registry

// NewServiceContext 内，尽量早的位置(不依赖数据库/Redis，其它组件都可以往里记)：
	metricsRegistry := commonmetrics.New(cpmetrics.Descriptions())
	// ...
	// 在最终构造并返回 &ServiceContext{...} 的字面量里加一行：
	//   MetricsRegistry: metricsRegistry,
```

- [ ] **Step 2: 实现 `routes.go` 改动**

对 `routes.go` 文件里**每一条** `rest.Route{...}` 字面量，把 `Handler:` 的值从 `<原表达式>` 改为 `instrumented(ctx.MetricsRegistry, "<Path 字段同一个字符串>", <原表达式>)`。例如：

```go
{Method: http.MethodGet, Path: "/operator/v1/audit", Handler: instrumented(ctx.MetricsRegistry, "/operator/v1/audit", requirePlatformSession(ctx, listOperatorAudit(ctx)))},
```

这是纯机械改动——`routeName` 参数与同一行的 `Path` 字段字面量相同，逐条对照复制即可，不需要新设计。

- [ ] **Step 3: 实现 `main.go` 改动**

```go
// service/aiServeWeaveControlPlane/main.go
// 新增 import："errors"、"net/http"

// run() 里，flag.Parse() 之后追加：
	metricsAddr := flag.String("metrics-addr", "127.0.0.1:9090",
		"address the Prometheus /metrics listener binds; loopback by default, empty disables it")
	// flag.Parse() 需要移到全部 flag.XXX(...) 声明之后，若尚未如此

// svcCtx 构造成功之后(main.go:96-99 之后)：
	var metricsServer *http.Server
	if *metricsAddr == "" {
		logger.Warn("no -metrics-addr; this replica exports no metrics")
	} else {
		metricsServer = svcCtx.MetricsRegistry.Server(*metricsAddr)
		go func() {
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("metrics listener stopped", slog.Any("error", err))
			}
		}()
		logger.Info("metrics listening", slog.String("metrics_addr", *metricsAddr))
	}

// 在既有的 defer server.Stop() 之后再加一个 defer(顺序上后加的先执行，因此写在它之后
// 会让指标监听器先于 REST 监听器关闭；照抄 Gateway"指标最后关"的顺序，改为写在
// defer server.Stop() 之前)：
	defer func() {
		if metricsServer != nil {
			_ = metricsServer.Close()
		}
	}()
```

- [ ] **Step 4: 跑测试确认通过**

Run:
```bash
go build ./service/aiServeWeaveControlPlane/...
go vet ./service/aiServeWeaveControlPlane/...
go test ./service/aiServeWeaveControlPlane/... -v
```
Expected: 全部通过；既有 handler 测试若直接构造 `http.HandlerFunc` 而绕过 `routes.go` 的路由表则不受影响，若有测试直接断言 `routes.go` 返回的 `[]rest.Route` 数量/形状，确认改动没有增删路由条目本身(只是包了一层)

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/svc/servicecontext.go service/aiServeWeaveControlPlane/internal/handler/routes.go service/aiServeWeaveControlPlane/main.go
git commit -m "$(cat <<'EOF'
feat(controlplane): wire the metrics registry through every route and add -metrics-addr

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 15: outbox 滞后量、Fleet/Registry 客户端调用计数与基数测试

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/revocationoutbox/`(具体文件以 `grep -rn "delivered_generation" service/aiServeWeaveControlPlane/internal/revocationoutbox/` 找到的实现文件为准)
- Modify: `service/aiServeWeaveControlPlane/internal/handler/handlers.go`(`listFleetNodes`/`listFleetModels`/`listOperatorWorkflows` 等 Fleet 调用点；Registry 转发调用点，如 `ApproveNode` 一类 `/operator/v1/nodes/:id/approve` 背后的 handler)
- Test: 对应包内新增用例
- Create: `service/aiServeWeaveControlPlane/internal/metrics/metrics_cardinality_test.go`

**Interfaces:**
- Consumes: Task 13 的 `cpmetrics.MetricOutboxLagGeneration`/`MetricFleetCallsTotal`/`MetricRegistryClientCallsTotal`；Task 14 的 `ctx.MetricsRegistry`。

- [ ] **Step 1: outbox 滞后量——写失败测试**

先 `grep -rn "delivered_generation\|generation" service/aiServeWeaveControlPlane/internal/revocationoutbox/*.go` 找到当前读取这两个字段的方法(P07 已实现，必然存在一个"发送前锁行读取 generation/delivered_generation"的方法)。在其所在文件同目录加一个新的、独立的只读方法(不影响既有发送逻辑)：

```go
// 追加到 revocationoutbox 包内某个 _test.go 文件
func TestGaugeReflectsLag(t *testing.T) {
	// 用本包已有的测试数据库/内存夹具，写入 generation=5, delivered_generation=3
	mx := metricstest.New()
	// ...调用本任务 Step 2 新增的 RefreshLagGauge(ctx, mx) 或等价方法...
	if got := mx.Sum(cpmetrics.MetricOutboxLagGeneration, nil); got != 2 {
		t.Errorf("lag gauge = %v, want 2", got)
	}
}
```

- [ ] **Step 2: outbox 滞后量——实现**

在 `revocationoutbox` 包新增一个只读方法(签名对齐该包现有的 store 访问方式，例如若现有发送逻辑是 `func (o *Outbox) pending(ctx context.Context) (generation, delivered int64, err error)` 这一类，直接复用它，不必新增查询)：

```go
// RefreshLagGauge sets the outbox lag gauge to generation - delivered_generation.
// Call it once per send attempt, alongside the existing send loop, so the gauge
// reflects the same read the sender just made rather than a separate query.
//
// RefreshLagGauge 把 outbox 滞后量表设为 generation - delivered_generation。
// 应在每次发送尝试时、与既有发送循环同一处调用，让量表反映发送方刚做的同一次
// 读取，而不是另开一次查询。
func (o *Outbox) RefreshLagGauge(mx runtime.Metrics) {
	// 复用既有发送循环里已经读到的 generation/delivered_generation 局部变量，
	// 在该处紧邻既有发送/重试逻辑之后插入：
	mx.Gauge(cpmetrics.MetricOutboxLagGeneration, nil).Set(float64(generation - delivered))
}
```

`ServiceContext` 构造 outbox 的 relay/发送器时，把 `metricsRegistry` 传进去(现有构造函数增加一个可选参数或字段，nil 安全)。

- [ ] **Step 3: Fleet/Registry 客户端调用计数——实现**

在 `handlers.go` 里每个直接调用 `ctx.Fleet.Xxx(...)` 或 Registry 转发方法的 handler，返回处按 `err == nil` 记两值分类(先用最简单、必然正确的两值分类；不去猜 Fleet/Registry 内部具体的错误分类词汇表，避免引入不确定的标签值)：

```go
// 以 listFleetNodes 为例，其余 Fleet/Registry 调用点做同样的包裹：
func listFleetNodes(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snapshot, err := ctx.Fleet.Nodes(r.Context())
		result := "success"
		if err != nil {
			result = "error"
		}
		if ctx.MetricsRegistry != nil {
			ctx.MetricsRegistry.Counter(cpmetrics.MetricFleetCallsTotal, map[string]string{"result": result}).Add(1)
		}
		if err != nil {
			respondFleetErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}
}
```

- [ ] **Step 4: 基数测试**

```go
// service/aiServeWeaveControlPlane/internal/metrics/metrics_cardinality_test.go
package metrics_test

import (
	"testing"

	"AIServeWeave/common/metrics/metricstest"
	cpmetrics "AIServeWeave/service/aiServeWeaveControlPlane/internal/metrics"
)

func TestMetricLabelValuesAreBounded(t *testing.T) {
	allowed := map[string]map[string]bool{
		"result": {"success": true, "error": true},
	}
	mx := metricstest.New()
	// 驱动一次会命中 listFleetNodes 等 handler 的调用，或直接对 mx 手工记录两条
	// 覆盖 success/error 的样本，确认标签取值不出这张表。
	for _, s := range mx.All() {
		for key, value := range s.Labels {
			if key == "route" || key == "status" {
				continue // instrumented() 的 route/status 标签取值来自 routes.go 的静态模板与 HTTP 状态码，天然有界，不在本表校验范围
			}
			set, checked := allowed[key]
			if !checked {
				continue
			}
			if !set[value] {
				t.Errorf("metric %s label %s = %q, which is not a bounded value", s.Name, key, value)
			}
		}
	}
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveControlPlane/... -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/revocationoutbox/ service/aiServeWeaveControlPlane/internal/handler/handlers.go service/aiServeWeaveControlPlane/internal/metrics/metrics_cardinality_test.go
git commit -m "$(cat <<'EOF'
feat(controlplane): record outbox lag and Fleet/Registry client call outcomes

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 16: `MetricsHistoryConf` 配置块

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/config/config.go`
- Test: 在既有的 config 测试文件中新增用例(以 `grep -rln "TestConfig\|func TestValidate" service/aiServeWeaveControlPlane/internal/config/` 找到的文件为准)

**Interfaces:**
- Produces: `type MetricsHistoryConf struct{ GatewayAddrs []string; RegistryAddr string; Interval time.Duration; Retention time.Duration }`，`func (m MetricsHistoryConf) Enabled() bool`。Task 19/21 据此构造采集器；Task 17 的迁移账本注册与本任务无关(始终注册，见 Task 17)。

采集周期与落库粒度合并成一个 `Interval`(默认 5 分钟)，而不是分开的"抓取间隔"与"汇总粒度"：Gateway/Registry 的计数器与量表都已经是累计值，每个 bucket 只需要在窗口收尾时抓一次当前值、跨副本求和后落一行——抓得更频繁不会提高准确度，只会白白增加请求次数。这比 spec 里"默认 60s 抓取"的设想更简单且同样正确，在文档回填时(Task 27)一并说明这处简化。

- [ ] **Step 1: 写失败测试**

```go
// 追加到 config 包既有测试文件
func TestMetricsHistoryConfValidation(t *testing.T) {
	cfg := baseValidConfig(t) // 复用本文件已有的"构造一份合法 Config"辅助函数
	cfg.MetricsHistory = MetricsHistoryConf{RegistryAddr: "http://registry:9090"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a MetricsHistory with only RegistryAddr set", err)
	}

	cfg.MetricsHistory.Interval = -time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error for a negative Interval")
	}
}

func TestMetricsHistoryConfDisabledByDefault(t *testing.T) {
	var m MetricsHistoryConf
	if m.Enabled() {
		t.Error("Enabled() = true, want false for the zero value")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveControlPlane/internal/config/... -run TestMetricsHistoryConf -v`
Expected: FAIL，`MetricsHistoryConf` 不存在

- [ ] **Step 3: 实现**

```go
// service/aiServeWeaveControlPlane/internal/config/config.go

// MetricsHistoryConf configures periodic scraping of Gateway/Registry
// Prometheus endpoints into rollup history for Console C27. Unconfigured (the
// zero value), the background collector does not start and
// GET /operator/v1/metrics/history is not mounted — the same "no config, no
// route" convention Fleet and Registry already follow.
//
// MetricsHistoryConf 配置定时抓取 Gateway/Registry 的 Prometheus 端点、为
// Console C27 落地汇总历史。未配置时(零值)后台采集器不启动，
// GET /operator/v1/metrics/history 也不挂载——与 Fleet、Registry 现有的
// "没配置就没有这条路由"约定一致。
type MetricsHistoryConf struct {
	// GatewayAddrs are Gateway replicas' -metrics-addr endpoints, e.g.
	// "http://gateway-1:9090". Distinct from Fleet.Gateways, which points at
	// adminapi on a different port.
	// GatewayAddrs 是各 Gateway 副本 -metrics-addr 的端点，例如
	// "http://gateway-1:9090"。与 Fleet.Gateways 不同——后者指向 adminapi，
	// 端口不同。
	GatewayAddrs []string `json:",optional"`
	// RegistryAddr is the Registry's -metrics-addr endpoint.
	// RegistryAddr 是 Registry 的 -metrics-addr 端点。
	RegistryAddr string `json:",optional"`
	// Interval is both the scrape cadence and the rollup bucket width.
	// Interval 既是抓取周期，也是落库的汇总粒度。
	Interval time.Duration `json:",default=5m"`
	// Retention bounds how long a rollup row is kept before cleanup deletes it.
	// Retention 限制一行汇总数据在被清理任务删除前保留多久。
	Retention time.Duration `json:",default=2160h"`
}

// Enabled reports whether the metrics-history collector is configured.
//
// Enabled 报告指标历史采集器是否已配置。
func (m MetricsHistoryConf) Enabled() bool {
	return len(m.GatewayAddrs) > 0 || m.RegistryAddr != ""
}

// Config 结构体新增字段(与既有 Fleet/Registry 并列)：
	MetricsHistory MetricsHistoryConf `json:",optional"`
```

`Validate()` 内，紧邻既有 `Fleet`/`Registry` 校验块之后：

```go
if c.MetricsHistory.Enabled() {
	if c.MetricsHistory.Interval <= 0 {
		return errors.New("config: MetricsHistory.Interval must be positive once metrics history is configured")
	}
	if c.MetricsHistory.Retention <= 0 {
		return errors.New("config: MetricsHistory.Retention must be positive once metrics history is configured")
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveControlPlane/internal/config/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/config/config.go
git commit -m "$(cat <<'EOF'
feat(controlplane): add MetricsHistoryConf

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 17: `metrics_history_points` 表的固定版本 SQL 迁移

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/migrations/metricshistory/postgres/0001_metrics_history_points.sql`
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/migrations/metricshistory/mysql/0001_metrics_history_points.sql`
- Modify: `service/aiServeWeaveControlPlane/internal/store/gormstore/migrate.go`(`migrationNamespaces`)
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/metricshistorymigrate.go`
- Test: `service/aiServeWeaveControlPlane/internal/store/gormstore/metricshistorymigrate_live_test.go`

**Interfaces:**
- Produces: `func (s *Store) MigrateMetricsHistory(ctx context.Context) ([]string, error)`(镜像 `routemigrate.go`)；新命名空间 `"metrics_history"` 加入 `migrationNamespaces`，因此也会被 `MigrateAll`/`CheckSchema` 自动覆盖，落到迁移账本表 `schema_migrations_metrics_history`。

- [ ] **Step 1: 写 SQL**

`migrations/metricshistory/postgres/0001_metrics_history_points.sql`(必须自身幂等——本命名空间不落在 `migrate.go` 的 `"base"`/`"jobs"` 特殊幂等分支里)：

```sql
CREATE TABLE IF NOT EXISTS metrics_history_points (
 id BIGSERIAL PRIMARY KEY,
 metric VARCHAR(128) NOT NULL,
 labels VARCHAR(512) NOT NULL,
 bucket_at TIMESTAMPTZ NOT NULL,
 value DOUBLE PRECISION NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_metrics_history_points_series ON metrics_history_points (metric, labels, bucket_at);
CREATE INDEX IF NOT EXISTS idx_metrics_history_points_bucket ON metrics_history_points (bucket_at);
```

`migrations/metricshistory/mysql/0001_metrics_history_points.sql`(MySQL 的 `CREATE INDEX IF NOT EXISTS` 不被通用支持，唯一索引改为随建表一次声明；实现前先读一份既有 `migrations/*/mysql/*.sql` 文件确认本仓库 MySQL 建表的实际写法并对齐)：

```sql
CREATE TABLE IF NOT EXISTS metrics_history_points (
 id BIGINT AUTO_INCREMENT PRIMARY KEY,
 metric VARCHAR(128) NOT NULL,
 labels VARCHAR(512) NOT NULL,
 bucket_at DATETIME NOT NULL,
 value DOUBLE NOT NULL,
 UNIQUE KEY idx_metrics_history_points_series (metric, labels, bucket_at),
 KEY idx_metrics_history_points_bucket (bucket_at)
);
```

`labels` 存 Task 19 产出的规范化字符串(标签按 key 排序后 `k1=v1,k2=v2` 拼接，空标签为空串)，不是 JSON——查询侧只需要整串相等匹配，不需要解析。

- [ ] **Step 2: 注册命名空间**

```go
// service/aiServeWeaveControlPlane/internal/store/gormstore/migrate.go
// migrationNamespaces(dialect string) 内：
func migrationNamespaces(dialect string) []string {
	groups := []string{"base", "routes", "workflow_templates", "metrics_history"}
	if dialect == "mysql" {
		groups = append(groups, "jobs")
	}
	return groups
}
```

`"metrics_history"` 不落在 `loadMigrations` 现有的 `"jobs"`/`"workflow_templates"` 特判分支里，会自动走默认的 `migrations/metrics_history/<dialect>` 路径——**注意目录名用下划线 `metricshistory` 还是 `metrics_history` 必须与 `loadMigrations` 默认分支拼接的路径完全一致**：先读 `loadMigrations` 的默认分支拼接逻辑(`"migrations/" + namespace + "/" + dialect"` 或类似)，确认命名空间字符串 `"metrics_history"` 与磁盘目录名逐字一致，若默认分支直接用 `namespace` 拼目录名，则本任务 Step 1 的两个 SQL 文件目录应改名为 `migrations/metrics_history/{postgres,mysql}/`(下划线，不是 Step 1 里写的 `metricshistory`)——以 `migrate.go` 里 `loadMigrations` 的实际拼接代码为准，两处必须字面一致。

- [ ] **Step 3: `metricshistorymigrate.go`**

```go
// service/aiServeWeaveControlPlane/internal/store/gormstore/metricshistorymigrate.go
package gormstore

import "context"

// MigrateMetricsHistory applies versioned metrics_history SQL under the
// shared migration lock.
//
// MigrateMetricsHistory 在共享迁移锁下应用 metrics_history 的版本化 SQL。
func (s *Store) MigrateMetricsHistory(ctx context.Context) ([]string, error) {
	return s.migrateOne(ctx, "metrics_history")
}
```

- [ ] **Step 4: 写真实数据库迁移测试**

```go
// service/aiServeWeaveControlPlane/internal/store/gormstore/metricshistorymigrate_live_test.go
package gormstore_test

import (
	"context"
	"testing"
)

func TestLiveMetricsHistoryMigrationIsRepeatable(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if _, err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() first run error = %v", err)
		}
		if applied, err := st.MigrateAll(ctx, false); err != nil || len(applied) != 0 {
			t.Fatalf("MigrateAll() second run = %v, %v, want no-op", applied, err)
		}
		if !db.Migrator().HasTable("metrics_history_points") {
			t.Fatal("metrics_history_points table was not created")
		}
	})
}
```

(`migrationDatabases` 是 `basemigrate_live_test.go` 已有的辅助函数，按 `AISW_POSTGRES_TEST_DSN`/`AISW_MYSQL_TEST_DSN` 门控，未设置时 `t.Skip`。)

- [ ] **Step 5: 跑测试确认通过**

Run:
```bash
go test ./service/aiServeWeaveControlPlane/internal/store/gormstore/... -v   # 默认跳过 live 用例
AISW_POSTGRES_TEST_DSN=... AISW_MYSQL_TEST_DSN=... go test ./service/aiServeWeaveControlPlane/internal/store/gormstore/... -run TestLiveMetricsHistory -v
```
Expected: PASS(默认门禁不需要真实数据库；有 DSN 时真实建表通过)

- [ ] **Step 6: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/store/gormstore/
git commit -m "$(cat <<'EOF'
feat(controlplane): add versioned migration for metrics_history_points

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 18: `model.MetricsHistoryPoint` + `store.MetricsHistory` + gormstore 实现

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/model/metricshistory.go`
- Create: `service/aiServeWeaveControlPlane/internal/store/metricshistory.go`
- Create: `service/aiServeWeaveControlPlane/internal/store/gormstore/metricshistory.go`
- Test: `service/aiServeWeaveControlPlane/internal/store/gormstore/metricshistory_live_test.go`

**Interfaces:**
- Produces: `model.MetricsHistoryPoint{Metric, Labels string; BucketAt time.Time; Value float64}`；`store.MetricsHistory` 接口，方法 `UpsertRollup(ctx, points []model.MetricsHistoryPoint) error`、`ListRollup(ctx, metrics []string, since, until time.Time) ([]model.MetricsHistoryPoint, error)`、`DeleteRollupBefore(ctx, before time.Time) (int64, error)`；三者均由 `gormstore.Store` 实现，并入 `store.go` 的组合接口 `Store`。
- Consumes: Task 17 的 `metrics_history_points` 表。

- [ ] **Step 1: 模型**

```go
// service/aiServeWeaveControlPlane/internal/model/metricshistory.go
package model

import "time"

// MetricsHistoryPoint is one rolled-up (metric, labels, bucket) sample —
// Labels is a canonical "k1=v1,k2=v2" string (keys sorted), not JSON: every
// read is an exact-match filter, never a partial-label query, so there is
// nothing a JSON column would buy here.
//
// MetricsHistoryPoint 是一个 (metric, labels, bucket) 的汇总样本——Labels 是
// 规范化的 "k1=v1,k2=v2" 字符串(key 已排序)，不是 JSON：每次读取都是精确匹配，
// 从不做标签的部分查询，JSON 列在这里买不到任何好处。
type MetricsHistoryPoint struct {
	ID       int64     `gorm:"primaryKey;autoIncrement"`
	Metric   string    `gorm:"column:metric"`
	Labels   string    `gorm:"column:labels"`
	BucketAt time.Time `gorm:"column:bucket_at"`
	Value    float64   `gorm:"column:value"`
}

func (MetricsHistoryPoint) TableName() string { return "metrics_history_points" }
```

- [ ] **Step 2: 窄接口**

```go
// service/aiServeWeaveControlPlane/internal/store/metricshistory.go
package store

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// MetricsHistory persists rolled-up metric samples for Console C27 and
// prunes them past retention.
//
// MetricsHistory 为 Console C27 持久化汇总后的指标样本，并在超出保留期后清理。
type MetricsHistory interface {
	// UpsertRollup writes points, replacing any existing row with the same
	// (metric, labels, bucket_at) — a collector restart mid-bucket must not
	// produce a duplicate row for a bucket it already wrote.
	// UpsertRollup 写入 points，遇到相同 (metric, labels, bucket_at) 的既有行
	// 直接替换——采集器在某个 bucket 中途重启，不应为它已写过的 bucket 产生
	// 重复行。
	UpsertRollup(ctx context.Context, points []model.MetricsHistoryPoint) error
	// ListRollup returns points for any of metrics with bucket_at in
	// [since, until), ordered by bucket_at ascending.
	// ListRollup 返回 metrics 中任意一个、且 bucket_at 落在 [since, until) 的
	// 全部样本，按 bucket_at 升序排列。
	ListRollup(ctx context.Context, metrics []string, since, until time.Time) ([]model.MetricsHistoryPoint, error)
	// DeleteRollupBefore deletes every row with bucket_at < before and
	// reports how many rows it removed.
	// DeleteRollupBefore 删除全部 bucket_at < before 的行，并报告删除行数。
	DeleteRollupBefore(ctx context.Context, before time.Time) (int64, error)
}
```

`store.go` 的组合接口 `Store`(`store.go:515-525`)追加一行 `MetricsHistory`。

- [ ] **Step 3: gormstore 实现——写失败测试**

```go
// service/aiServeWeaveControlPlane/internal/store/gormstore/metricshistory_live_test.go
package gormstore_test

func TestLiveMetricsHistoryUpsertIsIdempotentPerBucket(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if _, err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}
		bucket := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		point := model.MetricsHistoryPoint{Metric: "gateway_http_requests_total", Labels: "endpoint=chat,status=200", BucketAt: bucket, Value: 10}

		if err := st.UpsertRollup(ctx, []model.MetricsHistoryPoint{point}); err != nil {
			t.Fatalf("UpsertRollup() first write error = %v", err)
		}
		point.Value = 15 // 模拟同一个 bucket 的第二次采集，值已增长
		if err := st.UpsertRollup(ctx, []model.MetricsHistoryPoint{point}); err != nil {
			t.Fatalf("UpsertRollup() second write error = %v", err)
		}

		got, err := st.ListRollup(ctx, []string{"gateway_http_requests_total"}, bucket.Add(-time.Minute), bucket.Add(time.Minute))
		if err != nil {
			t.Fatalf("ListRollup() error = %v", err)
		}
		if len(got) != 1 || got[0].Value != 15 {
			t.Fatalf("ListRollup() = %+v, want exactly one row with the updated value 15", got)
		}
	})
}

func TestLiveMetricsHistoryDeleteRollupBefore(t *testing.T) {
	migrationDatabases(t, func(t *testing.T, db *gorm.DB, dialect string) {
		st := gormstore.New(db)
		ctx := context.Background()
		if _, err := st.MigrateAll(ctx, false); err != nil {
			t.Fatalf("MigrateAll() error = %v", err)
		}
		old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		recent := time.Now().UTC()
		if err := st.UpsertRollup(ctx, []model.MetricsHistoryPoint{
			{Metric: "m", Labels: "", BucketAt: old, Value: 1},
			{Metric: "m", Labels: "", BucketAt: recent, Value: 2},
		}); err != nil {
			t.Fatalf("UpsertRollup() error = %v", err)
		}

		n, err := st.DeleteRollupBefore(ctx, recent.Add(-time.Hour))
		if err != nil {
			t.Fatalf("DeleteRollupBefore() error = %v", err)
		}
		if n != 1 {
			t.Fatalf("DeleteRollupBefore() removed %d rows, want 1", n)
		}
	})
}
```

- [ ] **Step 4: 实现**

```go
// service/aiServeWeaveControlPlane/internal/store/gormstore/metricshistory.go
package gormstore

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

func (s *Store) UpsertRollup(ctx context.Context, points []model.MetricsHistoryPoint) error {
	if len(points) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "metric"}, {Name: "labels"}, {Name: "bucket_at"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&points).Error
}

func (s *Store) ListRollup(ctx context.Context, metrics []string, since, until time.Time) ([]model.MetricsHistoryPoint, error) {
	var points []model.MetricsHistoryPoint
	err := s.db.WithContext(ctx).
		Where("metric IN ? AND bucket_at >= ? AND bucket_at < ?", metrics, since, until).
		Order("bucket_at ASC").
		Find(&points).Error
	return points, err
}

func (s *Store) DeleteRollupBefore(ctx context.Context, before time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Where("bucket_at < ?", before).Delete(&model.MetricsHistoryPoint{})
	return res.RowsAffected, res.Error
}
```

`clause.OnConflict` 依赖 `metrics_history_points` 在 `(metric, labels, bucket_at)` 上的唯一索引(Task 17 已建)；PostgreSQL 与 MySQL 的 gorm 驱动都支持这个子句形式，不需要按方言分叉。

- [ ] **Step 5: 跑测试确认通过**

Run:
```bash
go build ./service/aiServeWeaveControlPlane/...
AISW_POSTGRES_TEST_DSN=... AISW_MYSQL_TEST_DSN=... go test ./service/aiServeWeaveControlPlane/internal/store/gormstore/... -run TestLiveMetricsHistory -v
```
Expected: PASS，两个引擎各跑一遍

- [ ] **Step 6: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/model/metricshistory.go service/aiServeWeaveControlPlane/internal/store/metricshistory.go service/aiServeWeaveControlPlane/internal/store/gormstore/metricshistory.go service/aiServeWeaveControlPlane/internal/store/gormstore/metricshistory_live_test.go service/aiServeWeaveControlPlane/internal/store/store.go
git commit -m "$(cat <<'EOF'
feat(controlplane): add metrics-history store interface and gorm implementation

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 19: `internal/metricshistory` 采集器

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/metricshistory/collector.go`
- Test: `service/aiServeWeaveControlPlane/internal/metricshistory/collector_test.go`

**Interfaces:**
- Consumes: `common/metrics.ParseExposition`(Task 1)；`store.MetricsHistory`(Task 18)。
- Produces: `type Config struct{ GatewayAddrs []string; RegistryAddr string; Interval time.Duration; Store store.MetricsHistory; Client HTTPDoer; Clock runtime.Clock; Logger *slog.Logger }`；`func New(cfg Config) *Collector`；`func (c *Collector) Run(ctx context.Context)`；`func (c *Collector) CollectOnce(ctx context.Context, at time.Time) error`(导出，既供 `Run` 内部调用，也供测试直接驱动一次采集而不依赖真实 ticker)。

- [ ] **Step 1: 写失败测试**

```go
// service/aiServeWeaveControlPlane/internal/metricshistory/collector_test.go
package metricshistory_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/metricshistory"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// fakeDoer returns a fixed Prometheus text body for every request, keyed by URL.
type fakeDoer struct{ bodies map[string]string }

func (f fakeDoer) Do(req *http.Request) (*http.Response, error) {
	body, ok := f.bodies[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// fakeStore records every UpsertRollup call for assertions.
type fakeStore struct{ upserts [][]model.MetricsHistoryPoint }

func (f *fakeStore) UpsertRollup(ctx context.Context, points []model.MetricsHistoryPoint) error {
	f.upserts = append(f.upserts, points)
	return nil
}
func (f *fakeStore) ListRollup(context.Context, []string, time.Time, time.Time) ([]model.MetricsHistoryPoint, error) {
	return nil, nil
}
func (f *fakeStore) DeleteRollupBefore(context.Context, time.Time) (int64, error) { return 0, nil }

func TestCollectOnceSumsAcrossReplicasAndDropsNodeID(t *testing.T) {
	doer := fakeDoer{bodies: map[string]string{
		"http://gw-1:9090/metrics": "gateway_http_requests_total{endpoint=\"chat\",status=\"200\"} 10\n" +
			"tunnel_server_slots_total{node_id=\"n1\",class=\"chat\",state=\"idle\"} 3\n",
		"http://gw-2:9090/metrics": "gateway_http_requests_total{endpoint=\"chat\",status=\"200\"} 7\n" +
			"tunnel_server_slots_total{node_id=\"n2\",class=\"chat\",state=\"idle\"} 5\n",
	}}
	store := &fakeStore{}
	c := metricshistory.New(metricshistory.Config{
		GatewayAddrs: []string{"http://gw-1:9090", "http://gw-2:9090"},
		Interval:     5 * time.Minute,
		Store:        store,
		Client:       doer,
	})

	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	if err := c.CollectOnce(context.Background(), at); err != nil {
		t.Fatalf("CollectOnce() error = %v", err)
	}

	if len(store.upserts) != 1 {
		t.Fatalf("UpsertRollup called %d times, want 1", len(store.upserts))
	}
	points := store.upserts[0]
	var gotRequests, gotSlots bool
	for _, p := range points {
		if p.Metric == "gateway_http_requests_total" && p.Value == 17 {
			gotRequests = true
		}
		if p.Metric == "tunnel_server_slots_total" && p.Value == 8 {
			gotSlots = true
			if strings.Contains(p.Labels, "node_id") {
				t.Errorf("tunnel_server_slots_total labels = %q, want node_id dropped", p.Labels)
			}
		}
		if p.BucketAt != at {
			t.Errorf("point %+v BucketAt = %v, want %v", p, p.BucketAt, at)
		}
	}
	if !gotRequests {
		t.Error("did not find gateway_http_requests_total summed to 17 across both replicas")
	}
	if !gotSlots {
		t.Error("did not find tunnel_server_slots_total summed to 8 with node_id rolled up away")
	}
}

func TestCollectOneUnreachableSourceStillWritesTheRest(t *testing.T) {
	doer := fakeDoer{bodies: map[string]string{
		"http://gw-1:9090/metrics": "gateway_http_requests_total{endpoint=\"chat\",status=\"200\"} 4\n",
	}}
	store := &fakeStore{}
	c := metricshistory.New(metricshistory.Config{
		GatewayAddrs: []string{"http://gw-1:9090", "http://gw-down:9090"},
		Interval:     5 * time.Minute,
		Store:        store,
		Client:       doer,
	})

	if err := c.CollectOnce(context.Background(), time.Now()); err != nil {
		t.Fatalf("CollectOnce() error = %v, want a partial scrape failure to not fail the whole collection", err)
	}
	if len(store.upserts) != 1 || len(store.upserts[0]) == 0 {
		t.Fatal("expected the reachable replica's samples to still be written")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveControlPlane/internal/metricshistory/... -v`
Expected: FAIL，包不存在

- [ ] **Step 3: 实现**

```go
// service/aiServeWeaveControlPlane/internal/metricshistory/collector.go

// Package metricshistory periodically scrapes Gateway and Registry
// Prometheus text endpoints, sums each series it cares about across
// replicas, drops labels not worth keeping at history granularity (most
// notably node_id — see seriesKeepLabels), and upserts one row per
// (metric, labels, bucket) into store.MetricsHistory.
//
// metricshistory 包定时抓取 Gateway 与 Registry 的 Prometheus 文本端点，把它
// 关心的每条序列跨副本求和，丢弃在历史粒度下不值得保留的标签(最主要是
// node_id——见 seriesKeepLabels)，把每个 (metric, labels, bucket) 各写一行进
// store.MetricsHistory。
package metricshistory

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// seriesKeepLabels declares, per metric name this collector rolls up, which
// label keys survive into history; everything else is summed away. This is
// what keeps history cardinality bounded regardless of fleet size.
//
// seriesKeepLabels 按指标名声明哪些标签键会保留进历史；其余全部被求和抹去。
// 这正是不论机群规模多大，历史数据基数始终有界的原因。
var seriesKeepLabels = map[string][]string{
	"gateway_http_requests_total":                    {"endpoint", "status"},
	"gateway_http_request_duration_seconds_bucket":    {"endpoint", "le"},
	"gateway_http_request_duration_seconds_sum":       {"endpoint"},
	"gateway_http_request_duration_seconds_count":     {"endpoint"},
	"gateway_tokens_total":                            {"direction"},
	"tunnel_server_slots_total":                       {"class", "state"},
}

// HTTPDoer is the subset of *http.Client the collector needs, so tests can
// substitute a fixed set of responses instead of a real network.
//
// HTTPDoer 是采集器需要的 *http.Client 子集，测试可以用一组固定响应替代真实网络。
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Store is the persistence surface the collector writes to.
//
// Store 是采集器写入的持久化接口。
type Store interface {
	UpsertRollup(ctx context.Context, points []model.MetricsHistoryPoint) error
}

// Config configures a Collector.
//
// Config 配置一个 Collector。
type Config struct {
	GatewayAddrs []string
	RegistryAddr string
	Interval     time.Duration
	Store        Store
	Client       HTTPDoer
	Clock        runtime.Clock
	Logger       *slog.Logger
}

// Collector periodically scrapes and rolls up metrics history.
//
// Collector 定时抓取并汇总指标历史。
type Collector struct {
	sources  []string
	interval time.Duration
	store    Store
	client   HTTPDoer
	clock    runtime.Clock
	logger   *slog.Logger
}

// New builds a Collector from cfg.
//
// New 用 cfg 构造一个 Collector。
func New(cfg Config) *Collector {
	sources := append([]string{}, cfg.GatewayAddrs...)
	if cfg.RegistryAddr != "" {
		sources = append(sources, cfg.RegistryAddr)
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Collector{sources: sources, interval: cfg.Interval, store: cfg.Store, client: client, clock: clock, logger: logger}
}

// Run collects once per Interval until ctx is done.
//
// Run 每隔一个 Interval 采集一次，直到 ctx 结束。
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			at := c.clock.Now().UTC().Truncate(c.interval)
			if err := c.CollectOnce(ctx, at); err != nil {
				c.logger.Error("metrics history collection failed", slog.Any("error", err))
			}
		}
	}
}

// CollectOnce scrapes every configured source, aggregates, and upserts one
// rollup row per surviving series for bucket at. A source that cannot be
// scraped is logged and skipped — a down replica must not blank out the
// history of every replica that answered.
//
// CollectOnce 抓取全部已配置的来源，聚合后为每条存活下来的序列写入一行以 at
// 为 bucket 的汇总数据。抓不到的来源只记日志并跳过——一个宕掉的副本不应该把
// 全部应答正常的副本历史一起抹平。
func (c *Collector) CollectOnce(ctx context.Context, at time.Time) error {
	type key struct{ metric, labels string }
	sums := map[key]float64{}

	for _, addr := range c.sources {
		samples, err := c.scrape(ctx, addr)
		if err != nil {
			c.logger.Warn("scrape failed", slog.String("addr", addr), slog.Any("error", err))
			continue
		}
		for _, s := range samples {
			keep, ok := seriesKeepLabels[s.Name]
			if !ok {
				continue
			}
			k := key{metric: s.Name, labels: canonicalLabels(s.Labels, keep)}
			sums[k] += s.Value
		}
	}

	points := make([]model.MetricsHistoryPoint, 0, len(sums))
	for k, v := range sums {
		points = append(points, model.MetricsHistoryPoint{Metric: k.metric, Labels: k.labels, BucketAt: at, Value: v})
	}
	if len(points) == 0 {
		return nil
	}
	return c.store.UpsertRollup(ctx, points)
}

func (c *Collector) scrape(ctx context.Context, addr string) ([]metrics.Sample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metricshistory: %s returned status %d", addr, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return metrics.ParseExposition(strings.NewReader(string(body)))
}

// canonicalLabels renders only the keys in keep, sorted, as "k1=v1,k2=v2".
//
// canonicalLabels 只渲染 keep 里的键，排序后拼成 "k1=v1,k2=v2"。
func canonicalLabels(labels map[string]string, keep []string) string {
	kept := make([]string, 0, len(keep))
	for _, k := range keep {
		if v, ok := labels[k]; ok {
			kept = append(kept, k+"="+v)
		}
	}
	sort.Strings(kept)
	return strings.Join(kept, ",")
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveControlPlane/internal/metricshistory/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/metricshistory/
git commit -m "$(cat <<'EOF'
feat(controlplane): add the metrics-history collector

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 20: 保留期清理

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/metricshistory/retention.go`
- Test: `service/aiServeWeaveControlPlane/internal/metricshistory/retention_test.go`

**Interfaces:**
- Consumes: `store.MetricsHistory.DeleteRollupBefore`(Task 18)。
- Produces: `type Retention struct{...}`、`func NewRetention(store Store2, retention time.Duration, clock runtime.Clock, logger *slog.Logger) *Retention`、`func (r *Retention) Run(ctx context.Context, interval time.Duration)`。清理周期与 `MetricsHistoryConf.Interval` 无关，固定按天跑一次即可(不需要新增配置项)。

- [ ] **Step 1: 写失败测试**

```go
// service/aiServeWeaveControlPlane/internal/metricshistory/retention_test.go
package metricshistory_test

import (
	"context"
	"testing"
	"time"

	"AIServeWeave/common/runtime/runtimetest" // 若仓库里 Clock 测试替身在别的包，改成实际路径，例如各服务自带的 faketest Clock
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/metricshistory"
)

type fakeRetentionStore struct{ deletedBefore []time.Time }

func (f *fakeRetentionStore) DeleteRollupBefore(ctx context.Context, before time.Time) (int64, error) {
	f.deletedBefore = append(f.deletedBefore, before)
	return 3, nil
}

func TestRetentionDeletesOlderThanWindow(t *testing.T) {
	clock := runtimetest.NewClock(time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	store := &fakeRetentionStore{}
	r := metricshistory.NewRetention(store, 90*24*time.Hour, clock, nil)

	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(store.deletedBefore) != 1 {
		t.Fatalf("DeleteRollupBefore called %d times, want 1", len(store.deletedBefore))
	}
	want := clock.Now().Add(-90 * 24 * time.Hour)
	if !store.deletedBefore[0].Equal(want) {
		t.Errorf("DeleteRollupBefore(%v), want %v", store.deletedBefore[0], want)
	}
}
```

若本仓库的 `runtime.Clock` 测试替身实际路径/构造函数名与上面不同，以 `grep -rn "func New.*Clock" common/runtime/` 或各服务已有的 `_test.go` 里注入 Clock 的写法为准，替换成实际可用的类型与调用方式——语义不变：需要一个可以设定固定"当前时间"的 `runtime.Clock` 实现。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveControlPlane/internal/metricshistory/... -run TestRetention -v`
Expected: FAIL，`retention.go` 不存在

- [ ] **Step 3: 实现**

```go
// service/aiServeWeaveControlPlane/internal/metricshistory/retention.go
package metricshistory

import (
	"context"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
)

// RetentionStore is the persistence surface retention cleanup needs.
//
// RetentionStore 是保留期清理所需要的持久化接口。
type RetentionStore interface {
	DeleteRollupBefore(ctx context.Context, before time.Time) (int64, error)
}

// Retention periodically deletes rollup rows past their retention window.
//
// Retention 定时删除超出保留期的汇总行。
type Retention struct {
	store     RetentionStore
	retention time.Duration
	clock     runtime.Clock
	logger    *slog.Logger
}

// NewRetention builds a Retention. A nil clock defaults to the system clock;
// a nil logger discards.
//
// NewRetention 构造一个 Retention。clock 为 nil 时使用系统时钟；logger 为 nil
// 时丢弃日志。
func NewRetention(store RetentionStore, retention time.Duration, clock runtime.Clock, logger *slog.Logger) *Retention {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Retention{store: store, retention: retention, clock: clock, logger: logger}
}

// RunOnce deletes every row older than the retention window, once.
//
// RunOnce 一次性删除全部超出保留期的行。
func (r *Retention) RunOnce(ctx context.Context) error {
	before := r.clock.Now().Add(-r.retention)
	n, err := r.store.DeleteRollupBefore(ctx, before)
	if err != nil {
		return err
	}
	if n > 0 {
		r.logger.Info("metrics history retention cleanup", slog.Int64("rows_deleted", n), slog.Time("before", before))
	}
	return nil
}

// Run calls RunOnce once per interval until ctx is done.
//
// Run 每隔 interval 调用一次 RunOnce，直到 ctx 结束。
func (r *Retention) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil {
				r.logger.Error("metrics history retention cleanup failed", slog.Any("error", err))
			}
		}
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveControlPlane/internal/metricshistory/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/metricshistory/retention.go service/aiServeWeaveControlPlane/internal/metricshistory/retention_test.go
git commit -m "$(cat <<'EOF'
feat(controlplane): add metrics-history retention cleanup

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 21: 把采集器与清理任务接入 `ServiceContext` 生命周期

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/svc/servicecontext.go`

**Interfaces:**
- Consumes: Task 16 `MetricsHistoryConf`、Task 19 `metricshistory.New`/`Run`、Task 20 `metricshistory.NewRetention`/`Run`、Task 18 `s.store`(已实现 `store.MetricsHistory`，可直接作为 `metricshistory.Store`/`RetentionStore` 传入)。

只在 `cfg.MetricsHistory.Enabled()` 时启动这两个后台 goroutine，未配置时零开销、也不产生任何多余日志噪音——与 `Fleet`/`Registry` 现有的可选装配方式一致。

- [ ] **Step 1: 实现**

```go
// service/aiServeWeaveControlPlane/internal/svc/servicecontext.go
// ServiceContext 结构体新增未导出字段(与既有 relayCancel/relayDone 并列)：
	metricsHistoryCancel context.CancelFunc
	metricsHistoryDone   chan struct{}

// NewServiceContext 内，在既有 revocation-outbox relay 启动逻辑之后：
	if cfg.MetricsHistory.Enabled() {
		collector := metricshistory.New(metricshistory.Config{
			GatewayAddrs: cfg.MetricsHistory.GatewayAddrs,
			RegistryAddr: cfg.MetricsHistory.RegistryAddr,
			Interval:     cfg.MetricsHistory.Interval,
			Store:        st,
			Logger:       slog.Default(),
		})
		retention := metricshistory.NewRetention(st, cfg.MetricsHistory.Retention, nil, slog.Default())

		mhCtx, mhCancel := context.WithCancel(ctx)
		mhDone := make(chan struct{})
		go func() {
			defer close(mhDone)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); collector.Run(mhCtx) }()
			go func() { defer wg.Done(); retention.Run(mhCtx, 24*time.Hour) }()
			wg.Wait()
		}()

		svcCtx.metricsHistoryCancel = mhCancel
		svcCtx.metricsHistoryDone = mhDone
	}
```

(`st` 是 `NewServiceContext` 里已经构造好的 `*gormstore.Store`，同一个赋给 `logic.New` 的实例；`svcCtx` 是本函数即将返回的 `*ServiceContext`——按该函数现有的构造顺序，在最终 `return svcCtx, nil` 之前完成这段赋值。)

`Close()` 方法内，紧邻既有 `relayCancel`/`relayDone` 的收尾逻辑之后：

```go
	if s.metricsHistoryCancel != nil {
		s.metricsHistoryCancel()
		<-s.metricsHistoryDone
	}
```

- [ ] **Step 2: 跑测试确认通过**

Run:
```bash
go build ./service/aiServeWeaveControlPlane/...
go vet ./service/aiServeWeaveControlPlane/...
go test ./service/aiServeWeaveControlPlane/... -v
```
Expected: PASS；`MetricsHistory` 未配置时(既有全部测试用的默认 `Config{}`)这段代码整体不执行，不引入行为变化

- [ ] **Step 3: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/svc/servicecontext.go
git commit -m "$(cat <<'EOF'
feat(controlplane): wire the metrics-history collector and retention into service lifecycle

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 22: `internal/logic.ListMetricsHistory`

**Files:**
- Create: `service/aiServeWeaveControlPlane/internal/logic/metricshistory.go`
- Test: `service/aiServeWeaveControlPlane/internal/logic/metricshistory_test.go`

**Interfaces:**
- Consumes: `store.MetricsHistory.ListRollup`(Task 18，通过既有的 `s.store` 字段)。
- Produces: `var HistoryMetricNames []string`(与 Task 19 `seriesKeepLabels` 的 key 集合保持一致，两处独立列出是刻意的——mirrors Gateway `tunnelserver/metrics_test.go` 用独立列表防止"随手记录了什么就断言什么"的同一原则)；`func (s *Service) ListMetricsHistory(ctx context.Context, since, until time.Time) ([]model.MetricsHistoryPoint, error)`。

- [ ] **Step 1: 写失败测试**

```go
// service/aiServeWeaveControlPlane/internal/logic/metricshistory_test.go
package logic_test

func TestListMetricsHistoryRejectsInvertedWindow(t *testing.T) {
	svc := newTestService(t) // 复用本包既有的"起一个内存 store 的 Service"辅助函数
	since := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	until := since.Add(-time.Hour)

	if _, err := svc.ListMetricsHistory(context.Background(), since, until); err == nil {
		t.Fatal("ListMetricsHistory() error = nil, want error for until before since")
	}
}

func TestListMetricsHistoryReturnsStoredPoints(t *testing.T) {
	svc, st := newTestServiceWithStore(t) // 同上，另外把底层 store 暴露出来方便直接写数据
	since := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	if err := st.UpsertRollup(context.Background(), []model.MetricsHistoryPoint{
		{Metric: logic.HistoryMetricNames[0], Labels: "", BucketAt: since.Add(time.Hour), Value: 42},
	}); err != nil {
		t.Fatalf("seeding UpsertRollup() error = %v", err)
	}

	points, err := svc.ListMetricsHistory(context.Background(), since, until)
	if err != nil {
		t.Fatalf("ListMetricsHistory() error = %v", err)
	}
	if len(points) != 1 || points[0].Value != 42 {
		t.Fatalf("ListMetricsHistory() = %+v, want one point with value 42", points)
	}
}
```

若本包目前没有内存版 `store.Store` 测试替身(`memstore`)提供 `MetricsHistory` 三个方法，先在该替身里补上(与 Task 18 的接口签名一致，用一个内存 slice 实现)——具体文件以 `grep -rln "memstore\|type.*fake.*Store" service/aiServeWeaveControlPlane/internal/logic/*_test.go` 找到的既有内存实现为准，在其中新增这三个方法而不是另起一个新的替身类型。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveControlPlane/internal/logic/... -run TestListMetricsHistory -v`
Expected: FAIL，`ListMetricsHistory`/`HistoryMetricNames` 不存在

- [ ] **Step 3: 实现**

```go
// service/aiServeWeaveControlPlane/internal/logic/metricshistory.go
package logic

import (
	"context"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// HistoryMetricNames is the closed list of metric names Console C27 charts.
// It mirrors internal/metricshistory's collector allowlist by name, not by
// import, so a rename on either side is a visible mismatch instead of a
// silent one.
//
// HistoryMetricNames 是 Console C27 图表用到的封闭指标名单，按名字而非依赖
// 与 internal/metricshistory 采集器的允许名单保持一致——任何一侧改名都会变成
// 显眼的不一致，而不是悄悄失联。
var HistoryMetricNames = []string{
	"gateway_http_requests_total",
	"gateway_http_request_duration_seconds_bucket",
	"gateway_http_request_duration_seconds_sum",
	"gateway_http_request_duration_seconds_count",
	"gateway_tokens_total",
	"tunnel_server_slots_total",
}

// ListMetricsHistory returns every rolled-up point across the metrics
// Console C27 charts, in [since, until).
//
// ListMetricsHistory 返回 Console C27 图表用到的全部指标在 [since, until) 内
// 的汇总数据点。
func (s *Service) ListMetricsHistory(ctx context.Context, since, until time.Time) ([]model.MetricsHistoryPoint, error) {
	if !until.After(since) {
		return nil, ErrInvalidInput
	}
	return s.store.ListRollup(ctx, HistoryMetricNames, since, until)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveControlPlane/internal/logic/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/logic/metricshistory.go service/aiServeWeaveControlPlane/internal/logic/metricshistory_test.go
git commit -m "$(cat <<'EOF'
feat(controlplane): add ListMetricsHistory logic method

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 23: `types.MetricsHistoryResponse` + handler + 路由注册

**Files:**
- Modify: `service/aiServeWeaveControlPlane/internal/types/`(新增类型的具体文件以该目录既有的组织方式为准，例如新建 `metricshistory.go` 或加进已有的 `types.go`)
- Create: `service/aiServeWeaveControlPlane/internal/handler/metricshistory.go`
- Modify: `service/aiServeWeaveControlPlane/internal/handler/routes.go`
- Test: `service/aiServeWeaveControlPlane/internal/handler/metricshistory_test.go`

**Interfaces:**
- Consumes: Task 22 `Service.ListMetricsHistory`；既有的 `listQuery`/`timeParam`(`handlers.go:538-572`)；既有的 `requirePlatformSession`；Task 14 的 `instrumented`。
- Produces: `GET /operator/v1/metrics/history?since=&until=`，返回 `types.MetricsHistoryResponse`。

- [ ] **Step 1: 写失败测试**

```go
// service/aiServeWeaveControlPlane/internal/handler/metricshistory_test.go
package handler

func TestMetricsHistoryHandlerRejectsMissingWindow(t *testing.T) {
	ctx := newTestServiceContext(t) // 复用本包既有的起一个带内存 store 的 ServiceContext 的辅助函数
	req := httptest.NewRequest(http.MethodGet, "/operator/v1/metrics/history", nil)
	w := httptest.NewRecorder()

	metricsHistory(ctx)(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for a request with no since/until", w.Code, http.StatusBadRequest)
	}
}

func TestMetricsHistoryHandlerReturnsSeries(t *testing.T) {
	ctx := newTestServiceContext(t)
	// ...通过 ctx 底层 store 直接 UpsertRollup 种一条数据，同 Task 22 的测试写法...

	req := httptest.NewRequest(http.MethodGet, "/operator/v1/metrics/history?since=2026-09-11T00:00:00Z&until=2026-09-12T00:00:00Z", nil)
	w := httptest.NewRecorder()
	metricsHistory(ctx)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	var resp types.MetricsHistoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Series) == 0 {
		t.Error("resp.Series is empty, want the seeded point back")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./service/aiServeWeaveControlPlane/internal/handler/... -run TestMetricsHistoryHandler -v`
Expected: FAIL，`metricsHistory`/`types.MetricsHistoryResponse` 不存在

- [ ] **Step 3: 实现**

```go
// internal/types 新增：
type MetricsHistoryPointResponse struct {
	BucketAt string  `json:"bucket_at"`
	Value    float64 `json:"value"`
}

type MetricsHistorySeriesResponse struct {
	Metric string            `json:"metric"`
	Labels map[string]string `json:"labels"`
	Points []MetricsHistoryPointResponse `json:"points"`
}

type MetricsHistoryResponse struct {
	Since  string                          `json:"since"`
	Until  string                          `json:"until"`
	Series []MetricsHistorySeriesResponse `json:"series"`
}
```

```go
// service/aiServeWeaveControlPlane/internal/handler/metricshistory.go
package handler

import (
	"net/http"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/svc"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/types"
)

// metricsHistory answers GET /operator/v1/metrics/history?since=&until=,
// grouping stored points by (metric, labels) into one time series each —
// the shape Console's ECharts panels want, not the flat row-per-bucket shape
// the store returns.
//
// metricsHistory 回答 GET /operator/v1/metrics/history?since=&until=，把存储
// 返回的"每个 bucket 一行"的扁平数据，按 (metric, labels) 分组成 Console
// ECharts 面板需要的每条序列一组的形状。
func metricsHistory(ctx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		since, sinceOK := timeParam(r.URL.Query().Get("since"))
		until, untilOK := timeParam(r.URL.Query().Get("until"))
		if !sinceOK || !untilOK || r.URL.Query().Get("since") == "" || r.URL.Query().Get("until") == "" {
			writeError(w, http.StatusBadRequest, "since and until are required RFC3339 timestamps")
			return
		}

		points, err := ctx.Logic.ListMetricsHistory(r.Context(), since, until)
		if err != nil {
			respondServiceErr(w, err) // 复用本文件所在包既有的错误映射函数；具体名字以 handlers.go 里其它 logic 调用错误处理的既有写法为准
			return
		}

		type seriesKey struct{ metric, labels string }
		grouped := map[seriesKey]*types.MetricsHistorySeriesResponse{}
		var order []seriesKey
		for _, p := range points {
			k := seriesKey{metric: p.Metric, labels: p.Labels}
			s, ok := grouped[k]
			if !ok {
				s = &types.MetricsHistorySeriesResponse{Metric: p.Metric, Labels: parseCanonicalLabels(p.Labels)}
				grouped[k] = s
				order = append(order, k)
			}
			s.Points = append(s.Points, types.MetricsHistoryPointResponse{BucketAt: p.BucketAt.UTC().Format(time.RFC3339), Value: p.Value})
		}
		resp := types.MetricsHistoryResponse{Since: since.UTC().Format(time.RFC3339), Until: until.UTC().Format(time.RFC3339)}
		for _, k := range order {
			resp.Series = append(resp.Series, *grouped[k])
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// parseCanonicalLabels is the inverse of metricshistory.canonicalLabels.
//
// parseCanonicalLabels 是 metricshistory.canonicalLabels 的逆操作。
func parseCanonicalLabels(s string) map[string]string {
	labels := map[string]string{}
	if s == "" {
		return labels
	}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			labels[k] = v
		}
	}
	return labels
}
```

`routes.go`：在既有的 `requirePlatformSession` 路由组里(与 `/operator/v1/audit` 同一组)新增一行：

```go
{Method: http.MethodGet, Path: "/operator/v1/metrics/history", Handler: instrumented(ctx.MetricsRegistry, "/operator/v1/metrics/history", requirePlatformSession(ctx, metricsHistory(ctx)))},
```

`points` 已经按 `bucket_at ASC` 排序(Task 18 `ListRollup` 的 `Order("bucket_at ASC")`)，因此按到达顺序 append 进每条 series 的 `Points` 自然保持时间升序，不需要额外排序。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./service/aiServeWeaveControlPlane/internal/... -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveControlPlane/internal/types/ service/aiServeWeaveControlPlane/internal/handler/metricshistory.go service/aiServeWeaveControlPlane/internal/handler/metricshistory_test.go service/aiServeWeaveControlPlane/internal/handler/routes.go
git commit -m "$(cat <<'EOF'
feat(controlplane): add GET /operator/v1/metrics/history

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 24: Console `upstream-routes.ts` 白名单

**Files:**
- Modify: `service/aiServeWeaveConsole/lib/console/upstream-routes.ts`
- Modify: `service/aiServeWeaveConsole/lib/console/upstream-routes.test.ts`

**Interfaces:**
- Produces: `OPERATOR_ROUTES` 新增一条 `GET /operator/v1/metrics/history`，允许透传 `since`/`until` 两个查询参数。

- [ ] **Step 1: 写失败测试**

```ts
// 追加到 service/aiServeWeaveConsole/lib/console/upstream-routes.test.ts
test("operator metrics history route forwards since/until", () => {
  const resolved = resolveOperatorUpstream("GET", ["operator", "v1", "metrics", "history"], new URLSearchParams("since=2026-09-11T00:00:00Z&until=2026-09-12T00:00:00Z&bogus=1"));
  assert.ok(resolved);
  assert.equal(resolved?.path, "/operator/v1/metrics/history");
  assert.equal(resolved?.search, "since=2026-09-11T00%3A00%3A00Z&until=2026-09-12T00%3A00%3A00Z");
});
```

(`resolveOperatorUpstream` 的具体导出名/签名以该测试文件已有用例的实际写法为准；核心断言不变——`bogus` 参数被丢弃，`since`/`until` 被保留。)

- [ ] **Step 2: 跑测试确认失败**

Run: `pnpm test -- lib/console/upstream-routes.test.ts`
Expected: FAIL，尚未匹配到该路由

- [ ] **Step 3: 实现**

```ts
// service/aiServeWeaveConsole/lib/console/upstream-routes.ts
// OPERATOR_ROUTES 数组内，与 operator audit 那条相邻处新增：
  { method: "GET", segments: [literal("operator"), literal("v1"), literal("metrics"), literal("history")], query: ["since", "until"] },
```

- [ ] **Step 4: 跑测试确认通过**

Run: `pnpm test -- lib/console/upstream-routes.test.ts`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveConsole/lib/console/upstream-routes.ts service/aiServeWeaveConsole/lib/console/upstream-routes.test.ts
git commit -m "$(cat <<'EOF'
feat(console): whitelist GET /operator/v1/metrics/history

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 25: Console `lib/console/metrics.ts` 解析器

**Files:**
- Create: `service/aiServeWeaveConsole/lib/console/metrics.ts`
- Create: `service/aiServeWeaveConsole/lib/console/metrics.test.ts`

**Interfaces:**
- Produces: `interface MetricsHistorySeries { metric: string; labels: Record<string,string>; points: { bucketAt: string; value: number }[] }`、`interface MetricsHistory { since: string; until: string; series: MetricsHistorySeries[] }`、`function parseMetricsHistory(value: unknown): MetricsHistory`。仿照 `lib/console/fleet.ts` 自带一套 `fail`/`record`/`text`/`list` 私有 helper 的写法，不依赖 `contract.ts`。

- [ ] **Step 1: 写失败测试**

```ts
// service/aiServeWeaveConsole/lib/console/metrics.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { parseMetricsHistory } from "./metrics.ts";
import { ApiError } from "./errors.ts";

test("parses a metrics history response with one series", () => {
  const history = parseMetricsHistory({
    since: "2026-09-11T00:00:00Z",
    until: "2026-09-12T00:00:00Z",
    series: [
      {
        metric: "gateway_http_requests_total",
        labels: { endpoint: "chat", status: "200" },
        points: [{ bucket_at: "2026-09-11T00:05:00Z", value: 12 }],
      },
    ],
  });
  assert.equal(history.series[0]?.metric, "gateway_http_requests_total");
  assert.equal(history.series[0]?.labels.endpoint, "chat");
  assert.equal(history.series[0]?.points[0]?.value, 12);
});

test("rejects a response missing series", () => {
  assert.throws(() => parseMetricsHistory({ since: "2026-09-11T00:00:00Z", until: "2026-09-12T00:00:00Z" }), ApiError);
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `pnpm test -- lib/console/metrics.test.ts`
Expected: FAIL，`metrics.ts` 不存在

- [ ] **Step 3: 实现**

```ts
// service/aiServeWeaveConsole/lib/console/metrics.ts
import { ApiError } from "./errors.ts";

export interface MetricsHistoryPoint {
  bucketAt: string;
  value: number;
}

export interface MetricsHistorySeries {
  metric: string;
  labels: Record<string, string>;
  points: MetricsHistoryPoint[];
}

export interface MetricsHistory {
  since: string;
  until: string;
  series: MetricsHistorySeries[];
}

function fail(): never {
  throw new ApiError("contract");
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) fail();
  return value as Record<string, unknown>;
}

function text(source: Record<string, unknown>, key: string): string {
  const v = source[key];
  if (typeof v !== "string") fail();
  return v;
}

function num(source: Record<string, unknown>, key: string): number {
  const v = source[key];
  if (typeof v !== "number") fail();
  return v;
}

function list<T>(value: unknown, parse: (item: unknown) => T): T[] {
  if (!Array.isArray(value)) fail();
  return value.map(parse);
}

function stringMap(value: unknown): Record<string, string> {
  const source = record(value ?? {});
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(source)) {
    if (typeof v !== "string") fail();
    out[k] = v;
  }
  return out;
}

function parsePoint(value: unknown): MetricsHistoryPoint {
  const source = record(value);
  return { bucketAt: text(source, "bucket_at"), value: num(source, "value") };
}

function parseSeries(value: unknown): MetricsHistorySeries {
  const source = record(value);
  return {
    metric: text(source, "metric"),
    labels: stringMap(source.labels),
    points: list(source.points, parsePoint),
  };
}

/** parseMetricsHistory validates a GET /operator/v1/metrics/history response.
 * parseMetricsHistory 校验 GET /operator/v1/metrics/history 的响应。 */
export function parseMetricsHistory(value: unknown): MetricsHistory {
  const source = record(value);
  return {
    since: text(source, "since"),
    until: text(source, "until"),
    series: list(source.series, parseSeries),
  };
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `pnpm test -- lib/console/metrics.test.ts`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add service/aiServeWeaveConsole/lib/console/metrics.ts service/aiServeWeaveConsole/lib/console/metrics.test.ts
git commit -m "$(cat <<'EOF'
feat(console): add parseMetricsHistory contract parser

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 26: `/operator/metrics` 页面与 ECharts 面板

**Files:**
- Create: `service/aiServeWeaveConsole/lib/console/metrics-charts.ts`
- Create: `service/aiServeWeaveConsole/lib/console/metrics-charts.test.ts`
- Create: `service/aiServeWeaveConsole/app/operator/(protected)/metrics/metrics-view.tsx`
- Create: `service/aiServeWeaveConsole/app/operator/(protected)/metrics/page.tsx`

**Interfaces:**
- Consumes: Task 25 `parseMetricsHistory`/`MetricsHistory`；既有的 `useResource`、`LoadingState`/`EmptyState`/`ErrorState`(`components/console/states.tsx`)。
- Produces: 纯逻辑辅助 `deltaByBucket(series: MetricsHistorySeries): {bucketAt:string; value:number}[]`(把累计计数器序列转成逐桶增量，供请求量/Token 面板使用)与 `approxP95(bucketSeries: MetricsHistorySeries[]): {bucketAt:string; value:number}[]`(从直方图 `_bucket{le=...}` 系列按桶边界近似出 p95，取"值首次达到该桶总数的 95%"的那个 `le` 边界，不做线性插值——足够画出趋势，不追求逐点精确)，从组件文件里剥离出来单独测试，UI 只负责渲染这两个函数的输出。

- [ ] **Step 1: 写失败测试**(纯逻辑部分)

```ts
// service/aiServeWeaveConsole/lib/console/metrics-charts.test.ts
import { test } from "node:test";
import assert from "node:assert/strict";
import { deltaByBucket, approxP95 } from "./metrics-charts.ts";
import type { MetricsHistorySeries } from "./metrics.ts";

test("deltaByBucket turns a cumulative counter into per-bucket increments", () => {
  const series: MetricsHistorySeries = {
    metric: "gateway_http_requests_total",
    labels: { endpoint: "chat", status: "200" },
    points: [
      { bucketAt: "2026-09-11T00:00:00Z", value: 10 },
      { bucketAt: "2026-09-11T00:05:00Z", value: 25 },
      { bucketAt: "2026-09-11T00:10:00Z", value: 25 },
    ],
  };
  const deltas = deltaByBucket(series);
  assert.deepEqual(deltas.map((d) => d.value), [15, 0]);
});

test("approxP95 picks the le boundary where the cumulative count first reaches 95%", () => {
  const buckets: MetricsHistorySeries[] = [
    { metric: "gateway_http_request_duration_seconds_bucket", labels: { endpoint: "chat", le: "0.1" }, points: [{ bucketAt: "t1", value: 50 }] },
    { metric: "gateway_http_request_duration_seconds_bucket", labels: { endpoint: "chat", le: "0.5" }, points: [{ bucketAt: "t1", value: 94 }] },
    { metric: "gateway_http_request_duration_seconds_bucket", labels: { endpoint: "chat", le: "1" }, points: [{ bucketAt: "t1", value: 100 }] },
  ];
  const p95 = approxP95(buckets);
  assert.equal(p95[0]?.value, 1); // 94/100=94% < 95%，取下一个边界 1
});
```

- [ ] **Step 2: 跑测试确认失败**

Run: `pnpm test -- lib/console/metrics-charts.test.ts`
Expected: FAIL，`metrics-charts.ts` 不存在

- [ ] **Step 3: 实现纯逻辑**

```ts
// service/aiServeWeaveConsole/lib/console/metrics-charts.ts
import type { MetricsHistorySeries, MetricsHistoryPoint } from "./metrics.ts";

/** deltaByBucket turns a cumulative counter series into per-bucket increments,
 * one shorter than the input since the first bucket has no predecessor.
 * deltaByBucket 把一条累计计数器序列转成逐桶增量，比输入短一位——第一个桶没有
 * 前驱可比较。 */
export function deltaByBucket(series: MetricsHistorySeries): MetricsHistoryPoint[] {
  const out: MetricsHistoryPoint[] = [];
  for (let i = 1; i < series.points.length; i++) {
    const prev = series.points[i - 1]!;
    const curr = series.points[i]!;
    out.push({ bucketAt: curr.bucketAt, value: Math.max(0, curr.value - prev.value) });
  }
  return out;
}

/** approxP95 groups histogram bucket series by bucketAt (they all share one
 * timestamp per call site in this codebase — one snapshot at a time) and, for
 * each timestamp, returns the smallest `le` boundary whose cumulative count is
 * at least 95% of the largest (== total) count at that timestamp.
 * approxP95 按 bucketAt 对直方图 bucket 序列分组(本仓库的调用方式下每次只传一个
 * 时间戳的快照)，对每个时间戳返回累计计数达到该时刻最大(即总数)计数 95% 所需的
 * 最小 `le` 边界。 */
export function approxP95(buckets: MetricsHistorySeries[]): MetricsHistoryPoint[] {
  if (buckets.length === 0) return [];
  const bucketAt = buckets[0]!.points[0]?.bucketAt ?? "";
  const total = Math.max(...buckets.map((b) => b.points[0]?.value ?? 0));
  if (total === 0) return [{ bucketAt, value: 0 }];
  const sorted = [...buckets].sort((a, b) => Number(a.labels.le) - Number(b.labels.le));
  for (const b of sorted) {
    const count = b.points[0]?.value ?? 0;
    if (count >= total * 0.95) {
      return [{ bucketAt, value: Number(b.labels.le) }];
    }
  }
  return [{ bucketAt, value: Number(sorted[sorted.length - 1]?.labels.le ?? 0) }];
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `pnpm test -- lib/console/metrics-charts.test.ts`
Expected: PASS

- [ ] **Step 5: 页面与视图组件**

```tsx
// service/aiServeWeaveConsole/app/operator/(protected)/metrics/page.tsx
import { MetricsView } from "./metrics-view.tsx";

/** MetricsPage displays fleet-wide historical request/latency/token/capacity
 * curves for platform operators.
 * MetricsPage 向平台运维展示机群级别的历史请求/延迟/Token/容量曲线。 */
export default function MetricsPage() {
  return <MetricsView />;
}
```

```tsx
// service/aiServeWeaveConsole/app/operator/(protected)/metrics/metrics-view.tsx
"use client";

import * as React from "react";
import ReactECharts from "echarts-for-react";
import { useResource } from "@/components/console/use-resource";
import { LoadingState, EmptyState, ErrorState } from "@/components/console/states";
import { parseMetricsHistory, type MetricsHistory, type MetricsHistorySeries } from "@/lib/console/metrics";
import { deltaByBucket, approxP95 } from "@/lib/console/metrics-charts";

function windowBounds() {
  const until = new Date();
  const since = new Date(until.getTime() - 24 * 60 * 60 * 1000);
  return { since: since.toISOString(), until: until.toISOString() };
}

function seriesFor(history: MetricsHistory, metric: string): MetricsHistorySeries[] {
  return history.series.filter((s) => s.metric === metric);
}

function lineOption(title: string, unit: string, lines: { name: string; points: { bucketAt: string; value: number }[] }[]) {
  return {
    title: { text: title, textStyle: { fontSize: 13 } },
    tooltip: { trigger: "axis" },
    legend: { top: 24, textStyle: { fontSize: 11 } },
    grid: { top: 64, left: 48, right: 24, bottom: 48 },
    xAxis: { type: "time" },
    yAxis: { type: "value", name: unit },
    dataZoom: [{ type: "inside" }, { type: "slider", height: 16 }],
    series: lines.map((l) => ({
      name: l.name,
      type: "line",
      showSymbol: false,
      data: l.points.map((p) => [p.bucketAt, p.value]),
    })),
  };
}

export function MetricsView() {
  const { since, until } = React.useMemo(windowBounds, []);
  const resource = useResource<MetricsHistory>({
    method: "GET",
    surface: "operator",
    path: "/operator/v1/metrics/history",
    query: { since, until },
    parse: parseMetricsHistory,
  });

  if (resource.error) {
    return <ErrorState message={resource.error} onRetry={resource.reload} />;
  }
  if (resource.loading || !resource.data) {
    return <LoadingState label="正在加载指标历史" rows={5} />;
  }
  const history = resource.data;
  if (history.series.length === 0) {
    return <EmptyState title="暂无指标历史" description="采集器尚未产生任何数据点，或所选时间窗口内没有流量。" />;
  }

  const requests = seriesFor(history, "gateway_http_requests_total");
  const tokens = seriesFor(history, "gateway_tokens_total");
  const capacity = seriesFor(history, "tunnel_server_slots_total");
  const durationBuckets = seriesFor(history, "gateway_http_request_duration_seconds_bucket");

  const byEndpointStatus = requests.map((s) => ({
    name: `${s.labels.endpoint ?? "?"} ${s.labels.status ?? "?"}`,
    points: deltaByBucket(s),
  }));
  const successSeries = requests.filter((s) => s.labels.status === "200");
  const errorSeries = requests.filter((s) => s.labels.status && s.labels.status !== "200");
  const successRate = successSeries.map((s) => ({ name: "成功", points: deltaByBucket(s) }));
  const tokenLines = tokens.map((s) => ({ name: s.labels.direction ?? "?", points: deltaByBucket(s) }));
  const capacityLines = capacity.map((s) => ({ name: `${s.labels.class ?? "?"} ${s.labels.state ?? "?"}`, points: s.points }));

  const byEndpoint = new Map<string, MetricsHistorySeries[]>();
  for (const s of durationBuckets) {
    const key = s.labels.endpoint ?? "?";
    byEndpoint.set(key, [...(byEndpoint.get(key) ?? []), s]);
  }
  const p95Lines = Array.from(byEndpoint.entries()).map(([endpoint, buckets]) => ({
    name: endpoint,
    points: approxP95(buckets),
  }));

  return (
    <div className="grid gap-6 md:grid-cols-2">
      <ReactECharts option={lineOption("请求量(按端点/状态)", "req/窗口", byEndpointStatus)} style={{ height: 280 }} />
      <ReactECharts option={lineOption("成功请求数", "req/窗口", successRate)} style={{ height: 280 }} />
      <ReactECharts option={lineOption("延迟 p95 近似值", "秒", p95Lines)} style={{ height: 280 }} />
      <ReactECharts option={lineOption("Token 用量", "token/窗口", tokenLines)} style={{ height: 280 }} />
      <ReactECharts option={lineOption("容量(槽位数)", "槽位", capacityLines)} style={{ height: 280 }} />
      {errorSeries.length === 0 ? null : (
        <ReactECharts
          option={lineOption("非 200 请求", "req/窗口", errorSeries.map((s) => ({ name: s.labels.status ?? "?", points: deltaByBucket(s) })))}
          style={{ height: 280 }}
        />
      )}
    </div>
  );
}
```

- [ ] **Step 6: 手工浏览器验收**

Run: `pnpm dev`，登录平台运维账户，打开 `/operator/metrics`：确认加载态/错误态/空态与既有页面一致，`dataZoom` 可拖动缩放，控制面无数据时显示 `EmptyState` 而不是空白图表。

- [ ] **Step 7: 提交**

```bash
git add service/aiServeWeaveConsole/lib/console/metrics-charts.ts service/aiServeWeaveConsole/lib/console/metrics-charts.test.ts service/aiServeWeaveConsole/app/operator/\(protected\)/metrics/
git commit -m "$(cat <<'EOF'
feat(console): add /operator/metrics with ECharts dataZoom panels

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 27: `operator-shell.tsx` 导航入口

**Files:**
- Modify: `service/aiServeWeaveConsole/app/operator/operator-shell.tsx`

- [ ] **Step 1: 实现**

```tsx
// navigation 数组内，"/operator/audit" 之后新增一行：
  { href: "/operator/metrics", label: "指标" },
```

- [ ] **Step 2: 跑门禁**

Run:
```bash
pnpm lint
pnpm build
pnpm typecheck
pnpm test
```
Expected: 全部通过(`pnpm typecheck` 依赖上一次 `pnpm build` 生成的 `.next/types`，顺序不能颠倒)

- [ ] **Step 3: 提交**

```bash
git add service/aiServeWeaveConsole/app/operator/operator-shell.tsx
git commit -m "$(cat <<'EOF'
feat(console): add metrics entry to the operator nav

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 28: 文档回填与全量门禁

**Files:**
- Modify: `STATUS.md`(P08 行)
- Modify: `README.md`(「可观测性」一节)
- Modify: `service/aiServeWeaveGateway/README.md`(「下一步」第 1 条改为已完成/删除)
- Modify: `service/aiServeWeaveRegistry/README.md`(若 Task 12 尚未覆盖到的细节)
- Modify: `service/aiServeWeaveControlPlane/README.md`(新增「指标与历史监控」一节，说明 `-metrics-addr`、`MetricsHistoryConf`、`/operator/v1/metrics/history`、采集频率与保留期默认值)
- Modify: `service/aiServeWeaveConsole/STATUS.md`(C27 行从 `[ ]` 改 `[x]`，补验收记录)
- Modify: `deploy/README.md` / `deploy/controlplane.yaml`(`-metrics-addr` 需要从回环改绑到控制面可达的内部网络接口，给出示例配置)
- Modify: `CHANGELOG.md`

**Interfaces:** 无新代码接口——本任务是文档同步与门禁验证。

- [ ] **Step 1: 更新 `STATUS.md`**

把 P08 行的 `[ ]` 改成 `[x]`，验收说明模仿既有 P05/P06/P07 行的写法：一句话概括 Registry/控制面指标接入、历史采集与查询、Console C27、trace 日志范围，以及跑过的门禁清单。

- [ ] **Step 2: 更新根 `README.md`「可观测性」一节**

在既有段落后补一段，说明历史时序落在控制面新表(不是独立 TSDB)、采集机制(定时抓取 `/metrics` 文本、`common/metrics.ParseExposition`)、Console C27 已接入、trace 仍是结构化日志而非独立存储——与本节现有的"已有 Prometheus 导出不等于可直接绘制历史曲线"那句话衔接，把"尚未做"改成"已做，做法是……"。

- [ ] **Step 3: 更新各服务 README 与部署文档**

Gateway README「下一步」第 1 条(Registry 侧指标)标记完成；控制面 README 新增一节记录 `-metrics-addr`、`MetricsHistoryConf` 的字段与默认值(`Interval=5m`、`Retention=2160h`)、`GET /operator/v1/metrics/history` 的授权方式(`requirePlatformSession`，与机群清单同一套平台运维身份)；`deploy/README.md` 补一条部署提示：Gateway/Registry 的 `-metrics-addr` 若要被控制面采集，必须绑定到 compose 内部网络可达的地址(不能停留在默认回环)，给出 `docker-compose.yaml` 里三个服务同处一个内部网络时的示例值。

- [ ] **Step 4: 更新 `CHANGELOG.md`**

按仓库既有格式追加一条 P08 的条目，列出 Registry/控制面指标接入、历史时序采集与查询、Console C27、trace 结构化日志四项。

- [ ] **Step 5: 跑全量门禁**

Run:
```bash
gofmt -l ./service ./api
go vet ./...
go build ./...
go generate ./api/...
git diff --stat api/proto   # 确认生成结果无 diff
go test ./...
go test -race ./service/...
```

```bash
cd service/aiServeWeaveConsole
pnpm lint
pnpm typecheck
pnpm test
pnpm build
```

Expected: 全部无错误、无失败用例、`gofmt -l` 无输出、`git diff --stat api/proto` 无输出。

- [ ] **Step 6: 提交**

```bash
git add STATUS.md README.md CHANGELOG.md service/aiServeWeaveGateway/README.md service/aiServeWeaveRegistry/README.md service/aiServeWeaveControlPlane/README.md service/aiServeWeaveConsole/STATUS.md deploy/
git commit -m "$(cat <<'EOF'
docs: record P08 metrics, tracing and history monitoring as complete

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage**：Registry 指标(Task 8-12)、控制面指标(Task 13-15)、历史采集/存储/查询(Task 16-23)、trace 结构化日志(Task 2-7)、Console C27(Task 24-27)、文档(Task 28)——spec 六个小节均有对应任务。
- **已知简化，非遗漏**：采集周期与汇总粒度合并成一个 `Interval`(Task 16 已注明原因)；Fleet/Registry 客户端调用指标用两值 `success`/`error` 而非细分错误分类(Task 15 已注明原因，避免臆造未经验证的错误分类词汇表)；p95 用最近桶边界近似而非线性插值(Task 26 已注明)。这些偏离 spec 字面表述的地方都是为了避免在没有确认底层代码细节时编造不存在的接口，均属于同一方向上更保守、更易验证的简化，不改变对外行为承诺。
- **未在本轮覆盖**：C28/C29(P09)、Registry↔Gateway mTLS、OTel。与 spec「范围外」一致。

