// Package quota is the contract between the control plane, which stores a
// tenant's limits, and the Gateway, which enforces them. It holds the limit
// values and their meaning, and nothing that enforces anything: the token
// bucket lives in the Gateway, because the Gateway is the only side that
// runs it.
//
// It is in common/ for the same reason common/apikey is: two services must
// interpret one piece of data by the same rules. A tenant whose limits mean
// "per minute" in one service and "per second" in the other is not a bug that
// review catches — it is a bug that shows up as a support ticket months later.
//
// quota 包是控制面（存储租户的限制）与 Gateway（执行它们）之间的契约。它保存限制值
// 及其含义，不保存任何执行逻辑：令牌桶在 Gateway 那边，因为只有那一侧运行它。
//
// 它放在 common/ 的理由与 common/apikey 相同：两个服务必须按同一套规则解释同一份
// 数据。一个租户的限制在一个服务里意味着「每分钟」、在另一个里意味着「每秒」，这不是
// 评审能抓到的缺陷——它会在几个月后以一张工单的形式出现。
package quota

import "fmt"

// Limits is what one tenant may consume. A zero field means that dimension is
// unlimited, which is also what an unconfigured tenant gets: adding a limit is
// an explicit act, and a deployment that upgrades into this feature must not
// suddenly start rejecting traffic it accepted yesterday.
//
// The three dimensions are deliberately different questions:
//
//   - RequestsPerMinute bounds call frequency. It is refilled continuously
//     rather than reset on a minute boundary, so a caller cannot spend a whole
//     minute's allowance in one burst at 59 seconds and another at 61.
//   - TokensPerMinute bounds work, which frequency does not: one request with
//     a 100k-token context costs far more than a hundred short ones. It is
//     charged after a response completes, because the cost is not known until
//     the backend reports it — so an over-large request is allowed through and
//     the overspend is paid by the requests behind it.
//   - MaxConcurrent bounds simultaneity. It is the one dimension that protects
//     capacity rather than fairness: a tenant holding a hundred streams open
//     occupies a hundred slots no matter how slowly it opened them.
//
// Limits 是一个租户可以消耗的量。字段为零表示该维度不限制，未配置的租户得到的也是
// 这个：设置限制是一个明确的动作，而一个升级到本功能的部署，绝不能突然开始拒绝它
// 昨天还接受的流量。
//
// 三个维度刻意是三个不同的问题：
//
//   - RequestsPerMinute 限制调用频率。它连续补充而不是按整分钟重置，因此调用方无法
//     在第 59 秒花掉一整分钟的额度、再在第 61 秒花掉另一整分钟的。
//   - TokensPerMinute 限制工作量，而频率衡量不了这个：一次带 10 万 token 上下文的
//     请求，代价远超一百次短请求。它在响应完成之后扣减，因为在后端上报之前无从得知
//     代价——因此一个过大的请求会被放行，超支由排在它后面的请求偿付。
//   - MaxConcurrent 限制同时性。它是三者中唯一保护容量而非公平性的维度：一个租户
//     同时挂着一百条流，就占着一百个槽，无论它开得多慢。
type Limits struct {
	RequestsPerMinute int `json:"requests_per_minute,omitempty"`
	TokensPerMinute   int `json:"tokens_per_minute,omitempty"`
	MaxConcurrent     int `json:"max_concurrent,omitempty"`
}

// Unlimited reports whether no dimension is bounded, which lets a caller skip
// the enforcement path entirely rather than walking three zero checks per
// request.
//
// Unlimited 报告是否没有任何维度受限，这让调用方可以整个跳过执行路径，而不必每个
// 请求走三次零值判断。
func (l Limits) Unlimited() bool {
	return l.RequestsPerMinute <= 0 && l.TokensPerMinute <= 0 && l.MaxConcurrent <= 0
}

// Validate rejects a negative limit. Zero means unlimited and is accepted;
// negative has no meaning this package is willing to invent one for, and
// silently treating it as unlimited would turn a typo into an outage nobody
// notices.
//
// Validate 拒绝负数限制。零表示不限制，是接受的；负数没有本包愿意为它编造的含义，
// 而默默把它当成不限制，会让一个笔误变成没人察觉的失控。
func (l Limits) Validate() error {
	for _, f := range []struct {
		name  string
		value int
	}{
		{"requests_per_minute", l.RequestsPerMinute},
		{"tokens_per_minute", l.TokensPerMinute},
		{"max_concurrent", l.MaxConcurrent},
	} {
		if f.value < 0 {
			return fmt.Errorf("quota: %s must not be negative, got %d", f.name, f.value)
		}
	}
	return nil
}
