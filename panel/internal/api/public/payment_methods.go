// [INPUT]: 依赖 domain/billing 的 PaymentMethods（只读 payment_providers 的 SQL 在那里），依赖 platform/httpx
// [OUTPUT]: 对外提供 handlers.listPaymentMethods
// [POS]: api/public 的可用支付方式（契约门户外壳 GET v1/payment-methods）：结账页与充值下拉的选项，一个渠道按 config.methods 展开成多行

package public

import (
	"net/http"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// paymentMethodLabels 是常见渠道内方式的中文名；不认识的方式用渠道的显示名。
var paymentMethodLabels = map[string]string{
	"alipay": "支付宝",
	"wxpay":  "微信支付",
	"qqpay":  "QQ 钱包",
}

type paymentMethod struct {
	Provider   string   `json:"provider"`
	Method     string   `json:"method"`
	Label      string   `json:"label"`
	Currencies []string `json:"currencies"`
}

type paymentMethodsResponse struct {
	Methods []paymentMethod `json:"methods"`
}

// listPaymentMethods 列出能用来下单的支付方式：启用且接受新支付的渠道
// （人工单专用渠道 accepting_new=false，自然不在里面）。方式取渠道 config.methods，
// 没配就用 default_method；两者都没有时渠道本身算一行，method 为空串由渠道决定。
func (h *handlers) listPaymentMethods(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	rows, err := h.d.Billing.PaymentMethods(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	out := []paymentMethod{}
	for _, row := range rows {
		m := paymentMethod{Provider: row.Provider, Method: row.Method, Label: row.DisplayName, Currencies: row.Currencies}
		if label, ok := paymentMethodLabels[m.Method]; ok {
			m.Label = label
		}
		if m.Currencies == nil {
			m.Currencies = []string{}
		}
		out = append(out, m)
	}
	httpx.OK(w, paymentMethodsResponse{Methods: out})
}
