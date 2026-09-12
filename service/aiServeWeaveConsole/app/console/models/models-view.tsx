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
import { parseModelCatalog, type CatalogModel, type ModelCatalog } from "@/lib/console/fleet";
import { matchesQuery } from "@/lib/console/format";

/**
 * The model catalog.
 *
 * The three levels C23 asks to keep apart are visible here as three columns of
 * one row: the logical model id, the backend that serves it, and the
 * deployment — a runtime on a node — that is the actual place it runs. Two
 * deployments of one model on different backends are not interchangeable in
 * capability, which is why the backend belongs to the deployment rather than
 * to the model.
 *
 * What this page must not be read as is the list of names an API caller may
 * use. The Gateway's routing table maps caller-facing aliases onto these ids,
 * and that table is the Gateway's file configuration, which the control plane
 * does not hold — so the Console cannot show it, and says so instead of
 * implying these ids are addressable.
 *
 * 模型目录。
 *
 * C23 要求分开的那三层，在这里体现为同一行里的三列：逻辑模型 id、提供它的后端，以及
 * 部署——某个节点上的某个运行时，也就是它真正运行的地方。同一个模型在不同后端上的两处
 * 部署在能力上并不等价，这正是「后端」属于部署而不属于模型的原因。
 *
 * 本页不能被读成「API 调用方可以使用的名字列表」。Gateway 的路由表把面向调用方的别名
 * 映射到这些 id 上，而那张表是 Gateway 的文件配置，控制面并不持有它——因此 Console 展示
 * 不了它，于是明说这一点，而不是暗示这些 id 可以直接寻址。
 */
const NO_MODELS: CatalogModel[] = [];

export function ModelsView() {
  const catalog = useResource<ModelCatalog>({
    method: "GET",
    surface: "operator",
    path: "/operator/v1/models",
    parse: parseModelCatalog,
  });
  const [query, setQuery] = React.useState("");

  const models = catalog.data?.models ?? NO_MODELS;
  const shown = React.useMemo(
    () =>
      models.filter((model) =>
        matchesQuery(
          [
            model.id,
            ...model.deployments.map((d) => `${d.nodeId} ${d.runtimeId} ${d.backend}`),
          ],
          query
        )
      ),
    [models, query]
  );

  return (
    <div className="grid grid-cols-1 gap-4">
      <div>
        <h1 className="font-heading text-lg font-semibold">模型与部署</h1>
        <p className="text-sm text-muted-foreground">
          运维视图：机群当前实际提供的模型，以及每个模型运行在哪些节点的哪些运行时上。
          这些是后端上报的模型 id，不是调用方可用的名字——别名由 Gateway 的路由表决定，
          那张表还在 Gateway 的文件配置里，控制面没有它。
        </p>
      </div>

      {catalog.error ? (
        <ErrorState message={catalog.error} onRetry={catalog.reload} />
      ) : catalog.loading || !catalog.data ? (
        <LoadingState label="正在采集模型目录" rows={5} />
      ) : (
        <>
          <FleetFreshness
            collectedAt={catalog.data.collectedAt}
            replicas={catalog.data.replicas}
            partial={catalog.data.partial}
          />

          <div className="flex flex-wrap items-center gap-3">
            <Input
              className="max-w-xs"
              placeholder="筛选模型、节点或后端"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              aria-label="筛选已采集的模型"
            />
            <span className="text-xs text-muted-foreground">
              显示 {shown.length} / 已采集 {models.length} 个模型；筛选只作用于本次采集到的
              内容。
            </span>
          </div>

          {shown.length === 0 ? (
            <EmptyState
              title={models.length === 0 ? "机群目前没有提供任何模型" : "没有符合筛选条件的模型"}
              description={
                models.length === 0
                  ? "只有在线节点上、已完成发现的运行时才会报告模型。"
                  : undefined
              }
            />
          ) : (
            <div className="grid grid-cols-1 gap-3">
              {shown.map((model) => (
                <div key={model.id} className="rounded-xl border">
                  <div className="flex flex-wrap items-center gap-2 border-b px-3 py-2">
                    <code className="font-mono text-sm">{model.id}</code>
                    <Badge
                      variant={model.availableDeployments > 0 ? "secondary" : "outline"}
                      className={model.availableDeployments > 0 ? undefined : "text-destructive"}
                    >
                      可用部署 {model.availableDeployments} / {model.deployments.length}
                    </Badge>
                    {model.availableDeployments === 0 ? (
                      <span className="text-xs text-muted-foreground">
                        列出的部署都不在「节点在线且运行时健康」的状态，此刻这个模型服务不了请求。
                      </span>
                    ) : null}
                  </div>
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>节点</TableHead>
                        <TableHead>运行时（部署）</TableHead>
                        <TableHead>后端</TableHead>
                        <TableHead>运行时状态</TableHead>
                        <TableHead>节点在线</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {model.deployments.map((deployment) => (
                        <TableRow key={`${deployment.nodeId}/${deployment.runtimeId}`}>
                          <TableCell className="font-mono text-xs">
                            {deployment.nodeId}
                          </TableCell>
                          <TableCell className="font-mono text-xs">
                            {deployment.runtimeId}
                          </TableCell>
                          <TableCell>{deployment.backend || "未知"}</TableCell>
                          <TableCell>
                            <Badge
                              variant="outline"
                              className={deployment.state === "healthy" ? undefined : "text-destructive"}
                            >
                              {deployment.state}
                            </Badge>
                          </TableCell>
                          <TableCell>{deployment.nodeLive ? "是" : "否"}</TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  );
}
