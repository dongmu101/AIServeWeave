"use client";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";
import type { Locale } from "@/lib/i18n/locale";


import * as React from "react";
import {
  createColumnHelper,
  tableFeatures,
  useTable,
} from "@tanstack/react-table";

import { CreateUserDialog } from "@/app/console/users/create-user-dialog";
import { UserActions } from "@/app/console/users/user-actions";
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
import { parseUsers, roleLabels, ROLES, type Role, type User } from "@/lib/console/contract";
import { formatDateTime } from "@/lib/console/format";
import { canCreateUser, canManageUsers } from "@/lib/console/permissions";

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

const columns = (t: Translator, locale: Locale) => (helper.columns([
  helper.accessor("name", {
    header: t("tenant.name"),
    cell: (info) => info.getValue() || <span className="text-muted-foreground"><Message id="tenant.notProvided" /></span>,
  }),
  helper.accessor("email", {
    header: t("tenant.email"),
    cell: (info) => <span className="font-mono text-xs">{info.getValue()}</span>,
  }),
  helper.accessor("role", {
    header: t("tenant.role"),
    cell: (info) => {
      const role = info.getValue();
      return <Badge variant="outline">{roleLabels(locale)[role as Role] ?? role}</Badge>;
    },
  }),
  helper.accessor("status", {
    header: t("tenant.status"),
    cell: (info) => (info.getValue() === "active" ? t("tenant.normal") : info.getValue()),
  }),
  helper.accessor("lastLoginAt", {
    header: t("tenant.lastSignedIn"),
    cell: (info) => formatDateTime(info.getValue(), t("tenant.neverSignedIn"), locale),
  }),
  helper.accessor("createdAt", {
    header: t("tenant.createdAt"),
    cell: (info) => formatDateTime(info.getValue(), undefined, locale),
  }),
]));

const PAGE_SIZE = 50;
const NO_USERS: User[] = [];

export function UsersView({ role, currentUserId }: { role: string; currentUserId: string }) {
  const { locale, t } = useI18n();
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
  const localizedColumns = React.useMemo(() => columns(t, locale), [t, locale]);
  const table = useTable({ features, columns: localizedColumns, data: rows });

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="font-heading text-lg font-semibold"><Message id="tenant.users" /></h1>
          <p className="text-sm text-muted-foreground">
            <Message id="tenant.usersInTheCurrentTenantPaginatedByCreation" /></p>
        </div>
        {canCreateUser(role) ? (
          <CreateUserDialog onCreated={users.reload} />
        ) : (
          <p className="text-sm text-muted-foreground">
            <Message id="tenant.onlyOwnersCanCreateUsers" /></p>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Input
          className="max-w-xs"
          placeholder={t("tenant.searchByEmailOrName")}
          value={draftQuery}
          onChange={(event) => setDraftQuery(event.target.value)}
          aria-label={t("tenant.searchUsersByEmailOrName")}
        />
        <Select
          value={filters.role === "" ? "all" : filters.role}
          onValueChange={(next) => {
            if (next !== null) {
              setFilters({ role: next === "all" ? "" : next });
            }
          }}
        >
          <SelectTrigger aria-label={t("tenant.filterByRole")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all"><Message id="tenant.allRoles" /></SelectItem>
            {ROLES.map((item) => (
              <SelectItem key={item} value={item}>
                {roleLabels(locale)[item]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {users.error ? (
        <ErrorState message={users.error} onRetry={users.reload} />
      ) : users.loading ? (
        <LoadingState label={t("tenant.loadingUsers")} rows={4} />
      ) : (
        <>
          <DataTable
            table={table}
            caption={t("tenant.currentTenantSUsers")}
            emptyMessage={
              filters.q === "" && filters.role === ""
                ? t("tenant.thisTenantHasNoUsersYet")
                : t("tenant.noUsersMatchTheFilters")
            }
          />
          <Pager resource={users} loadedCount={rows.length} />
          {canManageUsers(role) ? <section className="grid gap-3" aria-label={t("tenant.userManagementActions")}>
            <h2 className="font-heading font-semibold"><Message id="tenant.manageUsersOnThisPage" /></h2>
            {rows.filter((user) => user.id !== currentUserId).map((user) => <UserActions key={user.id} user={user} onChanged={users.reload} />)}
          </section> : null}
        </>
      )}
    </div>
  );
}
