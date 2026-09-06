"use client";

import { Badge } from "@/components/ui/badge";
import type { ReplicaStatus } from "@/lib/console/fleet";
import { formatDateTime } from "@/lib/console/format";

/** REPLICA_ERRORS is the fixed set the control plane emits, in Chinese. An
 * unfamiliar code is shown as it arrived rather than as "failed": a code this
 * build does not know is something to see.
 *
 * REPLICA_ERRORS 是控制面给出的固定集合的中文说法。不认识的代号原样展示而不是笼统写成
 * 「失败」：本次构建不认识的代号是一件需要被看见的事。 */
const REPLICA_ERRORS: Record<string, string> = {
  unreachable: "连不上",
  timeout: "超时",
  unauthorized: "凭据被拒",
  malformed: "响应无法解析",
};

/**
 * FleetFreshness says how old this view is and whether it is complete.
 *
 * It is a component rather than a line of copy in each page because both fleet
 * views must state the same two things. A replica that did not answer means
 * the nodes connected only to it are missing, and a fleet page that showed a
 * shorter list without saying so would send somebody to look for a machine
 * that is running.
 *
 * FleetFreshness 说明本视图有多旧、以及它是否完整。
 *
 * 它是一个组件而不是每个页面里的一行文案，因为两个机群视图都必须陈述同样这两件事。
 * 某个副本没有作答，意味着只连到它上面的节点会缺失，而一个把列表显示得更短却不说明这
 * 一点的机群页面，会把人派去找一台正在运行的机器。
 */
export function FleetFreshness({
  collectedAt,
  replicas,
  partial,
}: {
  collectedAt: string;
  replicas: ReplicaStatus[];
  partial: boolean;
}) {
  return (
    <div className="grid gap-2">
      {partial ? (
        <div
          role="alert"
          className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive"
        >
          这份清单不完整：有 Gateway 副本没有作答，只连到它们上面的节点不会出现在下面。
        </div>
      ) : null}
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <span>控制面采集于 {formatDateTime(collectedAt)}</span>
        {replicas.map((replica) => (
          <Badge
            key={replica.endpoint}
            variant="outline"
            className={replica.error ? "text-destructive" : undefined}
            title={replica.endpoint}
          >
            {replica.replicaId ?? replica.endpoint}
            {replica.error
              ? `：${REPLICA_ERRORS[replica.error] ?? replica.error}`
              : `：${replica.nodeCount} 个节点 · ${formatDateTime(replica.generatedAt)}`}
          </Badge>
        ))}
      </div>
      {replicas.length > 0 ? (
        <p className="text-xs text-muted-foreground">
          每个副本只知道连到它自己身上的节点，因此这里的每个数字都取自各副本各自查看的那个
          时刻，而不是同一瞬间。
        </p>
      ) : (
        // A tenant view is given no replica breakdown: which gateway replicas
        // exist and where they run is infrastructure identity. What remains
        // true, and is still worth saying, is that the answer was assembled
        // from several sources at slightly different instants.
        //
        // 租户视图不会拿到逐副本的明细：有哪些网关副本、它们跑在哪里，属于基础设施身份。
        // 依然成立、也依然值得说的是：这个答案是在略有先后的若干时刻从多个来源拼起来的。
        <p className="text-xs text-muted-foreground">
          这份内容由多个网关副本各自的回答拼合而成，各自的采集时刻略有先后。
        </p>
      )}
    </div>
  );
}
