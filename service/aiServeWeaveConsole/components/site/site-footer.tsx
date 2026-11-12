import Link from "next/link";

import { loadMessages } from "@/lib/i18n/messages";
import { createTranslator } from "@/lib/i18n/translate";
import { legalEntity } from "@/lib/server/legal";
import { readLocale } from "@/lib/server/locale";
import { SITE_CONTAINER } from "@/components/site/site-header";
import { cn } from "@/lib/utils";

/** LEGAL_PATHS are the public pages every footer points to.
 *
 * LEGAL_PATHS 是每个页脚都指向的公开页面。 */
export const LEGAL_PATHS = { terms: "/terms", privacy: "/privacy", refund: "/refund", pricing: "/pricing" } as const;

/**
 * SiteFooter is the footer of every public page: links to pricing and the
 * legal documents, the operator's name and how to reach them. Payment
 * reviewers look for exactly these on every page a buyer can land on.
 *
 * SiteFooter 是每个公开页面的页脚：价格与法律文件的链接、营运方名称与联系方式。
 * 支付审核人员会在买家可能到达的每个页面上找这些内容。
 */
export async function SiteFooter({ className }: { className?: string }) {
  const locale = await readLocale();
  const t = createTranslator(locale, await loadMessages(locale));
  const entity = legalEntity();
  // min-h-6 keeps each link a 24px-tall target; they are not inline in a sentence.
  // min-h-6 让每个链接至少 24px 高，便于点按；它们不是句中的行内链接。
  const link = "inline-flex min-h-6 items-center underline-offset-4 hover:text-foreground hover:underline";

  return (
    <footer className={cn("border-t py-6 text-xs text-muted-foreground", className)}>
      <div className={cn(SITE_CONTAINER, "flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between")}>
        <nav aria-label={t("site.legalNav")} className="flex flex-wrap gap-x-4 gap-y-1">
          <Link href={LEGAL_PATHS.pricing} className={link}>{t("site.pricing")}</Link>
          <Link href={LEGAL_PATHS.terms} className={link}>{t("site.terms")}</Link>
          <Link href={LEGAL_PATHS.privacy} className={link}>{t("site.privacy")}</Link>
          <Link href={LEGAL_PATHS.refund} className={link}>{t("site.refund")}</Link>
        </nav>
        <div className="space-y-1 sm:max-w-md sm:text-right">
          <p>{t("site.copyright", { year: new Date().getUTCFullYear(), company: entity.company })}</p>
          <p className="wrap-anywhere">
            {entity.email ? (
              <a href={`mailto:${entity.email}`} className={link}>{t("site.contact", { email: entity.email })}</a>
            ) : (
              t("site.contact", { email: entity.emailText })
            )}
          </p>
        </div>
      </div>
    </footer>
  );
}
