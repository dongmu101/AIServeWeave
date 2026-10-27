"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import type { Locale } from "@/lib/i18n/locale";

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
  lines: { name: string; points: { bucketAt: string; value: number }[] }[],
  locale: Locale,
) {
  return {
    title: { text: title, textStyle: { fontSize: 13 } },
    tooltip: { trigger: "axis", valueFormatter: (value: unknown) => new Intl.NumberFormat(locale).format(Number(value)) },
    legend: { top: 24, textStyle: { fontSize: 11 } },
    grid: { top: 64, left: 48, right: 24, bottom: 48 },
    xAxis: { type: "time", axisLabel: { formatter: (value: number) => new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(value) }, axisPointer: { label: { formatter: ({ value }: { value: number }) => new Intl.DateTimeFormat(locale, { dateStyle: "short", timeStyle: "short" }).format(new Date(value)) } } },
    yAxis: { type: "value", name: unit, axisLabel: { formatter: (value: number) => new Intl.NumberFormat(locale).format(value) } },
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
  const { locale, t } = useI18n();
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
    return <LoadingState label={t("operator.loadingMetricHistory")} rows={5} />;
  }
  const history = resource.data;
  if (history.series.length === 0) {
    return (
      <EmptyState
        title={t("operator.noMetricHistoryYet")}
        description={t("operator.theCollectorHasNotProducedAnyDataPoints")}
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
  const successRate = successSeries.map((s) => ({ name: t("tenant.succeeded"), points: deltaByBucket(s) }));
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
        <h1 className="font-heading text-lg font-semibold"><Message id="operator.metrics" /></h1>
        <p className="text-sm text-muted-foreground">
          <Message id="operator.fleetWideHistoricalChartsForRequestsSuccessRate" /></p>
      </div>
      <div className="grid gap-6 md:grid-cols-2">
        <ChartPanel title={t("operator.requestsByEndpointStatus")} option={lineOption(t("operator.requestsByEndpointStatus"), t("operator.reqWindow") , byEndpointStatus, locale)} />
        <ChartPanel title={t("operator.successfulRequests")} option={lineOption(t("operator.successfulRequests"), t("operator.reqWindow") , successRate, locale)} />
        <ChartPanel title={t("operator.approximateP95Latency")} option={lineOption(t("operator.approximateP95Latency"), t("operator.seconds") , p95Lines, locale)} />
        <ChartPanel title={t("operator.tokenUsage")} option={lineOption(t("operator.tokenUsage"), t("operator.tokensWindow") , tokenLines, locale)} />
        <ChartPanel title={t("operator.capacitySlots")} option={lineOption(t("operator.capacitySlots"), t("operator.slots") , capacityLines, locale)} />
        {errorSeries.length === 0 ? null : (
          <ChartPanel
            title={t("operator.non200Requests")}
            option={lineOption(
              t("operator.non200Requests"),
              t("operator.reqWindow"),
              errorSeries.map((s) => ({ name: s.labels.status ?? "?", points: deltaByBucket(s) })),
              locale,
            )}
          />
        )}
      </div>
    </div>
  );
}
