/**
 * Helpers for searching conversations by title. The search itself runs in the
 * control plane (a case-insensitive substring match on the title, newest
 * first); this module only shapes the request and the result rows.
 *
 * 按标题搜索对话的辅助函数。搜索本身在控制面执行（对标题做不区分大小写的子串匹配，按
 * 更新时间倒序）；本模块只负责构造请求与结果行的展示。
 */

/** MAX_SEARCH_RUNES matches the control plane's title limit; a longer query
 * is refused there, and could never match a title anyway.
 *
 * MAX_SEARCH_RUNES 与控制面的标题上限一致；更长的查询会被控制面拒绝，而且也不可能匹配
 * 任何标题。 */
export const MAX_SEARCH_RUNES = 200;

export const SEARCH_PAGE_SIZE = 20;

/** normalizeSearch trims the query and cuts it to MAX_SEARCH_RUNES code
 * points.
 *
 * normalizeSearch 去掉查询首尾空白，并截断到 MAX_SEARCH_RUNES 个码点。 */
export function normalizeSearch(raw: string): string {
  const trimmed = raw.trim();
  const runes = Array.from(trimmed);
  return runes.length > MAX_SEARCH_RUNES ? runes.slice(0, MAX_SEARCH_RUNES).join("").trim() : trimmed;
}

/** searchURL is the BFF request for one page of results. An empty query
 * lists the most recent conversations.
 *
 * searchURL 是读取一页结果的 BFF 请求。查询为空时列出最近的对话。 */
export function searchURL(query: string, cursor = ""): string {
  const params = new URLSearchParams({ limit: String(SEARCH_PAGE_SIZE) });
  const q = normalizeSearch(query);
  if (q !== "") params.set("q", q);
  if (cursor !== "") params.set("cursor", cursor);
  return `/api/studio/conversations?${params.toString()}`;
}

export interface TitlePart {
  text: string;
  match: boolean;
}

/** highlightTitle splits title into the parts that match query
 * (case-insensitively) and the rest. When lower-casing changes the length of
 * the title, offsets would no longer line up, so the title is returned
 * unmarked rather than marked in the wrong place.
 *
 * highlightTitle 把标题拆成与查询匹配（不区分大小写）的部分和其余部分。若转小写改变了
 * 标题长度，偏移就对不上，此时原样返回标题而不是标错位置。 */
export function highlightTitle(title: string, query: string): TitlePart[] {
  const needle = normalizeSearch(query).toLowerCase();
  const haystack = title.toLowerCase();
  if (title === "") return [];
  if (needle === "" || haystack.length !== title.length) return [{ text: title, match: false }];
  const parts: TitlePart[] = [];
  let from = 0;
  for (let at = haystack.indexOf(needle); at !== -1; at = haystack.indexOf(needle, from)) {
    if (at > from) parts.push({ text: title.slice(from, at), match: false });
    parts.push({ text: title.slice(at, at + needle.length), match: true });
    from = at + needle.length;
  }
  if (from < title.length) parts.push({ text: title.slice(from), match: false });
  return parts;
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

function startOfDay(date: Date): number {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
}

/** formatUpdatedAt labels when a conversation was last updated, in local
 * time: the time for today, 「昨天」, the date this year, or the full date.
 * An unreadable timestamp gives "".
 *
 * formatUpdatedAt 以本地时间标注对话的最后更新时间：今天显示时刻，昨天显示「昨天」，
 * 今年显示月日，更早显示完整日期。无法解析的时间返回空串。 */
export function formatUpdatedAt(iso: string, now: Date): string {
  const at = new Date(iso);
  if (iso === "" || Number.isNaN(at.getTime())) return "";
  const days = Math.round((startOfDay(now) - startOfDay(at)) / 86_400_000);
  if (days <= 0) return `${pad(at.getHours())}:${pad(at.getMinutes())}`;
  if (days === 1) return "昨天";
  if (at.getFullYear() === now.getFullYear()) return `${at.getMonth() + 1}月${at.getDate()}日`;
  return `${at.getFullYear()}年${at.getMonth() + 1}月${at.getDate()}日`;
}
