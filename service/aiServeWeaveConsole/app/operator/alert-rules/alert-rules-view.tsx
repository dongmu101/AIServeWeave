"use client";
import { MessageError } from "@/lib/i18n/problem";

import { ErrorMessage } from "@/components/i18n/error-message";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";
import type { Translator } from "@/lib/i18n/translate";

import * as React from "react";
import {
  createColumnHelper,
  tableFeatures,
  useTable,
} from "@tanstack/react-table";
import { toast } from "sonner";

import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { DataTable } from "@/components/console/data-table";
import { Pager } from "@/components/console/pager";
import { ErrorState, FormError, LoadingState } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { usePagedResource } from "@/components/console/use-paged-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { parseAlertRule, parseAlertRules, parseNoContent, type AlertRule } from "@/lib/console/contract";
import {ApiError } from "@/lib/console/errors";
import { formatDateTime } from "@/lib/console/format";

const PATH = "/operator/v1/alert-rules";
const PAGE_SIZE = 50;

/**
 * METRIC_LABELS maps model.AlertRule's five metric identifiers onto the
 * Chinese labels shown in the list and the create/edit select — the wire
 * value is what the control plane stores and matches against, the label is
 * only ever a rendering.
 *
 * METRIC_LABELS 把 model.AlertRule 的五个指标标识映射到列表与创建/编辑下拉框
 * 展示的中文标签——线上值是控制面存储与匹配所用的值，标签只是一种呈现。
 */
const METRIC_LABELS = (t: Translator): Record<string, string> => ({
  request_rate: t("operator.requestVolume"),
  success_rate: t("operator.successRate"),
  latency_p95: t("operator.p95Latency"),
  token_usage: t("operator.tokenUsage_1493f4"),
  capacity: t("operator.capacity"),
});
const METRIC_OPTIONS = ["request_rate", "success_rate", "latency_p95", "token_usage", "capacity"] as const;

/**
 * OPERATOR_SYMBOLS maps model.AlertRule's four comparison operators onto the
 * symbol used both in the select and in the merged "operator threshold"
 * phrase (e.g. "< 0.95") the list column renders.
 *
 * OPERATOR_SYMBOLS 把 model.AlertRule 的四种比较运算符映射到符号，供下拉框与
 * 列表列渲染的合并短语（如 "< 0.95"）共用。
 */
const OPERATOR_SYMBOLS: Record<string, string> = {
  lt: "<",
  lte: "≤",
  gt: ">",
  gte: "≥",
};
const OPERATOR_OPTIONS = Object.keys(OPERATOR_SYMBOLS) as (keyof typeof OPERATOR_SYMBOLS)[];

const DEFAULT_CONSECUTIVE_BUCKETS = 3;

/** metricLabel renders a metric's Chinese label, falling back to the raw wire
 * value for anything this Console does not yet recognize — an unknown value
 * is a fact worth showing, not something to hide behind a placeholder.
 *
 * metricLabel 渲染一个指标的中文标签；遇到本 Console 尚不认识的值，回退展示线上原始
 * 值——一个未知值是值得展示出来的事实，不该被占位符藏起来。 */
function metricLabel(metric: string, t: Translator): string {
  return METRIC_LABELS(t)[metric] ?? metric;
}

/** thresholdPhrase merges operator and threshold into one human phrase, e.g.
 * "< 0.95". An unrecognized operator falls back to its raw wire value for the
 * same reason metricLabel does.
 *
 * thresholdPhrase 把 operator 与 threshold 合并成一个人类可读短语，例如
 * "< 0.95"。无法识别的 operator 回退展示其线上原始值，理由与 metricLabel 相同。 */
function thresholdPhrase(rule: AlertRule): string {
  return `${OPERATOR_SYMBOLS[rule.operator] ?? rule.operator} ${rule.threshold}`;
}

/**
 * validateWebhookUrl mirrors alertengine.validateWebhookURL's shape on the
 * client — a plain http(s) URL with no embedded credentials. It is a UX
 * courtesy only, not a security boundary: submitting bypasses nothing on the
 * control plane, which re-applies the same check server-side and is what
 * actually enforces it.
 *
 * validateWebhookUrl 在客户端镜像 alertengine.validateWebhookURL 的检查
 * 形状——纯 http(s)、且不带内嵌凭据的 URL。这只是体验上的便利，不是安全边界：
 * 提交并不会绕过控制面上的任何东西，真正生效的是控制面重新执行的同一项检查。
 */
function validateWebhookUrl(raw: string): MessageError | null {
  if (raw.trim() === "") {
    return null;
  }
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    return new MessageError("operator.webhookUrlIsNotAValidUrl");
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return new MessageError("operator.webhookUrlMustUseHttpOrHttps");
  }
  if (parsed.username !== "" || parsed.password !== "") {
    return new MessageError("operator.webhookUrlCannotContainEmbeddedCredentials");
  }
  return null;
}

/** FormState is the create/edit dialog's own draft, kept as strings for the
 * numeric fields so an in-progress edit (an empty box, a lone "-") is not
 * forced through Number() on every keystroke.
 *
 * FormState 是创建/编辑对话框自己的草稿；数值字段保持为字符串，这样一次尚未完成的
 * 编辑（一个空框、一个孤立的 "-"）不会在每次按键时都被 Number() 硬转换。 */
interface FormState {
  name: string;
  metric: string;
  operator: string;
  threshold: string;
  consecutiveBuckets: string;
  webhookUrl: string;
  enabled: boolean;
}

const BLANK_FORM: FormState = {
  name: "",
  metric: METRIC_OPTIONS[0],
  operator: OPERATOR_OPTIONS[0],
  threshold: "",
  consecutiveBuckets: String(DEFAULT_CONSECUTIVE_BUCKETS),
  webhookUrl: "",
  enabled: true,
};

/** formFor seeds a draft from an existing rule for editing, or the blank
 * defaults for creation.
 *
 * formFor 为编辑场景从既有规则种出一份草稿，创建场景则取空白默认值。 */
function formFor(rule: AlertRule | null): FormState {
  if (rule === null) {
    return BLANK_FORM;
  }
  return {
    name: rule.name,
    metric: rule.metric,
    operator: rule.operator,
    threshold: String(rule.threshold),
    consecutiveBuckets: String(rule.consecutiveBuckets),
    webhookUrl: rule.webhookUrl,
    enabled: rule.enabled,
  };
}

const features = tableFeatures({});
const helper = createColumnHelper<typeof features, AlertRule>();

const NO_RULES: AlertRule[] = [];

/**
 * AlertRulesView is the operator page for managing alert rules
 * (STATUS.md's P09/C29): list, create, edit, enable/disable and delete.
 *
 * This is the first alert-rules page with a write path, so it follows the
 * same rules every other Console write already does: writes go through
 * `useConsoleRequest` (same-origin checked server-side, never auto-retried),
 * a destructive action confirms first via `ConfirmDialog`, and a write's
 * result is never assumed — the list is re-read from the server afterward
 * rather than patched in place from what the write returned.
 *
 * The edit form submits `PATCH` as a full replacement, not a partial merge:
 * the control plane's `AlertRuleRequest` is shared by `POST` and `PATCH` and
 * documents itself as full-replace (see store.AlertRuleUpdate), so the form
 * always sends every field rather than only the ones a person touched.
 *
 * AlertRulesView 是管理告警规则的运维页面（STATUS.md 的 P09/C29）：列表、创建、
 * 编辑、启用/禁用与删除。
 *
 * 这是第一个带写操作的告警规则页面，因此它遵循 Console 其余写操作已经在遵循的规则：
 * 写请求一律经过 `useConsoleRequest`（同源校验在服务端完成，从不自动重试）；破坏性
 * 操作先经 `ConfirmDialog` 确认；写操作的结果从不被假定——写完之后重新从服务端读取
 * 列表，而不是拿写请求返回的那一行去打补丁。
 *
 * 编辑表单以整体替换的方式提交 `PATCH`，而非局部合并：控制面的 `AlertRuleRequest`
 * 由 `POST` 与 `PATCH` 共用，且自我文档为整体替换（见 store.AlertRuleUpdate），因此
 * 表单总是发送全部字段，而不只是当事人改动过的那些。
 */
export function AlertRulesView() {
  const { locale, t } = useI18n();
  const run = useConsoleRequest();
  const [formOpen, setFormOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<AlertRule | null>(null);
  const [deleteTarget, setDeleteTarget] = React.useState<AlertRule | null>(null);
  const [toggleError, setToggleError] = React.useState<React.ReactNode>(null);

  const rules = usePagedResource<AlertRule>({
    path: PATH,
    surface: "operator",
    filters: {},
    pageSize: PAGE_SIZE,
    parse: parseAlertRules,
  });

  const rows = rules.items ?? NO_RULES;

  function openCreate() {
    setEditing(null);
    setToggleError(null);
    setFormOpen(true);
  }

  function openEdit(rule: AlertRule) {
    setEditing(rule);
    setToggleError(null);
    setFormOpen(true);
  }

  const toggleEnabled = React.useCallback(
    async (rule: AlertRule) => {
      setToggleError(null);
      try {
        await run({
          method: "PATCH",
          surface: "operator",
          path: `${PATH}/${encodeURIComponent(rule.id)}`,
          body: {
            name: rule.name,
            metric: rule.metric,
            operator: rule.operator,
            threshold: rule.threshold,
            consecutive_buckets: rule.consecutiveBuckets,
            webhook_url: rule.webhookUrl,
            enabled: !rule.enabled,
          },
          parse: parseAlertRule,
        });
      } catch (failure) {
        setToggleError(<ErrorMessage error={failure} />);
        return;
      }
      toast.success(rule.enabled ? <Message id={"operator.disabled_ae3f74"} values={{ name: rule.name }} /> : <Message id={"operator.enabled_27282f"} values={{ name: rule.name }} />);
      rules.reload();
    },
    [run, rules]
  );

  async function remove(rule: AlertRule) {
    try {
      await run({
        method: "DELETE",
        surface: "operator",
        path: `${PATH}/${encodeURIComponent(rule.id)}`,
        parse: parseNoContent,
      });
    } catch (failure) {
      if (failure instanceof ApiError && failure.kind === "not_found") {
        rules.reload();
        throw new MessageError("operator.thisRuleNoLongerExistsSomeoneElseMay");
      }
      throw failure;
    }
    toast.success(<Message id={"operator.deleted_5bad4a"} values={{ name: rule.name }} />);
    rules.reload();
  }

  const columns = React.useMemo(
    () =>
      helper.columns([
        helper.accessor("id", {
          header: "ID",
          cell: (info) => (
            <span className="font-mono text-xs">{info.getValue()}</span>
          ),
        }),
        helper.accessor("name", { header: t("tenant.name") }),
        helper.accessor("metric", {
          header: t("operator.metrics"),
          cell: (info) => metricLabel(info.getValue(), t),
        }),
        helper.display({
          id: "threshold",
          header: t("operator.threshold"),
          cell: (info) => thresholdPhrase(info.row.original),
        }),
        helper.accessor("consecutiveBuckets", { header: t("operator.consecutiveWindows") }),
        helper.accessor("enabled", {
          header: t("tenant.enable"),
          cell: (info) => {
            const rule = info.row.original;
            return (
              <Button
                variant={rule.enabled ? "secondary" : "outline"}
                size="sm"
                onClick={() => toggleEnabled(rule)}
              >
                {rule.enabled ? t("operator.enabled") : t("operator.disabled_6c7dcb")}
              </Button>
            );
          },
        }),
        helper.accessor("webhookUrl", {
          header: "Webhook",
          cell: (info) => (
            <Badge variant={info.getValue() !== "" ? "secondary" : "outline"}>
              {info.getValue() !== "" ? t("tenant.configured") : t("tenant.notConfigured")}
            </Badge>
          ),
        }),
        helper.accessor("createdAt", {
          header: t("tenant.createdAt"),
          cell: (info) => formatDateTime(info.getValue(), undefined, locale),
        }),
        helper.accessor("updatedAt", {
          header: t("operator.updatedAt"),
          cell: (info) => formatDateTime(info.getValue(), undefined, locale),
        }),
        helper.display({
          id: "actions",
          header: t("tenant.actions"),
          cell: (info) => {
            const rule = info.row.original;
            return (
              <div className="flex gap-2">
                <Button variant="outline" size="sm" onClick={() => openEdit(rule)}>
                  <Message id="common.edit" /></Button>
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => setDeleteTarget(rule)}
                >
                  <Message id="common.delete" /></Button>
              </div>
            );
          },
        }),
      ]),
    [toggleEnabled, t, locale]
  );

  const table = useTable({ features, columns, data: rows });

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="font-heading text-lg font-semibold"><Message id="operator.alertRules" /></h1>
          <p className="text-sm text-muted-foreground">
            <Message id="operator.thresholdRulesBasedOnMetricsDerivedFromMetrics" /></p>
        </div>
        <Button onClick={openCreate}><Message id="operator.createRule" /></Button>
      </div>

      <FormError message={toggleError} />

      {rules.error ? (
        <ErrorState message={rules.error} onRetry={rules.reload} />
      ) : rules.loading ? (
        <LoadingState label={t("operator.loadingAlertRules")} rows={4} />
      ) : (
        <>
          <DataTable
            table={table}
            caption={t("operator.allAlertRules")}
            emptyMessage={t("operator.noAlertRulesYet")}
          />
          <Pager resource={rules} loadedCount={rows.length} />
        </>
      )}

      <AlertRuleFormDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        editing={editing}
        run={run}
        onSaved={rules.reload}
      />

      <ConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={(next) => {
          if (!next) {
            setDeleteTarget(null);
          }
        }}
        title={t("operator.deleteAlertRule")}
        destructive
        confirmLabel={t("common.delete")}
        description={
          deleteTarget ? (
            <span><Message id="operator.deleteRuleThisActionIsIrreversibleOpenAlert" values={{ strong: <strong>{deleteTarget.name}</strong> }} /></span>
          ) : (
            ""
          )
        }
        onConfirm={async () => {
          if (deleteTarget) {
            await remove(deleteTarget);
          }
        }}
      />
    </div>
  );
}

/**
 * AlertRuleFormDialog is the create/edit form. `editing` selects the mode: a
 * rule pre-fills and submits `PATCH`, `null` starts from blank defaults and
 * submits `POST`. It is one component rather than two because the two modes
 * share every field, every validation rule and the same submit-once
 * discipline `SubmitButton` gives every other Console form.
 *
 * AlertRuleFormDialog 是创建/编辑共用的表单。`editing` 决定模式：传入一条规则则
 * 预填并提交 `PATCH`，传入 `null` 则从空白默认值开始并提交 `POST`。两种模式合成一个
 * 组件而非两个，是因为它们共用每一个字段、每一条校验规则，以及 `SubmitButton` 赋予
 * 其他每个 Console 表单的「只提交一次」纪律。
 */
function AlertRuleFormDialog({
  open,
  onOpenChange,
  editing,
  run,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  editing: AlertRule | null;
  run: ReturnType<typeof useConsoleRequest>;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [form, setForm] = React.useState<FormState>(() => formFor(editing));
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  // Re-seed the draft whenever a fresh dialog opens for a (possibly
  // different) target, rather than in an effect — the first paint of a
  // reopened dialog must already show the right record, not one render
  // later, or a person editing rule B briefly sees rule A's values.
  //
  // 每次为一个（可能不同的）目标重新打开对话框时都重新种入草稿，而不是放进 effect
  // ——一个被重新打开的对话框，其首次绘制就必须展示正确的记录，而不是晚一次渲染，
  // 否则正在编辑规则 B 的人会短暂看到规则 A 的值。
  const [seededFor, setSeededFor] = React.useState<{ open: boolean; id: string | null }>({
    open,
    id: editing?.id ?? null,
  });
  const key = { open, id: editing?.id ?? null };
  if (key.open !== seededFor.open || key.id !== seededFor.id) {
    setSeededFor(key);
    if (key.open) {
      setForm(formFor(editing));
      setError(null);
    }
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }
    const name = form.name.trim();
    if (name === "") {
      setError(<Message id={"operator.enterARuleName"} />);
      return;
    }
    const threshold = Number(form.threshold);
    if (form.threshold.trim() === "" || !Number.isFinite(threshold)) {
      setError(<Message id={"operator.enterAValidThreshold"} />);
      return;
    }
    const consecutiveBuckets = Number(form.consecutiveBuckets);
    if (
      !Number.isInteger(consecutiveBuckets) ||
      consecutiveBuckets < 1
    ) {
      setError(<Message id={"operator.consecutiveWindowsMustBeAnIntegerOfAt"} />);
      return;
    }
    const webhookUrl = form.webhookUrl.trim();
    const webhookError = validateWebhookUrl(webhookUrl);
    if (webhookError !== null) {
      setError(<ErrorMessage error={webhookError} />);
      return;
    }

    const body = {
      name,
      metric: form.metric,
      operator: form.operator,
      threshold,
      consecutive_buckets: consecutiveBuckets,
      webhook_url: webhookUrl,
      enabled: form.enabled,
    };

    setPending(true);
    setError(null);
    try {
      if (editing === null) {
        await run({
          method: "POST",
          surface: "operator",
          path: PATH,
          body,
          parse: parseAlertRule,
        });
        toast.success(<Message id={"operator.createdRule"} values={{ name: name }} />);
      } else {
        await run({
          method: "PATCH",
          surface: "operator",
          path: `${PATH}/${encodeURIComponent(editing.id)}`,
          body,
          parse: parseAlertRule,
        });
        toast.success(<Message id={"operator.updatedRule"} values={{ name: name }} />);
      }
      onOpenChange(false);
      onSaved();
    } catch (failure) {
      setError(<ErrorMessage error={failure} />);
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (pending) {
          return;
        }
        onOpenChange(next);
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <DialogHeader>
            <DialogTitle>{editing === null ? t("operator.createAlertRule") : t("operator.edit_feaeda", { name: editing.name })}</DialogTitle>
            <DialogDescription>
              <Message id="operator.aRuleTriggersOneAlertInstanceWhenThe" /></DialogDescription>
          </DialogHeader>

          <div className="grid gap-2">
            <label htmlFor="rule-name" className="text-sm font-medium">
              <Message id="tenant.name" /></label>
            <Input
              id="rule-name"
              required
              value={form.name}
              onChange={(event) => setForm({ ...form, name: event.target.value })}
              disabled={pending}
              placeholder={t("operator.eGGatewaySuccessRateTooLow")}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <label htmlFor="rule-metric" className="text-sm font-medium">
                <Message id="operator.metrics" /></label>
              <Select
                value={form.metric}
                onValueChange={(next) => {
                  if (next !== null) {
                    setForm({ ...form, metric: next });
                  }
                }}
                disabled={pending}
              >
                <SelectTrigger id="rule-metric" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {METRIC_OPTIONS.map((metric) => (
                    <SelectItem key={metric} value={metric}>
                      {METRIC_LABELS(t)[metric]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="grid gap-2">
              <label htmlFor="rule-operator" className="text-sm font-medium">
                <Message id="operator.comparison" /></label>
              <Select
                value={form.operator}
                onValueChange={(next) => {
                  if (next !== null) {
                    setForm({ ...form, operator: next });
                  }
                }}
                disabled={pending}
              >
                <SelectTrigger id="rule-operator" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {OPERATOR_OPTIONS.map((operator) => (
                    <SelectItem key={operator} value={operator}>
                      {OPERATOR_SYMBOLS[operator]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="grid gap-2">
              <label htmlFor="rule-threshold" className="text-sm font-medium">
                <Message id="operator.threshold" /></label>
              <Input
                id="rule-threshold"
                type="number"
                step="any"
                required
                value={form.threshold}
                onChange={(event) => setForm({ ...form, threshold: event.target.value })}
                disabled={pending}
              />
            </div>

            <div className="grid gap-2">
              <label htmlFor="rule-buckets" className="text-sm font-medium">
                <Message id="operator.consecutiveWindows" /></label>
              <Input
                id="rule-buckets"
                type="number"
                step="1"
                min="1"
                required
                value={form.consecutiveBuckets}
                onChange={(event) =>
                  setForm({ ...form, consecutiveBuckets: event.target.value })
                }
                disabled={pending}
              />
            </div>
          </div>

          <div className="grid gap-2">
            <label htmlFor="rule-webhook" className="text-sm font-medium">
              <Message id="operator.webhookUrlOptional" /></label>
            <Input
              id="rule-webhook"
              type="text"
              value={form.webhookUrl}
              onChange={(event) => setForm({ ...form, webhookUrl: event.target.value })}
              disabled={pending}
              placeholder="https://example.com/hooks/alert"
            />
            <p className="text-xs text-muted-foreground">
              <Message id="operator.mustUseHttpOrHttpsAndContainNo" /></p>
          </div>

          <label className="flex items-center gap-2 text-sm font-medium"><Message id="operator.enable" values={{ content: <input
              type="checkbox"
              checked={form.enabled}
              onChange={(event) => setForm({ ...form, enabled: event.target.checked })}
              disabled={pending}
            /> }} /></label>

          <FormError message={error} />

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={pending}
              onClick={() => onOpenChange(false)}
            >
              <Message id="common.cancel" /></Button>
            <SubmitButton pending={pending}>
              {editing === null ? t("common.create") : t("common.save")}
            </SubmitButton>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
