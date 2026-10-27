// responsespersist.go is the Gateway's side of STATUS.md's P2 "Responses
// 持久会话": the bounded asynchronous write half (a turn opted into
// store:true) and the synchronous read half (a turn's ancestor chain,
// walked when a caller sends previous_response_id). See the design in
// docs/superpowers/specs/2026-09-16-p2-images-responses-multimodal-boundary-design.md
// §三.
//
// The write half never blocks the response the caller is waiting on, the
// same discipline requestlogpush.go already follows for a different kind of
// record: a control plane hiccup here must not become inference-path
// latency, and a caller who never reads the turn back does not need to wait
// for it to be durable. The read half is the opposite: a caller who names a
// specific previous_response_id needs to know, synchronously, whether that
// conversation could be reconstructed — fabricating a shorter history
// because a background write is still in flight would silently answer a
// different question than the one asked.
//
// responsespersist.go 是 Gateway 一侧对 STATUS.md P2「Responses 持久会话」
// 的落实：有界异步的写入那一半（一轮选择了 store:true 的轮次），以及同步的
// 读取那一半（调用方发来 previous_response_id 时，沿着一轮的祖先链向前走）。
// 设计见
// docs/superpowers/specs/2026-09-16-p2-images-responses-multimodal-boundary-design.md
// 第三节。
//
// 写入那一半绝不阻塞调用方正在等待的响应，与 requestlogpush.go 已经为另一种
// 记录遵循的是同一份纪律：这里的一次控制面故障不能变成推理路径的延迟，而一个
// 从不回读这一轮的调用方，也不需要为它的持久化等待。读取那一半恰恰相反：一个
// 点名了具体 previous_response_id 的调用方，需要同步地知道那段会话能否被重建
// ——因为一次后台写入仍在途就编造出一段更短的历史，等于悄悄回答了一个不同于
// 调用方所问的问题。
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"AIServeWeave/common/runtime"
)

// ResponsesPersistClient is what httpapi needs to persist and continue
// Responses API turns opted into store:true.
//
// It is declared here with primitive-typed parameters rather than by
// importing controlplaneclient's request/response structs, for the same
// reason JobPersistClient is: controlplaneclient already imports this
// package (for Identity and KeyVerifier), so the reverse import would be a
// cycle. controlplaneclient provides an adapter satisfying this interface;
// see its ResponsesPersister.
//
// ResponsesPersistClient 是 httpapi 持久化与续接 Responses API 轮次（选择了
// store:true 的那些）所需的东西。
//
// 这里用原始类型参数声明它，而不是导入 controlplaneclient 的请求/响应
// 结构体，理由与 JobPersistClient 相同：controlplaneclient 已经导入了本包
// （用于 Identity 与 KeyVerifier），反过来导入就会成环。controlplaneclient
// 提供一个满足本接口的适配器，见它的 ResponsesPersister。
type ResponsesPersistClient interface {
	// CreateResponseTurn persists one turn. A duplicate responseID for the
	// same tenant is not an error — the control plane's own idempotent
	// handling, the same shape CreateJob already has.
	//
	// CreateResponseTurn 持久化一轮。同一租户下重复的 responseID 不是
	// 错误——控制面自身的幂等处理，与 CreateJob 已有的是同一种形状。
	CreateResponseTurn(ctx context.Context, responseID, tenantID, previousResponseID, model string, messages json.RawMessage) error
	// GetResponseTurn reads back one turn, scoped to tenantID: the
	// previousResponseID it was stored with (empty for a root turn) and its
	// own Messages contribution. It returns ErrResponseTurnNotFound for an
	// id the control plane has no record of for this tenant — an id
	// belonging to another tenant reads identically, the same
	// tenant-isolation shape store.ErrNotFound keeps everywhere else in this
	// repository.
	//
	// GetResponseTurn 读回一轮，限定在 tenantID 范围内：它存储时携带的
	// previousResponseID（根轮次为空）以及它自己贡献的 Messages。对于控制面
	// 在该租户下没有记录的 id，它返回 ErrResponseTurnNotFound——属于另一个
	// 租户的 id 读起来完全一样，与本仓库别处 store.ErrNotFound 保持的是
	// 同一种租户隔离形状。
	GetResponseTurn(ctx context.Context, tenantID, responseID string) (previousResponseID string, messages json.RawMessage, err error)
}

// ErrResponseTurnNotFound is ResponsesPersistClient.GetResponseTurn's answer
// for an unknown or not-this-tenant's response id.
//
// ErrResponseTurnNotFound 是 ResponsesPersistClient.GetResponseTurn 对一个
// 未知或不属于该租户的 response id 给出的答案。
var ErrResponseTurnNotFound = errors.New("httpapi: response turn not found")

// Defaults for the async response-turn persister. Concurrency bounds how
// many CreateResponseTurn calls this replica has in flight at once — the
// same "bounded, never unbounded" requirement AGENTS.md's security line
// states, applied here with a semaphore instead of requestLogPusher's
// buffered channel, because a turn is one call each rather than a batch.
//
// 异步轮次持久化器的默认值。Concurrency 限制本副本同时在途的
// CreateResponseTurn 调用数——与 AGENTS.md 安全红线「必须有界」是同一条
// 要求，这里用信号量而不是 requestLogPusher 的带缓冲 channel 来实现，因为
// 一轮对应一次调用而不是一个批次。
const (
	DefaultResponsePersistConcurrency = 8
	DefaultResponsePersistCallTimeout = 3 * time.Second
	// maxResponseChainDepth bounds how many ancestor turns loadResponsePrefix
	// will walk when reconstructing a conversation from previous_response_id
	// — the same "bounded, never unbounded" requirement, applied to a chain
	// walk rather than a buffer. A conversation deeper than this is refused
	// with a clear error rather than silently truncated, which would hand
	// the model a shorter history than the caller asked for without saying
	// so.
	//
	// maxResponseChainDepth 限制 loadResponsePrefix 在依据
	// previous_response_id 重建对话时最多沿祖先链走多少轮——同一条「必须
	// 有界」的要求，用在一次链式遍历上而不是一个缓冲区上。深度超出此值的
	// 会话会被明确拒绝，而不是被默默截断——后者会在不声明的情况下，把一段
	// 比调用方要求的更短的历史交给模型。
	maxResponseChainDepth = 50
)

// responsePersister is the bounded asynchronous write half. The zero value
// is not usable; construct one with newResponsePersister. A nil
// *responsePersister degrades every method to a safe no-op, the same
// nil-receiver-safe convention jobPersister's nudge already follows, so
// call sites never need to check h.responsePersist for nil themselves.
//
// responsePersister 是有界异步写入的那一半。零值不可用，请用
// newResponsePersister 构造。一个 nil 的 *responsePersister 会让每个方法
// 退化成安全的空操作——与 jobPersister 的 nudge 已经遵循的同一种「对 nil
// 接收者安全」约定，因此调用点从不需要自己检查 h.responsePersist 是否为 nil。
type responsePersister struct {
	client      ResponsesPersistClient
	sem         chan struct{}
	callTimeout time.Duration
	logger      *slog.Logger
	metrics     *recorder
}

// newResponsePersister builds a persister with zero fields replaced by
// defaults. client must be non-nil — the caller (httpapi.New) only
// constructs one when Config.ResponsesClient is configured, the same
// nil-degrades pattern jobPersister and requestLogPusher already follow.
//
// newResponsePersister 用默认值填补零值字段来构建一个持久化器。client
// 必须非 nil——调用方（httpapi.New）只在配置了 Config.ResponsesClient 时
// 才构造它，与 jobPersister、requestLogPusher 已经遵循的同一种「为 nil
// 时退化」模式。
func newResponsePersister(client ResponsesPersistClient, concurrency int, callTimeout time.Duration, logger *slog.Logger, metrics *recorder) *responsePersister {
	if concurrency <= 0 {
		concurrency = DefaultResponsePersistConcurrency
	}
	if callTimeout <= 0 {
		callTimeout = DefaultResponsePersistCallTimeout
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if metrics == nil {
		metrics = newRecorder(nil)
	}
	return &responsePersister{client: client, sem: make(chan struct{}, concurrency), callTimeout: callTimeout, logger: logger, metrics: metrics}
}

// persist asynchronously writes one turn, never blocking the caller: a full
// concurrency limit drops the turn and counts it rather than waiting for a
// slot, the same discipline requestLogPusher's full buffer already follows
// for the same reason — see AGENTS.md's "任何一跳都不得无界缓冲".
//
// persist 异步写入一轮，绝不阻塞调用方：并发上限已满时丢弃这一轮并计数，
// 而不是等待一个空位——与 requestLogPusher 缓冲已满时已经遵循的是同一份
// 纪律，理由相同——见 AGENTS.md 的「任何一跳都不得无界缓冲」。
func (p *responsePersister) persist(responseID, tenantID, previousResponseID, model string, messages json.RawMessage) {
	if p == nil {
		return
	}
	select {
	case p.sem <- struct{}{}:
	default:
		p.metrics.ResponsePersistDropped()
		return
	}
	go func() {
		defer func() { <-p.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), p.callTimeout)
		defer cancel()
		if err := p.client.CreateResponseTurn(ctx, responseID, tenantID, previousResponseID, model, messages); err != nil {
			p.metrics.ResponsePersistFailed()
			p.logger.Warn("failed to persist a response turn", slog.String("response_id", responseID), slog.Any("error", err))
		}
	}()
}

// get is a synchronous passthrough to the client, used by
// loadResponsePrefix. Unlike persist it is not asynchronous: a caller
// naming a specific previous_response_id is asking a question that needs a
// definite answer before this request can proceed, not a best-effort
// background write.
//
// get 是对 client 的同步透传，供 loadResponsePrefix 使用。与 persist 不同，
// 它不是异步的：一个点名了具体 previous_response_id 的调用方，问的是一个
// 需要确切答案才能让本次请求继续的问题，而不是一次尽力而为的后台写入。
func (p *responsePersister) get(ctx context.Context, tenantID, responseID string) (previousResponseID string, messages json.RawMessage, err error) {
	if p == nil {
		return "", nil, ErrResponseTurnNotFound
	}
	return p.client.GetResponseTurn(ctx, tenantID, responseID)
}

// loadResponsePrefix reconstructs the message prefix a previous_response_id
// refers to, by walking GetResponseTurn one hop at a time from the named
// turn back to its root and concatenating each turn's own Messages
// contribution in chronological order. See model.ResponseTurn's doc comment
// on the control plane side for why a turn stores only its own contribution
// rather than the whole accumulated history.
//
// loadResponsePrefix 重建 previous_response_id 所指的消息前缀：从点名的那
// 一轮起，经由 GetResponseTurn 逐跳向前走到其根，并按时间顺序拼接每一轮自己
// 贡献的 Messages。为什么一轮只存储自己的贡献而不是累积的完整历史，见控制面
// 一侧 model.ResponseTurn 的文档注释。
func (h *handlers) loadResponsePrefix(ctx context.Context, tenantID, responseID string) ([]runtime.ChatMessage, error) {
	var chain [][]runtime.ChatMessage
	id := responseID
	for depth := 0; id != ""; depth++ {
		if depth >= maxResponseChainDepth {
			return nil, fmt.Errorf("httpapi: conversation exceeds %d stored turns; it cannot be continued further", maxResponseChainDepth)
		}
		previous, raw, err := h.responsePersist.get(ctx, tenantID, id)
		if err != nil {
			return nil, err
		}
		var turn []runtime.ChatMessage
		if err := json.Unmarshal(raw, &turn); err != nil {
			return nil, fmt.Errorf("httpapi: a stored response turn is corrupt: %w", err)
		}
		chain = append(chain, turn)
		id = previous
	}
	prefix := make([]runtime.ChatMessage, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		prefix = append(prefix, chain[i]...)
	}
	return prefix, nil
}
