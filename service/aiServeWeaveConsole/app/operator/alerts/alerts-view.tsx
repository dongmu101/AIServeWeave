"use client";

import * as React from "react";
import {
  createColumnHelper,
  tableFeatures,
  useTable,
} from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";
import { toast } from "sonner";

import { Pager } from "@/components/console/pager";
import { ErrorState, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { useUrlFilters } from "@/components/console/use-url-filters";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { parseAlertInstances, parseNoContent, type AlertInstance } from "@/lib/console/contract";
import { ApiError, describe } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";

/**
 * The alerts view is the operator's read-mostly window onto fired
 * AlertRule instances (STATUS.md's P09/C29). It follows `RequestLogView`'s
 * and `AuditView`'s shape rather than `AlertRulesView`'s form-heavy one:
 * filtering and paging happen on the control plane, virtualization bounds
 * the DOM, and the one write this page has — acknowledging a firing
 * instance — is a single action on an otherwise read-only row rather than a
 * dialog.
 *
 * Acknowledging does not patch the clicked row from the write's own
 * response: `acknowledgeAlert` renders its result with an empty rule name
 * (the handler only resolves that name in the list path), so treating it as
 * the truth would blank a column that was correct a moment ago. The list is
 * reloaded instead, the same choice `AlertRulesView` makes after every write
 * of its own.
 *
 * 告警视图是运维查看已触发 AlertRule 实例的入口（STATUS.md 的 P09/C29），
 * 「读多写少」。它遵循的是 `RequestLogView` 与 `AuditView` 的结构，而不是
 * `AlertRulesView` 那种以表单为主的结构：筛选与翻页发生在控制面，虚拟滚动
 * 限制 DOM 数量，本页唯一的写操作——确认处理一条正在触发的实例——是一枚
 * 附着在原本只读行上的单一动作，而不是一个对话框。
 *
 * 确认处理不会用写请求自己的响应去给被点击的那一行打补丁：`acknowledgeAlert`
 * 渲染结果时 rule name 是空的（该名字只在列表路径里被解析出来），把它当作
 * 真相会把一列刚才还正确的内容清空。这里改为重新加载列表——与
 * `AlertRulesView` 每次写操作之后的做法一致。
 */
const PATH = "/operator/v1/alerts";
const PAGE_SIZE = 50;

/** STATUS_LABELS mirrors model.AlertStatus* — the three states an instance
 * moves through, firing to acknowledged or straight to resolved.
 *
 * STATUS_LABELS 镜像 model.AlertStatus* ——一条实例经历的三种状态，从 firing
 * 到 acknowledged，或直接到 resolved。 */
const STATUS_LABELS: Record<string, string> = {
  firing: "触发中",
  acknowledged: "已确认",
  resolved: "已解决",
};

const STATUSES = ["firing", "acknowledged", "resolved"] as const;

/** NOTIFY_STATUS_LABELS mirrors model.NotifyStatus* — the Webhook delivery
 * outcome for one instance, independent of the instance's own status.
 *
 * NOTIFY_STATUS_LABELS 镜像 model.NotifyStatus* ——一条实例的 Webhook 投递
 * 结果，与实例自身的状态无关。 */
const NOTIFY_STATUS_LABELS: Record<string, string> = {
  pending: "待发送",
  sent: "已发送",
  failed: "发送失败",
  skipped: "已跳过",
};

/** statusLabel and notifyStatusLabel fall back to the raw wire value for
 * anything this build does not yet recognize — an unknown value is a fact
 * worth showing, not something to hide behind a placeholder, the same rule
 * `AlertRulesView`'s `metricLabel` follows.
 *
 * statusLabel 与 notifyStatusLabel 对本次构建尚不认识的取值回退展示线上原始
 * 值——一个未知值是值得展示出来的事实，不该被占位符藏起来，与 `AlertRulesView`
 * 的 `metricLabel` 遵循同一条规则。 */
function statusLabel(status: string): string {
  return STATUS_LABELS[status] ?? status;
}

function notifyStatusLabel(status: string): string {
  return NOTIFY_STATUS_LABELS[status] ?? status;
}

/** statusBadgeVariant gives firing/acknowledged/resolved distinct visual
 * weight, the same convention `HistoryView`'s job-state column uses:
 * the state most needing attention reads as destructive, a settled one as
 * outline, and the state in between as secondary.
 *
 * statusBadgeVariant 让 firing/acknowledged/resolved 三种状态有明显区分，
 * 与 `HistoryView` 的 job 状态列同一套约定：最需要关注的状态用 destructive
 * 呈现，已尘埃落定的用 outline，居中的用 secondary。 */
function statusBadgeVariant(status: string): "destructive" | "secondary" | "outline" {
  if (status === "firing") {
    return "destructive";
  }
  if (status === "acknowledged") {
    return "secondary";
  }
  return "outline";
}

const features = tableFeatures({});
const helper = createColumnHelper<typeof features, AlertInstance>();

/** ROW_HEIGHT keeps every row one line tall, so the virtualizer needs no
 * measurement pass — the same reasoning `RequestLogView`'s ROW_HEIGHT gives.
 *
 * ROW_HEIGHT 让每一行都是一行高，这样虚拟滚动器不需要测量过程——与
 * `RequestLogView` 的 ROW_HEIGHT 同样的理由。 */
const ROW_HEIGHT = 40;

const NO_INSTANCES: AlertInstance[] = [];

/** GRID is the shared column geometry for the header and every row.
 *
 * GRID 是表头与每一行共用的列几何。 */
const GRID =
  "minmax(6rem,7rem) minmax(9rem,1fr) minmax(6rem,7rem) minmax(9rem,10rem) minmax(9rem,10rem) minmax(9rem,10rem) minmax(6rem,7rem) minmax(8rem,10rem)";

/** AlertsView lists fired alert instances and lets an operator acknowledge
 * one that is still firing.
 *
 * AlertsView 列出已触发的告警实例，并允许运维确认处理一条仍在触发中的实例。 */
export function AlertsView() {
  const run = useConsoleRequest();
  const [filters, setFilters] = useUrlFilters(["status", "rule_id", "since", "until"] as const);
  const [acknowledgingId, setAcknowledgingId] = React.useState<string | null>(null);
  const [ackErrors, setAckErrors] = React.useState<Record<string, string>>({});

  const alerts = usePagedResource<AlertInstance>({
    path: PATH,
    surface: "operator",
    filters: {
      status: filters.status,
      rule_id: filters.rule_id,
      // The date inputs give a local calendar day; the API takes an
      // instant, same conversion `RequestLogView` performs.
      //
      // 日期输入给出的是本地日历上的一天，而 API 接受的是一个时刻，转换方式
      // 与 `RequestLogView` 相同。
      since: startOfDay(filters.since),
      until: endOfDay(filters.until),
    },
    pageSize: PAGE_SIZE,
    parse: parseAlertInstances,
  });

  const rows = alerts.items ?? NO_INSTANCES;

  const acknowledge = React.useCallback(
    async (instance: AlertInstance) => {
      setAckErrors((previous) => {
        if (!(instance.id in previous)) {
          return previous;
        }
        const next = { ...previous };
        delete next[instance.id];
        return next;
      });
      setAcknowledgingId(instance.id);
      try {
        await run({
          method: "POST",
          surface: "operator",
          path: `${PATH}/${encodeURIComponent(instance.id)}/acknowledge`,
          parse: parseNoContent,
        });
      } catch (failure) {
        setAcknowledgingId(null);
        setAckErrors((previous) => ({ ...previous, [instance.id]: describe(failure) }));
        // A conflict means the instance's status already changed server-side
        // — e.g. it resolved between page load and this click — so the row
        // this button was drawn on no longer reflects reality. Reload rather
        // than leave a stale "firing" badge next to the error.
        //
        // 冲突意味着这条实例的状态已经在服务端发生了变化——例如页面加载之后、
        // 点击之前它已经被解决——此时按钮所在的这一行已经不反映现实。这里选择
        // 重新加载，而不是让一个过期的「触发中」徽章与错误提示并列。
        if (failure instanceof ApiError && failure.kind === "conflict") {
          alerts.reload();
        }
        return;
      }
      setAcknowledgingId(null);
      toast.success(`已确认处理 ${instance.ruleName || instance.ruleId}`);
      alerts.reload();
    },
    [run, alerts]
  );

  const columns = React.useMemo(
    () =>
      helper.columns([
        helper.accessor("status", {
          header: "状态",
          cell: (info) => (
            <Badge variant={statusBadgeVariant(info.getValue())}>
              {statusLabel(info.getValue())}
            </Badge>
          ),
        }),
        helper.accessor("ruleName", {
          header: "规则",
          cell: (info) => info.getValue() || info.row.original.ruleId,
        }),
        helper.accessor("valueAtFire", {
          header: "触发值",
          cell: (info) => info.getValue(),
        }),
        helper.accessor("createdAt", {
          header: "首次触发",
          cell: (info) => formatDateTime(info.getValue()),
        }),
        helper.accessor("lastEvaluatedAt", {
          header: "最近评估",
          cell: (info) => formatDateTime(info.getValue()),
        }),
        helper.accessor("resolvedAt", {
          header: "解决时间",
          cell: (info) => formatDateTime(info.getValue()),
        }),
        helper.accessor("notifyStatus", {
          header: "通知状态",
          cell: (info) => notifyStatusLabel(info.getValue()),
        }),
        helper.display({
          id: "actions",
          header: "操作",
          cell: (info) => {
            const instance = info.row.original;
            const rowError = ackErrors[instance.id];
            if (instance.status !== "firing") {
              return rowError ? <p className="text-xs text-destructive">{rowError}</p> : null;
            }
            const pending = acknowledgingId === instance.id;
            return (
              <div className="grid gap-1">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => acknowledge(instance)}
                  disabled={pending}
                  aria-busy={pending}
                >
                  确认处理
                </Button>
                {rowError ? <p className="text-xs text-destructive">{rowError}</p> : null}
              </div>
            );
          },
        }),
      ]),
    [acknowledge, acknowledgingId, ackErrors]
  );

  const table = useTable({ features, columns, data: rows });
  const modelRows = table.getRowModel().rows;

  const scrollRef = React.useRef<HTMLDivElement>(null);
  const virtualizer = useVirtualizer({
    count: modelRows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    getItemKey: (index) => modelRows[index]?.id ?? index,
    overscan: 12,
  });

  return (
    <div className="grid grid-cols-1 gap-4">
      <div>
        <h1 className="font-heading text-lg font-semibold">告警</h1>
        <p className="text-sm text-muted-foreground">
          告警规则触发出的实例，跨平台全部规则。仍在触发中的实例可以就地确认处理；创建、
          续期与解除由评估循环完成，这里不提供。
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1">
          <label htmlFor="alerts-status" className="text-xs text-muted-foreground">
            状态
          </label>
          <Select
            value={filters.status === "" ? "all" : filters.status}
            onValueChange={(next) => {
              if (next !== null) {
                setFilters({ status: next === "all" ? "" : next });
              }
            }}
          >
            <SelectTrigger id="alerts-status">
              <SelectValue>{filters.status === "" ? "全部状态" : statusLabel(filters.status)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部状态</SelectItem>
              {STATUSES.map((status) => (
                <SelectItem key={status} value={status}>
                  {STATUS_LABELS[status]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="grid gap-1">
          <label htmlFor="alerts-rule-id" className="text-xs text-muted-foreground">
            规则 ID
          </label>
          <Input
            id="alerts-rule-id"
            className="w-56 font-mono text-xs"
            placeholder="全部规则"
            value={filters.rule_id}
            onChange={(event) => setFilters({ rule_id: event.target.value })}
          />
        </div>

        <div className="grid gap-1">
          <label htmlFor="alerts-since" className="text-xs text-muted-foreground">
            起始日期（含）
          </label>
          <Input
            id="alerts-since"
            type="date"
            className="w-40"
            value={filters.since}
            onChange={(event) => setFilters({ since: event.target.value })}
          />
        </div>

        <div className="grid gap-1">
          <label htmlFor="alerts-until" className="text-xs text-muted-foreground">
            结束日期（含）
          </label>
          <Input
            id="alerts-until"
            type="date"
            className="w-40"
            value={filters.until}
            onChange={(event) => setFilters({ until: event.target.value })}
          />
        </div>

        {hasFilters(filters) ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setFilters({ status: "", rule_id: "", since: "", until: "" })}
          >
            清除筛选
          </Button>
        ) : null}
      </div>

      {alerts.error ? (
        <ErrorState message={alerts.error} onRetry={alerts.reload} />
      ) : alerts.loading ? (
        <LoadingState label="正在加载告警" rows={6} />
      ) : (
        <div className="rounded-xl border" role="table" aria-label="告警列表">
          <div className="overflow-x-auto">
            <div className="min-w-[64rem]">
              {table.getHeaderGroups().map((group) => (
                <div
                  key={group.id}
                  role="row"
                  className="grid gap-2 border-b bg-muted/40 px-3 py-2 text-sm font-medium"
                  style={{ gridTemplateColumns: GRID }}
                >
                  {group.headers.map((header) => (
                    <div key={header.id} role="columnheader">
                      {header.isPlaceholder ? null : <table.FlexRender header={header} />}
                    </div>
                  ))}
                </div>
              ))}

              {modelRows.length === 0 ? (
                <p className="px-3 py-8 text-center text-sm text-muted-foreground">
                  {hasFilters(filters) ? "没有符合筛选条件的告警" : "平台还没有告警实例"}
                </p>
              ) : (
                <div
                  ref={scrollRef}
                  className="max-h-[32rem] overflow-y-auto"
                  tabIndex={0}
                  aria-label="告警列表，可滚动"
                >
                  <div className="relative" style={{ height: virtualizer.getTotalSize() }}>
                    {virtualizer.getVirtualItems().map((item) => {
                      const row = modelRows[item.index];
                      if (!row) {
                        return null;
                      }
                      return (
                        <div
                          key={row.id}
                          role="row"
                          data-index={item.index}
                          className="absolute inset-x-0 grid items-center gap-2 border-b px-3 text-sm"
                          style={{
                            minHeight: ROW_HEIGHT,
                            transform: `translateY(${item.start}px)`,
                            gridTemplateColumns: GRID,
                          }}
                        >
                          {row.getAllCells().map((cell) => (
                            <div key={cell.id} role="cell" className="min-w-0 truncate">
                              <table.FlexRender cell={cell} />
                            </div>
                          ))}
                        </div>
                      );
                    })}
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>
      )}

      {alerts.error || alerts.loading ? null : <Pager resource={alerts} loadedCount={rows.length} />}

      <p className="text-xs text-muted-foreground">
        筛选与翻页由控制面执行；接口不提供总数，因此这里只显示页码而不显示总页数。
      </p>
    </div>
  );
}

/** hasFilters reports whether anything is narrowing the list, so an empty
 * page can say which kind of empty it is — the same helper `RequestLogView`
 * keeps for its own filter set.
 *
 * hasFilters 报告是否有条件正在收窄列表，好让一页空结果能说清自己是哪一种空
 * ——与 `RequestLogView` 为自己的筛选项保留的同一个帮助函数。 */
function hasFilters(filters: Record<string, string>): boolean {
  return filters.status !== "" || filters.rule_id !== "" || filters.since !== "" || filters.until !== "";
}

/**
 * startOfDay and endOfDay turn a local calendar day into the instants the
 * API takes, the same conversion `RequestLogView` and `HistoryView` each
 * keep their own copy of: `since` is inclusive and `until` is exclusive on
 * the control plane, so an end date becomes the start of the following day.
 *
 * startOfDay 与 endOfDay 把本地日历上的一天转换成 API 所接受的时刻，与
 * `RequestLogView`、`HistoryView` 各自保留的同一种转换：控制面那边 `since`
 * 含端点而 `until` 不含，因此结束日期会变成次日零点。
 */
function startOfDay(day: string): string {
  if (day === "") {
    return "";
  }
  const parsed = new Date(`${day}T00:00:00`);
  return Number.isNaN(parsed.getTime()) ? "" : parsed.toISOString();
}

function endOfDay(day: string): string {
  if (day === "") {
    return "";
  }
  const parsed = new Date(`${day}T00:00:00`);
  if (Number.isNaN(parsed.getTime())) {
    return "";
  }
  parsed.setDate(parsed.getDate() + 1);
  return parsed.toISOString();
}
