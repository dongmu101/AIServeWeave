"use client";

import * as React from "react";
import ReactECharts from "echarts-for-react";

import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { deltaByBucket, approxP95 } from "@/lib/console/metrics-charts";
import { parseMetricsHistory, type MetricsHistory, type MetricsHistorySeries } from "@/lib/console/metrics";

/**
 * The fleet-wide metrics history page (P08, Console C27).
 *
 * This is a platform-operator page, not a tenant one: Gateway's Prometheus
 * metrics carry no tenant_id label by design (a label proportional to tenant
 * count would make cardinality unbounded), so there is no per-tenant
 * breakdown to show — see the P08 design doc's "C27 视角" decision.
 *
 * 机群级别的指标历史页面(P08，Console C27)。
 *
 * 这是平台运维页面，不是租户页面：Gateway 的 Prometheus 指标按设计不带
 * tenant_id 标签(一个随租户数量增长的标签会让基数失控)，因此没有可展示的
 * 按租户拆分——见 P08 设计文档的「C27 视角」决策。
 */
const WINDOW_MS = 24 * 60 * 60 * 1000;

function windowBounds() {
  const until = new Date();
  const since = new Date(until.getTime() - WINDOW_MS);
  return { since: since.toISOString(), until: until.toISOString() };
}

function seriesFor(history: MetricsHistory, metric: string): MetricsHistorySeries[] {
  return history.series.filter((s) => s.metric === metric);
}

function lineOption(
  title: string,
  unit: string,
  lines: { name: string; points: { bucketAt: string; value: number }[] }[]
) {
  return {
    title: { text: title, textStyle: { fontSize: 13 } },
    tooltip: { trigger: "axis" },
    legend: { top: 24, textStyle: { fontSize: 11 } },
    grid: { top: 64, left: 48, right: 24, bottom: 48 },
    xAxis: { type: "time" },
    yAxis: { type: "value", name: unit },
    dataZoom: [{ type: "inside" }, { type: "slider", height: 16 }],
    series: lines.map((l) => ({
      name: l.name,
      type: "line",
      showSymbol: false,
      data: l.points.map((p) => [p.bucketAt, p.value]),
    })),
  };
}

function ChartPanel({ title, option }: { title: string; option: ReturnType<typeof lineOption> }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm font-medium">{title}</CardTitle>
      </CardHeader>
      <CardContent>
        <ReactECharts option={option} style={{ height: 260 }} notMerge />
      </CardContent>
    </Card>
  );
}

export function MetricsView() {
  const { since, until } = React.useMemo(() => windowBounds(), []);
  const resource = useResource<MetricsHistory>({
    method: "GET",
    surface: "operator",
    path: "/operator/v1/metrics/history",
    query: { since, until },
    parse: parseMetricsHistory,
  });

  if (resource.error) {
    return <ErrorState message={resource.error} onRetry={resource.reload} />;
  }
  if (resource.loading || !resource.data) {
    return <LoadingState label="正在加载指标历史" rows={5} />;
  }
  const history = resource.data;
  if (history.series.length === 0) {
    return (
      <EmptyState
        title="暂无指标历史"
        description="采集器尚未产生任何数据点，或所选时间窗口内没有流量。"
      />
    );
  }

  const requests = seriesFor(history, "gateway_http_requests_total");
  const tokens = seriesFor(history, "gateway_tokens_total");
  const capacity = seriesFor(history, "tunnel_server_slots_total");
  const durationBuckets = seriesFor(history, "gateway_http_request_duration_seconds_bucket");

  const byEndpointStatus = requests.map((s) => ({
    name: `${s.labels.endpoint ?? "?"} ${s.labels.status ?? "?"}`,
    points: deltaByBucket(s),
  }));
  const successSeries = requests.filter((s) => s.labels.status === "200");
  const errorSeries = requests.filter((s) => s.labels.status && s.labels.status !== "200");
  const successRate = successSeries.map((s) => ({ name: "成功", points: deltaByBucket(s) }));
  const tokenLines = tokens.map((s) => ({ name: s.labels.direction ?? "?", points: deltaByBucket(s) }));
  const capacityLines = capacity.map((s) => ({
    name: `${s.labels.class ?? "?"} ${s.labels.state ?? "?"}`,
    points: s.points,
  }));

  const byEndpoint = new Map<string, MetricsHistorySeries[]>();
  for (const s of durationBuckets) {
    const key = s.labels.endpoint ?? "?";
    byEndpoint.set(key, [...(byEndpoint.get(key) ?? []), s]);
  }
  const p95Lines = Array.from(byEndpoint.entries()).map(([endpoint, buckets]) => ({
    name: endpoint,
    points: approxP95(buckets),
  }));

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="font-heading text-lg font-semibold">指标</h1>
        <p className="text-sm text-muted-foreground">
          机群级别的历史曲线：请求量、成功率、延迟、Token 用量与容量，窗口为过去 24 小时。
          这是平台运维视角，不按租户拆分——Gateway 指标按设计不带租户维度。
        </p>
      </div>
      <div className="grid gap-6 md:grid-cols-2">
        <ChartPanel title="请求量(按端点/状态)" option={lineOption("请求量(按端点/状态)", "req/窗口", byEndpointStatus)} />
        <ChartPanel title="成功请求数" option={lineOption("成功请求数", "req/窗口", successRate)} />
        <ChartPanel title="延迟 p95 近似值" option={lineOption("延迟 p95 近似值", "秒", p95Lines)} />
        <ChartPanel title="Token 用量" option={lineOption("Token 用量", "token/窗口", tokenLines)} />
        <ChartPanel title="容量(槽位数)" option={lineOption("容量(槽位数)", "槽位", capacityLines)} />
        {errorSeries.length === 0 ? null : (
          <ChartPanel
            title="非 200 请求"
            option={lineOption(
              "非 200 请求",
              "req/窗口",
              errorSeries.map((s) => ({ name: s.labels.status ?? "?", points: deltaByBucket(s) }))
            )}
          />
        )}
      </div>
    </div>
  );
}
