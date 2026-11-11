import type { Metadata } from "next";
import Link from "next/link";

import { LanguageSwitcher } from "@/components/i18n/language-switcher";
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
import { loginPath } from "@/lib/console/internal-path";
import { loadMessages } from "@/lib/i18n/messages";
import { createTranslator } from "@/lib/i18n/translate";
import { serverConfig } from "@/lib/server/config";
import { callControlPlane } from "@/lib/server/control-plane";
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
 * loadCatalog reads the public catalog. Any failure — a deployment without
 * billing answers 404, a control plane that is down is unreachable — is the same
 * to a visitor: nothing to show yet. No token is sent.
 *
 * loadCatalog 读取公开目录。任何失败——未开启计费的部署回 404，控制面宕机则不可达——
 * 对访客来说都一样：暂时没有可展示的内容。不发送任何令牌。
 */
async function loadCatalog(): Promise<Catalog | null> {
  const result = await callControlPlane({ method: "GET", path: "/billing/v1/catalog" });
  if (result.kind !== "response" || result.status !== 200) return null;
  try {
    return parseCatalog(JSON.parse(result.text));
  } catch {
    return null;
  }
}

/**
 * The public pricing page. It needs no session: it only reads the catalog. The
 * Buy buttons are plain links, so the decision is made by the pages they lead
 * to — a visitor without a session is bounced to sign-in with the purchase page
 * as the return address, and the control plane still authorizes every purchase.
 *
 * 公开的价格页。它不需要会话，只读取目录。「购买」按钮只是普通链接，决定权在它们通向的
 * 页面——没有会话的访客会被送去登录、并以购买页作为返回地址；每一次购买仍由控制面授权。
 */
export default async function PricingPage() {
  const locale = await readLocale();
  const t = createTranslator(locale, await loadMessages(locale));
  const [catalog, session] = await Promise.all([loadCatalog(), readSession()]);
  const signedIn = session !== null;
  const buyHref = signedIn ? BUY_TARGET : loginPath(BUY_TARGET);
  const buy = (
    <Link href={buyHref} className={cn(buttonVariants({ size: "sm" }))}>
      {t("pricing.buy")}
    </Link>
  );
  const empty = catalog !== null && catalog.accounts.length + catalog.presales.length + catalog.packages.length + catalog.prices.length === 0;

  return (
    <main className="mx-auto flex w-full max-w-5xl flex-1 flex-col gap-8 p-4 sm:p-8">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <div className="space-y-1">
          <h1 className="font-heading text-2xl font-semibold tracking-tight">{t("pricing.title")}</h1>
          <p className="text-sm text-muted-foreground">{t("pricing.subtitle")}</p>
        </div>
        <nav className="flex items-center gap-2">
          <LanguageSwitcher />
          {signedIn ? (
            <Link href="/console" className={cn(buttonVariants({ variant: "outline", size: "sm" }))}>
              {t("pricing.openConsole")}
            </Link>
          ) : (
            <>
              <Link href={loginPath(BUY_TARGET)} className={cn(buttonVariants({ variant: "outline", size: "sm" }))}>
                {t("pricing.signIn")}
              </Link>
              {serverConfig().signupEnabled ? (
                <Link href="/signup" className={cn(buttonVariants({ size: "sm" }))}>
                  {t("pricing.signUp")}
                </Link>
              ) : null}
            </>
          )}
        </nav>
      </header>

      {catalog === null ? (
        <p className="rounded-lg border p-6 text-center text-sm text-muted-foreground">{t("pricing.unavailable")}</p>
      ) : empty ? (
        <p className="rounded-lg border p-6 text-center text-sm text-muted-foreground">{t("pricing.empty")}</p>
      ) : (
        <>
          {catalog.accounts.length > 0 ? (
            <section className="grid gap-3">
              <h2 className="text-lg font-medium">{t("pricing.accounts")}</h2>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {catalog.accounts.map(account => (
                  <Card key={account.id}>
                    <CardHeader><CardTitle className="break-all">{account.name}</CardTitle></CardHeader>
                    <CardContent className="flex items-center justify-between gap-3">
                      <p className="text-lg font-semibold">
                        {t("tenant.accountMonthlyPrice", { price: formatMoney(account.priceMinor, account.currency, locale) })}
                      </p>
                      {buy}
                    </CardContent>
                  </Card>
                ))}
              </div>
            </section>
          ) : null}

          {catalog.presales.length > 0 ? (
            <section className="grid gap-3">
              <h2 className="text-lg font-medium">{t("pricing.presales")}</h2>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {catalog.presales.map(presale => (
                  <Card key={presale.id}>
                    <CardHeader>
                      <CardTitle className="break-all">{presale.name}</CardTitle>
                      <p className="text-sm text-muted-foreground">{accountKindLabel(presale.kind)} · {t("tenant.presaleBadge")}</p>
                    </CardHeader>
                    <CardContent className="grid gap-3">
                      <p className="text-sm text-muted-foreground">{t("tenant.presaleDelivery")}</p>
                      <div className="flex items-center justify-between gap-3">
                        <p className="text-lg font-semibold">
                          {t("tenant.accountMonthlyPrice", { price: formatMoney(presale.priceMinor, presale.currency, locale) })}
                        </p>
                        {presale.available ? buy : <span className="text-sm font-medium text-muted-foreground">{t("tenant.presaleSoldOut")}</span>}
                      </div>
                    </CardContent>
                  </Card>
                ))}
              </div>
            </section>
          ) : null}

          {catalog.packages.length > 0 ? (
            <section className="grid gap-3">
              <h2 className="text-lg font-medium">{t("pricing.packages")}</h2>
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {catalog.packages.map(pkg => (
                  <Card key={pkg.id}>
                    <CardHeader><CardTitle className="break-all">{pkg.name}</CardTitle></CardHeader>
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
                      {buy}
                    </CardContent>
                  </Card>
                ))}
              </div>
            </section>
          ) : null}

          {catalog.prices.length > 0 ? (
            <section className="grid gap-3">
              <h2 className="text-lg font-medium">{t("pricing.prices")}</h2>
              <div className="rounded-lg border">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("pricing.model")}</TableHead>
                      <TableHead className="text-right">{t("pricing.input")}</TableHead>
                      <TableHead className="text-right">{t("pricing.output")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {catalog.prices.map(price => (
                      <TableRow key={price.model}>
                        <TableCell className="break-all">
                          {price.model === DEFAULT_PRICE_MODEL ? t("pricing.defaultModel") : price.model}
                        </TableCell>
                        <TableCell className="text-right">{formatCredits(price.inputMicroPerMTok, locale)}</TableCell>
                        <TableCell className="text-right">{formatCredits(price.outputMicroPerMTok, locale)}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </section>
          ) : null}
        </>
      )}
    </main>
  );
}
