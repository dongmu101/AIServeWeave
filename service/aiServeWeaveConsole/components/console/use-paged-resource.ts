"use client";

import * as React from "react";

import type { Page } from "@/lib/console/contract";
import {
  advanceHistory,
  currentCursor,
  INITIAL_HISTORY,
  MAX_PAGE_HISTORY,
  pageQuery,
  retreatHistory,
} from "@/lib/console/paging";
import { useResource, type Resource } from "@/components/console/use-resource";

/**
 * PagedResource is a list read one page at a time.
 *
 * PagedResource 是一次一页地读取的列表。
 */
export interface PagedResource<T> extends Omit<Resource<Page<T>>, "data"> {
  items: T[] | null;
  /** pageNumber is one-based, for display. It is a position in this session's
   * paging, not a claim about how many pages exist — the API sends no total.
   *
   * pageNumber 从 1 开始，用于展示。它是本次会话翻页中的位置，而不是对总页数的断言
   * —— API 不发送总数。 */
  pageNumber: number;
  hasNext: boolean;
  hasPrevious: boolean;
  next: () => void;
  previous: () => void;
  /** atHistoryLimit reports that going further back is no longer possible
   * because the oldest remembered positions were dropped.
   *
   * atHistoryLimit 报告已经无法再往回走，因为最早记住的那些位置已被丢弃。 */
  atHistoryLimit: boolean;
}

/**
 * usePagedResource walks a cursor-paginated list.
 *
 * Changing the filters resets to the first page. That is not a convenience:
 * a cursor names a position in one ordered, filtered list, and carrying it
 * across a filter change would ask the server to continue a list that no
 * longer exists.
 *
 * usePagedResource 沿着游标翻阅一份列表。
 *
 * 筛选条件改变会回到第一页。这不是为了方便：游标指的是某一份已排序、已筛选列表中的
 * 位置，把它带过一次筛选变更，等于要求服务端继续一份已经不存在的列表。
 */
export function usePagedResource<T>(spec: {
  path: string;
  surface?: "admin" | "operator";
  /** filters are the query parameters other than paging. Empty values are
   * dropped so an untouched filter does not become `?q=` on the wire.
   *
   * filters 是分页之外的查询参数。空值会被丢弃，好让一个没被碰过的筛选项不会在线上
   * 变成 `?q=`。 */
  filters: Record<string, string>;
  pageSize: number;
  parse: (value: unknown) => Page<T>;
}): PagedResource<T> {
  const filterKey = `${spec.surface ?? "admin"}|${spec.path}|${JSON.stringify(spec.filters)}|${spec.pageSize}`;
  const [history, setHistory] = React.useState<string[]>([...INITIAL_HISTORY]);
  const [lastFilterKey, setLastFilterKey] = React.useState(filterKey);

  // Reset during render rather than in an effect, so the first paint after a
  // filter change already asks for page one instead of briefly re-requesting
  // the old page's cursor against the new filters.
  //
  // 在渲染期间重置而不是放进 effect，这样筛选变更后的首次绘制就已经在请求第一页，
  // 而不是先拿旧页的游标去请求一次新筛选下的结果。
  if (filterKey !== lastFilterKey) {
    setLastFilterKey(filterKey);
    setHistory([...INITIAL_HISTORY]);
  }

  const query = pageQuery(spec.filters, spec.pageSize, currentCursor(history));

  const page = useResource<Page<T>>({
    method: "GET",
    path: spec.path,
    surface: spec.surface,
    query,
    parse: spec.parse,
  });

  return {
    items: page.data?.items ?? null,
    error: page.error,
    loading: page.loading,
    reload: page.reload,
    pageNumber: history.length,
    hasNext: page.data?.nextCursor != null,
    hasPrevious: history.length > 1,
    atHistoryLimit: history.length >= MAX_PAGE_HISTORY,
    next: () => {
      const nextCursor = page.data?.nextCursor;
      if (nextCursor == null) {
        return;
      }
      setHistory((previous) => advanceHistory(previous, nextCursor));
    },
    previous: () => setHistory(retreatHistory),
  };
}
