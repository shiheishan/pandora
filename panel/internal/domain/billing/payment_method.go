package billing

import "strings"

// OfflinePaymentMethod 是线下收款（后台「标记已支付」「线下已收款」）记在 payments.method 上的方式。
const OfflinePaymentMethod = "offline"

// paymentMethodOf 把渠道回传的付款方式规整成 payments.method 的值：小写、去空白，
// 只收 1–32 位的 [a-z0-9_]（alipay、wxpay、offline …）；空的或不像方式名的回 nil，
// 由结算改用发起支付时意图上记的方式。回调内容来自渠道，不把任意字符串原样写进账。
func paymentMethodOf(raw string) *string {
	m := strings.ToLower(strings.TrimSpace(raw))
	if m == "" || len(m) > 32 {
		return nil
	}
	for _, r := range m {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return nil
		}
	}
	return &m
}
