"use client";

import * as React from "react";
import Link from "next/link";
import { CheckIcon, ChevronDownIcon, MenuIcon, type LucideIcon } from "lucide-react";

import { Button, buttonVariants } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

export type NavLeaf = { href: string; label: string; icon: LucideIcon };
export type NavEntry = NavLeaf | { label: string; children: readonly NavLeaf[] };

/** activeLeaf finds the entry the current path belongs to: the longest href
 * that equals the path or is a parent segment of it, so `/a/b` beats `/a`.
 *
 * activeLeaf 找出当前路径所属的条目：取与路径相等或为其上级路径段的最长 href，
 * 因此 `/a/b` 优先于 `/a`。 */
export function activeLeaf(entries: readonly NavEntry[], pathname: string): NavLeaf | undefined {
  let best: NavLeaf | undefined;
  for (const entry of entries) {
    for (const leaf of "children" in entry ? entry.children : [entry]) {
      const matches = pathname === leaf.href || pathname.startsWith(`${leaf.href}/`);
      if (matches && (!best || leaf.href.length > best.href.length)) best = leaf;
    }
  }
  return best;
}

/** navItemClass styles one top-bar entry. The current page is marked by a bar
 * resting on the h-14 header's bottom edge, so the row reads as a set of tabs.
 *
 * navItemClass 为顶栏的一个条目设定样式。当前页面由贴在 h-14 页首底边上的一条色带
 * 标出，使这一行读起来像一组标签页。 */
function navItemClass(active: boolean): string {
  return buttonVariants({
    variant: "ghost",
    size: "sm",
    className: active
      ? "relative px-2.5 text-foreground after:absolute after:inset-x-2.5 after:-bottom-3.5 after:h-0.5 after:rounded-full after:bg-brand"
      : "relative px-2.5 text-muted-foreground",
  });
}

/**
 * TopNav is the one-row navigation of an h-14 header: top-level links and
 * grouped dropdowns from xl up when all entries fit, a single menu button
 * naming the current page otherwise. Both forms list the same entries, and
 * neither scrolls.
 *
 * TopNav 是 h-14 页首里的单行导航：xl 起且所有条目能放下时显示顶层链接与分组下拉，
 * 否则收成一个显示当前页面名称的菜单按钮。两种形态列出的条目相同，且都不产生滚动。
 */
export function TopNav({
  entries,
  pathname,
  label,
  menuLabel,
  fallbackLabel,
}: {
  entries: readonly NavEntry[];
  pathname: string;
  /** label names the navigation landmark. / label 是导航区域的无障碍名称。 */
  label: string;
  /** menuLabel names the narrow-screen menu button. / menuLabel 是窄屏菜单按钮的无障碍名称。 */
  menuLabel: string;
  /** fallbackLabel shows on the menu button when no entry matches. / 没有条目匹配时菜单按钮上显示 fallbackLabel。 */
  fallbackLabel: string;
}) {
  const active = activeLeaf(entries, pathname);
  const navRef = React.useRef<HTMLElement>(null);
  const rowRef = React.useRef<HTMLDivElement>(null);
  const [overflowing, setOverflowing] = React.useState(false);

  const entriesKey = entries
    .map((entry) =>
      "children" in entry ? `${entry.label}:${entry.children.map((child) => child.label).join(",")}` : entry.label,
    )
    .join("|");

  const measureFit = React.useCallback(() => {
    const nav = navRef.current;
    const row = rowRef.current;
    if (!nav || !row || getComputedStyle(row).display === "none") {
      setOverflowing((prev) => (prev ? false : prev));
      return;
    }
    const next = row.scrollWidth > nav.clientWidth + 1;
    setOverflowing((prev) => (prev === next ? prev : next));
  }, []);

  React.useLayoutEffect(() => {
    measureFit();
  }, [entriesKey, measureFit]);

  React.useEffect(() => {
    const nav = navRef.current;
    const row = rowRef.current;
    if (!nav || !row || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measureFit);
    observer.observe(nav);
    observer.observe(row);
    return () => observer.disconnect();
  }, [measureFit]);

  return (
    <nav
      ref={navRef}
      aria-label={label}
      className="relative flex h-full min-w-0 flex-1 items-center justify-end sm:justify-start"
    >
      <div
        ref={rowRef}
        aria-hidden={overflowing || undefined}
        inert={overflowing || undefined}
        className={cn(
          "hidden h-full min-w-0 max-w-full items-center gap-0.5 overflow-x-clip xl:flex",
          overflowing && "pointer-events-none invisible absolute inset-y-0 left-0",
        )}
      >
        {entries.map((entry) => {
          if (!("children" in entry)) {
            const current = entry.href === active?.href;
            return (
              <Link key={entry.href} href={entry.href} className={navItemClass(current)} aria-current={current ? "page" : undefined}>
                {entry.label}
              </Link>
            );
          }
          const groupActive = entry.children.some((child) => child.href === active?.href);
          return (
            <DropdownMenu key={entry.label}>
              <DropdownMenuTrigger className={navItemClass(groupActive)}>
                {entry.label}
                <ChevronDownIcon className="size-3 text-muted-foreground" />
              </DropdownMenuTrigger>
              <DropdownMenuContent className="w-48" aria-label={entry.label}>
                {entry.children.map((child) => (
                  <DropdownMenuItem
                    key={child.href}
                    render={<Link href={child.href} />}
                    aria-current={child.href === active?.href ? "page" : undefined}
                    className="min-h-9"
                  >
                    <child.icon className="text-muted-foreground" />
                    {child.label}
                    {child.href === active?.href ? <CheckIcon className="ml-auto" /> : null}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          );
        })}
      </div>
      <DropdownMenu>
        <DropdownMenuTrigger
          aria-label={menuLabel}
          render={<Button variant="ghost" className={cn("min-w-0 shrink", !overflowing && "xl:hidden")} />}
        >
          <MenuIcon />
          <span className="truncate">{active?.label ?? fallbackLabel}</span>
          <ChevronDownIcon className="size-3 text-muted-foreground" />
        </DropdownMenuTrigger>
        <DropdownMenuContent className="w-60 max-w-[calc(100vw-2rem)]" aria-label={label}>
          {entries.map((entry) => {
            const nested = "children" in entry;
            return (nested ? entry.children : [entry]).map((item) => (
              <DropdownMenuItem
                key={item.href}
                render={<Link href={item.href} />}
                aria-current={item.href === active?.href ? "page" : undefined}
                className={nested ? "min-h-9 pl-6" : "min-h-9"}
              >
                <item.icon className="text-muted-foreground" />
                {nested ? `${entry.label} / ${item.label}` : item.label}
                {item.href === active?.href ? <CheckIcon className="ml-auto" /> : null}
              </DropdownMenuItem>
            ));
          })}
        </DropdownMenuContent>
      </DropdownMenu>
    </nav>
  );
}
