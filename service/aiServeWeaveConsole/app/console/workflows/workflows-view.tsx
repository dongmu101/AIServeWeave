"use client";
import type { Translator } from "@/lib/i18n/translate";




import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";


import * as React from "react";

import { FleetFreshness } from "@/components/console/fleet-freshness";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  parseWorkflowCatalogue,
  type WorkflowCatalogue,
  type WorkflowTemplate,
} from "@/lib/console/fleet";
import { matchesQuery } from "@/lib/console/format";

/**
 * The workflow menu.
 *
 * A tenant sees what they may submit and what each workflow accepts. What they
 * do not see is the graph: the repository puts a full workflow JSON in the same
 * class as an API key, and it never leaves the Gateway — this page could not
 * show it if it wanted to.
 *
 * An operator sees the same menu plus the rollout. That second read is a
 * separate request to a separate endpoint with a separate guard, so a tenant
 * viewing this page issues one request and an operator issues two. Merging
 * them into one endpoint would mean the tenant response carried operator
 * fields that something had to remember to strip.
 *
 * 工作流菜单。
 *
 * 租户看到自己可以提交什么、每个工作流接受什么。他们看不到的是图：仓库把完整的工作流
 * JSON 与 API key 归为同一类，而它从不离开 Gateway——本页面就算想展示也展示不了。
 *
 * 运维看到同一份菜单，外加发布状态。第二次读取是发往另一个端点、由另一套守卫保护的独立
 * 请求，因此租户浏览本页发出一个请求，运维发出两个。把它们并成一个端点，就意味着租户的
 * 响应里会带着运维字段，而那需要某处记得把它剥掉。
 */
const NO_TEMPLATES: WorkflowTemplate[] = [];

export function WorkflowsView({ operator }: { operator: boolean }) {
  const {t } = useI18n();
  const menu = useResource<WorkflowCatalogue>({
    method: "GET",
    path: "/admin/v1/workflows",
    parse: parseWorkflowCatalogue,
  });
  const [query, setQuery] = React.useState("");

  const templates = menu.data?.templates ?? NO_TEMPLATES;
  const shown = React.useMemo(
    () =>
      templates.filter((template) =>
        matchesQuery(
          [template.id, template.description, ...template.inputs.map((input) => input.name)],
          query
        )
      ),
    [templates, query]
  );

  return (
    <div className="grid grid-cols-1 gap-4">
      <div>
        <h1 className="font-heading text-lg font-semibold"><Message id="tenant.workflows" /></h1>
        <p className="text-sm text-muted-foreground">
          <Message id="tenant.workflowTemplatesAvailableToThisTenantAndTheir" /></p>
      </div>

      {menu.error ? (
        <ErrorState message={menu.error} onRetry={menu.reload} />
      ) : menu.loading || !menu.data ? (
        <LoadingState label={t("tenant.collectingWorkflowCatalog")} rows={4} />
      ) : (
        <>
          <FleetFreshness
            collectedAt={menu.data.collectedAt}
            replicas={menu.data.replicas}
            partial={menu.data.partial}
          />

          <div className="flex flex-wrap items-center gap-3">
            <Input
              className="max-w-xs"
              placeholder={t("tenant.filterByTemplateOrInputName")}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              aria-label={t("tenant.filterCollectedWorkflows")}
            />
            <span className="text-xs text-muted-foreground"><Message id="tenant.templatesShownCollected" values={{ count: shown.length, count2: templates.length }} /></span>
          </div>

          {shown.length === 0 ? (
            <EmptyState
              title={templates.length === 0 ? t("tenant.noWorkflowTemplatesRegistered") : t("tenant.noTemplatesMatchTheFilters")}
              description={
                templates.length === 0
                  ? t("tenant.operatorsRegisterTemplatesThroughTheGatewaySWorkflow")
                  : undefined
              }
            />
          ) : (
            <div className="grid grid-cols-1 gap-3">
              {shown.map((template) => (
                <TemplateCard key={template.id} template={template} operator={operator} />
              ))}
            </div>
          )}

          {operator ? <RolloutSection /> : null}
        </>
      )}
    </div>
  );
}

/** TemplateCard renders one template and its declared inputs.
 *
 * TemplateCard 渲染一个模板及其已声明的输入。 */
function TemplateCard({
  template,
  operator,
}: {
  template: WorkflowTemplate;
  operator: boolean;
}) {
  const {t } = useI18n();
  return (
    <div className="rounded-xl border">
      <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
        <code className="font-mono text-sm">{template.id}</code>
        {template.valid ? null : (
          <Badge variant="outline" className="text-destructive">
            <Message id="tenant.validationFailed" /></Badge>
        )}
        {template.divergent ? (
          <Badge variant="outline" className="text-destructive">
            {operator ? t("tenant.inconsistentAcrossReplicas") : t("tenant.inconsistentAvailability")}
          </Badge>
        ) : null}
        {template.description ? (
          <span className="text-xs text-muted-foreground">{template.description}</span>
        ) : null}
      </div>
      {template.validationError ? (
        <p className="px-3 pt-2 text-xs text-destructive">{template.validationError}</p>
      ) : null}
      {template.outputs.length > 0 || template.customNodeDependencies.length > 0 || template.modelDependencies.length > 0 ? (
        <div className="space-y-1 px-3 pt-2 text-xs text-muted-foreground">
          {template.outputs.length > 0 ? (
            <p><Message id="tenant.outputs_64e199" values={{ value: template.outputs.map((output) => `${output.name}（${output.type}）`).join("、") }} /></p>
          ) : null}
          {template.customNodeDependencies.length > 0 || template.modelDependencies.length > 0 ? (
            <p><Message id="tenant.dependenciesDeclaredByTheTemplateAuthorNotChecked" values={{ value: [...template.customNodeDependencies, ...template.modelDependencies]
                .map((dep) => (dep.version ? `${dep.name}@${dep.version}` : dep.name))
                .join("、") }} /></p>
          ) : null}
        </div>
      ) : null}
      {template.divergent && !operator ? (
        // A tenant is told what they can act on — the request may behave
        // differently — without being told which replicas differ. That is
        // rollout state, and it is on the operator surface. Saying nothing at
        // all would leave them to explain intermittent 404s on their own.
        //
        // 租户被告知的是他们能据以行动的部分——请求可能表现不同——而不被告知是哪些副本
        // 不同。那是发布状态，属于运维面。什么都不说，则会把「时好时坏的 404」留给他们
        // 自己去解释。
        <p className="px-3 pt-2 text-xs text-muted-foreground">
          <Message id="tenant.thisTemplateIsUnavailableOrHasDifferentDeclarations" /></p>
      ) : null}
      {template.inputs.length === 0 ? (
        <p className="px-3 py-3 text-sm text-muted-foreground">
          <Message id="tenant.thisTemplateDeclaresNoReplaceableInputsAndAccepts" /></p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead><Message id="tenant.inputs" /></TableHead>
              <TableHead><Message id="tenant.type" /></TableHead>
              <TableHead><Message id="tenant.required" /></TableHead>
              <TableHead><Message id="tenant.default" /></TableHead>
              <TableHead><Message id="tenant.constraints" /></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {template.inputs.map((input) => (
              <TableRow key={input.name}>
                <TableCell className="font-mono text-xs">{input.name}</TableCell>
                <TableCell>{input.type}</TableCell>
                <TableCell>{input.required ? t("tenant.yes") : t("tenant.no")}</TableCell>
                <TableCell className="font-mono text-xs break-all">
                  {input.defaultValue ?? <span className="text-muted-foreground"><Message id="tenant.none" /></span>}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {describeBounds(input.maxLength, input.min, input.max, t)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

/** describeBounds renders an input's declared limits, saying "unbounded"
 * rather than leaving a blank that could be read as zero.
 *
 * describeBounds 渲染一个输入已声明的边界，写出「不限」而不是留一处可能被读成零的空白。 */
function describeBounds(
  maxLength: number | null,
  min: number | null,
  max: number | null,
  t: Translator
): string {
  const parts: string[] = [];
  if (maxLength !== null) {
    parts.push(t("tenant.maximumLengthBytes", { maxLength: maxLength }));
  }
  if (min !== null) {
    parts.push(`≥ ${min}`);
  }
  if (max !== null) {
    parts.push(`≤ ${max}`);
  }
  return parts.length === 0 ? t("tenant.unlimited") : parts.join(" · ");
}

/**
 * RolloutSection is the operator's second read: which replicas registered each
 * template, and whether they agree.
 *
 * It is what makes a half-finished rollout visible. A template only some
 * replicas hold answers 404 on the others, and a template they describe
 * differently accepts different inputs depending on where a request lands —
 * both are facts a caller experiences as intermittent failure.
 *
 * RolloutSection 是运维的第二次读取：哪些副本注册了每个模板，以及它们是否一致。
 *
 * 正是它让一次没做完的发布变得可见。只有部分副本持有的模板，在其余副本上会回 404；而被
 * 描述得不同的模板，会因请求落在哪里而接受不同的输入——两者对调用方而言，都表现为时好
 * 时坏的失败。
 */
function RolloutSection() {
  const {t } = useI18n();
  const rollout = useResource<WorkflowCatalogue>({
    method: "GET",
    surface: "operator",
    path: "/operator/v1/workflows",
    parse: parseWorkflowCatalogue,
  });

  if (rollout.error) {
    return <ErrorState message={rollout.error} onRetry={rollout.reload} />;
  }
  if (rollout.loading || !rollout.data) {
    return <LoadingState label={t("tenant.collectingReplicaConsistencyData")} rows={2} />;
  }

  const divergent = rollout.data.templates.filter((template) => template.divergent);
  return (
    <div className="grid gap-2 rounded-xl border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="font-heading text-sm font-semibold"><Message id="tenant.replicaConsistencyOperations" /></h2>
        <Badge variant={divergent.length === 0 ? "secondary" : "outline"}
          className={divergent.length === 0 ? undefined : "text-destructive"}>
          {divergent.length === 0 ? t("tenant.consistentAcrossReplicas") : t("tenant.inconsistentTemplates", { count: divergent.length })}
        </Badge>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead><Message id="tenant.template" /></TableHead>
            <TableHead><Message id="tenant.registeredReplicas" /></TableHead>
            <TableHead><Message id="tenant.consistency" /></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rollout.data.templates.map((template) => (
            <TableRow key={template.id}>
              <TableCell className="font-mono text-xs">{template.id}</TableCell>
              <TableCell className="text-xs">
                {template.replicas.length === 0 ? t("tenant.unknown") : template.replicas.join("、")}
              </TableCell>
              <TableCell>
                {template.divergent ? (
                  <span className="text-xs text-destructive">
                    <Message id="tenant.differsAcrossReplicasRequestBehaviorDependsOnWhich" /></span>
                ) : (
                  <span className="text-xs text-muted-foreground"><Message id="tenant.consistent" /></span>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <p className="text-xs text-muted-foreground">
        <Message id="tenant.thisComparesOnlyWhatCallersCanObserveDescriptions" /></p>
    </div>
  );
}
