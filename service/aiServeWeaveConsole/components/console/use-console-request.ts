"use client";

import * as React from "react";
import { usePathname, useRouter } from "next/navigation";

import { request, type RequestSpec } from "@/lib/console/api-client";
import { ApiError } from "@/lib/console/errors";
import { loginPath } from "@/lib/console/internal-path";

/**
 * useConsoleRequest is how a Console page calls the Admin API.
 *
 * It adds the one behaviour every caller needs and none should reimplement: a
 * 401 ends the session, so the page navigates to sign-in with its own address
 * as the return target instead of rendering an error the person cannot act on.
 * Everything else — retry policy, contract validation, cancellation — belongs
 * to the request layer and is unchanged here.
 *
 * The returned function is stable, and the hook aborts in-flight requests when
 * the component unmounts, so a view left mid-load does not resolve into a
 * state update against something that is gone. A caller may pass a signal of
 * its own to cancel one request without unmounting anything.
 *
 * useConsoleRequest 是 Console 页面调用 Admin API 的方式。
 *
 * 它只补上一件每个调用方都需要、且都不该各自重写的行为：401 意味着会话结束，于是页面
 * 带着自己的地址作为返回目标跳到登录页，而不是渲染一个当事人无从处置的错误。其余的
 * ——重试策略、契约校验、取消——属于请求层，在这里原样保留。
 *
 * 返回的函数是稳定的；组件卸载时本 hook 会中止在途请求，因此一个在加载途中被离开的视图，
 * 不会在事后对一个已经消失的东西发起状态更新。调用方也可以传入自己的 signal，从而在不
 * 卸载任何东西的情况下取消单次请求。
 */
export function useConsoleRequest() {
  const router = useRouter();
  const pathname = usePathname();
  const controllers = React.useRef(new Set<AbortController>());

  React.useEffect(() => {
    const inFlight = controllers.current;
    return () => {
      for (const controller of inFlight) {
        controller.abort();
      }
      inFlight.clear();
    };
  }, []);

  return React.useCallback(
    async <T,>(spec: RequestSpec<T>): Promise<T> => {
      const controller = new AbortController();
      controllers.current.add(controller);
      // A caller's own signal is combined with this hook's, not replaced by
      // it. The hook cancels on unmount; a caller cancels when its own reason
      // to want the answer is gone — a filter that changed, a page that moved
      // on — and both must be able to stop the same request.
      //
      // 调用方自己的 signal 与本 hook 的合并，而不是被它取代。hook 在卸载时取消；调用方
      // 在「自己想要这个答案的理由已经消失」时取消——筛选变了、页面翻过去了——两者都必须
      // 能够停下同一个请求。
      const signal = spec.signal
        ? AbortSignal.any([controller.signal, spec.signal])
        : controller.signal;
      try {
        return await request({ ...spec, signal });
      } catch (error) {
        if (error instanceof ApiError && error.kind === "unauthorized") {
          router.replace(spec.surface === "operator"
            ? `/operator/login?next=${encodeURIComponent(pathname)}`
            : loginPath(pathname));
        }
        throw error;
      } finally {
        controllers.current.delete(controller);
      }
    },
    [pathname, router]
  );
}
