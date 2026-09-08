// Package registryclient is this control plane's client for the Registry's
// TokenAdmin service (STATUS.md's P01): node approval, disable/enable and
// maintenance are node-identity operations, and node identity is the
// Registry's own account book (service/aiServeWeaveRegistry/internal/
// identitystore), not something this service persists a second copy of. A
// platform operator's request to approve or disable a node_id is forwarded
// here rather than answered from a local table, so there is exactly one
// authoritative record of a node's expected state.
//
// This is the first package in the control plane to speak gRPC to another
// service. That is a deliberate, narrow exception to the service's usual
// go-zero/gorm/net-http shape, for the same reason the Gateway's own
// registryclient package exists: TokenAdmin is guarded by a bearer token
// over plain TLS, exactly the transport the Registry's own CLI client (see
// service/aiServeWeaveRegistry/main.go's runTokenAdminClient) already uses,
// and duplicating that as a second, incompatible protocol would be strictly
// worse than importing the one dependency (google.golang.org/grpc) every
// other service in this repository already carries.
//
// Package registryclient 是本控制面对 Registry TokenAdmin 服务（STATUS.md 的
// P01）的客户端：节点审批、禁用/启用与维护都是节点身份操作，而节点身份是
// Registry 自己的账本（service/aiServeWeaveRegistry/internal/identitystore），
// 不是本服务要再存一份的东西。平台运维发起的批准或禁用某个 node_id 的请求，
// 在这里被转发出去，而不是靠本地一张表作答，这样节点的期望状态就只有一处
// 权威记录。
//
// 这是控制面里第一个用 gRPC 与另一个服务对话的包。这是对本服务惯常的
// go-zero/gorm/net-http 形状的一次刻意的、范围很窄的例外，理由与 Gateway 自己
// 的 registryclient 包存在的理由相同：TokenAdmin 由一个跑在纯 TLS 之上的
// bearer token 守卫，这正是 Registry 自己的 CLI 客户端（见
// service/aiServeWeaveRegistry/main.go 的 runTokenAdminClient）已经在用的
// 传输方式，重新发明一套不兼容的协议，只会比引入这一个依赖
// （google.golang.org/grpc，本仓库其余每个服务本就携带）更糟。
package registryclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
)

// DefaultTimeout bounds one call to the Registry.
//
// DefaultTimeout 限制对 Registry 的单次调用。
const DefaultTimeout = 5 * time.Second

// Config configures a Client.
//
// Config 配置一个 Client。
type Config struct {
	// Addr is the Registry's TokenAdmin endpoint, host:port.
	//
	// Addr 是 Registry 的 TokenAdmin 端点，形如 host:port。
	Addr string
	// CAFile is the PEM bundle used to verify the Registry's server
	// certificate. Empty uses the host's root store, which only works when
	// the Registry's certificate chains to a public CA — a self-issued
	// Registry CA (the common case) needs this set to that CA's bundle.
	//
	// CAFile 是用于校验 Registry 服务端证书的 PEM 证书包。留空则使用宿主机的
	// 根证书库，这只在 Registry 的证书链最终指向一个公共 CA 时才有效——一个
	// 自签的 Registry CA（常见情形）需要把它设为该 CA 的证书包。
	CAFile string
	// AdminToken authenticates this client to TokenAdmin, matching the
	// Registry's -admin-token-file.
	//
	// AdminToken 用于向 TokenAdmin 表明身份，须与 Registry 的
	// -admin-token-file 一致。
	AdminToken string
	// Timeout bounds one call. Zero uses DefaultTimeout.
	//
	// Timeout 限制单次调用。零值使用 DefaultTimeout。
	Timeout time.Duration
}

// Client calls the Registry's TokenAdmin service on behalf of a platform
// operator (STATUS.md's P01). Construct one with New; it holds one
// persistent connection, unlike the Gateway's registryclient, because this
// client makes occasional request/response calls rather than joining a
// long-lived stream.
//
// Client 代表一名平台运维调用 Registry 的 TokenAdmin 服务（STATUS.md 的
// P01）。用 New 构造；它持有一个长期连接，这与 Gateway 的 registryclient 不同
// ——本客户端发起的是偶发的请求/响应调用，而不是加入一条长期存在的流。
type Client struct {
	conn       *grpc.ClientConn
	admin      tunnelv1.TokenAdminClient
	adminToken string
	timeout    time.Duration
}

// New dials addr and returns a Client. The dial is lazy at the gRPC level —
// grpc.NewClient does not block on connecting — so New returning without
// error is not proof the Registry is reachable; the first call is.
//
// New 拨号 addr 并返回一个 Client。拨号在 gRPC 层面是惰性的——grpc.NewClient
// 不会阻塞等待连接建立——因此 New 无错返回并不能证明 Registry 可达；第一次
// 调用才能证明这一点。
func New(cfg Config) (*Client, error) {
	if cfg.Addr == "" {
		return nil, errors.New("registryclient: Addr is required")
	}
	if cfg.AdminToken == "" {
		return nil, errors.New("registryclient: AdminToken is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	creds, err := clientCredentials(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(cfg.Addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, err
	}
	return &Client{
		conn:       conn,
		admin:      tunnelv1.NewTokenAdminClient(conn),
		adminToken: cfg.AdminToken,
		timeout:    timeout,
	}, nil
}

// Close releases the underlying connection.
//
// Close 释放底层连接。
func (c *Client) Close() error { return c.conn.Close() }

// authCtx bounds ctx to c.timeout and attaches the admin token, the way
// every TokenAdmin call must.
//
// authCtx 把 ctx 限定在 c.timeout 之内，并附上 admin token——每次 TokenAdmin
// 调用都必须如此。
func (c *Client) authCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.adminToken)
	return ctx, cancel
}

// ApproveNode calls TokenAdmin.ApproveNode.
//
// ApproveNode 调用 TokenAdmin.ApproveNode。
func (c *Client) ApproveNode(ctx context.Context, nodeID string) error {
	ctx, cancel := c.authCtx(ctx)
	defer cancel()
	_, err := c.admin.ApproveNode(ctx, &tunnelv1.ApproveNodeRequest{NodeId: nodeID})
	return err
}

// DisableNode calls TokenAdmin.DisableNode.
//
// DisableNode 调用 TokenAdmin.DisableNode。
func (c *Client) DisableNode(ctx context.Context, nodeID string) error {
	ctx, cancel := c.authCtx(ctx)
	defer cancel()
	_, err := c.admin.DisableNode(ctx, &tunnelv1.DisableNodeRequest{NodeId: nodeID})
	return err
}

// EnableNode calls TokenAdmin.EnableNode.
//
// EnableNode 调用 TokenAdmin.EnableNode。
func (c *Client) EnableNode(ctx context.Context, nodeID string) error {
	ctx, cancel := c.authCtx(ctx)
	defer cancel()
	_, err := c.admin.EnableNode(ctx, &tunnelv1.EnableNodeRequest{NodeId: nodeID})
	return err
}

// SetMaintenance calls TokenAdmin.SetMaintenance.
//
// SetMaintenance 调用 TokenAdmin.SetMaintenance。
func (c *Client) SetMaintenance(ctx context.Context, nodeID string) error {
	ctx, cancel := c.authCtx(ctx)
	defer cancel()
	_, err := c.admin.SetMaintenance(ctx, &tunnelv1.SetMaintenanceRequest{NodeId: nodeID})
	return err
}

// ClearMaintenance calls TokenAdmin.ClearMaintenance.
//
// ClearMaintenance 调用 TokenAdmin.ClearMaintenance。
func (c *Client) ClearMaintenance(ctx context.Context, nodeID string) error {
	ctx, cancel := c.authCtx(ctx)
	defer cancel()
	_, err := c.admin.ClearMaintenance(ctx, &tunnelv1.ClearMaintenanceRequest{NodeId: nodeID})
	return err
}

// NodeState is one node_id's ledger entry, as ListNodeStates reports it —
// a plain copy of tunnelv1.NodeState so that logic, which must not import
// the tunnel proto package (STATUS.md's contract boundary is the three
// tunnel services, not the control plane), can depend on this shape
// instead.
//
// NodeState 是一个 node_id 在账本里的条目，即 ListNodeStates 所报告的内容——
// 是 tunnelv1.NodeState 的一份朴素拷贝，好让不该导入隧道 proto 包的 logic
// 层（STATUS.md 的契约边界是三个隧道服务，不包括控制面）可以依赖这个形状
// 而不是那一个。
type NodeState struct {
	NodeID          string
	PendingApproval bool
	Disabled        bool
	Maintenance     bool
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
}

// ListNodeStates calls TokenAdmin.ListNodeStates.
//
// ListNodeStates 调用 TokenAdmin.ListNodeStates。
func (c *Client) ListNodeStates(ctx context.Context) ([]NodeState, error) {
	ctx, cancel := c.authCtx(ctx)
	defer cancel()
	resp, err := c.admin.ListNodeStates(ctx, &tunnelv1.ListNodeStatesRequest{})
	if err != nil {
		return nil, err
	}
	states := make([]NodeState, 0, len(resp.GetStates()))
	for _, st := range resp.GetStates() {
		states = append(states, NodeState{
			NodeID:          st.GetNodeId(),
			PendingApproval: st.GetPendingApproval(),
			Disabled:        st.GetDisabled(),
			Maintenance:     st.GetMaintenance(),
			FirstSeenAt:     st.GetFirstSeenAt().AsTime(),
			LastSeenAt:      st.GetLastSeenAt().AsTime(),
		})
	}
	return states, nil
}

// clientCredentials mirrors the Gateway registryclient package's helper of
// the same name: empty caFile trusts the host's root store, otherwise it
// trusts exactly the bundle at caFile.
//
// clientCredentials 与 Gateway registryclient 包中的同名函数一致：caFile
// 为空时信任宿主机的根证书库，否则只信任 caFile 处的那一份证书包。
func clientCredentials(caFile string) (credentials.TransportCredentials, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("registryclient: no certificate found in " + caFile)
		}
		tlsCfg.RootCAs = pool
	}
	return credentials.NewTLS(tlsCfg), nil
}
