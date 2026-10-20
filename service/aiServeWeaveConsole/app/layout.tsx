import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

import { Toaster } from "@/components/ui/sonner";
import { I18nProvider } from "@/components/i18n/provider";
import { readLocale } from "@/lib/server/locale";
import { loadMessages } from "@/lib/i18n/messages";
import { createTranslator } from "@/lib/i18n/translate";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

/**
 * generateMetadata names the product rather than the framework. `robots` is set
 * because every page behind this layout is an administrative view of one
 * tenant's data: there is nothing here for an index to hold.
 *
 * generateMetadata 点出的是产品而不是框架。这里设置 `robots`，是因为本布局之下的每个页面都是
 * 对某一个租户数据的管理视图：没有任何内容值得被索引收录。
 */
export async function generateMetadata(): Promise<Metadata> {
  const locale = await readLocale();
  const t = createTranslator(locale, await loadMessages(locale));
  return { title: t("common.consoleTitle"), description: t("common.consoleDescription"), robots: { index: false, follow: false } };
}

export default async function RootLayout({ children }: LayoutProps<"/">) {
  const locale = await readLocale();
  const messages = await loadMessages(locale);
  return (
    <html
      lang={locale}
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      <body className="flex min-h-full flex-col bg-background text-foreground">
        <I18nProvider initialLocale={locale} initialMessages={messages}>
          {children}
          <Toaster position="top-center" />
        </I18nProvider>
      </body>
    </html>
  );
}
