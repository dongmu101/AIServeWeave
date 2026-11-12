import type { Metadata } from "next";

import { SiteFooter } from "@/components/site/site-footer";
import { SiteHeader } from "@/components/site/site-header";
import { legalDocument } from "@/lib/legal/documents";
import { LEGAL_UPDATED, type LegalBlock, type LegalKind } from "@/lib/legal/types";
import { loadMessages } from "@/lib/i18n/messages";
import { createTranslator } from "@/lib/i18n/translate";
import { legalEntity } from "@/lib/server/legal";
import { readLocale } from "@/lib/server/locale";

/** legalMetadata titles a legal page and, unlike the console, lets it be indexed.
 *
 * legalMetadata 为法律页面设定标题，并且与控制台不同，允许被索引。 */
export async function legalMetadata(kind: LegalKind): Promise<Metadata> {
  const locale = await readLocale();
  const { document } = legalDocument(kind, locale, legalEntity());
  return { title: `${document.title} · ${legalEntity().serviceName}`, robots: { index: true, follow: true } };
}

function Block({ block }: { block: LegalBlock }) {
  if (typeof block === "string") return <p>{block}</p>;
  return (
    <ul className="list-disc space-y-1 pl-5">
      {block.map(item => <li key={item}>{item}</li>)}
    </ul>
  );
}

/**
 * LegalPage renders one legal document in the visitor's language, or in
 * English with a notice when that language has no text of its own. The
 * article carries its real language so screen readers pronounce it correctly.
 *
 * LegalPage 以访客的语言渲染一份法律文件；该语言没有正文时改用英文并附提示。文章标注
 * 其实际语言，让屏幕阅读器正确发音。
 */
export async function LegalPage({ kind }: { kind: LegalKind }) {
  const locale = await readLocale();
  const t = createTranslator(locale, await loadMessages(locale));
  const { document, contentLocale, fallback } = legalDocument(kind, locale, legalEntity());

  return (
    <>
      <SiteHeader />
      <main className="mx-auto w-full max-w-3xl flex-1 px-4 py-8 sm:px-8 sm:py-12">
        {fallback ? (
          <p className="mb-6 rounded-lg border bg-muted/40 px-4 py-3 text-sm text-muted-foreground">{t("site.englishOnly")}</p>
        ) : null}
        <article lang={contentLocale} className="space-y-8 text-sm leading-7">
          <header className="space-y-2">
            <h1 className="font-heading text-3xl font-semibold tracking-tight">{document.title}</h1>
            <p className="text-muted-foreground">{t("site.lastUpdated", { date: LEGAL_UPDATED })}</p>
          </header>
          <div className="space-y-3">
            {document.intro.map((block, i) => <Block key={i} block={block} />)}
          </div>
          {document.sections.map((section, i) => (
            <section key={section.heading} className="space-y-3">
              <h2 className="text-lg font-semibold">{i + 1}. {section.heading}</h2>
              {section.blocks.map((block, j) => <Block key={j} block={block} />)}
            </section>
          ))}
        </article>
      </main>
      <SiteFooter />
    </>
  );
}
