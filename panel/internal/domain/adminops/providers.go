package adminops

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//==============================================================================
// 支付渠道
//==============================================================================

type ProviderRow struct {
	ID           string   `json:"id"`
	Code         string   `json:"code"`
	Adapter      string   `json:"adapter"`
	DisplayName  string   `json:"display_name"`
	Enabled      bool     `json:"enabled"`
	AcceptingNew bool     `json:"accepting_new"`
	HasCreds     bool     `json:"has_credentials"`
	BaseURL      string   `json:"base_url"`
	Currencies   []string `json:"currencies"`
	// 编辑表单回填用的非机密配置（w2pay）。商户号与密钥只写不读，这里只有 has_credentials。
	// Methods 是 config.methods 原样（没配为空数组），门户按它出方式，空时退回 default_method。
	SubmitPath       string   `json:"submit_path"`
	APIPath          string   `json:"api_path"`
	Methods          []string `json:"methods"`
	DefaultMethod    string   `json:"default_method"`
	AllowPrivateHost bool     `json:"allow_private_host"`
	// MinAmount 是这个渠道的最低付款额（分，购买模型统一 2.6）：config.min_amount（1–100000），
	// 没配或不合法时易支付 100、其它渠道 0。口径与 billing 的 minPaymentSQL 逐字相同（providerMinAmountSQL）
	MinAmount int64 `json:"min_amount"`
	// 渠道卡统计（后台-05）。Today 是租户时区今天成功入账的金额，按币种分开
	// ——与挂账合计同一个理由，分和美分不能相加。SuccessRate24h 是近 24 小时
	// 进入终态的支付尝试里成功的比例，没有样本为 nil；LastCallbackAt 是最近
	// 一条渠道回调（payment_events）的接收时间。
	Today          map[string]int64 `json:"today"`
	SuccessRate24h *float64         `json:"success_rate_24h"`
	LastCallbackAt *time.Time       `json:"last_callback_at"`
}

// providerMinAmountSQL 是一个渠道（别名 pp）的最低付款额（分）。站点「能不能在线付」的门槛是
// 启用且接单的 CNY 渠道里对这个值做的聚合（billing 的 minPaymentSQL），两处必须同一个 CASE：
// 守卫 providers_test.go 的 TestProviderMinAmountMatchesBilling。
const providerMinAmountSQL = `CASE
	         WHEN coalesce(pp.config->>'min_amount', '') ~ '^[0-9]{1,6}$'
	              AND (pp.config->>'min_amount')::bigint BETWEEN 1 AND 100000
	           THEN (pp.config->>'min_amount')::bigint
	         WHEN pp.adapter = 'epay' THEN 100
	         ELSE 0 END::bigint`

func (s *Service) ListProviders(ctx context.Context, tenantID string) ([]ProviderRow, error) {
	out := []ProviderRow{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT pp.id, pp.code, pp.adapter, pp.display_name, pp.enabled, pp.accepting_new,
			       pp.credentials_encrypted IS NOT NULL,
			       coalesce(pp.config->>'base_url', ''),
			       coalesce(pp.supported_currencies, '{}'),
			       coalesce(pp.config->>'submit_path', ''),
			       coalesce(pp.config->>'api_path', ''),
			       CASE WHEN jsonb_typeof(pp.config->'methods') = 'array'
			            THEN ARRAY(SELECT jsonb_array_elements_text(pp.config->'methods'))
			            ELSE '{}'::text[] END,
			       coalesce(pp.config->>'default_method', ''),
			       coalesce(pp.config->'allow_private_host' = 'true'::jsonb, false),
			       `+providerMinAmountSQL+`,
			       coalesce((SELECT jsonb_object_agg(d.currency, d.amount)
			                   FROM (SELECT p.currency::text AS currency, sum(p.amount) AS amount
			                           FROM payments p
			                          WHERE p.tenant_id = pp.tenant_id AND p.provider_id = pp.id
			                            AND p.status = 'succeeded'
			                            AND (p.paid_at AT TIME ZONE t.timezone)::date
			                                = (now() AT TIME ZONE t.timezone)::date
			                          GROUP BY p.currency) d), '{}'::jsonb),
			       (SELECT (count(*) FILTER (WHERE pi.status = 'succeeded'))::float8
			               / nullif(count(*), 0)
			          FROM payment_intents pi
			         WHERE pi.tenant_id = pp.tenant_id AND pi.provider_id = pp.id
			           AND pi.status IN ('succeeded','failed','cancelled','expired')
			           AND pi.updated_at > now() - interval '24 hours'),
			       (SELECT max(e.received_at) FROM payment_events e
			         WHERE e.tenant_id = pp.tenant_id AND e.provider_id = pp.id)
			  FROM payment_providers pp
			  JOIN tenants t ON t.id = pp.tenant_id
			 WHERE pp.tenant_id = $1 ORDER BY pp.code`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p ProviderRow
			if err := rows.Scan(&p.ID, &p.Code, &p.Adapter, &p.DisplayName,
				&p.Enabled, &p.AcceptingNew, &p.HasCreds, &p.BaseURL,
				&p.Currencies, &p.SubmitPath, &p.APIPath, &p.Methods, &p.DefaultMethod,
				&p.AllowPrivateHost, &p.MinAmount, &p.Today, &p.SuccessRate24h, &p.LastCallbackAt); err != nil {
				return err
			}
			if p.Methods == nil {
				p.Methods = []string{}
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}

// SetProviderEnabled 启停渠道。
// accepting_new 单独控制：渠道故障时先停收单但保留回调处理能力（PAY-009）。
func (s *Service) SetProviderEnabled(ctx context.Context, tenantID, actorID, code string, enabled, acceptingNew bool) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var id string
		var beforeEnabled, beforeAccepting bool
		if err := tx.QueryRow(ctx,
			`SELECT id, enabled, accepting_new FROM payment_providers
			  WHERE tenant_id = $1 AND code = $2 FOR UPDATE`,
			tenantID, code).Scan(&id, &beforeEnabled, &beforeAccepting); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}

		if _, err := tx.Exec(ctx,
			`UPDATE payment_providers SET enabled = $2, accepting_new = $3 WHERE id = $1`,
			id, enabled, acceptingNew); err != nil {
			return err
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "payment_provider.toggle", ResourceType: "payment_provider", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
			BeforeDigest: map[string]any{"enabled": beforeEnabled, "accepting_new": beforeAccepting},
			AfterDigest:  map[string]any{"enabled": enabled, "accepting_new": acceptingNew},
			RequestID:    httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
			return err
		}
		return httpx.Internal(err)
	}
	return nil
}
