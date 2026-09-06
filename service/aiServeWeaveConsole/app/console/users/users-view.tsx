"use client";

import * as React from "react";
import {
  createColumnHelper,
  tableFeatures,
  useTable,
} from "@tanstack/react-table";

import { CreateUserDialog } from "@/app/console/users/create-user-dialog";
import { DataTable } from "@/components/console/data-table";
import { Pager } from "@/components/console/pager";
import { ErrorState, LoadingState } from "@/components/console/states";
import { usePagedResource } from "@/components/console/use-paged-resource";
import {
  useDebouncedValue,
  useUrlFilters,
} from "@/components/console/use-url-filters";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { parseUsers, ROLE_LABELS, ROLES, type Role, type User } from "@/lib/console/contract";
import { formatDateTime } from "@/lib/console/format";
import { canCreateUser } from "@/lib/console/permissions";

/**
 * The user list.
 *
 * Filtering and paging both happen on the control plane. The difference from a
 * local filter is not performance — it is that the answer covers the tenant
 * rather than the rows this page happens to hold, so the view no longer has to
 * qualify what its own search means.
 *
 * 用户列表。
 *
 * 筛选与翻页都发生在控制面。它与本地筛选的区别不在性能——而在于结果覆盖的是整个租户，
 * 而不是本页碰巧持有的那些行，因此视图不必再为自己的搜索加上限定说明。
 */
const features = tableFeatures({});
const helper = createColumnHelper<typeof features, User>();

const columns = helper.columns([
  helper.accessor("name", {
    header: "名称",
    cell: (info) => info.getValue() || <span className="text-muted-foreground">未填写</span>,
  }),
  helper.accessor("email", {
    header: "邮箱",
    cell: (info) => <span className="font-mono text-xs">{info.getValue()}</span>,
  }),
  helper.accessor("role", {
    header: "角色",
    cell: (info) => {
      const role = info.getValue();
      return <Badge variant="outline">{ROLE_LABELS[role as Role] ?? role}</Badge>;
    },
  }),
  helper.accessor("status", {
    header: "状态",
    cell: (info) => (info.getValue() === "active" ? "正常" : info.getValue()),
  }),
  helper.accessor("lastLoginAt", {
    header: "最近登录",
    cell: (info) => formatDateTime(info.getValue(), "从未登录"),
  }),
  helper.accessor("createdAt", {
    header: "创建时间",
    cell: (info) => formatDateTime(info.getValue()),
  }),
]);

const PAGE_SIZE = 50;
const NO_USERS: User[] = [];

export function UsersView({ role }: { role: string }) {
  const [filters, setFilters] = useUrlFilters(["q", "role"] as const);
  const [draftQuery, setDraftQuery] = React.useState(filters.q);
  const debouncedQuery = useDebouncedValue(draftQuery);

  React.useEffect(() => {
    if (debouncedQuery !== filters.q) {
      setFilters({ q: debouncedQuery });
    }
  }, [debouncedQuery, filters.q, setFilters]);

  const users = usePagedResource<User>({
    path: "/admin/v1/users",
    filters: { q: filters.q, role: filters.role },
    pageSize: PAGE_SIZE,
    parse: parseUsers,
  });

  const rows = users.items ?? NO_USERS;
  const table = useTable({ features, columns, data: rows });

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="font-heading text-lg font-semibold">用户</h1>
          <p className="text-sm text-muted-foreground">
            当前租户的用户，按创建时间倒序分页。控制面尚未提供编辑、删除、禁用与重置
            密码接口，这些操作不在此开放。
          </p>
        </div>
        {canCreateUser(role) ? (
          <CreateUserDialog onCreated={users.reload} />
        ) : (
          <p className="text-sm text-muted-foreground">
            只有所有者可以创建用户。
          </p>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Input
          className="max-w-xs"
          placeholder="按邮箱或姓名搜索"
          value={draftQuery}
          onChange={(event) => setDraftQuery(event.target.value)}
          aria-label="按邮箱或姓名搜索用户"
        />
        <Select
          value={filters.role === "" ? "all" : filters.role}
          onValueChange={(next) => {
            if (next !== null) {
              setFilters({ role: next === "all" ? "" : next });
            }
          }}
        >
          <SelectTrigger aria-label="按角色筛选">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部角色</SelectItem>
            {ROLES.map((item) => (
              <SelectItem key={item} value={item}>
                {ROLE_LABELS[item]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {users.error ? (
        <ErrorState message={users.error} onRetry={users.reload} />
      ) : users.loading ? (
        <LoadingState label="正在加载用户" rows={4} />
      ) : (
        <>
          <DataTable
            table={table}
            caption="当前租户的用户"
            emptyMessage={
              filters.q === "" && filters.role === ""
                ? "该租户还没有用户"
                : "没有符合筛选条件的用户"
            }
          />
          <Pager resource={users} loadedCount={rows.length} />
        </>
      )}
    </div>
  );
}
