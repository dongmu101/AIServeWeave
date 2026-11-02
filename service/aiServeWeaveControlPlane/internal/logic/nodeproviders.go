package logic

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"AIServeWeave/common/nodeaccess"
	"AIServeWeave/common/remoteprovider"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/remoteproviderrouter"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/store"
)

// RemoteProviderRouter forwards sealed bundles to the Gateway replicas and
// reads each node's certificate keys and report back. It is implemented by
// remoteproviderrouter.Router and is an interface for the reason
// ModelPullRouter is.
//
// RemoteProviderRouter 把已封装的打包转发给 Gateway 副本，并读回每个节点的证书公钥与回报。
// 它由 remoteproviderrouter.Router 实现，作为接口的理由与 ModelPullRouter 相同。
type RemoteProviderRouter interface {
	Push(ctx context.Context, nodeID string, version uint64, sealed []byte) (remoteproviderrouter.Result, error)
	Status(ctx context.Context, nodeID string) (remoteproviderrouter.Result, error)
}

// NodeProviderConfig enables provisioning hosted providers on nodes.
//
// NodeProviderConfig 启用向节点下发托管供应商。
type NodeProviderConfig struct {
	Router  RemoteProviderRouter
	Secrets *ProviderSecrets
	Logger  *slog.Logger
}

// WithNodeProviders turns provisioning on. Without it every node provider
// method returns ErrNotFound, matching the routes that are not mounted.
//
// WithNodeProviders 开启下发。没有它时每个节点供应商方法都返回 ErrNotFound，与未挂载的
// 路由一致。
func WithNodeProviders(cfg NodeProviderConfig) Option {
	return func(s *Service) { s.nodeProviders = &cfg }
}

// ProviderSecrets encrypts provider keys at rest with AES-256-GCM. The node
// and runtime ids are the associated data, so a ciphertext copied to another
// row does not open.
//
// ProviderSecrets 用 AES-256-GCM 对供应商 Key 做静态加密。节点与运行时 id 是附加数据，
// 因此复制到其他行的密文打不开。
type ProviderSecrets struct {
	aead cipher.AEAD
}

// LoadProviderSecrets reads a 32-byte master key from path, written as 64
// hex digits or standard base64.
//
// LoadProviderSecrets 从 path 读取 32 字节主密钥，格式为 64 位十六进制或标准 base64。
func LoadProviderSecrets(path string) (*ProviderSecrets, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("provider master key: %w", err)
	}
	text := strings.TrimSpace(string(data))
	key, err := hex.DecodeString(text)
	if err != nil || len(key) != 32 {
		key, err = base64.StdEncoding.DecodeString(text)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("provider master key: the file must hold 32 bytes as hex or base64")
	}
	return NewProviderSecrets(key)
}

// NewProviderSecrets builds ProviderSecrets from a 32-byte key.
//
// NewProviderSecrets 用 32 字节密钥构造 ProviderSecrets。
func NewProviderSecrets(key []byte) (*ProviderSecrets, error) {
	if len(key) != 32 {
		return nil, errors.New("provider master key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &ProviderSecrets{aead: aead}, nil
}

func providerAAD(nodeID, runtimeID string) []byte {
	return []byte("aisw-node-provider/v1\x00" + nodeID + "\x00" + runtimeID)
}

func (p *ProviderSecrets) seal(nodeID, runtimeID, key string) (string, error) {
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := p.aead.Seal(nonce, nonce, []byte(key), providerAAD(nodeID, runtimeID))
	return base64.StdEncoding.EncodeToString(out), nil
}

func (p *ProviderSecrets) open(nodeID, runtimeID, ciphertext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil || len(raw) < p.aead.NonceSize() {
		return "", errors.New("provider key ciphertext is malformed")
	}
	n := p.aead.NonceSize()
	plain, err := p.aead.Open(nil, raw[:n], raw[n:], providerAAD(nodeID, runtimeID))
	if err != nil {
		return "", errors.New("provider key does not open with the master key")
	}
	return string(plain), nil
}

// NodeProviderInput is one provider write. A nil APIKey keeps the saved key,
// which is only allowed while kind and base_url stay the same: a key never
// follows its runtime to another endpoint.
//
// NodeProviderInput 是一次供应商写入。APIKey 为 nil 表示保留已保存的 Key，仅在种类与
// base_url 都不变时允许：Key 从不跟随运行时换到另一个端点。
type NodeProviderInput struct {
	Kind    string
	BaseURL string
	APIKey  *string
	Models  []string
}

// NodeProviderView is a provider without its key.
//
// NodeProviderView 是不含 Key 的供应商。
type NodeProviderView struct {
	RuntimeID string
	Kind      string
	BaseURL   string
	Models    []string
	KeyLast4  string
	UpdatedBy string
	UpdatedAt time.Time
}

// NodeProvidersStatus is a node's provisioned list and what the Agent last
// reported about it.
//
// NodeProvidersStatus 是节点的下发列表，以及 Agent 最后一次对它的回报。
type NodeProvidersStatus struct {
	Providers []NodeProviderView
	Version   uint64
	// Connected is false when no replica has the node, or the router could
	// not be asked; Report is then nil.
	//
	// Connected 在没有副本持有该节点或路由器无法询问时为 false，此时 Report 为 nil。
	Connected bool
	Report    *remoteproviderrouter.Report
}

// NodeProviderWrite is a write's outcome: the new version, and whether the
// sealed list reached the node now. Delivered false is not a failure — the
// reconciler sends it once the node is reachable.
//
// NodeProviderWrite 是一次写入的结果：新版本，以及封装后的列表此刻是否已送达节点。
// Delivered 为 false 不是失败——节点可达后由对账补发。
type NodeProviderWrite struct {
	Provider  NodeProviderView
	Version   uint64
	Delivered bool
}

// authorizeNodeProviders checks the actor may manage nodeID's providers and
// returns the scope rows are recorded under. A tenant reaches only its own
// enrolled, undeleted tnode; a platform operator reaches only non-tenant
// nodes, the same split dedicated nodes use. Writes need a tenant owner or
// admin.
//
// authorizeNodeProviders 检查 actor 能否管理 nodeID 的供应商，并返回行记录所用的范围。
// 商户只能触及自己已接入且未删除的 tnode；平台运维只能触及非商户节点，与专属节点的划分
// 相同。写入要求商户 owner 或 admin。
func (s *Service) authorizeNodeProviders(ctx context.Context, actor Actor, nodeID string, write bool) (string, error) {
	if s.nodeProviders == nil {
		return "", ErrNotFound
	}
	if nodeID == "" || len(nodeID) > 128 {
		return "", ErrInvalidInput
	}
	owner, owned := nodeaccess.OwnerOf(nodeID)
	if isPlatformActor(actor) {
		if owned {
			return "", ErrNotFound
		}
		return model.PlatformScope, nil
	}
	if err := s.requireTenantNodes(actor); err != nil {
		return "", err
	}
	if write && (!actor.canManageNodes() || actor.UserID == "") {
		return "", ErrForbidden
	}
	if !owned || owner != actor.TenantID {
		return "", ErrNotFound
	}
	row, err := s.store.GetTenantNode(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (row.TenantID != actor.TenantID || row.RevokedAt != nil)) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", translate(err)
	}
	return actor.TenantID, nil
}

// ListNodeProviders returns nodeID's provisioned providers, keys reduced to
// their last four characters, and the Agent's last report.
//
// ListNodeProviders 返回 nodeID 的下发供应商（Key 只保留末四位）与 Agent 的最后回报。
func (s *Service) ListNodeProviders(ctx context.Context, actor Actor, nodeID string) (NodeProvidersStatus, error) {
	if _, err := s.authorizeNodeProviders(ctx, actor, nodeID, false); err != nil {
		return NodeProvidersStatus{}, err
	}
	rows, err := s.store.ListNodeProviders(ctx, nodeID)
	if err != nil {
		return NodeProvidersStatus{}, translate(err)
	}
	version, err := s.store.NodeProviderVersion(ctx, nodeID)
	if err != nil {
		return NodeProvidersStatus{}, translate(err)
	}
	out := NodeProvidersStatus{Version: version, Providers: make([]NodeProviderView, 0, len(rows))}
	for _, row := range rows {
		out.Providers = append(out.Providers, providerView(row))
	}
	if status, err := s.nodeProviders.Router.Status(ctx, nodeID); err == nil && status.Connected {
		out.Connected, out.Report = true, status.Report
	}
	return out, nil
}

// PutNodeProvider creates or replaces one provider on nodeID, then sends the
// node its whole sealed list.
//
// PutNodeProvider 在 nodeID 上新建或替换一个供应商，然后把封装后的整份列表发给节点。
func (s *Service) PutNodeProvider(ctx context.Context, actor Actor, nodeID, runtimeID string, in NodeProviderInput) (NodeProviderWrite, error) {
	scope, err := s.authorizeNodeProviders(ctx, actor, nodeID, true)
	if err != nil {
		return NodeProviderWrite{}, err
	}
	rows, err := s.store.ListNodeProviders(ctx, nodeID)
	if err != nil {
		return NodeProviderWrite{}, translate(err)
	}
	var existing *model.NodeProvider
	for i := range rows {
		if rows[i].RuntimeID == runtimeID {
			existing = &rows[i]
		}
	}
	if existing == nil && len(rows) >= remoteprovider.MaxProviders {
		return NodeProviderWrite{}, ErrConflict
	}
	var key string
	switch {
	case in.APIKey != nil:
		key = *in.APIKey
	case existing != nil && existing.Kind == in.Kind && existing.BaseURL == in.BaseURL:
		if key, err = s.nodeProviders.Secrets.open(nodeID, runtimeID, existing.KeyCiphertext); err != nil {
			return NodeProviderWrite{}, ErrUnavailable
		}
	default:
		return NodeProviderWrite{}, ErrInvalidInput
	}
	provider := remoteprovider.Provider{ID: runtimeID, Kind: in.Kind, BaseURL: in.BaseURL, APIKey: key, Models: in.Models}
	if err := remoteprovider.ValidateProvider(provider); err != nil {
		return NodeProviderWrite{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	ciphertext, err := s.nodeProviders.Secrets.seal(nodeID, runtimeID, key)
	if err != nil {
		return NodeProviderWrite{}, ErrUnavailable
	}
	models, err := json.Marshal(nonNil(in.Models))
	if err != nil {
		return NodeProviderWrite{}, err
	}
	now := s.clock.Now().UTC()
	row := model.NodeProvider{
		NodeID: nodeID, RuntimeID: runtimeID, Scope: scope, Kind: in.Kind, BaseURL: in.BaseURL,
		ModelsJSON: string(models), KeyCiphertext: ciphertext, KeyLast4: last4(key),
		UpdatedBy: actor.UserID, CreatedAt: now, UpdatedAt: now,
	}
	version, err := s.store.PutNodeProvider(ctx, &row)
	if err != nil {
		return NodeProviderWrite{}, translate(err)
	}
	s.audit(ctx, scope, actor.UserID, model.ActionNodeProviderUpdate, nodeID+"/"+runtimeID,
		"kind="+in.Kind+" host="+endpointHost(in.BaseURL), actor.IP)
	return NodeProviderWrite{Provider: providerView(row), Version: version, Delivered: s.deliverNodeProviders(ctx, nodeID)}, nil
}

// DeleteNodeProvider removes one provider from nodeID, then sends the node
// its remaining list.
//
// DeleteNodeProvider 从 nodeID 移除一个供应商，然后把剩余列表发给节点。
func (s *Service) DeleteNodeProvider(ctx context.Context, actor Actor, nodeID, runtimeID string) (NodeProviderWrite, error) {
	scope, err := s.authorizeNodeProviders(ctx, actor, nodeID, true)
	if err != nil {
		return NodeProviderWrite{}, err
	}
	version, err := s.store.DeleteNodeProvider(ctx, nodeID, runtimeID, s.clock.Now().UTC())
	if err != nil {
		return NodeProviderWrite{}, translate(err)
	}
	s.audit(ctx, scope, actor.UserID, model.ActionNodeProviderDelete, nodeID+"/"+runtimeID, "", actor.IP)
	return NodeProviderWrite{Version: version, Delivered: s.deliverNodeProviders(ctx, nodeID)}, nil
}

// deliverNodeProviders sends the current list and logs, rather than
// returns, a failure: the write already succeeded and the reconciler
// retries.
//
// deliverNodeProviders 发送当前列表，失败时记录日志而不是返回错误：写入已经成功，由对账
// 重试。
func (s *Service) deliverNodeProviders(ctx context.Context, nodeID string) bool {
	delivered, err := s.pushNodeProviders(ctx, nodeID)
	if err != nil {
		s.nodeProviderLogger().Warn("remote providers not delivered / 远程供应商未送达",
			slog.String("node_id", nodeID), slog.Any("error", err))
	}
	return delivered
}

// errNodeKeyUnverified means no certificate key a replica reported matches
// the fingerprint the Registry holds for the node.
//
// errNodeKeyUnverified 表示副本报告的证书公钥都与 Registry 为该节点记录的指纹不符。
var errNodeKeyUnverified = errors.New("node certificate key is not the one the Registry issued")

// pushNodeProviders seals nodeID's whole list to the certificate key the
// Registry vouches for and sends it. It reports false with no error when
// the node is not connected.
//
// pushNodeProviders 把 nodeID 的整份列表封装给 Registry 认可的证书公钥并发送。节点未连接时
// 返回 false 且不报错。
func (s *Service) pushNodeProviders(ctx context.Context, nodeID string) (bool, error) {
	cfg := s.nodeProviders
	status, err := cfg.Router.Status(ctx, nodeID)
	if err != nil {
		return false, err
	}
	if !status.Connected {
		return false, nil
	}
	pub, err := s.verifiedNodeKey(ctx, nodeID, status.NodeKeys)
	if err != nil {
		return false, err
	}
	rows, err := s.store.ListNodeProviders(ctx, nodeID)
	if err != nil {
		return false, err
	}
	version, err := s.store.NodeProviderVersion(ctx, nodeID)
	if err != nil {
		return false, err
	}
	bundle := remoteprovider.Bundle{NodeID: nodeID, Version: version, Providers: make([]remoteprovider.Provider, 0, len(rows))}
	for _, row := range rows {
		key, err := cfg.Secrets.open(row.NodeID, row.RuntimeID, row.KeyCiphertext)
		if err != nil {
			return false, err
		}
		var models []string
		if err := json.Unmarshal([]byte(row.ModelsJSON), &models); err != nil {
			return false, errors.New("provider models are malformed")
		}
		bundle.Providers = append(bundle.Providers, remoteprovider.Provider{ID: row.RuntimeID, Kind: row.Kind, BaseURL: row.BaseURL, APIKey: key, Models: models})
	}
	sealed, err := remoteprovider.Seal(pub, bundle)
	if err != nil {
		return false, err
	}
	result, err := cfg.Router.Push(ctx, nodeID, version, sealed)
	if err != nil {
		return false, err
	}
	return result.Connected, nil
}

// verifiedNodeKey picks, among the keys replicas reported, the one whose
// fingerprint the Registry recorded for nodeID. A Gateway can report any key
// it likes; only the Registry's record makes one trustworthy.
//
// verifiedNodeKey 在副本报告的公钥中挑出 Registry 为 nodeID 记录了其指纹的那个。Gateway
// 可以报告任何公钥；只有 Registry 的记录能让某个公钥可信。
func (s *Service) verifiedNodeKey(ctx context.Context, nodeID string, keys [][]byte) (*ecdh.PublicKey, error) {
	if s.registryClient == nil {
		return nil, ErrRegistryUnconfigured
	}
	states, err := s.registryClient.ListNodeStates(ctx)
	if err != nil {
		return nil, err
	}
	var fingerprint string
	for _, st := range states {
		if st.NodeID == nodeID {
			if st.Disabled || st.PendingApproval {
				return nil, errNodeKeyUnverified
			}
			fingerprint = st.KeyFingerprint
		}
	}
	if fingerprint == "" {
		return nil, errNodeKeyUnverified
	}
	for _, spki := range keys {
		if remoteprovider.Fingerprint(spki) == fingerprint {
			return remoteprovider.ParsePublicKey(spki)
		}
	}
	return nil, errNodeKeyUnverified
}

// ReconcileNodeProviders resends the list to every node whose Agent has not
// applied the current version, which covers nodes that were offline during a
// write and nodes whose renewed certificate made a bundle unreadable. A node
// that refused the current version as disabled is skipped until its version
// changes.
//
// ReconcileNodeProviders 向 Agent 尚未应用当前版本的每个节点重发列表，覆盖写入时离线的
// 节点，以及证书续期导致打包打不开的节点。以关闭为由拒绝当前版本的节点会被跳过，直到其
// 版本变化。
func (s *Service) ReconcileNodeProviders(ctx context.Context) {
	if s.nodeProviders == nil {
		return
	}
	versions, err := s.store.ListNodeProviderVersions(ctx)
	if err != nil {
		s.nodeProviderLogger().Warn("remote provider reconcile skipped / 远程供应商对账跳过", slog.Any("error", err))
		return
	}
	for _, v := range versions {
		if ctx.Err() != nil {
			return
		}
		status, err := s.nodeProviders.Router.Status(ctx, v.NodeID)
		if err != nil || !status.Connected {
			continue
		}
		// An older Agent with its switch off refuses every push of this
		// version; resend only after it restarts (its report resets) or the
		// version moves. / 关闭开关的旧版 Agent 会拒绝该版本的每次推送；等它重启（回报
		// 重置）或版本变化后再重发。
		if r := status.Report; r != nil && (r.AppliedVersion >= v.Version ||
			(r.AttemptedVersion == v.Version && r.Outcome == remoteprovider.OutcomeDisabled.String())) {
			continue
		}
		s.deliverNodeProviders(ctx, v.NodeID)
	}
}

// RunNodeProviderReconciler calls ReconcileNodeProviders every interval
// until ctx ends.
//
// RunNodeProviderReconciler 每隔 interval 调用一次 ReconcileNodeProviders，直到 ctx 结束。
func (s *Service) RunNodeProviderReconciler(ctx context.Context, interval time.Duration) {
	if s.nodeProviders == nil || interval <= 0 {
		return
	}
	for {
		fired, stop := s.clock.NewTimer(interval)
		select {
		case <-ctx.Done():
			stop()
			return
		case <-fired:
		}
		s.ReconcileNodeProviders(ctx)
	}
}

func (s *Service) nodeProviderLogger() *slog.Logger {
	if s.nodeProviders.Logger != nil {
		return s.nodeProviders.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func providerView(row model.NodeProvider) NodeProviderView {
	var models []string
	_ = json.Unmarshal([]byte(row.ModelsJSON), &models)
	return NodeProviderView{
		RuntimeID: row.RuntimeID, Kind: row.Kind, BaseURL: row.BaseURL, Models: nonNil(models),
		KeyLast4: row.KeyLast4, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt,
	}
}

// last4 is the tail shown for a key, empty for a key too short to reveal
// any of it.
//
// last4 是为 Key 显示的末尾；Key 太短时为空，不泄露任何部分。
func last4(key string) string {
	if len(key) < 12 {
		return ""
	}
	return key[len(key)-4:]
}

// endpointHost is the audited host: the base URL's, or the kind's default.
// endpointHost 是审计记录的主机名：base URL 的主机名，或该种类默认端点的主机名。
func endpointHost(baseURL string) string {
	if baseURL == "" {
		return "default"
	}
	if u, err := url.Parse(baseURL); err == nil {
		return u.Hostname()
	}
	return ""
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
