// usageledger.go defines the record shape and closed endpoint vocabulary for
// STATUS.md's P2 "按租户/模型的持久化用量账本": a durable record of what
// tokens a tenant actually consumed, per model, replacing the Prometheus
// gateway_tokens_total counter (which resets on every replica restart and
// carries no model dimension — see metrics.go's own comment on why model
// never becomes a Prometheus label) as the thing a settlement process reads.
//
// The model value recorded here is never raw client free text: recordUsage
// (ratelimit.go) is called only from the dispatch success path, after the
// scheduler has already resolved the request against a modelroute alias —
// the same bounding metrics.go's comment describes as the reason per-model
// accounting belongs in usage records rather than Prometheus labels.
//
// usageledger.go 定义 STATUS.md P2「按租户/模型的持久化用量账本」的记录形状
// 与封闭 endpoint 词汇表：一份租户实际消耗了多少 token（按模型区分）的持久化
// 记录，取代 Prometheus 的 gateway_tokens_total 计数器（每次副本重启即归零，
// 也不带模型维度——原因见 metrics.go 自己关于模型为何从不成为 Prometheus
// 标签的说明）成为供结算流程读取的依据。
//
// 这里记录的模型值从不是客户端的自由文本：recordUsage（ratelimit.go）只在
// 派发成功路径上被调用，此时调度器早已把请求解析到了一个 modelroute 别名
// 上——与 metrics.go 注释里"按模型记账应属于用量记录而非 Prometheus 标签"
// 这条理由所依赖的是同一个约束。
package httpapi

import "time"

// Usage-ledger endpoint values, the closed set usage_records.endpoint
// accepts. One value per protocol front door that reports runtime.Usage —
// audio transcription and rerank are not token-metered (see A06 and the P2
// rerank/audio design docs) and never call recordUsage, so they have no
// value here.
//
// 用量账本 endpoint 取值，是 usage_records.endpoint 接受的封闭集合。每一个
// 会上报 runtime.Usage 的协议前门对应一个取值——音频转录与 rerank 不按
// token 计量（见 A06 与 P2 rerank/音频设计文档），从不调用 recordUsage，
// 因此这里没有它们的取值。
const (
	UsageEndpointChat              = "chat"
	UsageEndpointEmbeddings        = "embeddings"
	UsageEndpointResponses         = "responses"
	UsageEndpointAnthropicMessages = "anthropic_messages"
	UsageEndpointOllamaChat        = "ollama_chat"
	UsageEndpointOllamaGenerate    = "ollama_generate"
	UsageEndpointOllamaEmbeddings  = "ollama_embeddings"
)

// UsageRecord is one billable request's token usage, ready to be pushed to
// the control plane. RequestID doubles as the control plane's dedup key
// (STATUS.md P2's stated "去重规则"): the control plane's CreateUsageRecords
// inserts with ON CONFLICT DO NOTHING keyed on it, so a record pushed more
// than once — a retried batch, or a future retrying pusher — is charged
// exactly once, mirroring RequestLogRecord's own idempotency shape.
//
// UsageRecord 是一次可计费请求的 token 用量，供推送至控制面。RequestID 同时
// 充当控制面的去重键（STATUS.md P2 所要求的"去重规则"）：控制面的
// CreateUsageRecords 以它为键做 ON CONFLICT DO NOTHING 插入，因此一条被
// 推送超过一次的记录——一次被重试的批次，或未来某个会重试的推送器——只会
// 被计费一次，与 RequestLogRecord 自身的幂等形状相同。
type UsageRecord struct {
	RequestID        string
	TenantID         string
	Model            string
	Endpoint         string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	CreatedAt        time.Time
}

// usageLedgerSink is what recordUsage hands a finished usage record to. It
// is satisfied by the bounded background pusher (usageledgerpush.go); tests
// use a fake. enqueue reports whether the record was accepted, purely so
// recordUsage's own tests can observe the outcome — recordUsage never acts
// differently on false beyond a metric, since a dropped record is the
// sink's own bounded-buffer policy, matching requestLogSink's contract.
//
// usageLedgerSink 是 recordUsage 把一条完成的用量记录交付给的对象。它由
// 有界后台推送器（usageledgerpush.go）实现；测试中用假实现替代。enqueue
// 报告该记录是否被接受，纯粹是为了让 recordUsage 自己的测试能够观察结果——
// recordUsage 除了记一次指标外，从不因 false 而采取不同行动，因为一条记录
// 被丢弃是接收端自己的有界缓冲策略，与 requestLogSink 的契约相同。
type usageLedgerSink interface {
	enqueue(UsageRecord) bool
}
