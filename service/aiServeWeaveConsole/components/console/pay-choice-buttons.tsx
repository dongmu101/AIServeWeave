"use client";

import * as React from "react";

import { Message } from "@/components/i18n/message";
import { useI18n } from "@/components/i18n/provider";
import { Button } from "@/components/ui/button";
import { payChoiceKey, type PayChoice } from "@/lib/console/payment-choice";
import type { MessageKey } from "@/lib/i18n/messages";

/** payChoiceLabelKey names a way to pay: Dodo takes cards, the two EZFP
 * types are Alipay and WeChat Pay.
 *
 * payChoiceLabelKey 给出一种付款方式的名称：Dodo 走银行卡，易支付的两种类型是支付宝与
 * 微信支付。 */
export function payChoiceLabelKey(choice: PayChoice): MessageKey {
  if (choice.provider === "dodo") {
    return "tenant.payChoiceDodo";
  }
  return choice.payType === "alipay" ? "tenant.payChoiceAlipay" : "tenant.payChoiceWxpay";
}

/**
 * PayChoiceButtons is the buy control shared by packages, monthly accounts and
 * presales. It shows what the tenant can pay with and reports the one clicked;
 * it sends nothing itself.
 *
 * With Dodo as the only choice it is the single button the page always had,
 * under `label`. With several, each way to pay is its own button (「银行卡 /
 * Dodo」「支付宝」「微信支付」), grouped under `label` for assistive technology. With
 * none it is that same button, disabled. These are EZFP (易支付) 「支付宝」, which
 * pays a CNY checkout; it is not the 一人付 button that spends Alipay quota,
 * which the pages keep as a separate, differently named button.
 *
 * PayChoiceButtons 是充值包、账号现货与预售共用的购买控件。它展示商户能用什么付款，并
 * 报告被点击的那一个；自身不发送任何请求。
 *
 * 只有 Dodo 一种选择时，它就是页面一直以来的那个按钮，文案为 `label`。有多种选择时，每种
 * 付款方式各是一个按钮（「银行卡 / Dodo」「支付宝」「微信支付」），并以 `label` 为辅助技术
 * 分组。没有任何选择时，仍是那个按钮，但处于禁用。这里的「支付宝」是易支付，用于支付人民币
 * 结账；它不是花费支付宝额度的一人付按钮，后者在页面上是另一个名称不同的独立按钮。
 */
export function PayChoiceButtons({ choices, label, onChoose, disabled = false, busy = false }: {
  choices: readonly PayChoice[];
  label: MessageKey;
  onChoose: (choice: PayChoice) => void;
  disabled?: boolean;
  busy?: boolean;
}) {
  const { t } = useI18n();
  const only = choices.length === 1 && choices[0]?.provider === "dodo" ? choices[0] : null;
  if (choices.length === 0 || only !== null) {
    return <Button disabled={disabled || choices.length === 0} aria-busy={busy || undefined} onClick={() => { if (only) onChoose(only); }}><Message id={label} /></Button>;
  }
  return <div role="group" aria-label={t(label)} className="flex flex-wrap items-center gap-2">
    <span className="w-full text-xs text-muted-foreground"><Message id="tenant.payChoicePrompt" /></span>
    {choices.map((choice) => <Button key={payChoiceKey(choice)} disabled={disabled} aria-busy={busy || undefined} onClick={() => onChoose(choice)}><Message id={payChoiceLabelKey(choice)} /></Button>)}
  </div>;
}
