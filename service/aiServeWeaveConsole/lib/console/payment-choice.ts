import type { PaymentMethods } from "./billing.ts";

/** EZFP_PAY_TYPES is the closed set of payment types 易支付 (EZFP, 彩虹易支付 V2)
 * can be asked for: Alipay and WeChat Pay.
 *
 * EZFP_PAY_TYPES 是易支付（EZFP，彩虹易支付 V2）可指定的支付类型的封闭集合：支付宝与
 * 微信支付。 */
export const EZFP_PAY_TYPES = ["alipay", "wxpay"] as const;
export type EzfpPayType = (typeof EZFP_PAY_TYPES)[number];

/** PayChoice is how a tenant chose to pay a fiat checkout: through Dodo, or
 * through EZFP with one of its payment types. EZFP charges CNY only, so only
 * a CNY item can be paid that way; the control plane decides which items.
 *
 * PayChoice 是商户为一次法币结账选择的付款方式：经 Dodo，或经易支付并指定其中一种
 * 支付类型。易支付只收人民币，所以只有 CNY 商品能这样支付；哪些商品可以由控制面决定。 */
export type PayChoice = { provider: "dodo" } | { provider: "ezfp"; payType: EzfpPayType };

/** PayAvailability says which providers can sell one item right now, as the
 * control plane reported on the item itself.
 *
 * PayAvailability 说明某个商品此刻可经哪些服务商出售，以控制面在该商品上的报告为准。 */
export interface PayAvailability {
  dodoAvailable: boolean;
  ezfpAvailable: boolean;
}

/** payChoices lists the ways an item can be paid, in display order: Dodo
 * first, then each enabled EZFP type. A provider counts only when the
 * deployment enables it and the item is payable through it. An empty list
 * means the item cannot be bought by a fiat checkout at all.
 *
 * payChoices 按展示顺序列出一个商品的付款方式：先 Dodo，再依次是已开启的易支付类型。
 * 只有部署开启该服务商且该商品能经由它支付时才算数。空列表表示该商品根本不能走法币结账。 */
export function payChoices(methods: Pick<PaymentMethods, "dodo" | "ezfp" | "ezfpTypes">, item: PayAvailability): PayChoice[] {
  const choices: PayChoice[] = [];
  if (methods.dodo && item.dodoAvailable) {
    choices.push({ provider: "dodo" });
  }
  if (methods.ezfp && item.ezfpAvailable) {
    for (const payType of methods.ezfpTypes) {
      choices.push({ provider: "ezfp", payType });
    }
  }
  return choices;
}

/** withPayChoice adds `provider` and `pay_type` to a checkout body only when
 * the tenant chose EZFP. A Dodo checkout sends neither, because a control
 * plane from before EZFP rejects unknown fields with 400.
 *
 * withPayChoice 只在商户选择易支付时才给结账请求体加上 `provider` 与 `pay_type`。
 * Dodo 结账两者都不发送，因为早于易支付的控制面会以 400 拒绝未知字段。 */
export function withPayChoice<T extends object>(body: T, choice: PayChoice | undefined): T | (T & { provider: "ezfp"; pay_type: EzfpPayType }) {
  if (choice === undefined || choice.provider === "dodo") {
    return body;
  }
  return { ...body, provider: "ezfp", pay_type: choice.payType };
}

/** payChoiceKey is a stable key for a choice, for React lists and tests.
 *
 * payChoiceKey 是一个选择的稳定标识，用于 React 列表与测试。 */
export function payChoiceKey(choice: PayChoice): string {
  return choice.provider === "dodo" ? "dodo" : `ezfp:${choice.payType}`;
}
