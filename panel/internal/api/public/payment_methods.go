// [INPUT]: 依赖 platform 的 db/httpx，只读 payment_providers
// [OUTPUT]: 对外提供 handlers.listPaymentMethods
// [POS]: api/public 的可用支付方式（契约门户外壳 GET v1/payment-methods）：结账页与充值下拉的选项，一个渠道按 config.methods 展开成多行
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
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

// listPaymentMethods 列出能用来下单的支付方式：启用且接受新支付的渠道
// （人工单专用渠道 accepting_new=false，自然不在里面）。方式取渠道 config.methods，
// 没配就用 default_method；两者都没有时渠道本身算一行，method 为空串由渠道决定。
func (h *handlers) listPaymentMethods(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	out := []paymentMethod{}
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: p.TenantID, ActorID: p.UserID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT pp.code, pp.display_name, pp.supported_currencies::text[],
			       coalesce(m.method, '')
			  FROM payment_providers pp
			  LEFT JOIN LATERAL (
			        SELECT value AS method, ord
			          FROM jsonb_array_elements_text(
			                 CASE WHEN jsonb_typeof(pp.config->'methods') = 'array'
			                           AND jsonb_array_length(pp.config->'methods') > 0
			                      THEN pp.config->'methods'
			                      WHEN coalesce(pp.config->>'default_method', '') <> ''
			                      THEN jsonb_build_array(pp.config->>'default_method')
			                      ELSE '[""]'::jsonb END) WITH ORDINALITY AS t(value, ord)) m ON true
			 WHERE pp.tenant_id = $1 AND pp.enabled AND pp.accepting_new
			 ORDER BY pp.code, m.ord`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m paymentMethod
			var display string
			if err := rows.Scan(&m.Provider, &display, &m.Currencies, &m.Method); err != nil {
				return err
			}
			m.Label = display
			if label, ok := paymentMethodLabels[m.Method]; ok {
				m.Label = label
			}
			if m.Currencies == nil {
				m.Currencies = []string{}
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, map[string]any{"methods": out})
}
