import { ApiError } from "./errors.ts";
import { MAX_PRICE_MICRO_PER_MTOK } from "./billing.ts";

export interface AccountSale {
  id: string; nodeId: string; runtimeId: string; name: string;
  priceMicro: number; active: boolean; owned: boolean; expiresAt: string | null; tenantId: string;
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new ApiError("contract");
  return value as Record<string, unknown>;
}
function text(value: unknown, max: number): string {
  if (typeof value !== "string" || !value || new TextEncoder().encode(value).length > max) throw new ApiError("contract");
  return value;
}
function price(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value <= 0 || value > MAX_PRICE_MICRO_PER_MTOK) throw new ApiError("contract");
  return value;
}
function timestamp(value: unknown): string {
  const raw = text(value, 40);
  if (!Number.isFinite(Date.parse(raw))) throw new ApiError("contract");
  return raw;
}
export function parseAccountSale(value: unknown): AccountSale {
  const row = object(value);
  if (typeof row.active !== "boolean" || typeof row.owned !== "boolean") throw new ApiError("contract");
  const result = {
    id: text(row.id, 32), nodeId: text(row.node_id, 128), runtimeId: text(row.runtime_id, 128),
    name: text(row.name, 512), priceMicro: price(row.price_micro), active: row.active, owned: row.owned,
    expiresAt: row.expires_at === undefined ? null : timestamp(row.expires_at),
    tenantId: row.tenant_id === undefined ? "" : text(row.tenant_id, 32),
  };
  if (result.owned && result.expiresAt === null) throw new ApiError("contract");
  return result;
}
export function parseAccountSales(value: unknown): AccountSale[] {
  const source = object(value);
  if (!Array.isArray(source.items) || source.items.length > 1000) throw new ApiError("contract");
  const rows = source.items.map(parseAccountSale);
  if (new Set(rows.map(row => row.id)).size !== rows.length || new Set(rows.map(row => JSON.stringify([row.nodeId, row.runtimeId]))).size !== rows.length) throw new ApiError("contract");
  return rows;
}
export function parseAccountPurchase(value: unknown) {
  const row = object(value);
  return { id: text(row.id, 32), accountId: text(row.account_id, 32), priceMicro: price(row.price_micro), expiresAt: timestamp(row.expires_at) };
}
