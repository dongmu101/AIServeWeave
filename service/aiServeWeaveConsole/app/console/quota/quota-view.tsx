"use client";

import * as React from "react";
import { toast } from "sonner";

import { FormError } from "@/components/console/states";
import { ErrorState, LoadingState } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { useConsoleRequest } from "@/components/console/use-console-request";
import { useResource } from "@/components/console/use-resource";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  parseTenantLimits,
  parseTenantProfile,
  type TenantLimits,
  type TenantProfile,
} from "@/lib/console/contract";
import { describe } from "@/lib/console/errors";
import { formatDateTime, formatLimit, limitProblem, QUOTA_DIMENSIONS } from "@/lib/console/format";
import { canEditTenantLimits } from "@/lib/console/permissions";

/**
 * The tenant's quota.
 *
 * Three things here follow from the contract rather than from taste. The form
 * reads before it edits, so nobody submits a value on top of a number they
 * never saw. Zero is shown and labelled as unlimited, because that is what
 * quota.Limits says it means — never as an empty box, which would read as
 * "unset" and invite somebody to fill in a blank that is not blank. And a
 * failed read leaves the form unrendered instead of falling back to zeroes:
 * zeroes are a configuration, and showing them for an unknown value would let
 * a save turn "I could not read your quota" into "your tenant is unlimited".
 *
 * 租户配额。
 *
 * 这里有三点出自契约而不是偏好。表单先读取再编辑，这样就没人会在一个自己从未看到过的
 * 数字之上提交新值。零会被展示并标注为「不限制」，因为 quota.Limits 就是这么规定它的
 * 含义的——而绝不显示成空输入框，那会被读作「未设置」，诱使人去填一个并不空缺的空缺。
 * 读取失败时表单干脆不渲染，而不是退回成一组零：零是一种配置，把它当作未知值展示，
 * 会让一次保存把「我读不到你的配额」变成「你的租户不受限制」。
 */
export function QuotaView({ role }: { role: string }) {
  const run = useConsoleRequest();
  const profile = useResource<TenantProfile>({
    method: "GET",
    path: "/admin/v1/tenants/current",
    parse: parseTenantProfile,
  });

  if (profile.error) {
    return <ErrorState message={profile.error} onRetry={profile.reload} />;
  }
  if (profile.loading || !profile.data) {
    return <LoadingState label="正在加载租户配额" rows={5} />;
  }

  return (
    <QuotaEditor
      key={keyFor(profile.data.limits)}
      profile={profile.data}
      editable={canEditTenantLimits(role)}
      onSaved={profile.reload}
      run={run}
    />
  );
}

/** keyFor remounts the editor when the server's values change, so a reload
 * after a save starts from what was actually stored rather than from what was
 * typed.
 *
 * keyFor 在服务端的值发生变化时重挂载编辑器，这样一次保存后的重新读取，是从实际存储的
 * 内容开始，而不是从当时输入的内容开始。 */
function keyFor(limits: TenantLimits): string {
  return `${limits.requestsPerMinute}/${limits.tokensPerMinute}/${limits.maxConcurrent}`;
}

function QuotaEditor({
  profile,
  editable,
  onSaved,
  run,
}: {
  profile: TenantProfile;
  editable: boolean;
  onSaved: () => void;
  run: ReturnType<typeof useConsoleRequest>;
}) {
  const [draft, setDraft] = React.useState<Record<string, string>>(() =>
    Object.fromEntries(
      QUOTA_DIMENSIONS.map((dimension) => [
        dimension.field,
        String(profile.limits[dimension.field]),
      ])
    )
  );
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }
    for (const dimension of QUOTA_DIMENSIONS) {
      const problem = limitProblem(draft[dimension.field] ?? "");
      if (problem) {
        setError(`${dimension.label}：${problem}`);
        return;
      }
    }

    setPending(true);
    setError(null);
    try {
      // The endpoint is PUT and takes the whole set: there is no way to send
      // one dimension, because "leave this alone" and "set it to unlimited"
      // would be the same request. Every field is submitted, including the
      // ones nobody touched.
      //
      // 该端点是 PUT，接受的是完整的一组：没有办法只发送一个维度，因为「这个不动」与
      // 「设为不限制」会是同一个请求。每个字段都会被提交，包括没人碰过的那些。
      const saved = await run({
        method: "PUT",
        path: "/admin/v1/tenants/limits",
        body: Object.fromEntries(
          QUOTA_DIMENSIONS.map((dimension) => [
            dimension.wire,
            Number(draft[dimension.field]),
          ])
        ),
        parse: parseTenantLimits,
      });
      toast.success("配额已保存");
      // The server's answer is what gets shown, and the profile is re-read so
      // the page reflects the stored row rather than the request that was sent.
      //
      // 展示的是服务端的回答，并重新读取租户资料，好让页面反映的是存储中的那一行，
      // 而不是刚刚发出的那个请求。
      setDraft(
        Object.fromEntries(
          QUOTA_DIMENSIONS.map((dimension) => [
            dimension.field,
            String(saved[dimension.field]),
          ])
        )
      );
      onSaved();
    } catch (failure) {
      setError(describe(failure));
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="grid gap-6">
      <div>
        <h1 className="font-heading text-lg font-semibold">配额</h1>
        <p className="text-sm text-muted-foreground">
          限制适用于整个租户，本租户的所有 API Key 共用同一份额度；不是按 Key 计算的。
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>租户</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-3">
            <div className="grid gap-1">
              <dt className="text-xs text-muted-foreground">名称</dt>
              <dd className="text-sm">{profile.tenant.name}</dd>
            </div>
            <div className="grid gap-1">
              <dt className="text-xs text-muted-foreground">状态</dt>
              <dd className="text-sm">
                <Badge variant="outline">
                  {profile.tenant.status === "active" ? "正常" : profile.tenant.status}
                </Badge>
              </dd>
            </div>
            <div className="grid gap-1">
              <dt className="text-xs text-muted-foreground">创建时间</dt>
              <dd className="text-sm">{formatDateTime(profile.tenant.createdAt)}</dd>
            </div>
            <div className="grid gap-1 sm:col-span-3">
              <dt className="text-xs text-muted-foreground">租户 ID</dt>
              <dd className="font-mono text-sm break-all">{profile.tenant.id}</dd>
            </div>
          </dl>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>当前生效的限制</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="grid gap-5" noValidate>
            {QUOTA_DIMENSIONS.map((dimension) => (
              <div key={dimension.field} className="grid gap-2">
                <label
                  htmlFor={`quota-${dimension.field}`}
                  className="text-sm font-medium"
                >
                  {dimension.label}
                </label>
                <div className="flex flex-wrap items-center gap-3">
                  <Input
                    id={`quota-${dimension.field}`}
                    className="w-40"
                    inputMode="numeric"
                    value={draft[dimension.field] ?? ""}
                    onChange={(event) =>
                      setDraft((previous) => ({
                        ...previous,
                        [dimension.field]: event.target.value,
                      }))
                    }
                    disabled={!editable || pending}
                    aria-describedby={`quota-${dimension.field}-help`}
                  />
                  <span className="text-xs text-muted-foreground">
                    服务端当前值：{formatLimit(profile.limits[dimension.field])}
                  </span>
                </div>
                <p
                  id={`quota-${dimension.field}-help`}
                  className="text-xs text-muted-foreground"
                >
                  {dimension.help} 填 0 表示不限制。
                </p>
              </div>
            ))}

            <FormError message={error} />

            {editable ? (
              <div className="flex flex-wrap items-center gap-3">
                <SubmitButton pending={pending}>保存全部三项</SubmitButton>
                <span className="text-xs text-muted-foreground">
                  保存会整组覆盖：三项都会按上面填写的值写入，包括没有修改的那些。
                </span>
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">
                只有所有者与管理员可以修改配额。
              </p>
            )}
          </form>
        </CardContent>
      </Card>

      <p className="text-xs text-muted-foreground">
        限制由网关执行。网关会缓存租户身份与配额，因此一次修改可能在其配置的缓存有效期内
        才完全生效；默认部署为 30 秒，实际时长以该网关的配置为准。
      </p>
    </div>
  );
}
