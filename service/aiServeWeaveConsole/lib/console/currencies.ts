/** SUPPORTED_CURRENCIES mirrors model.SupportedCurrencies: the ISO 4217 codes
 * the payment provider accepts, sorted. A price in any other code would only
 * be refused when a checkout opens, so operators pick from this list instead
 * of typing a code.
 *
 * SUPPORTED_CURRENCIES 镜像 model.SupportedCurrencies：支付服务商接受的 ISO 4217
 * 代码，已排序。其他代码的标价只会在打开结账时被拒，因此运维从这份清单中选择，而不是
 * 手动输入代码。 */
export const SUPPORTED_CURRENCIES: readonly string[] = [
  "AED", "ALL", "AMD", "ANG", "AOA", "ARS", "AUD", "AWG", "AZN", "BAM", "BBD", "BDT", "BGN",
  "BHD", "BIF", "BMD", "BND", "BOB", "BRL", "BSD", "BWP", "BYN", "BZD", "CAD", "CHF", "CLP",
  "CNY", "COP", "CRC", "CUP", "CVE", "CZK", "DJF", "DKK", "DOP", "DZD", "EGP", "ETB", "EUR",
  "FJD", "FKP", "GBP", "GEL", "GHS", "GIP", "GMD", "GNF", "GTQ", "GYD", "HKD", "HNL", "HRK",
  "HTG", "HUF", "IDR", "ILS", "INR", "IQD", "JMD", "JOD", "JPY", "KES", "KGS", "KHR", "KMF",
  "KRW", "KWD", "KYD", "KZT", "LAK", "LBP", "LKR", "LRD", "LSL", "LYD", "MAD", "MDL", "MGA",
  "MKD", "MMK", "MNT", "MOP", "MRU", "MUR", "MVR", "MWK", "MXN", "MYR", "MZN", "NAD", "NGN",
  "NIO", "NOK", "NPR", "NZD", "OMR", "PAB", "PEN", "PGK", "PHP", "PKR", "PLN", "PYG", "QAR",
  "RON", "RSD", "RUB", "RWF", "SAR", "SBD", "SCR", "SEK", "SGD", "SHP", "SLE", "SLL", "SOS",
  "SRD", "SSP", "STN", "SVC", "SZL", "THB", "TND", "TOP", "TRY", "TTD", "TWD", "TZS", "UAH",
  "UGX", "USD", "UYU", "UZS", "VES", "VND", "VUV", "WST", "XAF", "XCD", "XOF", "XPF", "YER",
  "ZAR", "ZMW"
];

const SUPPORTED = new Set(SUPPORTED_CURRENCIES);

/** isSupportedCurrency reports whether code, upper case, may be a price's
 * currency.
 *
 * isSupportedCurrency 报告 code（大写）能否作为标价币种。 */
export function isSupportedCurrency(code: string): boolean {
  return SUPPORTED.has(code);
}

/** currencyOptions returns the codes a currency picker offers. A stored code
 * outside the list (a row saved before the list existed) is kept first, so
 * the picker shows what is stored rather than silently switching it; saving
 * still refuses it until another code is chosen.
 *
 * currencyOptions 返回币种选单提供的代码。清单之外的已存代码（清单出现之前保存的
 * 行）排在最前，使选单显示已存的值而不是悄悄换掉它；在改选其他代码之前，保存仍会
 * 拒绝它。 */
export function currencyOptions(current: string): readonly string[] {
  return current === "" || SUPPORTED.has(current) ? SUPPORTED_CURRENCIES : [current, ...SUPPORTED_CURRENCIES];
}
