// Package routesync pulls and durably activates complete routing publications.
// routesync 包拉取并持久化启用完整路由发布。
package routesync

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

	"AIServeWeave/common/modelroute"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/routing"
)

// Config supplies the protected source, durable cache and activation callback.
// Config 提供受保护的来源、持久缓存与启用回调。
type Config struct {
	Endpoint  string
	Token     string
	StateFile string
	Interval  time.Duration
	Clock     runtime.Clock
	Apply     func(*routing.Table)
}

// Syncer serializes pulls while publishing status without blocking readers.
// Syncer 串行拉取，同时提供不阻塞读取者的状态。
type Syncer struct {
	cfg     Config
	client  *http.Client
	mu      sync.Mutex
	applied atomic.Pointer[modelroute.Applied]
}

// New validates managed routing configuration. / New 校验托管路由配置。
func New(cfg Config) (*Syncer, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || cfg.Token == "" || cfg.StateFile == "" || cfg.Apply == nil || cfg.Interval < 0 {
		return nil, errors.New("routesync: invalid configuration")
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/") + "/internal/v1/routes/current"
	if cfg.Interval == 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = runtime.NewSystemClock()
	}
	s := &Syncer{cfg: cfg, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.applied.Store(&modelroute.Applied{Mode: "controlplane"})
	return s, nil
}

// Status reports only the locally activated version. / Status 仅报告本地已启用版本。
func (s *Syncer) Status() modelroute.Applied { return *s.applied.Load() }

// Start loads a validated cache and requires either it or a successful first pull.
// Start 加载已校验缓存，要求缓存或首次拉取至少一个成功。
func (s *Syncer) Start(ctx context.Context) error {
	if f, err := os.Open(s.cfg.StateFile); err == nil {
		snap, table, err := decode(f)
		_ = f.Close()
		if err == nil {
			s.activate(snap, table)
		}
	}
	if err := s.Sync(ctx); err != nil && s.Status().Revision == 0 {
		return errors.New("routesync: no valid initial publication")
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

// Sync fetches, checks version order, persists, then activates one publication.
// Sync 拉取并检查版本顺序，先持久化再启用一份发布。
func (s *Syncer) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fail := func(code string) error {
		v := s.Status()
		v.CheckedAt = s.cfg.Clock.Now().UTC()
		v.Error = code
		s.applied.Store(&v)
		return errors.New("routesync: " + code)
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
	snap, table, err := decode(resp.Body)
	if err != nil {
		return fail("invalid_snapshot")
	}
	old := s.Status()
	if snap.Revision < old.Revision {
		return fail("revision_regression")
	}
	if snap.Revision == old.Revision {
		if snap.Digest != old.Digest {
			return fail("revision_conflict")
		}
		old.CheckedAt = s.cfg.Clock.Now().UTC()
		old.Error = ""
		s.applied.Store(&old)
		return nil
	}
	if err = persist(s.cfg.StateFile, snap); err != nil {
		return fail("persist_failed")
	}
	s.activate(snap, table)
	return nil
}

func (s *Syncer) activate(snap modelroute.Snapshot, table *routing.Table) {
	pending := s.Status()
	pending.Error = "activating"
	s.applied.Store(&pending)
	s.cfg.Apply(table)
	now := s.cfg.Clock.Now().UTC()
	s.applied.Store(&modelroute.Applied{Mode: "controlplane", Revision: snap.Revision, Digest: snap.Digest, AppliedAt: now, CheckedAt: now})
}

func decode(r io.Reader) (modelroute.Snapshot, *routing.Table, error) {
	var snap modelroute.Snapshot
	body, err := io.ReadAll(io.LimitReader(r, modelroute.MaxDocumentBytes+1))
	if err != nil || len(body) > modelroute.MaxDocumentBytes || json.Unmarshal(body, &snap) != nil || snap.Revision <= 0 || snap.Routes == nil || snap.CreatedAt.IsZero() || strings.TrimSpace(snap.ActorID) == "" {
		return snap, nil, errors.New("invalid snapshot")
	}
	digest, err := modelroute.Digest(snap.Routes)
	if err != nil || digest != snap.Digest {
		return snap, nil, errors.New("invalid snapshot")
	}
	table, err := routing.New(snap.Routes)
	return snap, table, err
}

func persist(path string, snap modelroute.Snapshot) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".routes-*")
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
