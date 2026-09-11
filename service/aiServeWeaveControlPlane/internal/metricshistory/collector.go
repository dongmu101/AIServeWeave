// Package metricshistory periodically scrapes Gateway and Registry
// Prometheus text endpoints, sums each series it cares about across
// replicas, drops labels not worth keeping at history granularity (most
// notably node_id — see seriesKeepLabels), and upserts one row per
// (metric, labels, bucket) into a store.MetricsHistory.
//
// metricshistory 包定时抓取 Gateway 与 Registry 的 Prometheus 文本端点，把它
// 关心的每条序列跨副本求和，丢弃在历史粒度下不值得保留的标签(最主要是
// node_id——见 seriesKeepLabels)，把每个 (metric, labels, bucket) 各写一行进
// store.MetricsHistory。
package metricshistory

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"AIServeWeave/common/metrics"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveControlPlane/internal/model"
)

// seriesKeepLabels declares, per metric name this collector rolls up, which
// label keys survive into history; everything else is summed away. This is
// what keeps history cardinality bounded regardless of fleet size.
//
// seriesKeepLabels 按指标名声明哪些标签键会保留进历史；其余全部被求和抹去。
// 这正是不论机群规模多大，历史数据基数始终有界的原因。
var seriesKeepLabels = map[string][]string{
	"gateway_http_requests_total":                  {"endpoint", "status"},
	"gateway_http_request_duration_seconds_bucket": {"endpoint", "le"},
	"gateway_http_request_duration_seconds_sum":    {"endpoint"},
	"gateway_http_request_duration_seconds_count":  {"endpoint"},
	"gateway_tokens_total":                         {"direction"},
	"tunnel_server_slots_total":                    {"class", "state"},
}

// HTTPDoer is the subset of *http.Client the collector needs, so tests can
// substitute a fixed set of responses instead of a real network.
//
// HTTPDoer 是采集器需要的 *http.Client 子集，测试可以用一组固定响应替代真实网络。
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Store is the persistence surface the collector writes to.
//
// Store 是采集器写入的持久化接口。
type Store interface {
	UpsertRollup(ctx context.Context, points []model.MetricsHistoryPoint) error
}

// Config configures a Collector.
//
// Config 配置一个 Collector。
type Config struct {
	GatewayAddrs []string
	RegistryAddr string
	Interval     time.Duration
	Store        Store
	Client       HTTPDoer
	Clock        runtime.Clock
	Logger       *slog.Logger
}

// Collector periodically scrapes and rolls up metrics history.
//
// Collector 定时抓取并汇总指标历史。
type Collector struct {
	sources  []string
	interval time.Duration
	store    Store
	client   HTTPDoer
	clock    runtime.Clock
	logger   *slog.Logger
}

// New builds a Collector from cfg.
//
// New 用 cfg 构造一个 Collector。
func New(cfg Config) *Collector {
	sources := append([]string{}, cfg.GatewayAddrs...)
	if cfg.RegistryAddr != "" {
		sources = append(sources, cfg.RegistryAddr)
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Collector{sources: sources, interval: cfg.Interval, store: cfg.Store, client: client, clock: clock, logger: logger}
}

// Run collects once per Interval until ctx is done.
//
// Run 每隔一个 Interval 采集一次，直到 ctx 结束。
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			at := c.clock.Now().UTC().Truncate(c.interval)
			if err := c.CollectOnce(ctx, at); err != nil {
				c.logger.Error("metrics history collection failed", slog.Any("error", err))
			}
		}
	}
}

// CollectOnce scrapes every configured source, aggregates, and upserts one
// rollup row per surviving series for bucket at. A source that cannot be
// scraped is logged and skipped — a down replica must not blank out the
// history of every replica that answered.
//
// CollectOnce 抓取全部已配置的来源，聚合后为每条存活下来的序列写入一行以 at
// 为 bucket 的汇总数据。抓不到的来源只记日志并跳过——一个宕掉的副本不应该把
// 全部应答正常的副本历史一起抹平。
func (c *Collector) CollectOnce(ctx context.Context, at time.Time) error {
	type key struct{ metric, labels string }
	sums := map[key]float64{}

	for _, addr := range c.sources {
		samples, err := c.scrape(ctx, addr)
		if err != nil {
			c.logger.Warn("scrape failed", slog.String("addr", addr), slog.Any("error", err))
			continue
		}
		for _, s := range samples {
			keep, ok := seriesKeepLabels[s.Name]
			if !ok {
				continue
			}
			k := key{metric: s.Name, labels: canonicalLabels(s.Labels, keep)}
			sums[k] += s.Value
		}
	}

	points := make([]model.MetricsHistoryPoint, 0, len(sums))
	for k, v := range sums {
		points = append(points, model.MetricsHistoryPoint{Metric: k.metric, Labels: k.labels, BucketAt: at, Value: v})
	}
	if len(points) == 0 {
		return nil
	}
	return c.store.UpsertRollup(ctx, points)
}

func (c *Collector) scrape(ctx context.Context, addr string) ([]metrics.Sample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metricshistory: %s returned status %d", addr, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return metrics.ParseExposition(strings.NewReader(string(body)))
}

// canonicalLabels renders only the keys in keep, sorted, as "k1=v1,k2=v2".
//
// canonicalLabels 只渲染 keep 里的键，排序后拼成 "k1=v1,k2=v2"。
func canonicalLabels(labels map[string]string, keep []string) string {
	kept := make([]string, 0, len(keep))
	for _, k := range keep {
		if v, ok := labels[k]; ok {
			kept = append(kept, k+"="+v)
		}
	}
	sort.Strings(kept)
	return strings.Join(kept, ",")
}
