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
import { parseRequestLogEntries, type RequestLogEntry } from "@/lib/console/contract";
import { formatDateTime } from "@/lib/console/format";

/**
 * The request search view (STATUS.md's P09/C28).
 *
 * It shares its shape with `AuditView`: filtering and paging happen on the
 * control plane, virtualization bounds the DOM, and the pager states a
 * position rather than a page count because the API sends no total. What
 * differs from an audit trail is the subject — an authenticated front-door
 * call rather than an administrative action — and, on the operator surface
 * only, one extra column naming which tenant made the call.
 *
 * 请求检索视图（STATUS.md 的 P09/C28）。
 *
 * 它与 `AuditView` 共用同一套结构：筛选与翻页发生在控制面，虚拟滚动限制 DOM
 * 数量，翻页控件陈述的是位置而非页数，因为 API 不发送总数。与审计线索不同的是
 * 主体——这里是一次已鉴权的前门调用，而不是一次管理操作——以及仅在运维视角下
 * 才出现的、说明这次调用来自哪个租户的额外一列。
 */
const features = tableFeatures({});
const helper = createColumnHelper<typeof features, RequestLogEntry>();

/** PAGE_SIZES is what the person may ask one page to hold.
 *
 * PAGE_SIZES 是当事人可以要求单页容纳的条数。 */
const PAGE_SIZES = ["50", "100", "200"] as const;

/** ROW_HEIGHT keeps every row one line tall, so the virtualizer needs no
 * measurement pass and scrolling does not shift under the pointer.
 *
 * ROW_HEIGHT 让每一行都是一行高，这样虚拟滚动器不需要测量过程，滚动也不会在指针下
 * 发生跳动。 */
const ROW_HEIGHT = 40;

const NO_ENTRIES: RequestLogEntry[] = [];

/**
 * OUTCOMES is the closed vocabulary the Gateway writes (`httpapi.Outcome*`).
 * The filter offers exactly these, never a free-text box, because the API
 * matches an outcome exactly and a typed value outside this set would return
 * an empty page that reads as "nothing happened" rather than "no such value".
 *
 * OUTCOMES 是 Gateway 写入的封闭词汇表（`httpapi.Outcome*`）。筛选只提供这些
 * 选项，绝不是自由文本框，因为 API 对结果做精确匹配，集合之外的手打值会返回一个
 * 空页，读起来像「什么都没发生」而不是「没有这个取值」。
 */
const OUTCOMES: readonly { value: string; label: string }[] = [
  { value: "ok", label: "成功" },
  { value: "invalid_request", label: "请求无效" },
  { value: "unauthorized", label: "未鉴权" },
  { value: "forbidden", label: "无权限" },
  { value: "not_found", label: "未找到" },
  { value: "rate_limited", label: "限流" },
  { value: "internal", label: "内部错误" },
  { value: "upstream_unavailable", label: "上游不可用" },
  { value: "error", label: "其它错误" },
];

/** outcomeLabel resolves the display label for an outcome value, falling
 * back to the raw value so a build that does not yet know a newer outcome
 * still shows something rather than nothing.
 *
 * outcomeLabel 求解一个 outcome 取值对应的展示标签；找不到时回退为原始值，
 * 好让一个尚不认识某个较新取值的构建仍能显示点什么，而不是什么都不显示。 */
function outcomeLabel(value: string): string {
  return OUTCOMES.find((outcome) => outcome.value === value)?.label ?? value;
}

/** GRID_TENANT and GRID_OPERATOR are the shared column geometry for the
 * header and every row, on the tenant surface and the operator surface
 * respectively — the operator grid has one more track for the tenant column.
 *
 * GRID_TENANT 与 GRID_OPERATOR 分别是租户视角与运维视角下、表头与每一行共用的
 * 列几何——运维视角的网格多一条给租户列的轨道。 */
const GRID_TENANT =
  "minmax(9.5rem,10rem) minmax(5rem,6rem) minmax(7rem,9rem) minmax(7rem,9rem) minmax(5rem,6rem) minmax(9rem,1fr)";
const GRID_OPERATOR = `${GRID_TENANT} minmax(8rem,9rem)`;

/** RequestLogView shares paging and virtualization across the tenant and
 * operator request-search surfaces.
 *
 * RequestLogView 在租户与运维两个请求检索入口之间共用分页与虚拟滚动。 */
export function RequestLogView({ surface = "admin" }: { surface?: "admin" | "operator" }) {
  const [filters, setFilters] = useUrlFilters([
    "since",
    "until",
    "status",
    "request_id",
    "tenant_id",
    "size",
  ] as const);
  const [draftRequestId, setDraftRequestId] = React.useState(filters.request_id);
  const debouncedRequestId = useDebouncedValue(draftRequestId);

  React.useEffect(() => {
    if (debouncedRequestId !== filters.request_id) {
      setFilters({ request_id: debouncedRequestId });
    }
  }, [debouncedRequestId, filters.request_id, setFilters]);

  const pageSize = PAGE_SIZES.includes(filters.size as (typeof PAGE_SIZES)[number])
    ? Number(filters.size)
    : 100;

  const requests = usePagedResource<RequestLogEntry>({
    path: surface === "operator" ? "/operator/v1/requests" : "/admin/v1/requests",
    surface,
    filters: {
      status: filters.status,
      request_id: filters.request_id,
      // The date inputs give a local calendar day; the API takes an instant.
      // Converting here means the window a person picked is the window the
      // server applies, rather than one shifted by their timezone.
      //
      // 日期输入给出的是本地日历上的一天，而 API 接受的是一个时刻。在这里转换，意味着
      // 当事人选定的时间窗就是服务端施加的时间窗，而不是一个被其时区平移过的窗口。
      since: startOfDay(filters.since),
      until: endOfDay(filters.until),
      // tenant_id only narrows anything on the operator surface: the tenant
      // surface is already scoped to the caller's own tenant by the session,
      // and sending it there would ask the control plane to filter a column
      // the endpoint does not even take.
      //
      // tenant_id 只在运维视角下才有筛选意义：租户视角本身已经由会话限定在调用方
      // 自己的租户上，在那里发送它，等于要求控制面按一个该端点根本不接受的列筛选。
      ...(surface === "operator" ? { tenant_id: filters.tenant_id } : {}),
    },
    pageSize,
    parse: parseRequestLogEntries,
  });

  const rows = requests.items ?? NO_ENTRIES;

  // Columns vary by surface — the tenant column exists only on the operator
  // surface — so they are built per-render instead of hoisted to module
  // scope the way `audit-view.tsx`'s columns are.
  //
  // 列会随视角变化——租户列只在运维视角下存在——因此这里按渲染构建列，而不是像
  // `audit-view.tsx` 的列那样提升到模块作用域。
  const columns = React.useMemo(() => {
    const base = [
      helper.accessor("createdAt", {
        header: "时间",
        cell: (info) => formatDateTime(info.getValue()),
      }),
      helper.accessor("statusCode", {
        header: "状态码",
        cell: (info) => <span className="font-mono text-xs">{info.getValue()}</span>,
      }),
      helper.accessor("outcome", {
        header: "结果",
        cell: (info) => outcomeLabel(info.getValue()),
      }),
      helper.accessor("endpoint", {
        header: "接口",
        cell: (info) => info.getValue(),
      }),
      helper.accessor("durationMs", {
        header: "耗时",
        cell: (info) => `${info.getValue()} ms`,
      }),
      helper.accessor("keyDisplay", {
        header: "Key",
        cell: (info) => <span className="font-mono text-xs">{info.getValue()}</span>,
      }),
    ];
    if (surface !== "operator") {
      return helper.columns(base);
    }
    return helper.columns([
      ...base,
      helper.accessor("tenantId", {
        header: "租户",
        cell: (info) => <span className="font-mono text-xs">{info.getValue()}</span>,
      }),
    ]);
  }, [surface]);

  const table = useTable({ features, columns, data: rows });
  const modelRows = table.getRowModel().rows;
  const grid = surface === "operator" ? GRID_OPERATOR : GRID_TENANT;

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
        <h1 className="font-heading text-lg font-semibold">请求检索</h1>
        <p className="text-sm text-muted-foreground">
          {surface === "operator"
            ? "跨租户的请求检索，用于排查与客户支持。"
            : "当前租户已通过鉴权的 chat/responses/embeddings/models 请求，脱敏元数据，仅覆盖最近 30 天。"}
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <div className="grid gap-1">
          <label htmlFor="requests-status" className="text-xs text-muted-foreground">
            结果
          </label>
          <Select
            value={filters.status === "" ? "all" : filters.status}
            onValueChange={(next) => {
              if (next !== null) {
                setFilters({ status: next === "all" ? "" : next });
              }
            }}
          >
            <SelectTrigger id="requests-status">
              <SelectValue>{filters.status === "" ? "全部状态" : outcomeLabel(filters.status)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部状态</SelectItem>
              {OUTCOMES.map((outcome) => (
                <SelectItem key={outcome.value} value={outcome.value}>
                  {outcome.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="grid gap-1">
          <label htmlFor="requests-request-id" className="text-xs text-muted-foreground">
            请求 ID
          </label>
          <Input
            id="requests-request-id"
            className="w-56 font-mono text-xs"
            placeholder="req_..."
            value={draftRequestId}
            onChange={(event) => setDraftRequestId(event.target.value)}
          />
        </div>

        {surface === "operator" ? (
          <div className="grid gap-1">
            <label htmlFor="requests-tenant-id" className="text-xs text-muted-foreground">
              租户 ID
            </label>
            <Input
              id="requests-tenant-id"
              className="w-56 font-mono text-xs"
              placeholder="全部租户"
              value={filters.tenant_id}
              onChange={(event) => setFilters({ tenant_id: event.target.value })}
            />
          </div>
        ) : null}

        <div className="grid gap-1">
          <label htmlFor="requests-since" className="text-xs text-muted-foreground">
            起始日期（含）
          </label>
          <Input
            id="requests-since"
            type="date"
            className="w-40"
            value={filters.since}
            onChange={(event) => setFilters({ since: event.target.value })}
          />
        </div>

        <div className="grid gap-1">
          <label htmlFor="requests-until" className="text-xs text-muted-foreground">
            结束日期（含）
          </label>
          <Input
            id="requests-until"
            type="date"
            className="w-40"
            value={filters.until}
            onChange={(event) => setFilters({ until: event.target.value })}
          />
        </div>

        <div className="grid gap-1">
          <label htmlFor="requests-size" className="text-xs text-muted-foreground">
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
            <SelectTrigger id="requests-size">
              <SelectValue>{pageSize} 条 / 页</SelectValue>
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

        {hasFilters(filters, surface) ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={() =>
              setFilters({ status: "", request_id: "", tenant_id: "", since: "", until: "" })
            }
          >
            清除筛选
          </Button>
        ) : null}
      </div>

      {requests.error ? (
        <ErrorState message={requests.error} onRetry={requests.reload} />
      ) : requests.loading ? (
        <LoadingState label="正在加载请求记录" rows={6} />
      ) : (
        <div className="rounded-xl border" role="table" aria-label="请求检索记录">
          <div className="overflow-x-auto">
            <div className="min-w-[64rem]">
              {table.getHeaderGroups().map((group) => (
                <div
                  key={group.id}
                  role="row"
                  className="grid gap-2 border-b bg-muted/40 px-3 py-2 text-sm font-medium"
                  style={{ gridTemplateColumns: grid }}
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
                  {hasFilters(filters, surface)
                    ? "没有符合筛选条件的记录"
                    : surface === "operator" ? "平台还没有请求记录" : "该租户还没有请求记录"}
                </p>
              ) : (
                <div
                  ref={scrollRef}
                  className="max-h-[32rem] overflow-y-auto"
                  tabIndex={0}
                  aria-label="请求记录列表，可滚动"
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
                            gridTemplateColumns: grid,
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

      {requests.error || requests.loading ? null : (
        <Pager resource={requests} loadedCount={rows.length} />
      )}

      <p className="text-xs text-muted-foreground">
        筛选与翻页由控制面执行，覆盖{surface === "operator" ? "平台的全部请求记录" : "当前租户最近 30 天的全部记录"}；
        接口不提供总数，因此这里只显示页码而不显示总页数。
      </p>
    </div>
  );
}

/** hasFilters reports whether anything is narrowing the list, so an empty page
 * can say which kind of empty it is. tenant_id only counts on the operator
 * surface, where it is actually sent to the server.
 *
 * hasFilters 报告是否有条件正在收窄列表，好让一页空结果能说清自己是哪一种空。
 * tenant_id 只在运维视角下才计入，因为只有那里它才真的被发往服务端。 */
function hasFilters(filters: Record<string, string>, surface: "admin" | "operator"): boolean {
  return (
    filters.status !== "" ||
    filters.request_id !== "" ||
    filters.since !== "" ||
    filters.until !== "" ||
    (surface === "operator" && filters.tenant_id !== "")
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
