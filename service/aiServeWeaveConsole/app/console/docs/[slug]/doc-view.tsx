"use client";

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
  if (!isValidApiDocSlug(slug)) {
    return <EmptyState title="文档不存在" description="该接口文档不存在或尚未发布。" />;
  }
  return <ValidDocView slug={slug} />;
}

function ValidDocView({ slug }: { slug: string }) {
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
    return <LoadingState label="正在加载文档" rows={8} />;
  }
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b pb-3">
        <ExampleModelField />
        <span className="text-xs text-muted-foreground">更新于 {formatDateTime(doc.data.updatedAt)}</span>
      </div>
      <ApiDocArticle doc={doc.data} values={values} />
    </div>
  );
}
