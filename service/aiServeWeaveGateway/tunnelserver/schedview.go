package tunnelserver

import (
	"time"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// SchedNode is the scheduler's view of one node: only what node selection
// reads, with the immutable parts shared rather than copied. Nodes builds a
// full NodeInfo (cloned labels, CLI usage, a sorted runtime list, a map of
// idle slots) for every node on every call, which is the right shape for the
// admin API but far too much work for a path that runs once per request per
// routing target.
//
// Labels, Resources and Runtimes are shared with the node and with every
// other reader: they must be treated as read-only. They are never edited in
// place — a change publishes a new value — so a SchedNode stays valid for as
// long as its holder keeps it, merely growing stale. The remaining fields are
// read fresh under the node's lock when the view is built, so they are exactly
// as current as the NodeInfo fields of the same names.
//
// SchedNode 是调度器眼中的一个节点：只含节点选择会读取的内容，不可变的部分共享而非
// 复制。Nodes 每次调用都会为每个节点构造完整的 NodeInfo（克隆标签与 CLI 额度、排序的
// 运行时列表、空闲槽的 map），这对管理 API 是合适的形态，但对一条「每请求、每个路由
// target 执行一次」的路径来说工作量太大。
//
// Labels、Resources 与 Runtimes 与节点及其他所有读者共享，必须视作只读。它们从不被原地
// 修改——变化时会发布一个新值——所以 SchedNode 只要持有者保留着就一直有效，只是会逐渐
// 过时。其余字段在构造视图时于节点锁下实时读取，因此与同名的 NodeInfo 字段一样新。
type SchedNode struct {
	NodeID string
	// Resources, Labels and Runtimes are read-only and shared; see above.
	// Runtimes is sorted by runtime_id.
	//
	// Resources、Labels 与 Runtimes 只读且共享，见上。Runtimes 按 runtime_id 排序。
	Resources *tunnelv1.NodeResources
	Labels    map[string]string
	Runtimes  []runtime.Snapshot
	// IdleInference counts inference-class slots parked right now, the figure
	// NodeInfo.IdleSlots[SLOT_CLASS_INFERENCE] reports.
	//
	// IdleInference 是此刻停放的推理类槽位数，即 NodeInfo.IdleSlots[SLOT_CLASS_INFERENCE]
	// 报告的数字。
	IdleInference    int
	InflightRequests int
	Draining         bool
	Maintenance      bool
	Live             bool
}

// AppendSchedNodes appends the scheduler's view of every node connected to
// this replica to dst and returns it. The order is unspecified (callers that
// need one sort or shuffle it themselves); it neither takes the server lock
// nor allocates anything per node, so a caller that recycles dst pays only for
// one short critical section per node.
//
// Unlike Nodes it is a hot-path call: the scheduler makes it once per routing
// target of every request.
//
// AppendSchedNodes 把本副本上每个已连接节点的调度视图追加到 dst 并返回。顺序不作规定
// （需要某种顺序的调用方自行排序或打乱）；它既不拿服务器锁，也不为每个节点分配内存，
// 因此回收复用 dst 的调用方，只需为每个节点付出一次很短的临界区代价。
//
// 与 Nodes 不同，这是热路径调用：调度器为每个请求的每个路由 target 各调用一次。
func (s *Server) AppendSchedNodes(dst []SchedNode) []SchedNode {
	list := s.nodeList.Load()
	if list == nil {
		return dst
	}
	now := s.clock.Now()
	for _, n := range *list {
		dst = append(dst, n.schedView(now, s.cfg.HeartbeatTimeout))
	}
	return dst
}

// schedView builds n's SchedNode. Its dynamic fields are read under n.mu in a
// single short critical section; the static part comes from the published
// n.static.
//
// schedView 构造 n 的 SchedNode。其动态字段在一次很短的 n.mu 临界区内读取；静态部分
// 来自已发布的 n.static。
func (n *node) schedView(now time.Time, heartbeatTimeout time.Duration) SchedNode {
	v := SchedNode{NodeID: n.id}
	n.mu.Lock()
	v.IdleInference = len(n.idle[tunnelv1.SlotClass_SLOT_CLASS_INFERENCE])
	v.InflightRequests = n.inflight
	v.Draining = n.draining
	v.Maintenance = n.maintenance
	v.Live = n.liveLocked(now, heartbeatTimeout)
	st := n.static.Load()
	n.mu.Unlock()
	if st != nil {
		v.Resources = st.resources
		v.Labels = st.labels
		v.Runtimes = st.runtimes
	}
	return v
}
