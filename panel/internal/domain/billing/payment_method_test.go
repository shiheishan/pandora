package billing

import "testing"

func TestPaymentMethodOf(t *testing.T) {
	for raw, want := range map[string]string{
		"alipay": "alipay", " WXPAY ": "wxpay", "offline": "offline", "qq_pay2": "qq_pay2",
		"": "", "  ": "", "ali pay": "", "支付宝": "", "alipay;drop": "",
		"abcdefghijklmnopqrstuvwxyz0123456": "",
	} {
		got := paymentMethodOf(raw)
		if want == "" {
			if got != nil {
				t.Errorf("paymentMethodOf(%q)=%q want nil", raw, *got)
			}
			continue
		}
		if got == nil || *got != want {
			t.Errorf("paymentMethodOf(%q)=%v want %q", raw, got, want)
		}
	}
}
