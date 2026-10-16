"use client";

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
import { describe } from "@/lib/console/errors";
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
      toast.error(describe(failure));
    } finally {
      setLoadingSlug(null);
    }
  }

  return (
    <div className="grid grid-cols-1 gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="font-heading text-lg font-semibold">API 文档</h1>
          <p className="max-w-3xl text-sm text-muted-foreground">
            租户控制台「API 文档」页面的内容。内置文档随控制面启动自动写入并随版本更新；一旦在这里编辑，该文档即归运维所有，
            之后的内置更新不再覆盖，可用「恢复内置版本」交还。草稿对租户不可见。示例中可用占位符{" "}
            <code className="font-mono">{"{{BASE_URL}}"}</code>、<code className="font-mono">{"{{API_KEY}}"}</code>、
            <code className="font-mono">{"{{MODEL}}"}</code>。
          </p>
        </div>
        <Button size="sm" onClick={() => setEditing({ kind: "new" })}>
          <PlusIcon />
          新建文档
        </Button>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[20rem_minmax(0,1fr)]">
        <div className="min-w-0 rounded-xl border p-2">
          {list.error ? (
            <ErrorState message={list.error} onRetry={list.reload} />
          ) : !list.data ? (
            <LoadingState label="正在加载文档列表" rows={8} />
          ) : list.data.length === 0 ? (
            <EmptyState title="还没有文档" />
          ) : (
            <nav aria-label="文档列表" className="flex flex-col gap-3">
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
                        {doc.status === "draft" ? <Badge variant="outline">草稿</Badge> : null}
                        {doc.source === "operator" && doc.seeded ? <Badge variant="secondary">已编辑</Badge> : null}
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
            <EmptyState title="选择左侧的文档进行编辑" description="或新建一份文档，为新接入的模型或能力补充说明。" />
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
  const existing = editing.kind === "existing" ? editing.doc : null;
  const [slug, setSlug] = React.useState("");
  const [text, setText] = React.useState(() =>
    JSON.stringify(existing ? toApiDocInput(existing) : blankApiDocInput(), null, 2)
  );
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
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
      setError("JSON 格式或字段不符合文档结构，请检查后再保存");
      return;
    }
    if (!existing && !isValidApiDocSlug(slug)) {
      setError("标识只能包含小写字母、数字与连字符，且以字母或数字开头");
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
      toast.success(existing ? `已保存 ${doc.title}` : `已创建 ${doc.title}`);
      onSaved(doc);
    } catch (failure) {
      setError(describe(failure));
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
    toast.success(`已恢复内置版本 ${doc.title}`);
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
    toast.success(`已删除 ${existing.title}`);
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
              {existing.status === "published" ? "已发布" : "草稿"}
            </Badge>
            <Badge variant="secondary">{existing.source === "seed" ? "内置" : "运维编辑"}</Badge>
            <span className="text-xs text-muted-foreground">
              更新于 {formatDateTime(existing.updatedAt)}
              {existing.updatedBy ? ` · ${existing.updatedBy}` : ""}
            </span>
          </>
        ) : (
          <label className="flex flex-wrap items-center gap-2">
            标识（slug）
            <Input
              value={slug}
              onChange={(event) => setSlug(event.target.value.trim())}
              placeholder="例如 video-generations"
              className="h-8 w-64 font-mono"
              maxLength={128}
              spellCheck={false}
              required
            />
          </label>
        )}
        <div className="ml-auto flex flex-wrap gap-2">
          <Button type="button" variant="outline" size="sm" onClick={() => setPreview((v) => !v)}>
            {preview ? "隐藏预览" : "显示预览"}
          </Button>
          {existing?.seeded && existing.source === "operator" ? (
            <Button type="button" variant="outline" size="sm" onClick={() => setConfirm("reset")}>
              恢复内置版本
            </Button>
          ) : null}
          {existing && !existing.seeded ? (
            <Button type="button" variant="destructive" size="sm" onClick={() => setConfirm("delete")}>
              删除
            </Button>
          ) : null}
          <SubmitButton pending={pending} size="sm" disabled={pending || "error" in parsed}>
            {existing ? "保存" : "创建"}
          </SubmitButton>
        </div>
      </div>
      <FormError message={error} />

      <div className={cn("grid grid-cols-1 gap-4", preview && "2xl:grid-cols-2")}>
        <label className="flex min-w-0 flex-col gap-1 text-sm">
          <span className="flex items-center gap-2">
            文档 JSON
            {"error" in parsed ? <span className="text-xs text-destructive">格式有误</span> : null}
          </span>
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
              <p className="text-sm text-muted-foreground">JSON 修正后显示预览。</p>
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
          title={confirm === "reset" ? "恢复内置版本" : "删除文档"}
          description={
            confirm === "reset"
              ? `「${existing.title}」将恢复为当前内置版本，运维的编辑会被丢弃，之后的内置更新会自动应用。发布状态保持不变。`
              : `「${existing.title}」（${existing.slug}）将被永久删除，租户无法再看到它。`
          }
          confirmLabel={confirm === "reset" ? "恢复" : "删除"}
          destructive
          onConfirm={confirm === "reset" ? reset : remove}
        />
      ) : null}
    </form>
  );
}
