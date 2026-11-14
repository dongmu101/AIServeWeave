package scheduler

import (
	"fmt"
	"hash"
	"hash/fnv"
	"slices"

	"AIServeWeave/common/runtime"
)

// Policy names how the candidates inside one routing target are ordered. It
// never reaches across targets: priority and weight stay the operator's, and
// a policy only decides which of the equally preferred (node, runtime) pairs
// is tried first.
//
// Policy 指定同一个路由 target 内部的候选如何排序。它从不跨越 target：优先级与权重
// 仍归运维所有，策略只决定在同等偏好的 (node, runtime) 对之中先试哪一个。
type Policy string

const (
	// PolicyLeastLoad tries the candidate with the most idle inference slots
	// first, then the lowest reported queue occupancy, then the fewest
	// in-flight requests. It is the default and the behavior that predates
	// Policy.
	//
	// PolicyLeastLoad 先试空闲推理槽最多的候选，其次是上报队列占用最低的，再次是在途
	// 请求最少的。它是默认值，也是 Policy 出现之前的既有行为。
	PolicyLeastLoad Policy = "least_load"

	// PolicyPowerOfTwo samples two candidates at random and tries the less
	// loaded of the pair first; the rest follow in least-load order. Every
	// replica reading the same stale heartbeat figures then stops agreeing on
	// one "least loaded" node, at the price of sometimes not picking the
	// single best one.
	//
	// PolicyPowerOfTwo 随机抽两个候选，先试两者中负载较低的那个；其余候选按最少负载
	// 顺序排在后面。这样各副本读到同一份滞后的心跳数据时，不会全都认定同一个「最空闲」
	// 节点，代价是有时选不到唯一最优的那个。
	PolicyPowerOfTwo Policy = "power_of_two"

	// PolicyPrefixHash sends requests that share a prompt prefix to the same
	// candidate, so a backend's prompt/prefix cache (Ollama's per-slot cache,
	// vLLM/SGLang prefix caching) is reused across turns. Candidates are
	// ranked by rendezvous hashing of the prefix, with any candidate that has
	// no idle inference slot moved behind those that do. A request without a
	// usable prefix (embeddings, rerank, images, transcription, workflows)
	// falls back to PolicyLeastLoad.
	//
	// PolicyPrefixHash 把共享同一段提示前缀的请求发往同一个候选，让后端的提示/前缀
	// 缓存（Ollama 的按槽缓存、vLLM/SGLang 的前缀缓存）在多轮之间得到复用。候选按前缀
	// 的 rendezvous 哈希排序，没有空闲推理槽的候选移到有空闲槽的之后。没有可用前缀的
	// 请求（嵌入、rerank、图像、转录、工作流）退回 PolicyLeastLoad。
	PolicyPrefixHash Policy = "prefix_hash"
)

// defaultPrefixBytes is Config.PrefixBytes when a caller leaves it unset.
// 4 KiB covers a typical system prompt plus the opening user message, which is
// the part a conversation's later turns repeat verbatim.
//
// defaultPrefixBytes 是调用方未设置 Config.PrefixBytes 时使用的默认值。4 KiB
// 足以覆盖一段典型的系统提示加上开场的用户消息，正是一段对话后续各轮逐字重复的部分。
const defaultPrefixBytes = 4096

// ParsePolicy maps an operator-supplied name onto a Policy. The empty string
// means PolicyLeastLoad; any other unknown name is an error, so a typo in a
// flag fails at startup instead of silently scheduling by the default.
//
// ParsePolicy 把运维提供的名字映射为 Policy。空字符串表示 PolicyLeastLoad；其他未知
// 名字一律报错，这样 flag 里的笔误会在启动时失败，而不是悄悄按默认值调度。
func ParsePolicy(name string) (Policy, error) {
	switch Policy(name) {
	case "", PolicyLeastLoad:
		return PolicyLeastLoad, nil
	case PolicyPowerOfTwo, PolicyPrefixHash:
		return Policy(name), nil
	}
	return "", fmt.Errorf("scheduler: unknown policy %q (want %s, %s or %s)", name, PolicyLeastLoad, PolicyPowerOfTwo, PolicyPrefixHash)
}

// affinity is a request's prefix fingerprint for PolicyPrefixHash, or its
// session key's hash (see WithSessionKey). ok is false when the request
// carries neither, which every policy treats as "no preference". session marks
// a session key, which ranks by rendezvous hashing whatever the configured
// policy is. Only the hash is kept: the prompt text itself never leaves the
// function that computed it.
//
// affinity 是请求供 PolicyPrefixHash 使用的前缀指纹，或它的会话 key 的哈希（见
// WithSessionKey）。请求两者都没有时 ok 为 false，所有策略都把它当作「没有偏好」。
// session 标记这是会话 key，无论配置的策略是什么，都按 rendezvous 哈希排序。只保留
// 哈希：提示文本本身从不离开计算它的函数。
type affinity struct {
	hash    uint64
	ok      bool
	session bool
}

// scored is one eligible candidate together with the load figures every
// policy ranks on.
//
// scored 是一个有资格的候选及各策略据以排序的负载数据。
type scored struct {
	Candidate
	idle     int
	queued   int
	inflight int
	// pressure means the node reported resource pressure on its last
	// heartbeat (tunnelserver.SchedNode.Pressure). Every policy tries such a
	// candidate last; it is not skipped, because the Agent's own admission is
	// the hard gate and a pressured node may still be the only one able to
	// serve.
	//
	// pressure 表示节点在最近一次心跳报告了资源紧张（tunnelserver.SchedNode.Pressure）。
	// 所有策略都把这样的候选放到最后尝试；它不会被跳过，因为 Agent 自己的准入才是硬性
	// 闸门，而一个资源紧张的节点仍可能是唯一能服务的那个。
	pressure bool
}

// lessLoaded is the least-load comparator: more idle slots first, then lower
// queue occupancy, then fewer in-flight requests.
//
// queued (STATUS.md's P2 realtime-utilization signal) breaks ties inside an
// idle tier, before inflight: idle is a software slot count that a
// single-job-at-a-time backend like ComfyUI can report as high even while its
// one GPU is saturated (the necessity assessment in
// docs/superpowers/specs/2026-09-16-p2-realtime-utilization-necessity-design.md
// documents why), so idle alone cannot distinguish two such candidates. It is
// 0 for every runtime kind that does not report it, which sorts the same as
// "confirmed empty" — never a penalty for not reporting.
//
// lessLoaded 是最少负载比较器：空闲槽多的优先，其次队列占用低的，再次在途请求少的。
//
// queued（STATUS.md P2 的实时利用率信号）在同一个 idle 档位内、排在 inflight 之前
// 打破平局：idle 是一个软件层面的槽位计数，像 ComfyUI 这种同一时间只跑一个任务的后端
// 即便唯一的 GPU 已经跑满也能上报很高的 idle（必要性评估文档
// docs/superpowers/specs/2026-09-16-p2-realtime-utilization-necessity-design.md
// 记录了原因），所以单靠 idle 无法区分这样两个候选。对不上报该信号的运行时种类它恒为
// 0，排序上等同于"确认空闲"——不上报这件事本身从不被当作惩罚。
func lessLoaded(a, b scored) bool {
	if a.pressure != b.pressure {
		return !a.pressure
	}
	if a.idle != b.idle {
		return a.idle > b.idle
	}
	if a.queued != b.queued {
		return a.queued < b.queued
	}
	return a.inflight < b.inflight
}

// stableSort sorts found in place by less, keeping the order of equal
// elements, which is sort.SliceStable's contract without its reflection-based
// swapper and its O(n log^2 n) merge steps; those dominated the cost of
// ranking several hundred candidates on every request.
//
// It sorts a slice of indices instead of the (large) elements, breaking ties
// on the index, which yields exactly the order a stable sort would, and then
// applies that permutation to found in place. less receives pointers so a
// comparison copies nothing.
//
// stableSort 按 less 原地对 found 排序，并保持相等元素的原有顺序。这是 sort.SliceStable
// 的约定，却没有它基于反射的交换器与 O(n log^2 n) 的归并步骤——在每个请求都要给几百个
// 候选排序时，那正是主要开销。
//
// 它对下标切片而不是（较大的）元素排序，并以下标打破平局，结果与稳定排序给出的顺序
// 完全一致，随后把这一排列原地应用到 found 上。less 接收指针，使一次比较不复制任何东西。
func stableSort(found []scored, less func(a, b *scored) bool) {
	n := len(found)
	if n < 2 {
		return
	}
	idx := make([]int32, n)
	for i := range idx {
		idx[i] = int32(i)
	}
	slices.SortFunc(idx, func(i, j int32) int {
		a, b := &found[i], &found[j]
		switch {
		case less(a, b):
			return -1
		case less(b, a):
			return 1
		}
		return int(i - j)
	})
	// Apply the permutation (result[k] = old[idx[k]]) by following its
	// cycles; idx[k] = k marks a position as placed.
	// 沿排列的各个环应用该排列（result[k] = old[idx[k]]）；idx[k] = k 表示该位置已就位。
	for start := range idx {
		if int(idx[start]) == start {
			continue
		}
		held := found[start]
		k := start
		for {
			src := int(idx[k])
			idx[k] = int32(k)
			if src == start {
				found[k] = held
				break
			}
			found[k] = found[src]
			k = src
		}
	}
}

// orderCandidates sorts found in place according to policy. intn supplies the
// randomness PolicyPowerOfTwo needs, so tests can make it deterministic. An
// unrecognized policy orders as PolicyLeastLoad; operator input should go
// through ParsePolicy to reject typos before it gets here.
//
// orderCandidates 按 policy 原地排序 found。intn 提供 PolicyPowerOfTwo 所需的随机性，
// 以便测试使其确定。无法识别的策略按 PolicyLeastLoad 排序；运维输入应先经 ParsePolicy
// 拒绝笔误，再到达这里。
func orderCandidates(found []scored, policy Policy, key affinity, intn func(int) int) {
	stableSort(found, func(a, b *scored) bool { return lessLoaded(*a, *b) })
	// Whatever order the policy settles on, candidates under resource
	// pressure go last, keeping their relative order as retry fallbacks.
	// 无论策略最终排成什么顺序，资源紧张的候选都排到最后，并保持彼此的相对顺序作为重试回退。
	defer pressureLast(found)

	switch policy {
	case PolicyPowerOfTwo:
		if len(found) < 2 {
			return
		}
		i := intn(len(found))
		j := intn(len(found) - 1)
		if j >= i {
			j++
		}
		winner := i
		if lessLoaded(found[j], found[i]) {
			winner = j
		}
		// Rotate the winner to the front so the remaining candidates keep
		// their least-load order as retry fallbacks.
		//
		// 把胜者轮转到最前，其余候选保持最少负载顺序作为重试的回退。
		w := found[winner]
		copy(found[1:winner+1], found[:winner])
		found[0] = w

	case PolicyPrefixHash:
		if !key.ok {
			return
		}
		ranks := make(map[Candidate]uint64, len(found))
		for _, f := range found {
			ranks[f.Candidate] = rendezvousScore(key.hash, f.Candidate)
		}
		// A saturated candidate would answer backpressure and cost a wasted
		// attempt, so the preferred order only holds among candidates that
		// can take the request now. Spill is per key, so one hot prefix fills
		// its nodes one after another instead of scattering.
		//
		// 已满的候选只会回答背压、白白浪费一次尝试，所以偏好顺序只在此刻能接下请求的
		// 候选之间成立。溢出顺序按 key 确定，一个热门前缀会依次填满它的节点，而不是
		// 四处散开。
		stableSort(found, func(a, b *scored) bool {
			if (a.idle > 0) != (b.idle > 0) {
				return a.idle > 0
			}
			return ranks[a.Candidate] > ranks[b.Candidate]
		})
	}
}

// pressureLast stably moves candidates under resource pressure behind the
// rest. It allocates nothing when no candidate is under pressure, which is the
// common case on the hot path.
//
// pressureLast 把资源紧张的候选稳定地移到其余候选之后。没有候选资源紧张时（热路径上的
// 常见情形）不做任何分配。
func pressureLast(found []scored) {
	first := slices.IndexFunc(found, func(s scored) bool { return s.pressure })
	if first < 0 {
		return
	}
	held := make([]scored, 0, len(found)-first)
	k := first
	for _, s := range found[first:] {
		if s.pressure {
			held = append(held, s)
			continue
		}
		found[k] = s
		k++
	}
	copy(found[k:], held)
}

// rendezvousScore is the highest-random-weight score of c for key. Each
// candidate's score depends only on the key and the candidate itself, so a
// node joining or leaving moves only the prefixes that ranked it first —
// unlike hash-mod-N, which reshuffles nearly every prefix. FNV is used rather
// than hash/maphash because every Gateway replica must agree on the ranking.
//
// rendezvousScore 是 c 对 key 的最高随机权重得分。每个候选的得分只取决于 key 和候选
// 自身，因此节点加入或离开时，只有原本把它排第一的前缀会迁移——不像哈希取模那样几乎
// 打乱所有前缀。用 FNV 而不是 hash/maphash，是因为每个 Gateway 副本必须得出同一个排序。
func rendezvousScore(key uint64, c Candidate) uint64 {
	h := fnv.New64a()
	h.Write([]byte(c.NodeID))
	h.Write([]byte{0})
	h.Write([]byte(c.RuntimeID))
	return mix64(key ^ h.Sum64())
}

// mix64 is the SplitMix64 finalizer, spreading the XOR of two FNV sums over
// all 64 bits. / mix64 是 SplitMix64 的终结函数，把两个 FNV 值的异或结果散布到全部 64 位。
func mix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// prefixAffinity fingerprints the part of req a conversation's later turns
// repeat verbatim: every message up to and including the first user message
// (system and developer prompts, plus the opening question), capped at limit
// bytes. Turn one and turn five of the same conversation therefore hash
// alike, while two conversations that share only a system prompt shorter
// than limit hash apart. Tools are left out: they do not distinguish one
// conversation from another under the same client.
//
// prefixAffinity 对 req 中一段对话后续各轮会逐字重复的部分取指纹：直到并包括第一条
// 用户消息为止的所有消息（系统与开发者提示，加上开场的问题），最多 limit 字节。因此
// 同一段对话的第一轮与第五轮哈希相同，而只共享一段短于 limit 的系统提示的两段对话哈希
// 不同。工具定义不计入：在同一个客户端下，它们区分不出两段对话。
func prefixAffinity(req runtime.ChatRequest, limit int) affinity {
	if limit <= 0 {
		limit = defaultPrefixBytes
	}
	w := &cappedHash{h: fnv.New64a(), left: limit}
	content := 0 // bytes of message content hashed, separators excluded / 已哈希的消息内容字节数，不含分隔符
	for _, m := range req.Messages {
		w.write(m.Role + "\x00")
		content += w.write(m.Content)
		for _, part := range m.ContentParts {
			w.write("\x1f" + part.Type + "\x00")
			switch {
			case part.ImageURL != nil:
				content += w.write(part.ImageURL.URL)
			case part.Audio != nil:
				content += w.write(part.Audio.Data)
			case part.File != nil:
				content += w.write(part.File.URL)
			default:
				content += w.write(part.Text)
			}
		}
		w.write("\x1e")
		if m.Role == "user" || w.left == 0 {
			break
		}
	}
	if content == 0 {
		return affinity{}
	}
	return affinity{hash: w.h.Sum64(), ok: true}
}

// cappedHash feeds at most left bytes into h, silently dropping the rest.
// cappedHash 最多向 h 写入 left 字节，超出部分静默丢弃。
type cappedHash struct {
	h    hash.Hash64
	left int
}

// write hashes as much of s as the cap allows and reports how many bytes that
// was. / write 在上限允许的范围内哈希 s，并返回实际写入的字节数。
func (c *cappedHash) write(s string) int {
	if len(s) > c.left {
		s = s[:c.left]
	}
	if s == "" {
		return 0
	}
	c.h.Write([]byte(s))
	c.left -= len(s)
	return len(s)
}
