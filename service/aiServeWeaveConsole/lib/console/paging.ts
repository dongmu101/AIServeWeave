/**
 * The cursor bookkeeping behind a paged list, kept out of the hook so it can
 * be tested without a renderer.
 *
 * 分页列表背后的游标记账，从 hook 里拿出来，好让它无需渲染器即可测试。
 */

/**
 * MAX_PAGE_HISTORY bounds how far back paging can walk.
 *
 * The API's cursors only go forward, so going back means remembering the
 * cursor each page started from. Remembering them without a bound would turn a
 * long paging session into a slowly growing array — small, but unbounded, and
 * this Console has no reason to hold a hundred positions in a list nobody
 * scrolled back through.
 *
 * MAX_PAGE_HISTORY 限制翻页最多能往回走多远。
 *
 * API 的游标只能向前，因此往回走意味着记住每一页的起始游标。不加限制地记住它们，会让
 * 一次长时间的翻页变成一个缓慢增长的数组——它很小，但是无界的，而本 Console 没有理由为
 * 一份没人往回翻的列表保留上百个位置。
 */
export const MAX_PAGE_HISTORY = 50;

/**
 * INITIAL_HISTORY is the first page: no cursor.
 *
 * INITIAL_HISTORY 是第一页：没有游标。
 */
export const INITIAL_HISTORY: readonly string[] = [""];

/**
 * advanceHistory appends the next page's cursor, dropping the oldest position
 * once the bound is reached.
 *
 * advanceHistory 追加下一页的游标，达到上限后丢弃最早的位置。
 */
export function advanceHistory(
  history: readonly string[],
  cursor: string
): string[] {
  return [...history, cursor].slice(-MAX_PAGE_HISTORY);
}

/**
 * retreatHistory drops the current position. The first page is never dropped:
 * a history that emptied would leave the view with no page to read.
 *
 * retreatHistory 丢弃当前位置。第一页永远不会被丢弃：一个被清空的历史会让视图没有任何
 * 可读的页。
 */
export function retreatHistory(history: readonly string[]): string[] {
  return history.length > 1 ? history.slice(0, -1) : [...history];
}

/**
 * currentCursor is the cursor the current page is read with.
 *
 * currentCursor 是当前页据以读取的游标。
 */
export function currentCursor(history: readonly string[]): string {
  return history[history.length - 1] ?? "";
}

/**
 * pageQuery builds the query parameters for one page.
 *
 * An empty filter is dropped rather than sent as an empty value: the control
 * plane treats an empty string as "no filter" too, but sending it would put
 * `?q=` in the URL of every unfiltered read, and would make two identical
 * reads look like different requests to anything comparing them — including
 * this Console's own cache key.
 *
 * pageQuery 构造一页所需的查询参数。
 *
 * 空的筛选项会被丢弃而不是作为空值发送：控制面同样把空串当作「不筛选」，但发送它会让
 * 每一次未筛选的读取都在 URL 里带上 `?q=`，也会让两次相同的读取在任何比较它们的东西
 * 看来是不同的请求——包括本 Console 自己的缓存键。
 */
export function pageQuery(
  filters: Record<string, string>,
  pageSize: number,
  cursor: string
): Record<string, string> {
  const query: Record<string, string> = { limit: String(pageSize) };
  for (const [name, value] of Object.entries(filters)) {
    if (value !== "") {
      query[name] = value;
    }
  }
  if (cursor !== "") {
    query.cursor = cursor;
  }
  return query;
}
