"use client";

import * as React from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";

/**
 * useUrlFilters keeps a view's filters in the address bar.
 *
 * The URL is the source of truth rather than a copy of component state, so a
 * reload, a bookmark or a link opens the list somebody was actually looking
 * at. Only filters go here — never a cursor, and never anything a person
 * typed that identifies them: an address bar is copied into chat messages and
 * kept in shared browser history, so what lands here should be no more
 * sensitive than the page it names.
 *
 * The replace is deliberate: typing in a filter should not fill the back
 * button with one entry per keystroke.
 *
 * useUrlFilters 把一个视图的筛选条件保存在地址栏里。
 *
 * URL 是事实来源，而不是组件状态的一份副本，这样刷新、收藏或分享链接打开的，就是某人
 * 当时确实在看的那份列表。只有筛选条件放在这里——游标不放，任何能标识出某人身份的输入
 * 也不放：地址栏会被复制进聊天消息、留在共享的浏览历史里，因此落在这里的东西，敏感度
 * 不应超过它所指向的那个页面。
 *
 * 用 replace 是刻意的：在筛选框里打字，不该让后退按钮被每一次按键各填进一条记录。
 */
export function useUrlFilters<K extends string>(
  names: readonly K[]
): [Record<K, string>, (patch: Partial<Record<K, string>>) => void] {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  const values = {} as Record<K, string>;
  for (const name of names) {
    values[name] = searchParams.get(name) ?? "";
  }

  const apply = React.useCallback(
    (patch: Partial<Record<K, string>>) => {
      const next = new URLSearchParams(searchParams.toString());
      for (const [name, value] of Object.entries(patch)) {
        if (value === undefined || value === "") {
          next.delete(name);
        } else {
          next.set(name, String(value));
        }
      }
      const query = next.toString();
      router.replace(query === "" ? pathname : `${pathname}?${query}`, {
        scroll: false,
      });
    },
    [pathname, router, searchParams]
  );

  return [values, apply];
}

/**
 * useDebouncedValue delays a value until it has stopped changing.
 *
 * Filtering happens on the server now, so a filter box that committed every
 * keystroke would send a request per character and render whichever response
 * happened to arrive last. The request layer cancels the superseded ones; this
 * keeps them from being made.
 *
 * useDebouncedValue 把一个值推迟到它不再变化为止。
 *
 * 筛选现在发生在服务端，因此一个每次按键都提交的筛选框，会为每个字符发一次请求，并渲染
 * 碰巧最后到达的那个响应。请求层会取消被取代的那些请求；这里则让它们根本不必发出。
 */
export function useDebouncedValue<T>(value: T, delayMs = 400): T {
  const [settled, setSettled] = React.useState(value);

  React.useEffect(() => {
    const timer = setTimeout(() => setSettled(value), delayMs);
    return () => clearTimeout(timer);
  }, [value, delayMs]);

  return settled;
}
