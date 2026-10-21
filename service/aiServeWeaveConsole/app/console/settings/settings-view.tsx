"use client";
import { MessageError } from "@/lib/i18n/problem";
import { ErrorMessage } from "@/components/i18n/error-message";




import { useI18n } from "@/components/i18n/provider";
import { Message } from "@/components/i18n/message";


import * as React from "react";
import { useRouter } from "next/navigation";
import { toast } from "sonner";

import { FormError } from "@/components/console/states";
import { SubmitButton } from "@/components/console/submit-button";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { canManageGatewayKey } from "@/lib/console/permissions";

/**
 * The settings page's one section: the Gateway API Key Console uses, on this
 * tenant's behalf, to reach the two data-plane endpoints the control plane
 * cannot forward — canceling a run and downloading its artifacts. Read the
 * long version of why this exists, and what it costs, in
 * SessionPayload.gatewayApiKey and the Console AGENTS.md's 与后端的边界.
 *
 * The key itself is never fetched back from the server once set — the same
 * "shown once, never read again" shape the control plane's own API Key
 * creation already uses — so this view only ever knows *whether* one is
 * configured, never what it is.
 *
 * 设置页唯一的一节：Console 代表本租户，用来触达那两个控制面无法转发的数据面
 * 端点——取消一次运行、下载它的产物——所使用的 Gateway API Key。它为什么存在、
 * 代价是什么，完整版本见 SessionPayload.gatewayApiKey 与 Console AGENTS.md
 * 的「与后端的边界」。
 *
 * 这把 key 一旦设置，就再也不会从服务端被取回——与控制面自己的 API Key 创建早已
 * 采用的「只展示一次，再也读不回」是同一种形状——因此本视图始终只知道*是否*已经
 * 配置了一把，从不知道它是什么。
 */
export function SettingsView({
  role,
  hasGatewayKey,
}: {
  role: string;
  hasGatewayKey: boolean;
}) {
  const {t } = useI18n();
  const router = useRouter();
  const editable = canManageGatewayKey(role);
  const [configured, setConfigured] = React.useState(hasGatewayKey);
  const [value, setValue] = React.useState("");
  const [pending, setPending] = React.useState(false);
  const [error, setError] = React.useState<React.ReactNode>(null);

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) {
      return;
    }
    const trimmed = value.trim();
    if (trimmed === "") {
      setError(<Message id={"tenant.pasteAnApiKeyForThisTenant"} />);
      return;
    }
    setPending(true);
    setError(null);
    try {
      const response = await fetch("/api/gateway-key", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ apiKey: trimmed }),
      });
      if (response.status === 401) {
        router.replace("/login");
        return;
      }
      if (!response.ok) {
        setError(<ErrorMessage error={settingsFailure(response)} />);
        return;
      }
      setValue("");
      setConfigured(true);
      // The rest of the console (the jobs page especially) decided what to
      // render from the server-read session this page itself started from.
      // refresh() re-runs that server read so those decisions catch up with
      // what was just saved, without a full reload.
      //
      // 控制台其余部分（尤其是运行页面）渲染什么，是根据本页面自己一开始所依据
      // 的那次服务端会话读取决定的。refresh() 重新跑一遍那次服务端读取，好让
      // 那些决定跟上刚刚保存的结果，而不必整页重新加载。
      router.refresh();
      toast.success(<Message id={"tenant.saved"} />);
    } catch {
      setError(<Message id={"tenant.unableToConnectToTheServerCheckYour"} />);
    } finally {
      setPending(false);
    }
  }

  async function clear() {
    if (pending) {
      return;
    }
    setPending(true);
    setError(null);
    try {
      const response = await fetch("/api/gateway-key", { method: "DELETE" });
      if (response.status === 401) {
        router.replace("/login");
        return;
      }
      if (!response.ok) {
        setError(<ErrorMessage error={settingsFailure(response)} />);
        return;
      }
      setConfigured(false);
      router.refresh();
      toast.success(<Message id={"tenant.cleared"} />);
    } catch {
      setError(<Message id={"tenant.unableToConnectToTheServerCheckYour"} />);
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="grid gap-4">
      <div>
        <h1 className="font-heading text-lg font-semibold"><Message id="tenant.settings" /></h1>
        <p className="text-sm text-muted-foreground">
          <Message id="tenant.currentlyTheOnlySettingIsTheGatewayApi" /></p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            Gateway API Key
            <Badge variant={configured ? "secondary" : "outline"}>
              {configured ? t("tenant.configured") : t("tenant.notConfigured")}
            </Badge>
          </CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4">
          <div className="grid gap-1 text-sm text-muted-foreground">
            <p>
              <Message id="tenant.cancellationAndArtifactDownloadsOnTheRunsPage" /></p>
            <p><Message id="tenant.toConfigureItCreateAKeyForThis_695fe7" values={{ strong: <strong><Message id="tenant.isNeverSentToTheBrowser" /></strong> }} /></p>
            <p className="text-destructive"><Message id="tenant.thisKeyCurrentlyItHasExactlyTheSame" values={{ strong: <strong><Message id="tenant.hasNoRestrictedScope" /></strong> }} /></p>
          </div>

          {editable ? (
            <form onSubmit={save} className="grid gap-3" noValidate>
              <div className="grid gap-2">
                <label htmlFor="gateway-key-input" className="text-sm font-medium">
                  {configured ? t("tenant.replaceWithANewKey") : t("tenant.pasteKey")}
                </label>
                <Input
                  id="gateway-key-input"
                  type="password"
                  autoComplete="off"
                  spellCheck={false}
                  value={value}
                  onChange={(event) => setValue(event.target.value)}
                  disabled={pending}
                  placeholder={t("tenant.apiKeyPlaintextStartingWithAisw")}
                />
              </div>

              <FormError message={error} />

              <div className="flex flex-wrap items-center gap-2">
                <SubmitButton pending={pending}>
                  {configured ? t("tenant.replace") : t("common.save")}
                </SubmitButton>
                {configured ? (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={pending}
                    onClick={clear}
                  >
                    <Message id="tenant.clear" /></Button>
                ) : null}
              </div>
            </form>
          ) : (
            <p className="text-sm text-muted-foreground">
              <Message id="tenant.onlyOwnerOrAdminCanConfigureThisKey" /></p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

/** settingsFailure turns a failed response into on-screen text. This
 * view does not go through lib/console/api-client.ts, so it does not reuse
 * ApiErrorKind — the endpoint behind it is Console's own state, not an
 * upstream forward, and its failure modes are a smaller, different set.
 *
 * settingsFailure 把一个失败的响应转换成屏幕上的文案。本视图不经由
 * lib/console/api-client.ts，因此不复用 ApiErrorKind——它背后的端点是 Console
 * 自己的状态，不是一次上游转发，失败模式是另一套更小的集合。 */
function settingsFailure(response: Response): MessageError {
  if (response.status === 403) {
    return new MessageError("tenant.yourCurrentRoleCannotConfigureThisKey");
  }
  if (response.status === 400 || response.status === 413) {
    return new MessageError("tenant.theSubmittedDataDoesNotMeetTheRequirements");
  }
  return new MessageError("tenant.unableToSavePleaseTryAgainLater");
}
