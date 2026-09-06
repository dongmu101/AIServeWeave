"use client";

import { Button } from "@/components/ui/button";
import type { PagedResource } from "@/components/console/use-paged-resource";

/**
 * Pager is the control for a cursor-paginated list.
 *
 * It shows a page number and never a page count, because the API sends no
 * total and this Console will not compute one from the rows it happens to
 * hold. "Next" is enabled by the cursor the server sent, so the last page is
 * something the server said rather than something the page size implied.
 *
 * Pager 是游标分页列表的翻页控件。
 *
 * 它显示页码但绝不显示总页数，因为 API 不发送总数，而本 Console 不会拿手上碰巧有的
 * 那些行去算一个出来。「下一页」的可用与否由服务端发来的游标决定，因此「这是最后一页」
 * 是服务端说的，而不是从分页大小推断出来的。
 */
export function Pager<T>({
  resource,
  loadedCount,
}: {
  resource: PagedResource<T>;
  loadedCount: number;
}) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <p className="text-xs text-muted-foreground">
        第 {resource.pageNumber} 页 · 本页 {loadedCount} 条
        {resource.hasNext ? "" : " · 已到末页"}
        {resource.atHistoryLimit ? " · 已达可回退的页数上限" : ""}
      </p>
      <div className="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={resource.previous}
          disabled={!resource.hasPrevious || resource.loading}
        >
          上一页
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={resource.next}
          disabled={!resource.hasNext || resource.loading}
        >
          下一页
        </Button>
      </div>
    </div>
  );
}
