"use client";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

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
  const { t } = useI18n();
  const rollout = useResource({ method: "GET", surface: "operator", path: "/operator/v1/workflows", parse: parseWorkflowCatalogue });
  const [query, setQuery] = React.useState("");
  const shown = rollout.data?.templates.filter((template) => matchesQuery([template.id, template.description, ...template.replicas], query)) ?? [];
  return <div className="grid gap-4">
    <div>
      <h1 className="font-heading text-lg font-semibold"><Message id="operator.workflowReleaseStatus" /></h1>
      <p className="text-sm text-muted-foreground"><Message id="operator.checkTemplateRegistrationDescriptionsAndInputDeclarationsAcross" /></p>
    </div>
    <div className="flex flex-wrap gap-3">
      <Input className="max-w-xs" value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t("operator.filterTemplatesOrReplicas")} aria-label={t("operator.filterTemplatesOrReplicas")} />
      <Button variant="outline" disabled={rollout.loading} onClick={rollout.reload}><Message id="operator.refreshReleaseStatus" /></Button>
    </div>
    {rollout.error ? <ErrorState message={rollout.error} onRetry={rollout.reload} /> : rollout.loading || !rollout.data ? <LoadingState label={t("operator.collectingReleaseStatus")} /> : <>
      <FleetFreshness collectedAt={rollout.data.collectedAt} replicas={rollout.data.replicas} partial={rollout.data.partial} />
      {shown.length === 0 ? <EmptyState title={rollout.data.templates.length ? t("tenant.noTemplatesMatchTheFilters") : t("operator.noTemplatesFoundInThisCollection")} /> : <Table>
        <TableHeader><TableRow><TableHead><Message id="tenant.template" /></TableHead><TableHead><Message id="operator.registeredReplicas" /></TableHead><TableHead><Message id="operator.versionVisibility" /></TableHead><TableHead><Message id="operator.validation" /></TableHead><TableHead><Message id="operator.declarationConsistency" /></TableHead></TableRow></TableHeader>
        <TableBody>{shown.map((template) => <TableRow key={template.id}>
          <TableCell><code className="text-xs">{template.id}</code><p className="text-xs text-muted-foreground">{template.description}</p></TableCell>
          <TableCell className="text-xs">{template.replicas.join("、") || t("tenant.unknown")}</TableCell>
          <TableCell className="text-xs">{template.version ? `v${template.version}` : t("operator.fileModeUnversioned")}<br />{template.visibleTenantIds.length ? t("operator.visibleToTenants", { count: template.visibleTenantIds.length }) : t("operator.visibleToAllTenants")}</TableCell>
          <TableCell><Badge variant={template.valid ? "outline" : "destructive"}>{template.valid ? t("operator.passed") : t("operator.failed")}</Badge>{template.validationError ? <p className="text-xs text-destructive">{template.validationError}</p> : null}</TableCell>
          <TableCell>{template.divergent ? t("operator.differsAcrossReplicasCallResultsMayVary") : rollout.data?.partial ? t("operator.noDifferencesFoundWithinTheCollectedScope") : t("operator.collectedReplicasAreConsistent")}</TableCell>
        </TableRow>)}</TableBody>
      </Table>}
    </>}
  </div>;
}
