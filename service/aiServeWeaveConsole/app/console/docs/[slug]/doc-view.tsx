"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import { localizeApiDoc } from "@/lib/i18n/docs/localize";
import { useDocs } from "@/app/console/docs/docs-shell";
import { ExampleModelField } from "@/app/console/docs/example-model-field";
import { ApiDocArticle } from "@/components/console/api-doc-article";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { isValidApiDocSlug, parseApiDocDetail, type ApiDocDetail } from "@/lib/console/api-docs";
import { formatDateTime } from "@/lib/console/format";

/**
 * DocView loads and renders one published page. A slug that cannot be valid
 * is answered locally rather than sent upstream.
 *
 * DocView 加载并渲染一个已发布页面。不可能合法的 slug 在本地直接作答，不发往上游。
 */
export function DocView({ slug }: { slug: string }) {
  const {t } = useI18n();
  if (!isValidApiDocSlug(slug)) {
    return <EmptyState title={t("tenant.documentationNotFound")} description={t("tenant.thisApiDocumentationDoesNotExistOrHas")} />;
  }
  return <ValidDocView slug={slug} />;
}

function ValidDocView({ slug }: { slug: string }) {
  const { locale, messages, t } = useI18n();
  const { values } = useDocs();
  const doc = useResource<ApiDocDetail>({
    method: "GET",
    path: `/admin/v1/api-docs/${slug}`,
    parse: parseApiDocDetail,
  });

  if (doc.error) {
    return <ErrorState message={doc.error} onRetry={doc.reload} />;
  }
  if (doc.loading || !doc.data) {
    return <LoadingState label={t("tenant.loadingDocumentation")} rows={8} />;
  }
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b pb-3">
        <ExampleModelField />
        <span className="text-xs text-muted-foreground"><Message id="tenant.updated_734a94" values={{ value: formatDateTime(doc.data.updatedAt, undefined, locale) }} /></span>
      </div>
      <ApiDocArticle doc={localizeApiDoc(doc.data, locale, messages)} values={values} />
    </div>
  );
}
