// Package remoteproviders keeps the hosted provider runtimes the control plane
// provisions on this node: it opens each sealed bundle with the node key,
// persists the newest one next to the node's other private state and hands
// its runtimes to the Agent's reloader.
//
// A bundle is the node's whole provisioned list at one version, so applying
// one replaces the previous list, a lower version is refused and a copy of
// the same version — every Gateway replica relays it — changes nothing. The
// node key is read again for every bundle, because certificate renewal
// replaces it.
//
// remoteproviders 包保存控制面下发到本节点的托管供应商运行时：用节点密钥打开每份已
// 封装的打包，把最新一份与节点其他私有状态放在一起持久化，并把其中的运行时交给 Agent
// 的重新加载器。
//
// 一份打包是节点在某个版本下的全部下发列表，因此应用它会替换之前的列表；版本更低的被
// 拒绝，同一版本的副本（每个 Gateway 副本都会转发一份）不改变任何东西。每份打包都重新
// 读取节点密钥，因为证书续期会替换它。
package remoteproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"sync"

	"AIServeWeave/common/remoteprovider"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
)

// FileName is the bundle file's name inside the node state directory.
//
// FileName 是打包文件在节点状态目录中的文件名。
const FileName = "remote-providers.json"

// maxFileBytes bounds the persisted bundle read at startup.
// maxFileBytes 限制启动时读取的已持久化打包大小。
const maxFileBytes = remoteprovider.MaxBundleBytes + 4096

// Config configures a Store.
//
// Config 配置一个 Store。
type Config struct {
	// Path is the bundle file, written mode 0600.
	// Path 是打包文件，以 0600 权限写入。
	Path string
	// KeyFile is the node private key file (-key-file).
	// KeyFile 是节点私钥文件（-key-file）。
	KeyFile string
	// NodeID is this node's identity; a bundle addressed to any other node
	// does not open.
	//
	// NodeID 是本节点身份；发给其他节点的打包打不开。
	NodeID string
	// Enabled is the local consent (remote_providers). A disabled Store
	// deletes any saved bundle, supplies no runtimes and reports
	// OutcomeDisabled for every push.
	//
	// Enabled 是本地同意（remote_providers）。关闭时 Store 删除已保存的打包、不提供
	// 任何运行时，并对每次推送回报 OutcomeDisabled。
	Enabled bool
	// Reload asks the Agent to reconcile its runtimes with Runtimes; the
	// reloader reports each outcome back through Observe.
	//
	// Reload 请求 Agent 按 Runtimes 调和运行时；重新加载器通过 Observe 回报每次结果。
	Reload func(ctx context.Context) error
	Clock  runtime.Clock
	Logger *slog.Logger
}

// Store holds the applied bundle and the report the tunnel sends back.
//
// Store 保存已应用的打包，以及隧道回传的回报。
type Store struct {
	cfg Config

	mu      sync.Mutex
	bundle  remoteprovider.Bundle
	report  remoteprovider.Report
	pending *push

	wake chan struct{}
}

type push struct {
	version uint64
	sealed  []byte
}

// New builds a Store and loads the saved bundle. A saved bundle that cannot
// be read, or belongs to another node, is ignored and left for the next push
// to replace; a disabled Store deletes it instead.
//
// New 构造 Store 并加载已保存的打包。读不出来或属于其他节点的已保存打包被忽略，留给下一
// 次推送替换；关闭状态的 Store 则删除它。
func New(cfg Config) (*Store, error) {
	if cfg.Path == "" || cfg.KeyFile == "" || cfg.NodeID == "" {
		return nil, errors.New("remoteproviders: Path, KeyFile and NodeID are required")
	}
	if cfg.Clock == nil {
		cfg.Clock = runtime.NewSystemClock()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Store{cfg: cfg, wake: make(chan struct{}, 1)}
	if !cfg.Enabled {
		s.report.Outcome = remoteprovider.OutcomeDisabled
		if err := os.Remove(cfg.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("remoteproviders: remove %s: %w", cfg.Path, err)
		}
		return s, nil
	}
	b, err := load(cfg.Path, cfg.NodeID)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		cfg.Logger.Warn("saved remote providers ignored / 已保存的远程供应商被忽略", slog.Any("error", err))
	default:
		s.bundle = b
		s.report = remoteprovider.Report{AppliedVersion: b.Version, AttemptedVersion: b.Version, Outcome: remoteprovider.OutcomeApplied}
	}
	return s, nil
}

// load reads and validates the saved bundle.
// load 读取并校验已保存的打包。
func load(path, nodeID string) (remoteprovider.Bundle, error) {
	if err := localstate.CheckPrivate(path, true); err != nil {
		return remoteprovider.Bundle{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return remoteprovider.Bundle{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return remoteprovider.Bundle{}, err
	}
	if len(data) > maxFileBytes {
		return remoteprovider.Bundle{}, errors.New("remoteproviders: saved bundle is too large")
	}
	var b remoteprovider.Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return remoteprovider.Bundle{}, errors.New("remoteproviders: saved bundle is not valid JSON")
	}
	if b.NodeID != nodeID {
		return remoteprovider.Bundle{}, errors.New("remoteproviders: saved bundle belongs to another node")
	}
	if err := b.Validate(); err != nil {
		return remoteprovider.Bundle{}, err
	}
	return b, nil
}

// Apply queues a sealed bundle for Run and never blocks. Only the newest
// queued push is kept.
//
// Apply 把一份已封装的打包排给 Run 处理，从不阻塞。只保留最新排入的一份。
func (s *Store) Apply(version uint64, sealed []byte) {
	s.mu.Lock()
	if s.pending == nil || version >= s.pending.version {
		s.pending = &push{version: version, sealed: slices.Clone(sealed)}
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run applies the saved bundle's runtimes once, then processes pushes until
// ctx ends.
//
// Run 先应用一次已保存打包的运行时，然后处理推送直到 ctx 结束。
func (s *Store) Run(ctx context.Context) {
	s.mu.Lock()
	saved := len(s.bundle.Providers) > 0
	s.mu.Unlock()
	if saved {
		s.reload(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		s.mu.Lock()
		p := s.pending
		s.pending = nil
		s.mu.Unlock()
		if p != nil {
			s.process(ctx, *p)
		}
	}
}

// process opens, checks, persists and applies one push.
// process 打开、检查、持久化并应用一次推送。
func (s *Store) process(ctx context.Context, p push) {
	log := s.cfg.Logger.With(slog.Uint64("version", p.version))
	if !s.cfg.Enabled {
		s.finish(remoteprovider.OutcomeDisabled, p.version)
		log.Warn("remote providers refused: remote_providers is off / 拒绝远程供应商：remote_providers 未开启")
		return
	}
	s.mu.Lock()
	applied := s.bundle.Version
	s.mu.Unlock()
	switch {
	case p.version == applied:
		return
	case p.version < applied:
		s.finish(remoteprovider.OutcomeStale, p.version)
		return
	}

	b, err := s.open(p)
	if err != nil {
		outcome := remoteprovider.OutcomeInvalid
		if errors.Is(err, remoteprovider.ErrOpen) {
			outcome = remoteprovider.OutcomeOpenFailed
		}
		s.finish(outcome, p.version)
		log.Warn("remote providers not applied / 远程供应商未应用", slog.String("outcome", outcome.String()))
		return
	}
	data, err := json.Marshal(b)
	if err == nil {
		err = localstate.WriteFile(s.cfg.Path, data, 0o600)
	}
	if err != nil {
		s.finish(remoteprovider.OutcomeStoreFailed, p.version)
		log.Error("remote providers not saved / 远程供应商未保存", slog.Any("error", err))
		return
	}

	s.mu.Lock()
	s.bundle = b
	s.report.AppliedVersion = p.version
	s.mu.Unlock()
	s.finish(remoteprovider.OutcomeApplied, p.version)
	log.Info("remote providers applied / 远程供应商已应用", slog.Int("runtimes", len(b.Providers)))
	s.reload(ctx)
}

// open reads the node key and opens the push with it.
// open 读取节点密钥并用它打开推送。
func (s *Store) open(p push) (remoteprovider.Bundle, error) {
	pem, err := os.ReadFile(s.cfg.KeyFile)
	if err != nil {
		return remoteprovider.Bundle{}, remoteprovider.ErrOpen
	}
	priv, err := remoteprovider.PrivateKeyFromPEM(pem)
	if err != nil {
		return remoteprovider.Bundle{}, remoteprovider.ErrOpen
	}
	return remoteprovider.Open(priv, s.cfg.NodeID, p.version, p.sealed)
}

// finish records the outcome of one push.
// finish 记录一次推送的结果。
func (s *Store) finish(outcome remoteprovider.Outcome, attempted uint64) {
	s.mu.Lock()
	s.report.Outcome = outcome
	s.report.AttemptedVersion = attempted
	s.report.UpdatedAt = s.cfg.Clock.Now()
	s.mu.Unlock()
}

// reload asks the Agent to reconcile; a failure to reload at all marks every
// provisioned runtime failed, since none of them is known to be running.
//
// reload 请求 Agent 调和；完全无法重新加载时把所有下发的运行时标为失败，因为无法确认
// 其中任何一个在运行。
func (s *Store) reload(ctx context.Context) {
	if s.cfg.Reload == nil {
		return
	}
	if err := s.cfg.Reload(ctx); err != nil {
		s.cfg.Logger.Error("remote providers reload failed / 远程供应商重新加载失败", slog.Any("error", err))
		s.mu.Lock()
		ids := make([]string, 0, len(s.bundle.Providers))
		for _, p := range s.bundle.Providers {
			ids = append(ids, p.ID)
		}
		s.mu.Unlock()
		s.Observe(ids, nil)
	}
}

// Observe records which provisioned runtimes the last reconcile failed to
// apply and which collide with a local declaration; every other provisioned
// runtime is active. IDs that are not provisioned are ignored.
//
// Observe 记录上一次调和中哪些下发的运行时应用失败、哪些与本地声明冲突；其余下发的运行
// 时均为活动状态。不属于下发的 ID 被忽略。
func (s *Store) Observe(failed, conflicts []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]remoteprovider.RuntimeResult, 0, len(s.bundle.Providers))
	for _, p := range s.bundle.Providers {
		state := remoteprovider.RuntimeActive
		switch {
		case slices.Contains(conflicts, p.ID):
			state = remoteprovider.RuntimeConflict
		case slices.Contains(failed, p.ID):
			state = remoteprovider.RuntimeFailed
		}
		results = append(results, remoteprovider.RuntimeResult{ID: p.ID, State: state})
	}
	if !slices.Equal(results, s.report.Runtimes) {
		s.report.Runtimes = results
		s.report.UpdatedAt = s.cfg.Clock.Now()
	}
}

// Runtimes returns the applied bundle as runtime declarations; nil while
// disabled.
//
// Runtimes 把已应用的打包转换成运行时声明返回；关闭时为 nil。
func (s *Store) Runtimes() []runtime.Config {
	if !s.cfg.Enabled {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bundle.Providers) == 0 {
		return nil
	}
	out := make([]runtime.Config, 0, len(s.bundle.Providers))
	for _, p := range s.bundle.Providers {
		out = append(out, runtime.Config{
			ID: p.ID, Kind: runtime.Kind(p.Kind), BaseURL: p.BaseURL, APIKey: p.APIKey,
			Models: slices.Clone(p.Models),
		})
	}
	return out
}

// Snapshot returns a copy of the current report.
//
// Snapshot 返回当前回报的副本。
func (s *Store) Snapshot() remoteprovider.Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.report
	r.Runtimes = slices.Clone(r.Runtimes)
	return r
}
