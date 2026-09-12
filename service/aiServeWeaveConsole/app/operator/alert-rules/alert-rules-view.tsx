"use client";

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
import { ApiError, describe } from "@/lib/console/errors";
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
const METRIC_LABELS: Record<string, string> = {
  request_rate: "请求量",
  success_rate: "成功率",
  latency_p95: "延迟 P95",
  token_usage: "token 用量",
  capacity: "容量",
};
const METRIC_OPTIONS = Object.keys(METRIC_LABELS) as (keyof typeof METRIC_LABELS)[];

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
function metricLabel(metric: string): string {
  return METRIC_LABELS[metric] ?? metric;
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
function validateWebhookUrl(raw: string): string | null {
  if (raw.trim() === "") {
    return null;
  }
  let parsed: URL;
  try {
    parsed = new URL(raw);
  } catch {
    return "Webhook URL 不是合法的 URL。";
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return "Webhook URL 必须使用 http 或 https。";
  }
  if (parsed.username !== "" || parsed.password !== "") {
    return "Webhook URL 不能包含内嵌凭据。";
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
  const run = useConsoleRequest();
  const [formOpen, setFormOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<AlertRule | null>(null);
  const [deleteTarget, setDeleteTarget] = React.useState<AlertRule | null>(null);
  const [toggleError, setToggleError] = React.useState<string | null>(null);

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
        setToggleError(describe(failure));
        return;
      }
      toast.success(rule.enabled ? `已停用 ${rule.name}` : `已启用 ${rule.name}`);
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
        throw new Error("该规则已不存在，可能已被他人删除。列表已刷新。");
      }
      throw failure;
    }
    toast.success(`已删除 ${rule.name}`);
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
        helper.accessor("name", { header: "名称" }),
        helper.accessor("metric", {
          header: "指标",
          cell: (info) => metricLabel(info.getValue()),
        }),
        helper.display({
          id: "threshold",
          header: "阈值",
          cell: (info) => thresholdPhrase(info.row.original),
        }),
        helper.accessor("consecutiveBuckets", { header: "连续窗口数" }),
        helper.accessor("enabled", {
          header: "启用",
          cell: (info) => {
            const rule = info.row.original;
            return (
              <Button
                variant={rule.enabled ? "secondary" : "outline"}
                size="sm"
                onClick={() => toggleEnabled(rule)}
              >
                {rule.enabled ? "已启用" : "已停用"}
              </Button>
            );
          },
        }),
        helper.accessor("webhookUrl", {
          header: "Webhook",
          cell: (info) => (
            <Badge variant={info.getValue() !== "" ? "secondary" : "outline"}>
              {info.getValue() !== "" ? "已配置" : "未配置"}
            </Badge>
          ),
        }),
        helper.accessor("createdAt", {
          header: "创建时间",
          cell: (info) => formatDateTime(info.getValue()),
        }),
        helper.accessor("updatedAt", {
          header: "更新时间",
          cell: (info) => formatDateTime(info.getValue()),
        }),
        helper.display({
          id: "actions",
          header: "操作",
          cell: (info) => {
            const rule = info.row.original;
            return (
              <div className="flex gap-2">
                <Button variant="outline" size="sm" onClick={() => openEdit(rule)}>
                  编辑
                </Button>
                <Button
                  variant="destructive"
                  size="sm"
                  onClick={() => setDeleteTarget(rule)}
                >
                  删除
                </Button>
              </div>
            );
          },
        }),
      ]),
    [toggleEnabled]
  );

  const table = useTable({ features, columns, data: rows });

  return (
    <div className="grid gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="font-heading text-lg font-semibold">告警规则</h1>
          <p className="text-sm text-muted-foreground">
            基于 metrics_history 派生指标的阈值规则；每条规则在连续多个窗口越过阈值时
            触发一次告警实例。停用的规则不参与评估，也不会重新触发已打开的实例。
          </p>
        </div>
        <Button onClick={openCreate}>创建规则</Button>
      </div>

      <FormError message={toggleError} />

      {rules.error ? (
        <ErrorState message={rules.error} onRetry={rules.reload} />
      ) : rules.loading ? (
        <LoadingState label="正在加载告警规则" rows={4} />
      ) : (
        <>
          <DataTable
            table={table}
            caption="全部告警规则"
            emptyMessage="还没有告警规则"
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
        title="删除告警规则"
        destructive
        confirmLabel="删除"
        description={
          deleteTarget ? (
            <span>
              将删除规则 <strong>{deleteTarget.name}</strong>。此操作不可撤销；该规则
              已打开的告警实例不会被一并删除。
            </span>
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
  const [form, setForm] = React.useState<FormState>(() => formFor(editing));
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

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
      setError("请填写规则名称。");
      return;
    }
    const threshold = Number(form.threshold);
    if (form.threshold.trim() === "" || !Number.isFinite(threshold)) {
      setError("请填写有效的阈值。");
      return;
    }
    const consecutiveBuckets = Number(form.consecutiveBuckets);
    if (
      !Number.isInteger(consecutiveBuckets) ||
      consecutiveBuckets < 1
    ) {
      setError("连续窗口数必须是不小于 1 的整数。");
      return;
    }
    const webhookUrl = form.webhookUrl.trim();
    const webhookError = validateWebhookUrl(webhookUrl);
    if (webhookError !== null) {
      setError(webhookError);
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
        toast.success(`已创建规则 ${name}`);
      } else {
        await run({
          method: "PATCH",
          surface: "operator",
          path: `${PATH}/${encodeURIComponent(editing.id)}`,
          body,
          parse: parseAlertRule,
        });
        toast.success(`已更新规则 ${name}`);
      }
      onOpenChange(false);
      onSaved();
    } catch (failure) {
      setError(describe(failure));
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
            <DialogTitle>{editing === null ? "创建告警规则" : `编辑 ${editing.name}`}</DialogTitle>
            <DialogDescription>
              规则在连续 N 个 5 分钟窗口内越过阈值时触发一次告警实例；停用的规则不参与
              评估。
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-2">
            <label htmlFor="rule-name" className="text-sm font-medium">
              名称
            </label>
            <Input
              id="rule-name"
              required
              value={form.name}
              onChange={(event) => setForm({ ...form, name: event.target.value })}
              disabled={pending}
              placeholder="例如 网关成功率过低"
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <label htmlFor="rule-metric" className="text-sm font-medium">
                指标
              </label>
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
                      {METRIC_LABELS[metric]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="grid gap-2">
              <label htmlFor="rule-operator" className="text-sm font-medium">
                比较方式
              </label>
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
                阈值
              </label>
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
                连续窗口数
              </label>
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
              Webhook URL（可选）
            </label>
            <Input
              id="rule-webhook"
              type="text"
              value={form.webhookUrl}
              onChange={(event) => setForm({ ...form, webhookUrl: event.target.value })}
              disabled={pending}
              placeholder="https://example.com/hooks/alert"
            />
            <p className="text-xs text-muted-foreground">
              必须是 http 或 https 且不含内嵌凭据；留空表示该规则不发送 Webhook 通知。
              这里的校验只是镜像控制面的检查，实际生效以控制面重新校验为准。
            </p>
          </div>

          <label className="flex items-center gap-2 text-sm font-medium">
            <input
              type="checkbox"
              checked={form.enabled}
              onChange={(event) => setForm({ ...form, enabled: event.target.checked })}
              disabled={pending}
            />
            启用
          </label>

          <FormError message={error} />

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={pending}
              onClick={() => onOpenChange(false)}
            >
              取消
            </Button>
            <SubmitButton pending={pending}>
              {editing === null ? "创建" : "保存"}
            </SubmitButton>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
