"use client";

import * as React from "react";

/**
 * useStoredValue reads a per-browser preference from localStorage without a
 * hydration mismatch: the server snapshot is null, the client reads storage,
 * and writes made through the returned setter (or in another tab) re-render
 * every reader. A blocked or empty storage reads as null.
 *
 * useStoredValue 从 localStorage 读取单浏览器偏好，且不会造成水合不一致：服务端快照为
 * null，客户端读存储；经返回的 setter（或在另一个标签页）写入后，所有读取方都会重新渲染。
 * 存储被禁用或为空时读作 null。
 */

const LOCAL_EVENT = "aisw-studio:storage";

function subscribe(onChange: () => void): () => void {
  window.addEventListener("storage", onChange);
  window.addEventListener(LOCAL_EVENT, onChange);
  return () => {
    window.removeEventListener("storage", onChange);
    window.removeEventListener(LOCAL_EVENT, onChange);
  };
}

function read(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function useStoredValue(key: string): [string | null, (value: string) => void] {
  const value = React.useSyncExternalStore(subscribe, () => read(key), () => null);
  const set = React.useCallback(
    (next: string) => {
      try {
        window.localStorage.setItem(key, next);
      } catch {
        // Blocked storage: the value is not kept. / 存储被禁用：该值不会被保留。
      }
      window.dispatchEvent(new Event(LOCAL_EVENT));
    },
    [key]
  );
  return [value, set];
}
