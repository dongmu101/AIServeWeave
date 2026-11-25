"use client";

import * as React from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Dialog } from "@base-ui/react/dialog";
import { LoaderCircleIcon, MessageSquareIcon, SearchIcon } from "lucide-react";
import { cn } from "cn";

import { sendToSignIn } from "@/components/studio/session-expired";
import { ContractError, errorMessage } from "@/lib/studio/contract";
import { parseConversationPage, type Conversation } from "@/lib/studio/conversations";
import { formatUpdatedAt, highlightTitle, MAX_SEARCH_RUNES, normalizeSearch, searchURL } from "@/lib/studio/conversation-search";

const DEBOUNCE_MS = 200;

/**
 * ConversationSearch is the dialog that finds the user's conversations by
 * title. An empty query lists the most recent ones. The dialog's content
 * unmounts on close, so each opening starts with an empty query.
 *
 * ConversationSearch 是按标题查找用户对话的对话框。查询为空时列出最近的对话。对话框内容
 * 在关闭时卸载，因此每次打开都从空查询开始。
 */
export function ConversationSearch({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const inputRef = React.useRef<HTMLInputElement>(null);
  return (
    <Dialog.Root open={open} onOpenChange={(next) => onOpenChange(next)}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-50 bg-black/40 duration-100 data-open:animate-in data-open:fade-in-0 data-closed:animate-out data-closed:fade-out-0" />
        <Dialog.Popup
          initialFocus={inputRef}
          className="fixed top-[10vh] left-1/2 z-50 flex max-h-[min(70vh,36rem)] w-[calc(100%-2rem)] max-w-xl -translate-x-1/2 flex-col overflow-hidden rounded-xl bg-popover text-popover-foreground shadow-xl ring-1 ring-foreground/10 outline-none duration-100 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95"
        >
          <Dialog.Title className="sr-only">搜索对话</Dialog.Title>
          <SearchBody inputRef={inputRef} onPicked={() => onOpenChange(false)} />
        </Dialog.Popup>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

interface Page {
  /** query is the normalized query these results answer. */
  query: string;
  items: Conversation[];
  next: string;
  error: string;
  moreError: string;
}

type Fetched = { items: Conversation[]; next: string; error: string };

/** fetchPage reads one page through the BFF. It returns null when the request
 * was aborted or the session has expired (the browser is then leaving for
 * sign-in), so the caller has nothing to render.
 *
 * fetchPage 经 BFF 读取一页。请求被中止或会话已失效（浏览器随即跳转登录）时返回 null，
 * 调用方无需渲染任何内容。 */
async function fetchPage(query: string, cursor: string, signal: AbortSignal): Promise<Fetched | null> {
  const failed = (error: string): Fetched => ({ items: [], next: "", error });
  try {
    const response = await fetch(searchURL(query, cursor), { signal, cache: "no-store" });
    if (response.status === 401) {
      sendToSignIn(window.location.pathname);
      return null;
    }
    const body: unknown = await response.json().catch(() => null);
    if (!response.ok) {
      const code = typeof body === "object" && body !== null && typeof (body as { error?: unknown }).error === "string" ? (body as { error: string }).error : "";
      return failed(errorMessage(code));
    }
    const page = parseConversationPage(body);
    return { items: page.conversations, next: page.next, error: "" };
  } catch (error) {
    if (signal.aborted) return null;
    return failed(error instanceof ContractError ? errorMessage("upstream_contract") : "网络连接失败，请稍后重试。");
  }
}

function SearchBody({ inputRef, onPicked }: { inputRef: React.RefObject<HTMLInputElement | null>; onPicked: () => void }) {
  const router = useRouter();
  const listId = React.useId();
  const [text, setText] = React.useState("");
  const query = normalizeSearch(text);
  const [page, setPage] = React.useState<Page | null>(null);
  const [loadingMore, setLoadingMore] = React.useState("");
  const [active, setActive] = React.useState(0);
  const moreAbort = React.useRef<AbortController | null>(null);
  const [now] = React.useState(() => new Date());

  React.useEffect(() => {
    const controller = new AbortController();
    const timer = window.setTimeout(async () => {
      const result = await fetchPage(query, "", controller.signal);
      if (result === null) return;
      setPage({ query, ...result, moreError: "" });
      setActive(0);
    }, query === "" ? 0 : DEBOUNCE_MS);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [query]);

  React.useEffect(() => () => moreAbort.current?.abort(), []);

  const items = page?.items ?? [];
  const loading = page === null || page.query !== query;
  const current = Math.min(active, items.length - 1);
  const optionId = (i: number) => `${listId}-option-${i}`;

  React.useEffect(() => {
    if (current >= 0) document.getElementById(optionId(current))?.scrollIntoView({ block: "nearest" });
    // optionId only depends on listId, which never changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [current]);

  async function loadMore() {
    if (!page || page.next === "" || loadingMore !== "") return;
    const { query: key, next: cursor } = page;
    moreAbort.current?.abort();
    const controller = new AbortController();
    moreAbort.current = controller;
    setLoadingMore(cursor);
    const result = await fetchPage(key, cursor, controller.signal);
    if (result === null) return;
    setLoadingMore("");
    setPage((prev) => {
      if (!prev || prev.query !== key || prev.next !== cursor) return prev;
      if (result.error) return { ...prev, moreError: result.error };
      const seen = new Set(prev.items.map((item) => item.id));
      return { ...prev, items: [...prev.items, ...result.items.filter((item) => !seen.has(item.id))], next: result.next, moreError: "" };
    });
  }

  function open(conversation: Conversation) {
    router.push(`/c/${conversation.id}`);
    onPicked();
  }

  function onKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.nativeEvent.isComposing || items.length === 0) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      if (current === items.length - 1 && page?.next) void loadMore();
      setActive(Math.min(current + 1, items.length - 1));
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      setActive(Math.max(current - 1, 0));
    } else if (event.key === "Enter" && current >= 0) {
      event.preventDefault();
      open(items[current]);
    }
  }

  return (
    <>
      <div className="flex items-center gap-2 border-b px-4">
        {loading && page !== null ? (
          <LoaderCircleIcon className="size-[18px] shrink-0 animate-spin text-muted-foreground" aria-hidden />
        ) : (
          <SearchIcon className="size-[18px] shrink-0 text-muted-foreground" aria-hidden />
        )}
        <input
          ref={inputRef}
          value={text}
          onChange={(event) => setText(event.target.value)}
          onKeyDown={onKeyDown}
          maxLength={MAX_SEARCH_RUNES * 2}
          placeholder="按标题搜索对话"
          aria-label="搜索对话"
          role="combobox"
          aria-expanded={items.length > 0}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={current >= 0 ? optionId(current) : undefined}
          className="h-12 min-w-0 flex-1 bg-transparent text-[15px] outline-none placeholder:text-muted-foreground"
        />
      </div>

      <div className={cn("min-h-0 flex-1 overflow-y-auto p-2", loading && page !== null && "opacity-60")} aria-busy={loading}>
        {page === null ? (
          <p className="px-2 py-6 text-center text-sm text-muted-foreground">正在加载…</p>
        ) : page.error ? (
          <p role="alert" className="px-2 py-6 text-center text-sm text-muted-foreground">搜索失败：{page.error}</p>
        ) : items.length === 0 ? (
          <p className="px-2 py-6 text-center text-sm text-muted-foreground">
            {page.query === "" ? "还没有对话。" : `没有标题包含「${page.query}」的对话。`}
          </p>
        ) : (
          <>
            <p className="px-2 pt-1 pb-2 text-xs font-medium text-muted-foreground">{page.query === "" ? "最近对话" : "搜索结果"}</p>
            <ul id={listId} role="listbox" aria-label={page.query === "" ? "最近对话" : "搜索结果"} className="grid gap-0.5">
              {items.map((conversation, i) => (
                <li key={conversation.id} role="presentation">
                  <Link
                    id={optionId(i)}
                    href={`/c/${conversation.id}`}
                    role="option"
                    aria-selected={i === current}
                    tabIndex={-1}
                    onClick={onPicked}
                    onMouseMove={() => i !== current && setActive(i)}
                    className={cn("flex h-10 items-center gap-3 rounded-lg px-2.5 text-sm", i === current && "bg-muted")}
                  >
                    <MessageSquareIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                    <span className="min-w-0 flex-1 truncate">
                      <Title title={conversation.title} query={page.query} />
                    </span>
                    <span className="shrink-0 text-xs text-muted-foreground">{formatUpdatedAt(conversation.updatedAt, now)}</span>
                  </Link>
                </li>
              ))}
            </ul>
            {page.next ? (
              <div className="px-2 pt-2 pb-1 text-center">
                {page.moreError ? <p role="alert" className="pb-1 text-xs text-muted-foreground">加载失败：{page.moreError}</p> : null}
                <button
                  type="button"
                  disabled={loadingMore !== ""}
                  onClick={() => void loadMore()}
                  className="rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-60"
                >
                  {loadingMore !== "" ? "正在加载…" : page.moreError ? "重试" : "加载更多"}
                </button>
              </div>
            ) : null}
          </>
        )}
      </div>

      <div className="hidden items-center gap-4 border-t px-4 py-2 text-xs text-muted-foreground md:flex">
        <span><Kbd>↑</Kbd> <Kbd>↓</Kbd> 选择</span>
        <span><Kbd>Enter</Kbd> 打开</span>
        <span><Kbd>Esc</Kbd> 关闭</span>
      </div>
    </>
  );
}

function Title({ title, query }: { title: string; query: string }) {
  if (title === "") return <span className="text-muted-foreground">新对话</span>;
  return (
    <>
      {highlightTitle(title, query).map((part, i) =>
        part.match ? (
          <mark key={i} className="rounded-[3px] bg-primary/15 text-inherit">
            {part.text}
          </mark>
        ) : (
          <React.Fragment key={i}>{part.text}</React.Fragment>
        )
      )}
    </>
  );
}

function Kbd({ children }: { children: React.ReactNode }) {
  return <kbd className="rounded border bg-muted px-1 py-px font-sans text-[11px]">{children}</kbd>;
}

function subscribeNothing(): () => void {
  return () => {};
}

/** useSearchShortcutLabel is the platform's label for the search shortcut,
 * empty during server rendering so hydration matches.
 *
 * useSearchShortcutLabel 是当前平台下搜索快捷键的标签；服务端渲染时为空，以保证水合一致。 */
export function useSearchShortcutLabel(): string {
  return React.useSyncExternalStore(
    subscribeNothing,
    () => (/Mac|iPhone|iPad/.test(navigator.userAgent) ? "⌘K" : "Ctrl K"),
    () => ""
  );
}

/** isSearchShortcut reports ⌘K / Ctrl+K.
 * isSearchShortcut 判断是否为 ⌘K / Ctrl+K。 */
export function isSearchShortcut(event: KeyboardEvent): boolean {
  return (event.metaKey || event.ctrlKey) && !event.shiftKey && !event.altKey && event.key.toLowerCase() === "k";
}
