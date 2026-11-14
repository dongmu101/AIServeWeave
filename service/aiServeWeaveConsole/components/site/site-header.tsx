import Link from "next/link";

import { LanguageSwitcher } from "@/components/i18n/language-switcher";
import { SupportLink } from "@/components/site/support-link";
import { buttonVariants } from "@/components/ui/button";
import { loadMessages } from "@/lib/i18n/messages";
import { createTranslator } from "@/lib/i18n/translate";
import { DEFAULT_LANDING, loginPath } from "@/lib/console/internal-path";
import { serverConfig } from "@/lib/server/config";
import { legalEntity } from "@/lib/server/legal";
import { readLocale } from "@/lib/server/locale";
import { readSession } from "@/lib/server/session";
import { cn } from "@/lib/utils";

/** SITE_CONTAINER is the content column every public header, page and footer
 * shares, so their left and right edges line up.
 *
 * SITE_CONTAINER 是所有公开页首、正文与页脚共用的内容栏，使它们的左右边缘对齐。 */
export const SITE_CONTAINER = "mx-auto w-full max-w-5xl px-4 sm:px-8";

/**
 * SiteHeader is the top bar of the public pages: the product name back to the
 * landing page, pricing, the language picker, and either the console or
 * sign-in / sign-up. On a phone it takes two rows — name and language on top,
 * the actions below, right-aligned — instead of wrapping wherever it overflows.
 * `signInNext` is where sign-in returns to.
 *
 * SiteHeader 是公开页面的顶栏：产品名称（返回首页）、价格、语言选择，以及控制台或
 * 登录／注册入口。手机上分成两行——上行是名称与语言，下行是右对齐的操作——而不是在溢出处
 * 随意换行。`signInNext` 是登录后返回的地址。
 */
export async function SiteHeader({ signInNext = DEFAULT_LANDING }: { signInNext?: string }) {
  const locale = await readLocale();
  const t = createTranslator(locale, await loadMessages(locale));
  const session = await readSession();

  return (
    <header className="border-b">
      <div className={cn(SITE_CONTAINER, "flex flex-wrap items-center gap-x-2 gap-y-3 py-3")}>
        <Link href="/" className="mr-auto font-heading text-base font-semibold tracking-tight">{legalEntity().serviceName}</Link>
        <div className="flex items-center gap-1 sm:order-2"><SupportLink label={t("common.telegramSupport")} /><LanguageSwitcher /></div>
        <nav className="order-3 flex w-full items-center justify-end gap-2 sm:order-1 sm:w-auto">
          <Link href="/pricing" className={cn(buttonVariants({ variant: "ghost", size: "sm" }))}>{t("site.pricing")}</Link>
          {session ? (
            <Link href={DEFAULT_LANDING} className={cn(buttonVariants({ size: "sm" }))}>{t("site.openConsole")}</Link>
          ) : (
            <>
              <Link href={loginPath(signInNext)} className={cn(buttonVariants({ variant: "outline", size: "sm" }))}>{t("pricing.signIn")}</Link>
              {serverConfig().signupEnabled ? (
                <Link href="/signup" className={cn(buttonVariants({ size: "sm" }))}>{t("pricing.signUp")}</Link>
              ) : null}
            </>
          )}
        </nav>
      </div>
    </header>
  );
}
