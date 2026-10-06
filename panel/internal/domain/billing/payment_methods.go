package billing

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// PaymentMethodRow 是一个渠道内的一种支付方式；Method 为空串表示交给渠道决定。
type PaymentMethodRow struct {
	Provider    string
	DisplayName string
	Currencies  []string
	Method      string
}

// PaymentMethods 列出能用来下单的支付方式：启用且接受新支付的渠道
// （人工单专用渠道 accepting_new=false，自然不在里面），按渠道代码与方式顺序排列。
func (s *Service) PaymentMethods(ctx context.Context, tenantID, userID string) ([]PaymentMethodRow, error) {
	var out []PaymentMethodRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
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
			 ORDER BY pp.code, m.ord`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m PaymentMethodRow
			if err := rows.Scan(&m.Provider, &m.DisplayName, &m.Currencies, &m.Method); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
