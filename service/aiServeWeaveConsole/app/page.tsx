import type { Metadata } from "next";
import Link from "next/link";
import { redirect } from "next/navigation";

import { Message } from "@/components/i18n/message";
import { LEGAL_PATHS, SiteFooter } from "@/components/site/site-footer";
import { SITE_CONTAINER, SiteHeader } from "@/components/site/site-header";
import { buttonVariants } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { DEFAULT_LANDING, loginPath } from "@/lib/console/internal-path";
import { loadMessages } from "@/lib/i18n/messages";
import { createTranslator } from "@/lib/i18n/translate";
import { serverConfig } from "@/lib/server/config";
import { legalEntity } from "@/lib/server/legal";
import { readLocale } from "@/lib/server/locale";
import { readSession } from "@/lib/server/session";
import { cn } from "@/lib/utils";

export async function generateMetadata(): Promise<Metadata> {
  const locale = await readLocale();
  const t = createTranslator(locale, await loadMessages(locale));
  // The root layout marks every page noindex; the landing page is meant to be found.
  // 根布局把所有页面标为 noindex；首页是希望被找到的。
  return {
    title: `${legalEntity().serviceName} · ${t("site.title")}`,
    description: t("site.heroSubtitle"),
    robots: { index: true, follow: true },
  };
}

/**
 * The public landing page. A signed-in member still goes straight to the
 * overview, as before; everyone else — including payment reviewers — sees what
 * is sold, where the prices are and where the terms are, without signing in.
 *
 * 公开首页。已登录成员照旧直接进入概览页；其他人（包括支付审核人员）无需登录，就能看到
 * 销售的产品、价格在哪里以及条款在哪里。
 */
export default async function Home() {
  const session = await readSession();
  if (session) redirect(DEFAULT_LANDING);

  const locale = await readLocale();
  const t = createTranslator(locale, await loadMessages(locale));
  const startHref = serverConfig().signupEnabled ? "/signup" : loginPath(DEFAULT_LANDING);
  const linkClass = "underline underline-offset-4 hover:text-foreground";
  const offers = [
    { title: t("site.offerApiTitle"), body: t("site.offerApiBody") },
    { title: t("site.offerCreditsTitle"), body: t("site.offerCreditsBody") },
    { title: t("site.offerPlansTitle"), body: t("site.offerPlansBody") },
  ];

  return (
    <>
      <SiteHeader />
      <main className={cn(SITE_CONTAINER, "flex flex-1 flex-col gap-12 py-12 sm:py-20")}>
        <section className="max-w-3xl space-y-5">
          <h1 className="font-heading text-3xl font-semibold tracking-tight break-keep text-balance sm:text-5xl">{t("site.heroTitle")}</h1>
          <p className="text-base text-muted-foreground sm:text-lg">{t("site.heroSubtitle")}</p>
          <div className="flex flex-wrap gap-3">
            <Link href="/pricing" className={cn(buttonVariants({ size: "lg" }))}>{t("site.viewPricing")}</Link>
            <Link href={startHref} className={cn(buttonVariants({ variant: "outline", size: "lg" }))}>{t("site.getStarted")}</Link>
          </div>
        </section>

        <section className="grid gap-4">
          <h2 className="text-xl font-semibold">{t("site.offerTitle")}</h2>
          <div className="grid gap-4 sm:grid-cols-3">
            {offers.map(offer => (
              <Card key={offer.title}>
                <CardHeader><CardTitle>{offer.title}</CardTitle></CardHeader>
                <CardContent className="text-sm text-muted-foreground">{offer.body}</CardContent>
              </Card>
            ))}
          </div>
          <p className="text-sm text-muted-foreground">
            <Message id="site.purchaseNote" values={{
              terms: <Link href={LEGAL_PATHS.terms} className={linkClass}>{t("site.terms")}</Link>,
              refund: <Link href={LEGAL_PATHS.refund} className={linkClass}>{t("site.refund")}</Link>,
            }} />
          </p>
        </section>
      </main>
      <SiteFooter />
    </>
  );
}
