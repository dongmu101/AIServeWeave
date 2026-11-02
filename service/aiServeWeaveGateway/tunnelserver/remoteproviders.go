package tunnelserver

import (
	"fmt"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/remoteprovider"
	"AIServeWeave/common/tunnelwire"
)

// RemoteProvidersState is what this replica knows about one node's remote
// providers: the public key of the certificate on its newest Control stream,
// and the Agent's last report, if any.
//
// RemoteProvidersState 是本副本对一个节点远程供应商情况的了解：最新一条 Control 流所用
// 证书的公钥，以及 Agent 最后一次回报（如有）。
type RemoteProvidersState struct {
	// NodeKey is a DER SubjectPublicKeyInfo. It is only a claim relayed by
	// this replica: the control plane checks it against the Registry before
	// sealing anything to it.
	//
	// NodeKey 是 DER SubjectPublicKeyInfo。它只是本副本转述的声明：控制面在向它封装任何
	// 内容之前，先用 Registry 核对。
	NodeKey   []byte
	Report    remoteprovider.Report
	HasReport bool
}

// SendRemoteProviders forwards a sealed bundle to nodeID's Control streams on
// this replica. The payload is opaque here. Like TriggerModelPull it fails when
// the node has no Control stream on this replica, and its effect is only
// observable from the report that follows.
//
// SendRemoteProviders 把已封装的打包转发到 nodeID 在本副本上的 Control 流。负载在这里
// 不透明。与 TriggerModelPull 一样，节点在本副本上没有 Control 流时失败，其效果只能从
// 随后的回报中观察到。
func (s *Server) SendRemoteProviders(nodeID string, version uint64, sealed []byte) error {
	n, ok := s.lookup(nodeID)
	if !ok {
		return fmt.Errorf("tunnelserver: node %q is not connected to this replica", nodeID)
	}
	n.mu.Lock()
	connected := len(n.controls) > 0
	n.mu.Unlock()
	if !connected {
		return fmt.Errorf("tunnelserver: node %q has no active control stream on this replica", nodeID)
	}
	n.broadcast(&tunnelv1.GatewayControl{Body: &tunnelv1.GatewayControl_RemoteProviders{
		RemoteProviders: tunnelwire.RemoteProvidersToProto(version, sealed),
	}})
	return nil
}

// RemoteProvidersStatus returns this replica's view of nodeID, and whether the
// node is known here at all.
//
// RemoteProvidersStatus 返回本副本对 nodeID 的视图，以及本副本是否知道这个节点。
func (s *Server) RemoteProvidersStatus(nodeID string) (RemoteProvidersState, bool) {
	n, ok := s.lookup(nodeID)
	if !ok {
		return RemoteProvidersState{}, false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return RemoteProvidersState{
		NodeKey:   append([]byte(nil), n.nodeKey...),
		Report:    n.remoteProviders,
		HasReport: n.hasRemoteProviders,
	}, true
}

// applyRemoteProvidersReport records the Agent's latest remote provider report.
//
// applyRemoteProvidersReport 记录 Agent 最新的远程供应商回报。
func (n *node) applyRemoteProvidersReport(r remoteprovider.Report) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.remoteProviders = r
	n.hasRemoteProviders = true
}
