"use client";
import { ErrorMessage } from "@/components/i18n/error-message";

import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";

import * as React from "react";
import { PlusIcon } from "lucide-react";
import { toast } from "sonner";
import { cn } from "cn";

import { ApiDocArticle, MethodBadge } from "@/components/console/api-doc-article";
import { ConfirmDialog } from "@/components/console/confirm-dialog";
import { EmptyState, ErrorState, FormError, LoadingState } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  blankApiDocInput,
  groupApiDocs,
  isValidApiDocSlug,
  parseApiDocInput,
  parseOperatorApiDocDetail,
  parseOperatorApiDocList,
  toApiDocInput,
  type ApiDocInput,
  type OperatorApiDocDetail,
  type OperatorApiDocSummary,
} from "@/lib/console/api-docs";

import { formatDateTime } from "@/lib/console/format";
import { parseNoContent } from "@/lib/console/contract";

const BASE = "/operator/v1/api-docs";

/** Editing is either an existing page (with the version every write echoes)
 * or a new one being created. / 编辑对象要么是已有页面（附带每次写入都要回传的版本），
 * 要么是正在新建的页面。 */
type Editing = { kind: "existing"; doc: OperatorApiDocDetail } | { kind: "new" };

/**
 * ApiDocsAdminView lists every API reference page, drafts included, and edits
 * one at a time as JSON beside a live preview of what tenants will see.
 *
 * Writes follow the Console's write rules: same-origin checked on the server,
 * never retried, guarded by the page's version, and followed by a re-read
 * rather than an assumed result.
 *
 * ApiDocsAdminView 列出全部 API 参考页面（包括草稿），并以 JSON 形式逐个编辑，旁边是
 * 租户将看到的效果的实时预览。
 *
 * 写入遵循 Console 的写入规则：在服务端做同源校验、从不重试、以页面版本守护，写完后
 * 重新读取而不是假定结果。
 */
export function ApiDocsAdminView({ baseUrl }: { baseUrl: string }) {
  const { t } = useI18n();
  const run = useConsoleRequest();
  const list = useResource<OperatorApiDocSummary[]>({
    method: "GET",
    surface: "operator",
    path: BASE,
    parse: parseOperatorApiDocList,
  });
  const [editing, setEditing] = React.useState<Editing | null>(null);
  const [loadingSlug, setLoadingSlug] = React.useState<string | null>(null);

  const groups = React.useMemo(() => groupApiDocs(list.data ?? []), [list.data]);

  async function open(slug: string) {
    setLoadingSlug(slug);
    try {
      const doc = await run({ method: "GET", surface: "operator", path: `${BASE}/${slug}`, parse: parseOperatorApiDocDetail });
      setEditing({ kind: "existing", doc });
    } catch (failure) {
      toast.error(<ErrorMessage error={failure} />);
    } finally {
      setLoadingSlug(null);
    }
  }

  return (
    <div className="grid grid-cols-1 gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="font-heading text-lg font-semibold"><Message id="tenant.apiDocumentation" /></h1>
          <p className="max-w-3xl text-sm text-muted-foreground"><Message id="operator.contentForTheTenantConsoleSApiDocumentation_ee5c4b" values={{ code: <code className="font-mono">{"{{BASE_URL}}"}</code>, code2: <code className="font-mono">{"{{API_KEY}}"}</code>, code3: <code className="font-mono">{"{{MODEL}}"}</code> }} /></p>
        </div>
        <Button size="sm" onClick={() => setEditing({ kind: "new" })}><Message id="operator.newDocument_76182b" values={{ content: <PlusIcon /> }} /></Button>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[20rem_minmax(0,1fr)]">
        <div className="min-w-0 rounded-xl border p-2">
          {list.error ? (
            <ErrorState message={list.error} onRetry={list.reload} />
          ) : !list.data ? (
            <LoadingState label={t("operator.loadingDocumentList")} rows={8} />
          ) : list.data.length === 0 ? (
            <EmptyState title={t("operator.noDocumentsYet")} />
          ) : (
            <nav aria-label={t("operator.documentList")} className="flex flex-col gap-3">
              {groups.map((group) => (
                <div key={group.category} className="flex flex-col gap-0.5">
                  <p className="px-2 py-1 text-xs font-semibold">{group.category}</p>
                  {group.items.map((doc) => {
                    const active = editing?.kind === "existing" && editing.doc.slug === doc.slug;
                    return (
                      <button
                        key={doc.slug}
                        type="button"
                        onClick={() => open(doc.slug)}
                        disabled={loadingSlug !== null}
                        aria-current={active ? "true" : undefined}
                        className={cn(
                          "flex min-w-0 items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm",
                          active ? "bg-primary/10 text-primary" : "hover:bg-muted"
                        )}
                      >
                        <MethodBadge method={doc.method} className="w-12 justify-center" />
                        <span className="min-w-0 flex-1 truncate">{doc.title}</span>
                        {doc.status === "draft" ? <Badge variant="outline"><Message id="operator.draft" /></Badge> : null}
                        {doc.source === "operator" && doc.seeded ? <Badge variant="secondary"><Message id="operator.edited" /></Badge> : null}
                      </button>
                    );
                  })}
                </div>
              ))}
            </nav>
          )}
        </div>

        <div className="min-w-0">
          {editing ? (
            <DocEditor
              key={editing.kind === "existing" ? `${editing.doc.slug}@${editing.doc.version}` : "new"}
              editing={editing}
              baseUrl={baseUrl}
              run={run}
              onSaved={(doc) => {
                setEditing({ kind: "existing", doc });
                list.reload();
              }}
              onDeleted={() => {
                setEditing(null);
                list.reload();
              }}
            />
          ) : (
            <EmptyState title={t("operator.selectADocumentOnTheLeftToEdit")} description={t("operator.orCreateADocumentForNewlyAddedModels")} />
          )}
        </div>
      </div>
    </div>
  );
}

function DocEditor({
  editing,
  baseUrl,
  run,
  onSaved,
  onDeleted,
}: {
  editing: Editing;
  baseUrl: string;
  run: ReturnType<typeof useConsoleRequest>;
  onSaved: (doc: OperatorApiDocDetail) => void;
  onDeleted: () => void;
}) {
  const { locale, t } = useI18n();
  const existing = editing.kind === "existing" ? editing.doc : null;
  const [slug, setSlug] = React.useState("");
  const [text, setText] = React.useState(() =>
    JSON.stringify(existing ? toApiDocInput(existing) : blankApiDocInput(), null, 2)
  );
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);
  const [confirm, setConfirm] = React.useState<"reset" | "delete" | null>(null);
  const [preview, setPreview] = React.useState(true);

  // Re-parse on every keystroke so the preview and the save button agree on
  // whether the text is acceptable. / 每次按键都重新解析，使预览与保存按钮对文本是否
  // 可接受的判断一致。
  const parsed = React.useMemo((): { input: ApiDocInput } | { error: true } => {
    try {
      return { input: parseApiDocInput(text) };
    } catch {
      return { error: true };
    }
  }, [text]);

  async function save(event: React.FormEvent) {
    event.preventDefault();
    if ("error" in parsed) {
      setError(<Message id={"operator.theJsonFormatOrFieldsDoNotMatch"} />);
      return;
    }
    if (!existing && !isValidApiDocSlug(slug)) {
      setError(<Message id={"operator.theIdentifierMayContainOnlyLowercaseLettersDigits"} />);
      return;
    }
    setPending(true);
    setError(null);
    try {
      const doc = existing
        ? await run({
            method: "PUT",
            surface: "operator",
            path: `${BASE}/${existing.slug}`,
            body: { expected_version: existing.version, ...parsed.input },
            parse: parseOperatorApiDocDetail,
          })
        : await run({
            method: "POST",
            surface: "operator",
            path: BASE,
            body: { slug, ...parsed.input },
            parse: parseOperatorApiDocDetail,
          });
      toast.success(existing ? <Message id={"operator.saved"} values={{ title: doc.title }} /> : <Message id={"operator.created"} values={{ title: doc.title }} />);
      onSaved(doc);
    } catch (failure) {
      setError(<ErrorMessage error={failure} />);
    } finally {
      setPending(false);
    }
  }

  async function reset() {
    if (!existing) return;
    const doc = await run({
      method: "POST",
      surface: "operator",
      path: `${BASE}/${existing.slug}/reset`,
      body: { expected_version: existing.version },
      parse: parseOperatorApiDocDetail,
    });
    toast.success(<Message id={"operator.restoredBuiltInVersionOf"} values={{ title: doc.title }} />);
    setConfirm(null);
    onSaved(doc);
  }

  async function remove() {
    if (!existing) return;
    await run({
      method: "DELETE",
      surface: "operator",
      path: `${BASE}/${existing.slug}`,
      query: { expected_version: String(existing.version) },
      parse: parseNoContent,
    });
    toast.success(<Message id={"operator.deleted"} values={{ title: existing.title }} />);
    setConfirm(null);
    onDeleted();
  }

  return (
    <form onSubmit={save} className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2 rounded-xl border p-3 text-sm">
        {existing ? (
          <>
            <code className="font-mono">{existing.slug}</code>
            <Badge variant="outline">v{existing.version}</Badge>
            <Badge variant={existing.status === "published" ? "default" : "outline"}>
              {existing.status === "published" ? t("operator.published") : t("operator.draft")}
            </Badge>
            <Badge variant="secondary">{existing.source === "seed" ? t("operator.builtIn") : t("operator.operatorEdited")}</Badge>
            <span className="text-xs text-muted-foreground"><Message id="operator.updated" values={{ value: formatDateTime(existing.updatedAt, undefined, locale), value2: existing.updatedBy ? ` · ${existing.updatedBy}` : "" }} /></span>
          </>
        ) : (
          <label className="flex flex-wrap items-center gap-2"><Message id="operator.identifierSlug_6191ef" values={{ content: <Input
              value={slug}
              onChange={(event) => setSlug(event.target.value.trim())}
              placeholder={t("operator.eGVideoGenerations")}
              className="h-8 w-64 font-mono"
              maxLength={128}
              spellCheck={false}
              required
            /> }} /></label>
        )}
        <div className="ml-auto flex flex-wrap gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => setPreview((v) => !v)}>
            {preview ? t("operator.hidePreview") : t("operator.showPreview")}
          </Button>
          {existing?.seeded && existing.source === "operator" ? (
            <Button type="button" variant="outline" size="sm" onClick={() => setConfirm("reset")}>
              <Message id="operator.restoreBuiltInVersion" /></Button>
          ) : null}
          {existing && !existing.seeded ? (
            <Button type="button" variant="destructive" size="sm" onClick={() => setConfirm("delete")}>
              <Message id="common.delete" /></Button>
          ) : null}
          <SubmitButton pending={pending} size="sm" disabled={pending || "error" in parsed}>
            {existing ? t("common.save") : t("common.create")}
          </SubmitButton>
        </div>
      </div>
      <FormError message={error} />

      <div className={cn("grid grid-cols-1 gap-4", preview && "2xl:grid-cols-2")}>
        <label className="flex min-w-0 flex-col gap-1 text-sm">
          <span className="flex items-center gap-2"><Message id="operator.documentJson_04cddd" values={{ value: "error" in parsed ? <span className="text-xs text-destructive"><Message id="operator.invalidFormat" /></span> : null }} /></span>
          <textarea
            value={text}
            onChange={(event) => setText(event.target.value)}
            spellCheck={false}
            aria-invalid={"error" in parsed}
            className="min-h-[40rem] w-full rounded-lg border border-input bg-transparent px-2.5 py-1.5 font-mono text-xs outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 aria-invalid:border-destructive"
          />
        </label>
        {preview ? (
          <div className="min-w-0 rounded-xl border p-4">
            {"error" in parsed ? (
              <p className="text-sm text-muted-foreground"><Message id="operator.aPreviewWillAppearOnceTheJsonIs" /></p>
            ) : (
              <ApiDocArticle
                doc={parsed.input}
                values={{ baseUrl, apiKey: "YOUR_API_KEY", model: "your-model" }}
              />
            )}
          </div>
        ) : null}
      </div>

      {existing ? (
        <ConfirmDialog
          open={confirm !== null}
          onOpenChange={(next) => setConfirm(next ? confirm : null)}
          title={confirm === "reset" ? t("operator.restoreBuiltInVersion") : t("operator.deleteDocument")}
          description={
            confirm === "reset"
              ? t("operator.willBeRestoredToTheCurrentBuiltIn", { title: existing.title })
              : t("operator.willBePermanentlyDeletedAndWillNoLonger", { title: existing.title, slug: existing.slug })
          }
          confirmLabel={confirm === "reset" ? t("operator.restore") : t("common.delete")}
          destructive
          onConfirm={confirm === "reset" ? reset : remove}
        />
      ) : null}
    </form>
  );
}
