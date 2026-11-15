/**
 * CC Switch (https://github.com/farion1231/cc-switch) manages the provider
 * settings of Claude Code, Codex and similar tools, and imports a provider
 * from a `ccswitch://v1/import` deep link. This module builds that link for
 * the client setup page; CC Switch shows a confirmation before it saves.
 *
 * CC Switch 管理 Claude Code、Codex 等工具的提供方配置，并能从 `ccswitch://v1/import`
 * deep link 导入提供方。本模块为客户端接入页构造该链接；CC Switch 保存前会弹出确认。
 */

/** CcSwitchApp is the target tool. / CcSwitchApp 是目标工具。 */
export type CcSwitchApp = "claude" | "codex";

/** CcSwitchProvider is what one import carries. / CcSwitchProvider 是一次导入携带的内容。 */
export interface CcSwitchProvider {
  app: CcSwitchApp;
  name: string;
  /** baseUrl is the Gateway base URL, without /v1. / baseUrl 是网关基地址，不带 /v1。 */
  baseUrl: string;
  apiKey: string;
  model: string;
}

/**
 * ccSwitchImportLink builds the provider import link. Claude Code speaks the
 * Anthropic protocol against the base URL itself; Codex speaks Responses
 * against `/v1`.
 *
 * ccSwitchImportLink 构造提供方导入链接。Claude Code 以 Anthropic 协议直连基地址；
 * Codex 以 Responses 协议访问 `/v1`。
 */
export function ccSwitchImportLink(provider: CcSwitchProvider): string {
  const base = provider.baseUrl.replace(/\/+$/, "");
  const params = new URLSearchParams({
    resource: "provider",
    app: provider.app,
    name: provider.name,
    endpoint: provider.app === "codex" ? `${base}/v1` : base,
    apiKey: provider.apiKey,
    model: provider.model,
  });
  return `ccswitch://v1/import?${params.toString().replace(/\+/g, "%20")}`;
}
