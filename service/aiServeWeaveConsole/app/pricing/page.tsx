import type { Metadata } from "next";
import Link from "next/link";

import { SiteFooter } from "@/components/site/site-footer";
import { SiteHeader } from "@/components/site/site-header";
import { buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
  DEFAULT_PRICE_MODEL,
  formatCredits,
  formatMoney,
  parseCatalog,
  type Catalog,
} from "@/lib/console/billing";
import { accountKindLabel } from "@/lib/console/account-sales";
import { loginPath, signupPath } from "@/lib/console/internal-path";
import { loadMessages } from "@/lib/i18n/messages";
import { createTranslator } from "@/lib/i18n/translate";
import { callControlPlane } from "@/lib/server/control-plane";
import { serverConfig } from "@/lib/server/config";
import { readLocale } from "@/lib/server/locale";
import { readSession } from "@/lib/server/session";
import { cn } from "@/lib/utils";

/** BUY_TARGET is where every Buy button leads once the visitor is signed in.
 *
 * BUY_TARGET 是访客登录后，每个「购买」按钮通向的地方。 */
const BUY_TARGET = "/console/billing";

export async function generateMetadata(): Promise<Metadata> {
  const locale = await readLocale();
  const t = createTranslator(locale, await loadMessages(locale));
  // The root layout marks every page noindex; this one is meant to be found.
  // 根布局把所有页面标为 noindex；这一页是希望被找到的。
  return { title: t("pricing.title"), robots: { index: true, follow: true } };
}

/**
 * The control plane uses 404 for billing disabled. A transient failure must
 * not look like an empty catalog. This request needs no session.
 */
async function loadCatalog(): Promise<Catalog | "disabled" | "failed"> {
  const result = await callControlPlane({ method: "GET", path: "/billing/v1/catalog" });
  if (result.kind !== "response") return "failed";
  if (result.status === 404) return "disabled";
  if (result.status !== 200) return "failed";
  try {
    return parseCatalog(JSON.parse(result.text));
  } catch {
    return "failed";
  }
}

/**
 * The public pricing page. It needs no session: it only reads the catalog. The
 * Buy buttons are plain links, so the decision is made by the pages they lead
 * to — a visitor without a session signs up or in before opening the selected
 * billing tab. The control plane authorizes every purchase.
 *
 * 公开的价格页。它不需要会话，只读取目录。「购买」按钮只是普通链接，决定权在它们通向的
 * 页面。未登录的访客注册或登录后直接进入对应计费标签，每次购买仍由控制面授权。
 */
export default async function PricingPage() {
  const locale = await readLocale();
  const t = createTranslator(locale, await loadMessages(locale));
  const [catalog, session] = await Promise.all([loadCatalog(), readSession()]);
  const signupEnabled = serverConfig().signupEnabled;
  // Each Buy button opens the billing tab that sells what its card shows.
  // 每个「购买」按钮打开出售该卡片内容的计费分区。
  const buy = (tab: "accounts" | "presales" | "packages") => {
    const target = `${BUY_TARGET}?tab=${tab}`;
    return (
      <Link href={session ? target : signupEnabled ? signupPath(target) : loginPath(target)} className={cn(buttonVariants({ size: "sm" }))}>
        {t("pricing.buy")}
      </Link>
    );
  };

  return (
    <>
    <SiteHeader signInNext={BUY_TARGET} />
    <main className="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-8 px-4 py-8 sm:px-8">
      <header>
        <div className="space-y-1">
          <h1 className="font-heading text-2xl font-semibold tracking-tight">{t("pricing.heading")}</h1>
          <p className="text-sm text-muted-foreground">{t("site.heroSubtitle")}</p>
        </div>
      </header>

      {typeof catalog === "string" ? (
        <p id="models" className="rounded-lg border p-6 text-center text-sm text-muted-foreground">{t(catalog === "disabled" ? "pricing.unavailable" : "pricing.catalogLoadFailed")}</p>
      ) : (
        <>
          {catalog.accounts.length + catalog.presales.length + catalog.packages.length === 0 ? (
            <p className="rounded-lg border p-6 text-center text-sm text-muted-foreground">{t("pricing.empty")}</p>
          ) : null}
          {catalog.accounts.length > 0 ? (
            <section className="grid grid-cols-1 gap-3">
              <div className="space-y-1">
                <h2 className="text-lg font-medium">{t("pricing.accounts")}</h2>
                <p className="text-sm text-muted-foreground">{t("pricing.accountsHint")}</p>
              </div>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {catalog.accounts.map(account => (
                  <Card key={account.id}>
                    <CardHeader><CardTitle className="wrap-anywhere">{account.name}</CardTitle></CardHeader>
                    <CardContent className="flex items-center justify-between gap-3">
                      <p className="text-lg font-semibold">
                        {t("tenant.accountMonthlyPrice", { price: formatMoney(account.priceMinor, account.currency, locale) })}
                      </p>
                      {buy("accounts")}
                    </CardContent>
                  </Card>
                ))}
              </div>
            </section>
          ) : null}

          {catalog.presales.length > 0 ? (
            <section className="grid grid-cols-1 gap-3">
              <h2 className="text-lg font-medium">{t("pricing.presales")}</h2>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {catalog.presales.map(presale => (
                  <Card key={presale.id}>
                    <CardHeader>
                      <CardTitle className="wrap-anywhere">{presale.name}</CardTitle>
                      <p className="text-sm text-muted-foreground">{accountKindLabel(presale.kind)}</p>
                    </CardHeader>
                    <CardContent className="grid gap-3">
                      <p className="text-sm text-muted-foreground">{t("tenant.presaleDelivery")}</p>
                      {presale.available && presale.remaining !== null ? (
                        <p className="text-sm font-medium">{t("pricing.remaining", { count: presale.remaining })}</p>
                      ) : null}
                      <div className="flex items-center justify-between gap-3">
                        <p className="text-lg font-semibold">
                          {t("tenant.accountMonthlyPrice", { price: formatMoney(presale.priceMinor, presale.currency, locale) })}
                        </p>
                        {presale.available ? buy("presales") : <span className="text-sm font-medium text-muted-foreground">{t("tenant.presaleSoldOut")}</span>}
                      </div>
                    </CardContent>
                  </Card>
                ))}
              </div>
            </section>
          ) : null}

          {catalog.packages.length > 0 ? (
            <section className="grid grid-cols-1 gap-3">
              <div className="space-y-1">
                <h2 className="text-lg font-medium">{t("pricing.packages")}</h2>
                <p className="text-sm text-muted-foreground">{t("pricing.packagesHint")}</p>
              </div>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {catalog.packages.map(pkg => (
                  <Card key={pkg.id}>
                    <CardHeader><CardTitle className="wrap-anywhere">{pkg.name}</CardTitle></CardHeader>
                    <CardContent className="flex items-center justify-between gap-3">
                      <div>
                        {pkg.kind === "fixed" ? (
                          <>
                            <p className="text-lg font-semibold">{formatMoney(pkg.priceMinor, pkg.currency, locale)}</p>
                            <p className="text-sm text-muted-foreground">
                              {t("pricing.packageCredits", { credits: formatCredits(pkg.creditsMicro, locale) })}
                            </p>
                          </>
                        ) : (
                          <p className="text-sm">
                            {t("pricing.customAmount", {
                              min: formatMoney(pkg.minAmountMinor, pkg.currency, locale),
                              max: formatMoney(pkg.maxAmountMinor, pkg.currency, locale),
                            })}
                          </p>
                        )}
                      </div>
                      {buy("packages")}
                    </CardContent>
                  </Card>
                ))}
              </div>
            </section>
          ) : null}

          <section id="models" className="grid scroll-mt-4 grid-cols-1 gap-3">
            <div className="space-y-1">
              <h2 className="text-lg font-medium">{t("pricing.prices")}</h2>
              <p className="text-sm text-muted-foreground">{t("pricing.pricesHint")}</p>
              <Link href="/console/docs/clients" className="text-sm font-medium underline underline-offset-4">{t("tenant.docsClientsTitle")}</Link>
            </div>
            {catalog.prices.length === 0 ? (
              <p className="rounded-lg border p-6 text-sm text-muted-foreground">{t("pricing.noModelPrices")}</p>
            ) : (
              <div className="rounded-lg border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("pricing.model")}</TableHead>
                      <TableHead className="h-auto py-2 text-right whitespace-normal">{t("pricing.input")}</TableHead>
                      <TableHead className="h-auto py-2 text-right whitespace-normal">{t("pricing.output")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {catalog.prices.map(price => (
                      <TableRow key={price.model}>
                        <TableCell className="whitespace-normal wrap-anywhere">
                          {price.model === DEFAULT_PRICE_MODEL ? t("pricing.defaultModel") : price.model}
                        </TableCell>
                        <TableCell className="text-right">{formatCredits(price.inputMicroPerMTok, locale)}</TableCell>
                        <TableCell className="text-right">{formatCredits(price.outputMicroPerMTok, locale)}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
          </section>
        </>
      )}
    </main>
    <SiteFooter />
    </>
  );
}
