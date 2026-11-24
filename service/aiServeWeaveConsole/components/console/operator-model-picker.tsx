"use client";

import * as React from "react";

import { SELECT_CLASS } from "@/components/console/account-sales";
import { useResource } from "@/components/console/use-resource";
import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Input } from "@/components/ui/input";
import { parseModelCatalog } from "@/lib/console/fleet";

export function useOperatorModelNames() {
  const catalog = useResource({ method: "GET", surface: "operator", path: "/operator/v1/models", parse: parseModelCatalog });
  const names = React.useMemo(() => catalog.data?.models.map(model => model.id) ?? [], [catalog.data]);
  return { names, loading: catalog.loading };
}

type ModelOptions = { label?: string; names: readonly string[] };

export function OperatorModelPicker({ id, value, onChange, groups, disabled, loading }: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  groups: readonly ModelOptions[];
  disabled?: boolean;
  loading?: boolean;
}) {
  const { t } = useI18n();
  const hasChoices = groups.some(group => group.names.length > 0);
  const hasSelection = groups.some(group => group.names.includes(value));
  return <>
    <select id={id} className={SELECT_CLASS} value={value} disabled={disabled || (!hasChoices && value === "")} onChange={event => onChange(event.target.value)}>
      <option value="">{loading && !hasChoices ? t("operator.loading") : hasChoices ? t("operator.pricingAliasPick") : t("operator.pricingAliasNoChoices")}</option>
      {value && !hasSelection ? <option value={value}>{value}</option> : null}
      {groups.map(group => {
        const options = group.names.map(name => <option key={name} value={name}>{name}</option>);
        return group.label && options.length > 0 ? <optgroup key={group.label} label={group.label}>{options}</optgroup> : options;
      })}
    </select>
    <details className="text-xs text-muted-foreground">
      <summary className="w-fit cursor-pointer"><Message id="operator.modelNameCustom" /></summary>
      <Input aria-label={t("operator.modelNameCustom")} className="mt-1" value={value} disabled={disabled} onChange={event => onChange(event.target.value)} autoComplete="off" spellCheck={false} />
    </details>
  </>;
}
