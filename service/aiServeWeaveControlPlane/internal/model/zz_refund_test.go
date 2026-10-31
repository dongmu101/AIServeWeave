package model

import (
	"math"
	"testing"
)

func TestRefundAmounts(t *testing.T) {
	order := func(credits, amount, paid, refunded int64) CheckoutOrder {
		return CheckoutOrder{CreditsMicro: credits, AmountMinor: amount, PaidAmountMinor: paid, RefundedMinor: refunded}
	}
	tests := []struct {
		name          string
		order         CheckoutOrder
		available     int64
		wantRemaining int64
		wantHeld      int64
		wantUnused    int64
	}{
		{"all unused", order(100e6, 1000, 0, 0), 100e6, 1000, 100e6, 1000},
		{"balance above order credits", order(100e6, 1000, 0, 0), 500e6, 1000, 100e6, 1000},
		{"partly spent", order(100e6, 1000, 0, 0), 30e6, 1000, 100e6, 300},
		{"rounds down", order(3e6, 1000, 0, 0), 1e6, 1000, 3e6, 333},
		{"zero balance", order(100e6, 1000, 0, 0), 0, 1000, 100e6, 0},
		{"negative balance", order(100e6, 1000, 0, 0), -5, 1000, 100e6, 0},
		{"no credits", order(0, 1000, 0, 0), 10, 1000, 0, 0},
		{"paid with tax is the basis", order(100e6, 1000, 1100, 0), 100e6, 1100, 100e6, 1100},
		{"paid with tax partly spent", order(100e6, 1000, 1100, 0), 50e6, 1100, 100e6, 550},
		{"half refunded, rest unused", order(100e6, 1000, 0, 500), 60e6, 500, 50e6, 500},
		{"half refunded, rest partly spent", order(100e6, 1000, 0, 500), 20e6, 500, 50e6, 200},
		{"fully refunded", order(100e6, 1000, 0, 1000), 100e6, 0, 0, 0},
		{"over refunded clamps", order(100e6, 1000, 0, 2000), 100e6, 0, 0, 0},
		{"huge values", order(math.MaxInt64/2, 1e12, 0, 0), math.MaxInt64 / 4, 1e12, math.MaxInt64 / 2, 499999999999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.order.RemainingRefundMinor(); got != tt.wantRemaining {
				t.Errorf("RemainingRefundMinor() = %d, want %d", got, tt.wantRemaining)
			}
			if got := tt.order.HeldCredits(); got != tt.wantHeld {
				t.Errorf("HeldCredits() = %d, want %d", got, tt.wantHeld)
			}
			if got := tt.order.UnusedRefundMinor(tt.available); got != tt.wantUnused {
				t.Errorf("UnusedRefundMinor(%d) = %d, want %d", tt.available, got, tt.wantUnused)
			}
		})
	}
}

// The unused-credits refund never takes back more credits than the balance
// had, whatever the rounding.
func TestUnusedRefundNeverExceedsBalance(t *testing.T) {
	for _, credits := range []int64{1, 7, 999, 3e6, 100e6 + 7} {
		for _, basis := range []int64{1, 3, 999, 1000, 12345} {
			for _, refunded := range []int64{0, 1, basis / 3, basis - 1} {
				o := CheckoutOrder{CreditsMicro: credits, AmountMinor: basis, RefundedMinor: refunded}
				for _, available := range []int64{1, 2, credits / 7, credits / 2, o.HeldCredits() - 1, o.HeldCredits()} {
					amount := o.UnusedRefundMinor(available)
					if amount < 0 || amount > o.RemainingRefundMinor() {
						t.Fatalf("credits=%d basis=%d refunded=%d available=%d: amount %d outside [0,%d]", credits, basis, refunded, available, amount, o.RemainingRefundMinor())
					}
					if taken := o.RefundDelta(amount); available > 0 && taken > max(available, 0) {
						t.Fatalf("credits=%d basis=%d refunded=%d available=%d: amount %d takes %d credits, more than available", credits, basis, refunded, available, amount, taken)
					}
				}
			}
		}
	}
}

func TestRefundDelta(t *testing.T) {
	o := CheckoutOrder{CreditsMicro: 100e6, AmountMinor: 1000, RefundedMinor: 250}
	tests := []struct {
		name   string
		amount int64
		want   int64
	}{
		{"zero", 0, 0},
		{"negative", -10, 0},
		{"part", 250, 25e6},
		{"rest", 750, 75e6},
		{"beyond rest clamps", 5000, 75e6},
		{"one minor unit rounds up", 1, 100000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := o.RefundDelta(tt.amount); got != tt.want {
				t.Errorf("RefundDelta(%d) = %d, want %d", tt.amount, got, tt.want)
			}
		})
	}
	if (RefundRequest{Status: RefundStatusPending}).Active() != true ||
		(RefundRequest{Status: RefundStatusProcessing}).Active() != true ||
		(RefundRequest{Status: RefundStatusFailed}).Active() != false {
		t.Error("Active() does not match pending/processing")
	}
}
