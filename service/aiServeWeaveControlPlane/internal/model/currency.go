package model

import "slices"

// SupportedCurrencies lists, in order, the ISO 4217 codes a price may be in:
// the currencies the payment provider (Dodo Payments) accepts. A code outside
// it, however well formed, would only be refused when a checkout opens.
// The Console offers the same list; a test keeps the two, and the provider
// SDK, in step.
//
// SupportedCurrencies 按顺序列出标价可以使用的 ISO 4217 代码，即支付服务商（Dodo
// Payments）接受的币种。不在其中的代码即使格式正确，也只会在打开结账时被拒。Console
// 提供同一份清单；有测试保证两者与服务商 SDK 一致。
var SupportedCurrencies = []string{
	"AED", "ALL", "AMD", "ANG", "AOA", "ARS", "AUD", "AWG", "AZN", "BAM", "BBD", "BDT",
	"BGN", "BHD", "BIF", "BMD", "BND", "BOB", "BRL", "BSD", "BWP", "BYN", "BZD", "CAD",
	"CHF", "CLP", "CNY", "COP", "CRC", "CUP", "CVE", "CZK", "DJF", "DKK", "DOP", "DZD",
	"EGP", "ETB", "EUR", "FJD", "FKP", "GBP", "GEL", "GHS", "GIP", "GMD", "GNF", "GTQ",
	"GYD", "HKD", "HNL", "HRK", "HTG", "HUF", "IDR", "ILS", "INR", "IQD", "JMD", "JOD",
	"JPY", "KES", "KGS", "KHR", "KMF", "KRW", "KWD", "KYD", "KZT", "LAK", "LBP", "LKR",
	"LRD", "LSL", "LYD", "MAD", "MDL", "MGA", "MKD", "MMK", "MNT", "MOP", "MRU", "MUR",
	"MVR", "MWK", "MXN", "MYR", "MZN", "NAD", "NGN", "NIO", "NOK", "NPR", "NZD", "OMR",
	"PAB", "PEN", "PGK", "PHP", "PKR", "PLN", "PYG", "QAR", "RON", "RSD", "RUB", "RWF",
	"SAR", "SBD", "SCR", "SEK", "SGD", "SHP", "SLE", "SLL", "SOS", "SRD", "SSP", "STN",
	"SVC", "SZL", "THB", "TND", "TOP", "TRY", "TTD", "TWD", "TZS", "UAH", "UGX", "USD",
	"UYU", "UZS", "VES", "VND", "VUV", "WST", "XAF", "XCD", "XOF", "XPF", "YER", "ZAR",
	"ZMW",
}

// ValidCurrency reports whether code is one of SupportedCurrencies, upper
// case as stored.
//
// ValidCurrency 报告 code 是否为 SupportedCurrencies 之一（与存储一致，须为大写）。
func ValidCurrency(code string) bool {
	_, found := slices.BinarySearch(SupportedCurrencies, code)
	return found
}
