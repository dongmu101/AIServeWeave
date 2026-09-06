"use client";

import * as React from "react";
import {
  createColumnHelper,
  tableFeatures,
  useTable,
} from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";

import { Pager } from "@/components/console/pager";
import { ErrorState, LoadingState } from "@/components/console/states";
import { usePagedResource } from "@/components/console/use-paged-resource";
import {
  useDebouncedValue,
  useUrlFilters,
} from "@/components/console/use-url-filters";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { parseAuditEntries, type AuditEntry } from "@/lib/console/contract";
import { AUDIT_ACTIONS, formatDateTime } from "@/lib/console/format";

/**
 * The audit trail.
 *
 * Filtering and paging are the control plane's now, which changes what this
 * view is allowed to claim: a search here answers a question about the tenant
 * rather than about the rows already downloaded. What it still cannot show is
 * a total — the API sends none, on purpose — so the pager states a position
 * and never a count of pages.
 *
 * Virtualization stays, and its job is unchanged: it bounds the DOM. Memory is
 * bounded by the page size, because a page replaces the previous one instead
 * of appending to it.
 *
 * 审计线索。
 *
 * 筛选与翻页现在归控制面了，这改变了本视图有资格宣称的内容：这里的一次搜索回答的是
 * 关于整个租户的问题，而不是关于已经下载到的那些行。它依然无法展示的是总数——API 刻意
 * 不发送——因此翻页控件陈述的是位置，绝不是页数。
 *
 * 虚拟滚动保留，职责不变：它限制 DOM 的数量。内存则由分页大小限制，因为新的一页是替换
 * 上一页而不是追加在它后面。
 */
const features = tableFeatures({});
const helper = createColumnHelper<typeof features, AuditEntry>();

const columns = helper.columns([
  helper.accessor("createdAt", {
    header: "时间",
    cell: (info) => formatDateTime(info.getValue()),
  }),
  helper.accessor("action", {
    header: "动作",
    cell: (info) => <span className="font-mono text-xs">{info.getValue()}</span>,
  }),
  helper.accessor("actorId", {
    header: "操作者",
    cell: (info) => (
      <span className="font-mono text-xs">
        {info.getValue() === "" ? "系统" : info.getValue()}
      </span>
    ),
  }),
  helper.accessor("target", {
    header: "目标",
    cell: (info) => <span className="font-mono text-xs">{info.getValue()}</span>,
  }),
  helper.accessor("detail", {
    header: "详情",
    cell: (info) => {
      const detail = info.getValue();
      if (detail === "") {
        return <span className="text-muted-foreground">—</span>;
      }
      // Rendered as text with the full value in the title: a detail is written
      // by the control plane, but it is still data, and a row is not a place
      // to interpret markup.
      //
      // 以纯文本渲染，完整值放在 title 里：详情由控制面写入，但它终究是数据，而表格行
      // 不是解释标记语言的地方。
      return (
        <span className="block truncate" title={detail}>
          {detail}
        </span>
      );
    },
  }),
  helper.accessor("ip", {
    header: "IP",
    cell: (info) => <span className="font-mono text-xs">{info.getValue()}</span>,
  }),
]);

/** PAGE_SIZES is what the person may ask one page to hold.
 *
 * PAGE_SIZES 是当事人可以要求单页容纳的条数。 */
const PAGE_SIZES = ["50", "100", "200"] as const;

/** GRID is the shared column geometry for the header and every row.
 *
 * GRID 是表头与每一行共用的列几何。 */
const GRID =
  "minmax(9.5rem,10rem) minmax(8rem,9rem) minmax(9rem,11rem) minmax(9rem,11rem) minmax(10rem,1fr) minmax(6rem,7rem)";

/** ROW_HEIGHT keeps every row one line tall, so the virtualizer needs no
 * measurement pass and scrolling does not shift under the pointer.
 *
 * ROW_HEIGHT 让每一行都是一行高，这样虚拟滚动器不需要测量过程，滚动也不会在指针下
 * 发生跳动。 */
const ROW_HEIGHT = 40;

const NO_ENTRIES: AuditEntry[] = [];

export function AuditView() {
  const [filters, setFilters] = useUrlFilters([
    "q",
    "action",
    "actor_id",
    "since",
    "until",
    "size",
  ] as const);
  const [draftActor, setDraftActor] = React.useState(filters.actor_id);
  const debouncedActor = useDebouncedValue(draftActor);

  React.useEffect(() => {
    if (debouncedActor !== filters.actor_id) {
      setFilters({ actor_id: debouncedActor });
    }
  }, [debouncedActor, filters.actor_id, setFilters]);

  const pageSize = PAGE_SIZES.includes(filters.size as (typeof PAGE_SIZES)[number])
    ? Number(filters.size)
    : 100;

  const audit = usePagedResource<AuditEntry>({
    path: "/admin/v1/audit",
    filters: {
      action: filters.action,
      actor_id: filters.actor_id,
      // The date inputs give a local calendar day; the API takes an instant.
      // Converting here means the window a person picked is the window the
      // server applies, rather than one shifted by their timezone.
      //
      // 日期输入给出的是本地日历上的一天，而 API 接受的是一个时刻。在这里转换，意味着
      // 当事人选定的时间窗就是服务端施加的时间窗，而不是一个被其时区平移过的窗口。
      since: startOfDay(filters.since),
      until: endOfDay(filters.until),
    },
    pageSize,
    parse: parseAuditEntries,
  });

  const rows = audit.items ?? NO_ENTRIES;

  const table = useTable({ features, columns, data: rows });
  const modelRows = table.getRowModel().rows;

  const scrollRef = React.useRef<HTMLDivElement>(null);
  // The virtualizer holds a live scroll subscription, so the React Compiler
  // lint refuses to memoize around it. That is a property of the library, not
  // a mistake here: this is the composition TanStack documents for virtualized
  // rows, and the alternative is rendering every row into the DOM.
  //
  // 虚拟滚动器持有一个实时的滚动订阅，因此 React Compiler 的 lint 拒绝围绕它做记忆化。
  // 这是该库的性质而不是这里的错误：这正是 TanStack 为虚拟化行所记载的组合方式，而
  // 另一种选择是把每一行都渲染进 DOM。
  // eslint-disable-next-line react-hooks/incompatible-library
  const virtualizer = useVirtualizer({
    count: modelRows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => ROW_HEIGHT,
    getItemKey: (index) => modelRows[index]?.id ?? index,
    overscan: 12,
  });

  return (
    <div className="grid gap-4">
      <div>
        <h1 className="font-heading text-lg font-semibold">管理审计</h1>
        <p className="text-sm text-muted-foreground">
          当前租户的管理操作记录：登录、创建用户、创建与吊销 Key、修改配额。
          这不是推理请求日志，网关处理的调用不会出现在这里。
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1">
          <label htmlFor="audit-action" className="text-xs text-muted-foreground">
            动作
          </label>
          <Select
            value={filters.action === "" ? "all" : filters.action}
            onValueChange={(next) => {
              if (next !== null) {
                setFilters({ action: next === "all" ? "" : next });
              }
            }}
          >
            <SelectTrigger id="audit-action">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部动作</SelectItem>
              {AUDIT_ACTIONS.map((action) => (
                <SelectItem key={action.value} value={action.value}>
                  {action.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="grid gap-1">
          <label htmlFor="audit-actor" className="text-xs text-muted-foreground">
            操作者 ID
          </label>
          <Input
            id="audit-actor"
            className="w-56 font-mono text-xs"
            placeholder="usr_..."
            value={draftActor}
            onChange={(event) => setDraftActor(event.target.value)}
          />
        </div>

        <div className="grid gap-1">
          <label htmlFor="audit-since" className="text-xs text-muted-foreground">
            起始日期（含）
          </label>
          <Input
            id="audit-since"
            type="date"
            className="w-40"
            value={filters.since}
            onChange={(event) => setFilters({ since: event.target.value })}
          />
        </div>

        <div className="grid gap-1">
          <label htmlFor="audit-until" className="text-xs text-muted-foreground">
            结束日期（含）
          </label>
          <Input
            id="audit-until"
            type="date"
            className="w-40"
            value={filters.until}
            onChange={(event) => setFilters({ until: event.target.value })}
          />
        </div>

        <div className="grid gap-1">
          <label htmlFor="audit-size" className="text-xs text-muted-foreground">
            每页
          </label>
          <Select
            value={String(pageSize)}
            onValueChange={(next) => {
              if (next !== null) {
                setFilters({ size: next });
              }
            }}
          >
            <SelectTrigger id="audit-size">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PAGE_SIZES.map((size) => (
                <SelectItem key={size} value={size}>
                  {size} 条
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {hasFilters(filters) ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={() =>
              setFilters({ action: "", actor_id: "", since: "", until: "" })
            }
          >
            清除筛选
          </Button>
        ) : null}
      </div>

      {audit.error ? (
        <ErrorState message={audit.error} onRetry={audit.reload} />
      ) : audit.loading ? (
        <LoadingState label="正在加载审计记录" rows={6} />
      ) : (
        <div className="rounded-xl border" role="table" aria-label="管理审计记录">
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
                      {header.isPlaceholder ? null : (
                        <table.FlexRender header={header} />
                      )}
                    </div>
                  ))}
                </div>
              ))}

              {modelRows.length === 0 ? (
                <p className="px-3 py-8 text-center text-sm text-muted-foreground">
                  {hasFilters(filters)
                    ? "没有符合筛选条件的记录"
                    : "该租户还没有管理操作记录"}
                </p>
              ) : (
                <div
                  ref={scrollRef}
                  className="max-h-[32rem] overflow-y-auto"
                  tabIndex={0}
                  aria-label="审计记录列表，可滚动"
                >
                  <div
                    className="relative"
                    style={{ height: virtualizer.getTotalSize() }}
                  >
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
                            height: ROW_HEIGHT,
                            transform: `translateY(${item.start}px)`,
                            gridTemplateColumns: GRID,
                          }}
                        >
                          {row.getAllCells().map((cell) => (
                            <div
                              key={cell.id}
                              role="cell"
                              className="min-w-0 truncate"
                            >
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

      {audit.error || audit.loading ? null : (
        <Pager resource={audit} loadedCount={rows.length} />
      )}

      <p className="text-xs text-muted-foreground">
        筛选与翻页由控制面执行，覆盖当前租户的全部记录；接口不提供总数，因此这里只显示
        页码而不显示总页数。审计写入与被记录的操作不在同一个事务中，控制面在写入失败时
        只记录日志；因此这份记录可能少于实际发生的操作，不能当作完备账本使用。
      </p>
    </div>
  );
}

/** hasFilters reports whether anything is narrowing the list, so an empty page
 * can say which kind of empty it is.
 *
 * hasFilters 报告是否有条件正在收窄列表，好让一页空结果能说清自己是哪一种空。 */
function hasFilters(filters: Record<string, string>): boolean {
  return (
    filters.action !== "" ||
    filters.actor_id !== "" ||
    filters.since !== "" ||
    filters.until !== ""
  );
}

/**
 * startOfDay and endOfDay turn a local calendar day into the instants the API
 * takes. `since` is inclusive and `until` is exclusive there, so an end date
 * becomes the start of the following day — otherwise picking the same day for
 * both would select an empty window, and the server would rightly refuse it.
 *
 * startOfDay 与 endOfDay 把本地日历上的一天转换成 API 所接受的时刻。那边 `since` 含
 * 端点而 `until` 不含，因此结束日期会变成次日零点——否则两端选同一天就会得到一个空窗口，
 * 而服务端理应拒绝它。
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
