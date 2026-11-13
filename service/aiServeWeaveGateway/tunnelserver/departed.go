package tunnelserver

import "time"

// departedNode is the scheduler view a node had when it was forgotten, kept
// for Config.DepartedRetention so the scheduler can tell a model whose only
// node is reconnecting from a model no node has ever served.
//
// departedNode 是节点被遗忘时的调度视图，保留 Config.DepartedRetention 时长，使调度器
// 能区分「唯一的节点正在重连」与「从来没有节点提供过」这两种模型。
type departedNode struct {
	view SchedNode
	at   time.Time
}

// recordDepartureLocked remembers n's last inventory and drops departures
// older than the retention. The caller holds s.mu for writing; n.mu is taken
// inside, the same s.mu-then-n.mu order forget already uses.
//
// recordDepartureLocked 记住 n 最后的清单，并丢弃超过保留时长的离开记录。调用方须持有
// s.mu 写锁；内部会再取 n.mu，与 forget 已有的先 s.mu 后 n.mu 顺序一致。
func (s *Server) recordDepartureLocked(n *node) {
	now := s.clock.Now()
	for id, d := range s.departed {
		if now.Sub(d.at) > s.cfg.DepartedRetention {
			delete(s.departed, id)
		}
	}
	view := n.schedView(now, s.cfg.HeartbeatTimeout)
	if len(view.Runtimes) == 0 {
		return
	}
	view.Live = false
	view.IdleInference = 0
	view.InflightRequests = 0
	if s.departed == nil {
		s.departed = make(map[string]departedNode)
	}
	s.departed[n.id] = departedNode{view: view, at: now}
}

// AppendDepartedNodes appends the last view of every node that left this
// replica within Config.DepartedRetention and has not reconnected, and
// returns dst. Revoked and maintenance nodes are left out: they are not
// expected back on their own. Every appended entry has Live false, so no
// selection that requires a live node can pick it.
//
// It takes the server lock and is meant for the rare path where a request
// found no candidate, never for per-request selection.
//
// AppendDepartedNodes 把在 Config.DepartedRetention 内离开本副本且尚未重连的每个节点的
// 最后视图追加到 dst 并返回。已吊销与维护中的节点不包括在内：它们不会自行回来。追加的
// 每个条目 Live 都为 false，因此任何要求存活节点的选择都不会选中它。
//
// 它会获取服务器锁，只用于请求找不到候选的少见路径，不用于每个请求的选择。
func (s *Server) AppendDepartedNodes(dst []SchedNode) []SchedNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.clock.Now()
	for id, d := range s.departed {
		if now.Sub(d.at) > s.cfg.DepartedRetention {
			continue
		}
		if _, revoked := s.revoked[id]; revoked {
			continue
		}
		if _, maintenance := s.maintenance[id]; maintenance {
			continue
		}
		dst = append(dst, d.view)
	}
	return dst
}
