// Package workflowsync pulls and durably activates the control plane's
// published workflow-template bundle (P03). It is routesync's structure
// applied to a different shape of publication: routesync activates one
// singleton revision, this package activates a set of independently
// versioned templates, so its regression guard tracks a revision per
// template id rather than one counter.
//
// workflowsync 包拉取并持久化启用控制面已发布的工作流模板整包（P03）。它是
// routesync 的结构套用到另一种形状的发布上：routesync 启用的是一个单例版本，
// 本包启用的是一组各自独立版本化的模板，因此它的回归防护按模板 id 分别追踪版本，
// 而不是用单一计数器。
package workflowsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"AIServeWeave/common/runtime"
	"AIServeWeave/common/workflowtemplate"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// Config supplies the protected source, durable cache and activation callback.
// Config 提供受保护的来源、持久缓存与启用回调。
type Config struct {
	Endpoint  string
	Token     string
	StateFile string
	Interval  time.Duration
	Clock     runtime.Clock
	Apply     func(*workflow.Registry)
}

// Syncer serializes pulls while publishing status without blocking readers.
// Syncer 串行拉取，同时提供不阻塞读取者的状态。
type Syncer struct {
	cfg     Config
	client  *http.Client
	mu      sync.Mutex
	applied atomic.Pointer[workflowtemplate.Applied]
	// revisions is the last activated (revision, digest) per template id,
	// read and written only while mu is held. It is what lets Sync tell a
	// genuine rollback-on-the-wire (a bug or a corrupted response) from an
	// ordinary republish: a template's revision here only ever goes up, and a
	// repeated revision must carry the same digest it did before.
	//
	// revisions 是按模板 id 记录的、最后一次启用的 (版本号, 摘要)，只在持有 mu 时
	// 读写。正是它让 Sync 能分辨"线上出现的真回退"（缺陷或响应损坏）与一次正常的
	// 重新发布：这里记录的某个模板版本只会往上走，而重复的版本号必须携带与之前
	// 相同的摘要。
	revisions map[string]revisionDigest
}

type revisionDigest struct {
	revision int64
	digest   string
}

// New validates managed workflow-template configuration. / New 校验托管工作流模板配置。
func New(cfg Config) (*Syncer, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || cfg.Token == "" || cfg.StateFile == "" || cfg.Apply == nil || cfg.Interval < 0 {
		return nil, errors.New("workflowsync: invalid configuration")
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/") + "/internal/v1/workflow-templates/current"
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = runtime.NewSystemClock()
	}
	s := &Syncer{cfg: cfg, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, revisions: make(map[string]revisionDigest)}
	s.applied.Store(&workflowtemplate.Applied{Mode: "controlplane"})
	return s, nil
}

// Status reports only the locally activated bundle. / Status 仅报告本地已启用的整包。
func (s *Syncer) Status() workflowtemplate.Applied { return *s.applied.Load() }

// Start loads a validated cache and requires either it or a successful first
// pull. An empty published bundle (zero templates) is a valid pull, not a
// missing one, so this checks whether activation ever happened rather than
// whether the count is nonzero.
//
// Start 加载已校验缓存，要求缓存或首次拉取至少一个成功。一份空的已发布整包
// （零模板）是一次有效的拉取，不是缺失的拉取，因此这里检查的是启用是否发生过，
// 而不是数量是否非零。
func (s *Syncer) Start(ctx context.Context) error {
	if f, err := os.Open(s.cfg.StateFile); err == nil {
		snaps, reg, err := decode(f)
		_ = f.Close()
		if err == nil {
			s.activate(snaps, reg)
		}
	}
	if err := s.Sync(ctx); err != nil && s.Status().AppliedAt.IsZero() {
		return errors.New("workflowsync: no valid initial publication")
	}
	return nil
}

// Run polls serially until cancellation using the injected clock.
// Run 使用注入时钟串行轮询，直到取消。
func (s *Syncer) Run(ctx context.Context) {
	for {
		ch, stop := s.cfg.Clock.NewTimer(s.cfg.Interval)
		select {
		case <-ctx.Done():
			stop()
			return
		case <-ch:
			stop()
		}
		_ = s.Sync(ctx)
	}
}

// Sync fetches, checks per-template version order, persists, then activates
// one bundle.
//
// Sync 拉取并按模板逐一检查版本顺序，先持久化再启用一份整包。
func (s *Syncer) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(code string) error {
		v := s.Status()
		v.CheckedAt = s.cfg.Clock.Now().UTC()
		v.Error = code
		s.applied.Store(&v)
		return errors.New("workflowsync: " + code)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.Endpoint, nil)
	if err != nil {
		return fail("fetch_failed")
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.Token)
	resp, err := s.client.Do(req)
	if err != nil {
		return fail("fetch_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail("fetch_failed")
	}
	snaps, reg, err := decode(resp.Body)
	if err != nil {
		return fail("invalid_snapshot")
	}
	for _, snap := range snaps {
		last, ok := s.revisions[snap.TemplateID]
		if !ok {
			continue
		}
		if snap.Revision < last.revision {
			return fail("revision_regression")
		}
		if snap.Revision == last.revision && snap.Digest != last.digest {
			return fail("revision_conflict")
		}
	}
	if err = persist(s.cfg.StateFile, snaps); err != nil {
		return fail("persist_failed")
	}
	s.activate(snaps, reg)
	return nil
}

func (s *Syncer) activate(snaps []workflowtemplate.Snapshot, reg *workflow.Registry) {
	pending := s.Status()
	pending.Error = "activating"
	s.applied.Store(&pending)
	s.cfg.Apply(reg)
	for _, snap := range snaps {
		s.revisions[snap.TemplateID] = revisionDigest{revision: snap.Revision, digest: snap.Digest}
	}
	infos := make([]workflowtemplate.RevisionInfo, len(snaps))
	for i, snap := range snaps {
		infos[i] = snap.RevisionInfo
	}
	digest, err := workflowtemplate.BundleDigest(infos)
	if err != nil {
		digest = ""
	}
	now := s.cfg.Clock.Now().UTC()
	s.applied.Store(&workflowtemplate.Applied{Mode: "controlplane", TemplateCount: len(snaps), BundleDigest: digest, AppliedAt: now, CheckedAt: now})
}

// MaxBundleBytes bounds one decoded publication bundle: many templates, each
// itself bounded by workflowtemplate.MaxContentBytes, so this leaves room for
// workflowtemplate.MaxTemplates of them without being unbounded.
//
// MaxBundleBytes 限制一次解码的发布整包：多个模板，每个本身受
// workflowtemplate.MaxContentBytes 限制，因此这里为
// workflowtemplate.MaxTemplates 份模板留出空间，同时不至于无界。
const MaxBundleBytes = workflowtemplate.MaxContentBytes * workflowtemplate.MaxTemplates

func decode(r io.Reader) ([]workflowtemplate.Snapshot, *workflow.Registry, error) {
	var snaps []workflowtemplate.Snapshot
	body, err := io.ReadAll(io.LimitReader(r, MaxBundleBytes+1))
	if err != nil || len(body) > MaxBundleBytes || json.Unmarshal(body, &snaps) != nil {
		return nil, nil, errors.New("invalid bundle")
	}
	for _, snap := range snaps {
		if snap.TemplateID == "" || snap.Revision <= 0 || snap.CreatedAt.IsZero() || strings.TrimSpace(snap.ActorID) == "" {
			return nil, nil, errors.New("invalid snapshot")
		}
		digest, err := workflowtemplate.Digest(snap.TemplateID, snap.Content, snap.VisibleTenantIDs)
		if err != nil || digest != snap.Digest {
			return nil, nil, errors.New("invalid snapshot")
		}
	}
	reg, err := workflow.FromBundle(snaps)
	if err != nil {
		return nil, nil, err
	}
	return snaps, reg, nil
}

func persist(path string, snaps []workflowtemplate.Snapshot) error {
	body, err := json.Marshal(snaps)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".workflow-templates-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
