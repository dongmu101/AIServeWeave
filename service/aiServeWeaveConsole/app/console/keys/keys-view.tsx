"use client";


import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";


import * as React from "react";
import {
  createColumnHelper,
  tableFeatures,
  useTable,
} from "@tanstack/react-table";
import { toast } from "sonner";

import { CreateKeyDialog } from "@/app/console/keys/create-key-dialog";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { DataTable } from "@/components/console/data-table";
import { Pager } from "@/components/console/pager";
import { ErrorState, LoadingState } from "@/components/console/states";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { usePagedResource } from "@/components/console/use-paged-resource";
import {
  useDebouncedValue,
  useUrlFilters,
} from "@/components/console/use-url-filters";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  parseApiKeys,
  parseNoContent,
  type ApiKeySummary,
} from "@/lib/console/contract";
import { ApiError } from "@/lib/console/errors";
import {
  apiKeyState,
  formatDateTime,
  NEVER_EXPIRES,
} from "@/lib/console/format";
import { canCreateApiKey, canRevokeApiKey } from "@/lib/console/permissions";

/**
 * The API key list.
 *
 * Two things here are load-bearing rather than cosmetic. The column shows the
 * `display` form the control plane returned and never assembles one from
 * parts, because that form is defined in `common/apikey` and is the only
 * rendering of a credential this system has. And the state column is computed
 * — a key stored as "active" whose expiry has passed is shown as expired,
 * because the control plane does not rewrite the row when time passes.
 *
 * API Key 列表。
 *
 * 这里有两处是承重的而非装饰。列里展示的是控制面返回的 `display` 形式，绝不自行拼接，
 * 因为那个形式定义在 `common/apikey`，是本系统对一个凭据唯一的呈现方式。状态列是算出来
 * 的——一个存着 "active" 但已过期的 key 会显示为已过期，因为控制面不会因为时间流逝而
 * 回头改写那一行。
 */
const features = tableFeatures({});
const helper = createColumnHelper<typeof features, ApiKeySummary>();

const NO_KEYS: ApiKeySummary[] = [];

const PAGE_SIZE = 50;

export function KeysView({ role, userId }: { role: string; userId: string }) {
  const { locale, t } = useI18n();
  const run = useConsoleRequest();
  const [filters, setFilters] = useUrlFilters(["q", "status"] as const);
  const [draftQuery, setDraftQuery] = React.useState(filters.q);
  const debouncedQuery = useDebouncedValue(draftQuery);
  const [target, setTarget] = React.useState<ApiKeySummary | null>(null);

  React.useEffect(() => {
    if (debouncedQuery !== filters.q) {
      setFilters({ q: debouncedQuery });
    }
  }, [debouncedQuery, filters.q, setFilters]);

  const keys = usePagedResource<ApiKeySummary>({
    path: "/admin/v1/apikeys",
    filters: { q: filters.q, status: filters.status },
    pageSize: PAGE_SIZE,
    parse: parseApiKeys,
  });

  const rows = keys.items ?? NO_KEYS;

  const columns = React.useMemo(
    () =>
      helper.columns([
        helper.accessor("name", { header: t("tenant.name") }),
        helper.accessor("display", {
          header: "Key",
          cell: (info) => (
            <span className="font-mono text-xs">{info.getValue()}</span>
          ),
        }),
        helper.accessor("createdBy", {
          header: t("tenant.createdBy"),
          cell: (info) => (
            <span className="font-mono text-xs">
              {info.getValue()}
              {info.getValue() === userId ? t("tenant.me") : ""}
            </span>
          ),
        }),
        helper.display({
          id: "state",
          header: t("tenant.status"),
          cell: (info) => {
            const state = apiKeyState(info.row.original, new Date(), locale);
            return (
              <Badge
                variant={state.kind === "active" ? "secondary" : "outline"}
                className={state.kind === "revoked" ? "text-destructive" : undefined}
              >
                {state.label}
              </Badge>
            );
          },
        }),
        helper.accessor("expiresAt", {
          header: t("tenant.expiresAt"),
          cell: (info) => formatDateTime(info.getValue(), NEVER_EXPIRES, locale),
        }),
        helper.accessor("lastUsedAt", {
          header: t("tenant.lastUsed"),
          cell: (info) => formatDateTime(info.getValue(), t("tenant.neverUsed"), locale),
        }),
        helper.accessor("createdAt", {
          header: t("tenant.createdAt"),
          cell: (info) => formatDateTime(info.getValue(), undefined, locale),
        }),
        helper.display({
          id: "actions",
          header: t("tenant.actions"),
          cell: (info) => {
            const key = info.row.original;
            const state = apiKeyState(key, new Date(), locale);
            if (state.kind === "revoked" || !canRevokeApiKey(role, userId, key)) {
              return null;
            }
            return (
              <Button
                variant="destructive"
                size="sm"
                onClick={() => setTarget(key)}
              >
                <Message id="tenant.revoke" /></Button>
            );
          },
        }),
      ]),
    [role, userId, t, locale]
  );

  const table = useTable({ features, columns, data: rows });

  async function revoke(key: ApiKeySummary) {
    try {
      await run({
        method: "DELETE",
        path: `/admin/v1/apikeys/${encodeURIComponent(key.id)}`,
        parse: parseNoContent,
      });
    } catch (failure) {
      // Revocation is not idempotent: the control plane matches an active row,
      // so revoking an already-revoked key answers 404 — the same code it uses
      // for another tenant's key and for an id that never existed. From here
      // the three are indistinguishable, so the honest response is to re-read
      // and say what is actually known.
      //
      // 吊销不是幂等的：控制面匹配的是一行 active 记录，因此吊销一个已被吊销的 key
      // 会得到 404 —— 与「别的租户的 key」和「从来不存在的 id」同一个码。站在这里，
      // 三者无法区分，因此诚实的做法是重新读取，并只说确实知道的事。
      if (failure instanceof ApiError && failure.kind === "not_found") {
        keys.reload();
        throw new Error(t("tenant.thisKeyCanNoLongerBeRevokedSomeone"));
      }
      throw failure;
    }
    toast.success(<Message id={"tenant.revoked_2b45c6"} values={{ name: key.name }} />);
    // The list is re-read rather than patched in place: a write whose result
    // this Console only assumes is a write it has not confirmed, and the
    // server's copy is the one that decides.
    //
    // 列表是重新读取而不是就地打补丁：一次只被本 Console 假定了结果的写入，就是一次
    // 未经确认的写入，而作数的是服务端那一份。
    keys.reload();
  }

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="font-heading text-lg font-semibold">API Key</h1>
          <p className="text-sm text-muted-foreground">
            <Message id="tenant.keysForTheCurrentTenantPaginatedByCreation" /></p>
        </div>
        {canCreateApiKey(role) ? (
          <CreateKeyDialog onCreated={keys.reload} />
        ) : (
          <p className="text-sm text-muted-foreground">
            <Message id="tenant.onlyOwnersAndAdministratorsCanCreateKeysMembers" /></p>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Input
          className="max-w-xs"
          placeholder={t("tenant.searchByNameOrKeyDisplayForm")}
          value={draftQuery}
          onChange={(event) => setDraftQuery(event.target.value)}
          aria-label={t("tenant.searchKeysByNameOrDisplayForm")}
        />
        <Select
          value={filters.status === "" ? "all" : filters.status}
          onValueChange={(next) => {
            if (next !== null) {
              setFilters({ status: next === "all" ? "" : next });
            }
          }}
        >
          <SelectTrigger aria-label={t("tenant.filterByStatus")}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all"><Message id="tenant.allStatuses" /></SelectItem>
            <SelectItem value="active"><Message id="tenant.notRevoked" /></SelectItem>
            <SelectItem value="revoked"><Message id="tenant.revoked" /></SelectItem>
          </SelectContent>
        </Select>
        <span className="text-xs text-muted-foreground">
          <Message id="tenant.filtersUseTheStatusStoredInTheControl" /></span>
      </div>

      {keys.error ? (
        <ErrorState message={keys.error} onRetry={keys.reload} />
      ) : keys.loading ? (
        <LoadingState label={t("tenant.loadingApiKeys")} rows={4} />
      ) : (
        <>
          <DataTable
            table={table}
            caption={t("tenant.currentTenantSApiKeys")}
            emptyMessage={
              filters.q === "" && filters.status === ""
                ? t("tenant.thisTenantHasNoApiKeysYet")
                : t("tenant.noKeysMatchTheFilters")
            }
          />
          <Pager resource={keys} loadedCount={rows.length} />
        </>
      )}

      <ConfirmDialog
        open={target !== null}
        onOpenChange={(next) => {
          if (!next) {
            setTarget(null);
          }
        }}
        title={t("tenant.revokeApiKey")}
        destructive
        confirmLabel={t("tenant.revoke")}
        description={
          target ? (
            <span className="grid gap-2">
              <span><Message id="tenant.revokeThisActionIsIrreversibleTheKeyCannot" values={{ strong: <strong>{target.name}</strong>, code: <code className="font-mono">{target.display}</code> }} /></span>
              <span className="text-xs">
                <Message id="tenant.revocationIsWrittenToTheControlPlaneImmediately" /></span>
            </span>
          ) : (
            ""
          )
        }
        onConfirm={async () => {
          if (target) {
            await revoke(target);
          }
        }}
      />
    </div>
  );
}
