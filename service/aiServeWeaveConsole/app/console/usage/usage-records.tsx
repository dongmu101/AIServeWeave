"use client";

import * as React from "react";
import { ChevronDown, ChevronRight } from "lucide-react";

import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Pager } from "@/components/console/pager";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { meterLabel, priceSourceLabel, pricingNoteKey } from "@/components/console/usage-labels";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatCredits } from "@/lib/console/billing";
import type { ApiKeySummary } from "@/lib/console/contract";
import { formatDateTime } from "@/lib/console/format";
import {
  formatRate,
  meterDepth,
  parseUsageRecordPage,
  recordPricing,
  sortedMeters,
  USAGE_RECORDS_PAGE_SIZE,
  type UsageRecord,
} from "@/lib/console/usage-records";

const ALL = "__all__";

/**
 * UsageRecords lists this tenant's requests one by one, newest first, within
 * the page's time window (by request time), optionally narrowed to one model
 * or one key. A row expands to its meters: quantity, the part billed at the
 * meter's own price, the unit price and the cost, plus the rate and price
 * source. With billing off the server sends no prices, and none are shown.
 *
 * UsageRecords 在页面的时间窗口内（按请求时间）逐笔列出本租户的请求，新的在前，可再按
 * 模型或 Key 收窄。展开一行可看各计量项：数量、按本计量项单价计费的部分、单价与费用，
 * 以及倍率与价格来源。计费关闭时服务端不发送价格，也就不显示。
 */
export function UsageRecords({ since, until, models, keys }: { since: string; until: string; models: string[]; keys: ApiKeySummary[] }) {
  const { t, locale } = useI18n();
  const [model, setModel] = React.useState("");
  const [keyId, setKeyId] = React.useState("");
  const filters: Record<string, string> = { since, until, model, key_id: keyId };
  const records = usePagedResource<UsageRecord>({
    path: "/admin/v1/usage/records",
    filters,
    pageSize: USAGE_RECORDS_PAGE_SIZE,
    parse: parseUsageRecordPage,
  });
  const [open, setOpen] = React.useState<string | null>(null);
  const billing = records.items?.some((r) => r.costMicro !== null) ?? false;
  const keyLabel = (id: string) => {
    if (!id) return "—";
    const key = keys.find((k) => k.id === id);
    return key ? `${key.name} (${key.display})` : id;
  };

  return (
    <Card>
      <CardContent className="grid gap-3">
        <p className="text-xs text-muted-foreground"><Message id="tenant.usageRecordsHint" /></p>
        <div className="flex flex-wrap gap-3">
          <Select value={model === "" ? ALL : model} onValueChange={(next) => { if (next !== null) setModel(next === ALL ? "" : next); }}>
            <SelectTrigger className="w-full sm:w-56" aria-label={t("tenant.usageFilterModel")}>
              <SelectValue>{model === "" ? t("tenant.usageAllModels") : model}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}><Message id="tenant.usageAllModels" /></SelectItem>
              {models.map((m) => <SelectItem key={m} value={m}>{m}</SelectItem>)}
            </SelectContent>
          </Select>
          <Select value={keyId === "" ? ALL : keyId} onValueChange={(next) => { if (next !== null) setKeyId(next === ALL ? "" : next); }}>
            <SelectTrigger className="w-full sm:w-56" aria-label={t("tenant.usageFilterKey")}>
              <SelectValue>{keyId === "" ? t("tenant.usageAllKeys") : keyLabel(keyId)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}><Message id="tenant.usageAllKeys" /></SelectItem>
              {keys.map((k) => <SelectItem key={k.id} value={k.id}>{keyLabel(k.id)}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        {records.error ? <ErrorState message={records.error} onRetry={records.reload} />
          : records.loading || !records.items ? <LoadingState label={t("tenant.usageLoadingRecords")} rows={4} />
          : records.items.length === 0 ? <EmptyState title={t("tenant.usageNoRecords")} />
          : <>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8"><span className="sr-only"><Message id="tenant.usageShowDetail" /></span></TableHead>
                  <TableHead><Message id="tenant.time" /></TableHead>
                  <TableHead><Message id="tenant.models_98fd0c" /></TableHead>
                  <TableHead>API Key</TableHead>
                  <TableHead className="text-right"><Message id="tenant.inputTokens" /></TableHead>
                  <TableHead className="text-right"><Message id="tenant.outputTokens" /></TableHead>
                  {billing ? <TableHead className="text-right"><Message id="tenant.usageCost" /></TableHead> : null}
                </TableRow>
              </TableHeader>
              <TableBody>
                {records.items.map((r) => {
                  const expanded = open === r.requestId;
                  const note = pricingNoteKey(recordPricing(r));
                  return (
                    <React.Fragment key={r.requestId}>
                      <TableRow className="align-top">
                        <TableCell>
                          <Button
                            type="button"
                            size="icon-sm"
                            variant="ghost"
                            aria-expanded={expanded}
                            aria-label={t(expanded ? "tenant.usageHideDetail" : "tenant.usageShowDetail")}
                            onClick={() => setOpen(expanded ? null : r.requestId)}
                          >
                            {expanded ? <ChevronDown aria-hidden /> : <ChevronRight aria-hidden />}
                          </Button>
                        </TableCell>
                        <TableCell className="whitespace-nowrap">{formatDateTime(r.createdAt, undefined, locale)}</TableCell>
                        <TableCell><code className="text-xs break-all">{r.model}</code></TableCell>
                        <TableCell><span className="block max-w-40 truncate text-xs" title={keyLabel(r.keyId)}>{keyLabel(r.keyId)}</span></TableCell>
                        <TableCell className="text-right tabular-nums">
                          {r.promptTokens.toLocaleString(locale)}
                          {r.cacheReadTokens + r.cacheWriteTokens > 0 ? (
                            <span className="block text-xs text-muted-foreground whitespace-nowrap">
                              {t("tenant.usageCacheRead")} {r.cacheReadTokens.toLocaleString(locale)} · {t("tenant.usageCacheWrite")} {r.cacheWriteTokens.toLocaleString(locale)}
                            </span>
                          ) : null}
                        </TableCell>
                        <TableCell className="text-right tabular-nums">{r.completionTokens.toLocaleString(locale)}</TableCell>
                        {billing ? (
                          <TableCell className="text-right">
                            <span className="font-mono whitespace-nowrap">{r.costMicro !== null ? formatCredits(r.costMicro, locale) : "—"}</span>
                            {note ? <Badge variant="outline" className="mt-1 block w-fit ml-auto"><Message id={note} /></Badge> : null}
                          </TableCell>
                        ) : null}
                      </TableRow>
                      {expanded ? (
                        <TableRow>
                          <TableCell colSpan={billing ? 7 : 6} className="bg-muted/30">
                            <RecordDetail record={r} />
                          </TableCell>
                        </TableRow>
                      ) : null}
                    </React.Fragment>
                  );
                })}
              </TableBody>
            </Table>
            <Pager resource={records} loadedCount={records.items.length} />
          </>}
      </CardContent>
    </Card>
  );
}

/** RecordDetail is one request's meters and how they were priced.
 * RecordDetail 是一个请求的各计量项及其计价方式。 */
function RecordDetail({ record: r }: { record: UsageRecord }) {
  const { t, locale } = useI18n();
  const priced = r.costMicro !== null;
  return (
    <div className="grid gap-2 py-1">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead><Message id="tenant.usageMeter" /></TableHead>
            <TableHead className="text-right"><Message id="tenant.usageQuantity" /></TableHead>
            {priced ? <>
              <TableHead className="text-right"><Message id="tenant.usageBilled" /></TableHead>
              <TableHead className="text-right"><Message id="tenant.usageUnitPrice" /></TableHead>
              <TableHead className="text-right"><Message id="tenant.usageLineCost" /></TableHead>
            </> : null}
          </TableRow>
        </TableHeader>
        <TableBody>
          {sortedMeters(r.meters).map((line) => (
            <TableRow key={line.meter}>
              <TableCell style={{ paddingInlineStart: `${0.5 + meterDepth(line.meter)}rem` }} className="whitespace-nowrap">{meterLabel(line.meter, t)}</TableCell>
              <TableCell className="text-right tabular-nums">{line.quantity.toLocaleString(locale)}</TableCell>
              {priced ? <>
                <TableCell className="text-right tabular-nums">{line.billed.toLocaleString(locale)}</TableCell>
                <TableCell className="text-right font-mono">{line.priceMicroPerM !== null ? formatCredits(line.priceMicroPerM, locale) : "—"}</TableCell>
                <TableCell className="text-right font-mono">{line.costMicro !== null ? formatCredits(line.costMicro, locale) : "—"}</TableCell>
              </> : null}
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {priced ? (
        <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <li>{t("tenant.usagePriceSource", { source: priceSourceLabel(r.priceSource, t) })}</li>
          {r.baseCostMicro !== null ? <li>{t("tenant.usageBaseCost", { cost: formatCredits(r.baseCostMicro, locale) })}</li> : null}
          {r.ratePpm !== null && r.ratePpm > 0 ? <li>{t("tenant.usageRate", { rate: formatRate(r.ratePpm, locale) })}</li> : null}
          {r.tier > 0 ? <li>{t("tenant.usageTier", { tokens: r.tier.toLocaleString(locale) })}</li> : null}
          {r.charged === false && (r.costMicro ?? 0) > 0 ? <li><Message id="tenant.usageNotCharged" /></li> : null}
          <li><Message id="tenant.usageCreditsNote" /></li>
        </ul>
      ) : null}
      {r.adjusted ? <p className="text-xs text-muted-foreground"><Message id="tenant.usageAdjusted" /></p> : null}
    </div>
  );
}
