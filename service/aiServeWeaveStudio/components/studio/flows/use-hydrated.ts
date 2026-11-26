"use client";

import { useSyncExternalStore } from "react";

const subscribe = () => () => {};

/**
 * useHydrated is false on the server and while hydrating, true afterwards.
 * The canvas renders only once it is true: it reads the colour scheme from the
 * browser, and a server-rendered guess would stick, since hydration does not
 * correct a mismatched class.
 *
 * useHydrated 在服务端与水合期间为 false，之后为 true。画布只在它为 true 后渲染：画布从
 * 浏览器读取配色方案，服务端渲染时的猜测会一直保留，因为水合不会修正不一致的 class。
 */
export function useHydrated(): boolean {
  return useSyncExternalStore(subscribe, () => true, () => false);
}
