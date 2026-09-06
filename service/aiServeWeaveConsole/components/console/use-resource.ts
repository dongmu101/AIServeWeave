"use client";

import * as React from "react";

import type { RequestSpec } from "@/lib/console/api-client";
import { ApiError, describe } from "@/lib/console/errors";
import { useConsoleRequest } from "@/components/console/use-console-request";

/**
 * Resource is the state of one read, in the three shapes a view must be able
 * to tell apart: still loading, loaded (possibly to nothing), and failed.
 *
 * `data` stays null on failure rather than falling back to an empty array.
 * An empty array would render as "no records", which is a claim about the
 * tenant that a failed read is in no position to make.
 *
 * Resource 是一次读取的状态，形态有三种，视图必须能分辨：仍在加载、已加载（可能什么
 * 都没有）、以及失败。
 *
 * 失败时 `data` 保持为 null，而不是退化成空数组。空数组会渲染成「没有记录」，那是一个
 * 关于该租户的断言，而一次失败的读取没有资格作出它。
 */
export interface Resource<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  reload: () => void;
}

/**
 * useResource reads once on mount, again whenever the request changes, and
 * again on demand.
 *
 * The request is identified by its method, path and query rather than by
 * object identity, so a caller may build the spec inline without memoizing it
 * — an object rebuilt every render would otherwise re-fetch forever.
 *
 * Cancellation and the 401 redirect come from useConsoleRequest; a canceled
 * read leaves the state alone, because the only reason it was canceled is that
 * something newer is already on its way.
 *
 * useResource 在挂载时读取一次，请求发生变化时再读，以及按需再读。
 *
 * 请求由它的方法、路径与查询串标识，而不是由对象标识，因此调用方可以就地构造 spec 而
 * 不必记忆化它——否则一个每次渲染都重建的对象会导致无休止的重新请求。
 *
 * 取消与 401 跳转来自 useConsoleRequest；被取消的读取不改动状态，因为它被取消的唯一
 * 原因就是已经有更新的一次在路上了。
 */
export function useResource<T>(spec: Omit<RequestSpec<T>, "signal">): Resource<T> {
  const run = useConsoleRequest();
  const [token, setToken] = React.useState(0);

  const requestKey = `${spec.surface ?? "admin"} ${spec.method} ${spec.path} ${JSON.stringify(
    spec.query ?? {}
  )} #${token}`;

  // The outcome is stored together with the request it belongs to, and
  // "loading" is derived by comparing that request to the current one. Storing
  // a loading flag instead would mean writing state from inside the effect
  // that starts the read, which costs an extra render and — worse — leaves one
  // render in which a new query is already current while the previous query's
  // rows are still on screen as if they answered it.
  //
  // 结果与它所属的那次请求存在一起，「加载中」则通过比较该请求与当前请求推导出来。
  // 改为存一个 loading 标志，就意味着要在发起读取的那个 effect 内部写状态，代价是多一次
  // 渲染，而且更糟的是：会留下这样一次渲染——新的查询已经生效，上一次查询的行却还留在
  // 屏幕上，仿佛它们就是这次查询的答案。
  const [settled, setSettled] = React.useState<{
    key: string;
    data: T | null;
    error: string | null;
  }>({ key: "", data: null, error: null });

  // The spec is held in a ref, updated after render: the parser is a function
  // that is usually rebuilt every render, and depending on it directly would
  // make every render a new read.
  //
  // spec 放在 ref 里、在渲染之后更新：解析器是一个通常每次渲染都会重建的函数，直接
  // 依赖它会让每一次渲染都变成一次新的读取。
  const latest = React.useRef(spec);
  React.useEffect(() => {
    latest.current = spec;
  });

  React.useEffect(() => {
    // The request is aborted when this effect is torn down, not merely
    // ignored. A filter that changes while a slow read is in flight would
    // otherwise leave that read running — and retrying, since a read retries
    // up to three times — so a person typing into a filter box would stack up
    // requests nobody will ever look at. Ignoring the answer is not the same
    // as not asking for it.
    //
    // 这个 effect 被拆除时请求会被中止，而不只是被忽略。否则，一次慢读取在筛选条件改变
    // 后仍会继续跑——而且还会重试，因为读请求最多重试三次——于是一个在筛选框里打字的人
    // 会堆起一串没人会去看的请求。忽略答案与不去提问，不是一回事。
    const controller = new AbortController();
    let current = true;
    const { method, surface, path, query, body, parse } = latest.current;
    run({ method, surface, path, query, body, parse, signal: controller.signal })
      .then((value) => {
        if (current) {
          setSettled({ key: requestKey, data: value as T, error: null });
        }
      })
      .catch((failure: unknown) => {
        if (!current || (failure instanceof ApiError && failure.kind === "canceled")) {
          return;
        }
        setSettled({ key: requestKey, data: null, error: describe(failure) });
      });
    return () => {
      current = false;
      controller.abort();
    };
  }, [run, requestKey]);

  const reload = React.useCallback(() => setToken((value) => value + 1), []);
  const fresh = settled.key === requestKey;
  return {
    data: fresh ? settled.data : null,
    error: fresh ? settled.error : null,
    loading: !fresh,
    reload,
  };
}
