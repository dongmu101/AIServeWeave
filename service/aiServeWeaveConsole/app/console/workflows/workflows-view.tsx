"use client";

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
    <div className="grid gap-4">
      <div>
        <h1 className="font-heading text-lg font-semibold">工作流</h1>
        <p className="text-sm text-muted-foreground">
          可以提交的工作流模板及其输入声明。模板是网关的文件配置，对所有租户相同；工作流的
          图不在这里，也不会经由任何接口交出。提交与取消运行走网关数据面，用本租户的 API Key。
        </p>
      </div>

      {menu.error ? (
        <ErrorState message={menu.error} onRetry={menu.reload} />
      ) : menu.loading || !menu.data ? (
        <LoadingState label="正在采集工作流目录" rows={4} />
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
              placeholder="筛选模板或输入名"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              aria-label="筛选已采集的工作流"
            />
            <span className="text-xs text-muted-foreground">
              显示 {shown.length} / 已采集 {templates.length} 个模板。
            </span>
          </div>

          {shown.length === 0 ? (
            <EmptyState
              title={templates.length === 0 ? "没有已注册的工作流模板" : "没有符合筛选条件的模板"}
              description={
                templates.length === 0
                  ? "模板由运维通过网关的 -workflow-templates 注册；没有模板时，工作流接口对任何 id 都返回 404。"
                  : undefined
              }
            />
          ) : (
            <div className="grid gap-3">
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
  return (
    <div className="rounded-xl border">
      <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
        <code className="font-mono text-sm">{template.id}</code>
        {template.valid ? null : (
          <Badge variant="outline" className="text-destructive">
            校验未通过
          </Badge>
        )}
        {template.divergent ? (
          <Badge variant="outline" className="text-destructive">
            {operator ? "副本间不一致" : "可用性不一致"}
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
            <p>产出：{template.outputs.map((output) => `${output.name}（${output.type}）`).join("、")}</p>
          ) : null}
          {template.customNodeDependencies.length > 0 || template.modelDependencies.length > 0 ? (
            <p>
              依赖：
              {[...template.customNodeDependencies, ...template.modelDependencies]
                .map((dep) => (dep.version ? `${dep.name}@${dep.version}` : dep.name))
                .join("、")}
              （模板作者声明，未与节点实际已装能力核对）
            </p>
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
          该模板在部分网关副本上不可用或声明不同，同一次提交可能因落到哪个副本而表现不同。
          这通常意味着一次尚未完成的发布，请联系运维。
        </p>
      ) : null}
      {template.inputs.length === 0 ? (
        <p className="px-3 py-3 text-sm text-muted-foreground">
          该模板没有声明可替换的输入，提交时不接受参数。
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>输入</TableHead>
              <TableHead>类型</TableHead>
              <TableHead>必填</TableHead>
              <TableHead>默认值</TableHead>
              <TableHead>约束</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {template.inputs.map((input) => (
              <TableRow key={input.name}>
                <TableCell className="font-mono text-xs">{input.name}</TableCell>
                <TableCell>{input.type}</TableCell>
                <TableCell>{input.required ? "是" : "否"}</TableCell>
                <TableCell className="font-mono text-xs break-all">
                  {input.defaultValue ?? <span className="text-muted-foreground">无</span>}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {describeBounds(input.maxLength, input.min, input.max)}
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
  max: number | null
): string {
  const parts: string[] = [];
  if (maxLength !== null) {
    parts.push(`最长 ${maxLength} 字节`);
  }
  if (min !== null) {
    parts.push(`≥ ${min}`);
  }
  if (max !== null) {
    parts.push(`≤ ${max}`);
  }
  return parts.length === 0 ? "不限" : parts.join(" · ");
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
    return <LoadingState label="正在采集副本一致性" rows={2} />;
  }

  const divergent = rollout.data.templates.filter((template) => template.divergent);
  return (
    <div className="grid gap-2 rounded-xl border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="font-heading text-sm font-semibold">副本一致性（运维）</h2>
        <Badge variant={divergent.length === 0 ? "secondary" : "outline"}
          className={divergent.length === 0 ? undefined : "text-destructive"}>
          {divergent.length === 0 ? "各副本一致" : `${divergent.length} 个模板不一致`}
        </Badge>
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>模板</TableHead>
            <TableHead>已注册的副本</TableHead>
            <TableHead>一致性</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rollout.data.templates.map((template) => (
            <TableRow key={template.id}>
              <TableCell className="font-mono text-xs">{template.id}</TableCell>
              <TableCell className="text-xs">
                {template.replicas.length === 0 ? "未知" : template.replicas.join("、")}
              </TableCell>
              <TableCell>
                {template.divergent ? (
                  <span className="text-xs text-destructive">
                    副本之间不同：请求会因落在哪个副本而表现不同
                  </span>
                ) : (
                  <span className="text-xs text-muted-foreground">一致</span>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <p className="text-xs text-muted-foreground">
        这里比较的是调用方可观察的部分：描述与输入声明。工作流的图不离开网关，因此「同一个
        id 下图不同」是本视图看不见的一种不一致。
      </p>
    </div>
  );
}
