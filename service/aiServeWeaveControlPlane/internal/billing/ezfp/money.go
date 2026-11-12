package ezfp

import (
	"errors"
	"strconv"
	"strings"
)

// maxMoneyMinor bounds an amount, in fen, far above any real order and far
// below where arithmetic could overflow.
//
// maxMoneyMinor 是金额（分）的上限，远高于任何真实订单，也远低于运算可能溢出之处。
const maxMoneyMinor = 1_000_000_000_000

var errMoney = errors.New("ezfp: not an amount in yuan with at most two decimals")

// FormatMoney writes fen as the yuan string EZFP takes, always with two
// decimals: 1234 → "12.34", 5 → "0.05".
//
// FormatMoney 把分写成易支付接受的元字符串，总是带两位小数：1234 → "12.34"，
// 5 → "0.05"。
func FormatMoney(minor int64) string {
	if minor < 0 {
		minor = 0
	}
	return strconv.FormatInt(minor/100, 10) + "." + pad2(minor%100)
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

// ParseMoney reads a yuan string exactly into fen. It accepts digits with an
// optional point and one or two decimals, nothing else: no sign, exponent,
// space or thousands separator.
//
// ParseMoney 把元字符串精确读成分。只接受数字加可选的小数点与一到两位小数，其他一律
// 拒绝：没有正负号、指数、空格或千分位。
func ParseMoney(s string) (int64, error) {
	whole, frac, dotted := strings.Cut(s, ".")
	if whole == "" || len(whole) > 13 || (dotted && (frac == "" || len(frac) > 2)) {
		return 0, errMoney
	}
	for _, part := range []string{whole, frac} {
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, errMoney
			}
		}
	}
	yuan, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, errMoney
	}
	var fen int64
	if frac != "" {
		f, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, errMoney
		}
		if len(frac) == 1 {
			f *= 10
		}
		fen = f
	}
	minor := yuan*100 + fen
	if minor > maxMoneyMinor {
		return 0, errMoney
	}
	return minor, nil
}
