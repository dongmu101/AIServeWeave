"use client";

import * as React from "react";

import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { BillingDisabled } from "@/components/console/billing-tables";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { ModelFamilyIcon } from "@/components/console/model-family-icon";
import { meterLabel } from "@/components/console/usage-labels";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { formatCredits } from "@/lib/console/billing";
import { groupByFamily } from "@/lib/console/model-family";
import { basePrice, parseTenantPrices, priceTiers, type PriceTier, type TenantPrice } from "@/lib/console/tenant-prices";
import { formatRate } from "@/lib/console/usage-records";

/** COLUMNS are the meters every model's base tier is shown with; the rest
 * (one-hour cache writes, reasoning, long-context tiers, service tiers) are
 * under "show all tiers".
 *
 * COLUMNS 是每个模型基础档直接显示的计量项；其余（1 小时缓存写入、推理、长上下文档位、
 * 服务等级）放在「查看全部档位」里。 */
const COLUMNS = ["input", "output", "input.cache_read", "input.cache_write"] as const;

/**
 * TenantPrices lists the sale prices of every model this tenant may use, on
 * its price plan, in credits per million tokens. A model whose price list has
 * more than its base-tier columns can be expanded to every tier. With billing
 * off the endpoint is not mounted and the section says so, unless the caller
 * marks it optional (the overview), in which case it renders nothing.
 *
 * TenantPrices 列出本租户按其价格方案可用的每个模型的售价，单位为点 / 百万 token。单价
 * 表中不止基础档几列的模型可以展开查看全部档位。计费关闭时端点未挂载，分区会说明这一点；
 * 调用方标记为 optional（概览页）时则不渲染。
 */
export function TenantPrices({ optional = false }: { optional?: boolean }) {
  const { t } = useI18n();
  const prices = useResource<TenantPrice[]>({ method: "GET", path: "/admin/v1/billing/prices", parse: parseTenantPrices });
  const [open, setOpen] = React.useState<string | null>(null);

  if (optional && prices.errorKind === "not_found") return null;
  return (
    <Card>
      <CardHeader>
        <CardTitle><Message id="tenant.billingPrices" /></CardTitle>
      </CardHeader>
      <CardContent className="grid gap-3">
        <p className="text-sm text-muted-foreground"><Message id="tenant.billingPricesNote" /></p>
        {prices.errorKind === "not_found" ? <BillingDisabled />
          : prices.error ? <ErrorState message={prices.error} onRetry={prices.reload} />
          : prices.loading || !prices.data ? <LoadingState label={t("tenant.billingPricesLoading")} rows={3} />
          : prices.data.length === 0 ? <EmptyState title={t("tenant.billingPricesEmpty")} />
          : (
            <div className="overflow-hidden rounded-lg border">
              <Table>
                <TableHeader className="bg-muted/40">
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="pl-4"><Message id="tenant.models_98fd0c" /></TableHead>
                    {COLUMNS.map((meter) => <TableHead key={meter} className="text-right">{meterLabel(meter, t)}</TableHead>)}
                    <TableHead className="pr-4"><span className="sr-only"><Message id="tenant.billingPriceShowTiers" /></span></TableHead>
                  </TableRow>
                </TableHeader>
                {groupByFamily(prices.data, (price) => price.model).map((group) => (
                  <TableBody key={group.id} className="border-b last:border-b-0">
                    <TableRow className="border-b bg-muted/20 hover:bg-muted/20">
                      <TableHead scope="rowgroup" colSpan={COLUMNS.length + 2} className="h-auto px-4 py-2.5 text-foreground">
                        <div className="flex items-center gap-2.5">
                          <ModelFamilyIcon family={group.family} />
                          <span className="text-sm font-semibold">{group.family?.name ?? t("tenant.billingPriceOtherFamily")}</span>
                          {group.family && group.family.vendor !== group.family.name ? <span className="text-xs font-normal text-muted-foreground">{group.family.vendor}</span> : null}
                          <Badge variant="secondary" className="ml-auto font-normal">{t("tenant.models_4678e8", { count: group.items.length })}</Badge>
                        </div>
                      </TableHead>
                    </TableRow>
                    {group.items.map((price) => <PriceRow key={price.model} price={price} expanded={open === price.model} onToggle={(next) => setOpen(next ? price.model : null)} />)}
                  </TableBody>
                ))}
              </Table>
            </div>
          )}
      </CardContent>
    </Card>
  );
}

/** PriceRow is one model's base-tier prices, plus its other tiers when
 * expanded. The model name is indented to line up with its family's name.
 *
 * PriceRow 是一个模型的基础档单价，展开时附上其余档位。模型名缩进到与系列名称对齐。 */
function PriceRow({ price, expanded, onToggle }: { price: TenantPrice; expanded: boolean; onToggle: (expanded: boolean) => void }) {
  const { t, locale } = useI18n();
  const tiers = priceTiers(price);
  const more = tiers.length > 1 || (tiers[0]?.items.some((item) => !(COLUMNS as readonly string[]).includes(item.meter)) ?? false);
  return (
    <>
      <TableRow className="align-top">
        <TableCell className="py-2.5 pl-4 whitespace-normal sm:pl-13.5">
          <code className="font-mono text-[13px] break-all">{price.model}</code>
          {price.ratePpm !== 1_000_000 ? <span className="mt-0.5 block text-xs text-muted-foreground">{t("tenant.billingPriceRate", { rate: formatRate(price.ratePpm, locale) })}</span> : null}
        </TableCell>
        {COLUMNS.map((meter) => {
          const value = basePrice(price, meter);
          return (
            <TableCell key={meter} className="py-2.5 text-right font-mono tabular-nums">
              {value === null ? <span className="text-muted-foreground/60">—</span> : formatCredits(value, locale)}
            </TableCell>
          );
        })}
        <TableCell className="py-1.5 pr-4 text-right">
          {more ? (
            <Button type="button" size="sm" variant="ghost" aria-expanded={expanded} onClick={() => onToggle(!expanded)}>
              <Message id={expanded ? "tenant.billingPriceHideTiers" : "tenant.billingPriceShowTiers"} />
            </Button>
          ) : null}
        </TableCell>
      </TableRow>
      {expanded ? (
        <TableRow className="hover:bg-transparent">
          <TableCell colSpan={COLUMNS.length + 2} className="bg-muted/30 px-4 sm:pl-13.5">
            <div className="grid gap-3 py-1 sm:grid-cols-2">
              {tiers.map((tier) => <TierList key={`${tier.variant}|${tier.aboveInputTokens}`} tier={tier} />)}
            </div>
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

/** TierList is one tier's prices, as a short list.
 * TierList 以简短列表展示一个档位的单价。 */
function TierList({ tier }: { tier: PriceTier }) {
  const { t, locale } = useI18n();
  const title = [
    tier.aboveInputTokens > 0 ? t("tenant.billingPriceTier", { tokens: tier.aboveInputTokens.toLocaleString(locale) }) : t("tenant.billingPriceBaseTier"),
    tier.variant ? t("tenant.billingPriceVariant", { variant: tier.variant }) : "",
  ].filter(Boolean).join(" · ");
  return (
    <div className="grid gap-1">
      <span className="text-xs font-medium">{title}</span>
      <dl className="grid grid-cols-[1fr_auto] gap-x-4 gap-y-0.5 text-xs">
        {tier.items.map((item) => (
          <React.Fragment key={item.meter}>
            <dt className="text-muted-foreground">{meterLabel(item.meter, t)}</dt>
            <dd className="text-right font-mono">{formatCredits(item.saleMicroPerM, locale)}</dd>
          </React.Fragment>
        ))}
      </dl>
    </div>
  );
}
