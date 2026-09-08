"use client";

import * as React from "react";
import { FleetFreshness } from "@/components/console/fleet-freshness";
import { EmptyState, ErrorState, LoadingState } from "@/components/console/states";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { parseWorkflowCatalogue } from "@/lib/console/fleet";
import { matchesQuery } from "@/lib/console/format";

/** PublicationView reads only operator publication data and preserves partial status.
 * PublicationView 仅读取运维发布数据，并保留部分采集状态。 */
export function PublicationView() {
  const rollout = useResource({ method: "GET", surface: "operator", path: "/operator/v1/workflows", parse: parseWorkflowCatalogue });
  const [query, setQuery] = React.useState("");
  const shown = rollout.data?.templates.filter((template) => matchesQuery([template.id, template.description, ...template.replicas], query)) ?? [];
  return <div className="grid gap-4">
    <div>
      <h1 className="font-heading text-lg font-semibold">工作流发布状态</h1>
      <p className="text-sm text-muted-foreground">按副本核对模板注册、描述与输入声明。工作流图不离开 Gateway，因此这里无法确认图内容相同；部分采集成功也不代表整个机群一致。</p>
    </div>
    <div className="flex flex-wrap gap-3">
      <Input className="max-w-xs" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="筛选模板或副本" aria-label="筛选模板或副本" />
      <Button variant="outline" disabled={rollout.loading} onClick={rollout.reload}>刷新发布状态</Button>
    </div>
    {rollout.error ? <ErrorState message={rollout.error} onRetry={rollout.reload} /> : rollout.loading || !rollout.data ? <LoadingState label="正在采集发布状态" /> : <>
      <FleetFreshness collectedAt={rollout.data.collectedAt} replicas={rollout.data.replicas} partial={rollout.data.partial} />
      {shown.length === 0 ? <EmptyState title={rollout.data.templates.length ? "没有符合筛选条件的模板" : "本次采集未发现模板"} /> : <Table>
        <TableHeader><TableRow><TableHead>模板</TableHead><TableHead>已注册副本</TableHead><TableHead>校验</TableHead><TableHead>声明一致性</TableHead></TableRow></TableHeader>
        <TableBody>{shown.map((template) => <TableRow key={template.id}>
          <TableCell><code className="text-xs">{template.id}</code><p className="text-xs text-muted-foreground">{template.description}</p></TableCell>
          <TableCell className="text-xs">{template.replicas.join("、") || "未知"}</TableCell>
          <TableCell><Badge variant={template.valid ? "outline" : "destructive"}>{template.valid ? "通过" : "未通过"}</Badge>{template.validationError ? <p className="text-xs text-destructive">{template.validationError}</p> : null}</TableCell>
          <TableCell>{template.divergent ? "副本之间不同，调用结果可能不同" : rollout.data?.partial ? "已采集范围内未发现差异" : "已采集副本一致"}</TableCell>
        </TableRow>)}</TableBody>
      </Table>}
    </>}
  </div>;
}
