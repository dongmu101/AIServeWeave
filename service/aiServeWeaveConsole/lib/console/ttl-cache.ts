/**
 * createTtlCache wraps a loader so its result is reused for ttlMs. Callers
 * that arrive while a load is in flight share it, so an expiry under load
 * costs one upstream call, not one per visitor. Only results keep() accepts
 * are stored: a transient failure is returned to its callers and the next
 * call loads again. A load that throws is treated the same way.
 *
 * createTtlCache 包装一个加载函数，使其结果在 ttlMs 内被复用。加载进行中到达的调用方共用这次
 * 加载，因此高负载下缓存过期只产生一次上游调用，而不是每个访客一次。只保存 keep() 接受的
 * 结果：一次临时失败照常返回给本批调用方，下一次调用重新加载。加载抛出异常时同样处理。
 */
export function createTtlCache<T>(
  load: () => Promise<T>,
  options: { ttlMs: number; keep: (value: T) => boolean; now?: () => number },
): () => Promise<T> {
  const now = options.now ?? Date.now;
  let stored: { value: T; expires: number } | null = null;
  let inflight: Promise<T> | null = null;
  return () => {
    if (stored && now() < stored.expires) return Promise.resolve(stored.value);
    if (inflight) return inflight;
    const current = load().then(
      value => {
        if (options.keep(value)) stored = { value, expires: now() + options.ttlMs };
        return value;
      },
    ).finally(() => {
      if (inflight === current) inflight = null;
    });
    inflight = current;
    return current;
  };
}
