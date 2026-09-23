package adminops

import "testing"

func TestRevenueValidation(t *testing.T) {
	for _, c := range []string{"CNY", "USD"} {
		if err := validateRevenueCurrency(c); err != nil {
			t.Fatalf("%s rejected: %v", c, err)
		}
	}
	for _, c := range []string{"EUR", "cny", ""} {
		if err := validateRevenueCurrency(c); err == nil {
			t.Fatalf("%q accepted", c)
		}
	}
	for _, d := range []int{7, 30, 90} {
		if err := validateRevenueDays(d); err != nil {
			t.Fatalf("%d rejected", d)
		}
	}
	for _, d := range []int{0, 14, 365} {
		if err := validateRevenueDays(d); err == nil {
			t.Fatalf("%d accepted", d)
		}
	}
}
