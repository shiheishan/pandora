// [INPUT]: 依赖 platform/db 的租户事务与 httpx，读 plans / plan_versions / quota_definitions / prices 与当前用户所在的用户组
// [OUTPUT]: 对外提供 handlers.listPlans（GET 套餐目录）
// [POS]: api/public 的套餐目录：从 handlers.go 拆出。只列当前用户可见、已发布且有可用币种（CNY / USD）适用价格的套餐，取当前发布版本的额度、限速 throttle_kbps（R99）与重置策略，带卖点 highlights 与推荐 recommended（R100）以及续费、变更开关
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// 目录与账户
//------------------------------------------------------------------------------

func (h *handlers) listPlans(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := httpx.TenantIDFrom(ctx)
	authed := !httpx.PrincipalFrom(ctx).IsAnonymous()
	// 分组套餐要知道「现在是谁在看」。未登录传 nil，
	// SQL 里那句 $3::uuid IS NOT NULL 会把整个分组分支短路掉
	var viewerID any
	if p := httpx.PrincipalFrom(ctx); p != nil && p.UserID != "" {
		viewerID = p.UserID
	}

	type priceView struct {
		ID            string `json:"id"`
		Currency      string `json:"currency"`
		UnitAmount    int64  `json:"unit_amount"`
		Interval      string `json:"billing_interval"`
		IntervalCount int16  `json:"interval_count"`
		TrialDays     int16  `json:"trial_days"`
	}
	// 套餐能给多少流量、几台设备，是用户选购时唯一真正关心的事。
	// 这些值来自当前 plan_version 的 quota_definitions，
	// 不能在前端写死 —— 换套餐版本时展示必须跟着变（SUB-002）。
	type quotaView struct {
		Metric string `json:"metric"`
		Limit  *int64 `json:"limit"`
		Unit   string `json:"unit"`
		Period string `json:"period"`
	}
	type planView struct {
		ID          string  `json:"id"`
		Code        string  `json:"code"`
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Version     *int    `json:"version"`
		MaxDevices  *int    `json:"max_devices"`
		// 限速（kbps）全程生效，null = 不限速，与超额策略无关（R99）
		ThrottleKbps *int `json:"throttle_kbps"`
		// 卖点列表与「推荐」徽标（R100），取套餐本身，不随版本
		Highlights  []string `json:"highlights"`
		Recommended bool     `json:"recommended"`
		// 卡片上的「每月 1 日重置」与续费 / 变更按钮要这几项，取当前发布版本与套餐开关
		QuotaResetStrategy string      `json:"quota_reset_strategy"`
		QuotaResetDay      *int16      `json:"quota_reset_day"`
		AllowRenewal       bool        `json:"allow_renewal"`
		AllowUpgrade       bool        `json:"allow_upgrade"`
		Quotas             []quotaView `json:"quotas"`
		Prices             []priceView `json:"prices"`
	}

	out := []planView{}

	err := h.d.Pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// XBD-011 可见性：匿名只看 public，登录后加 authenticated。
		// group / invite_only / hidden 一律不在此列出，
		// 且下单路径会独立复查，列表接口的过滤不是唯一防线。
		visibilities := []string{"public"}
		if authed {
			visibilities = append(visibilities, "authenticated")
		}
		// visibility='group' 的套餐由下面的 SQL 单独判断：
		// 它对组内的人可见，对其他人连存在都不该暴露

		rows, err := tx.Query(ctx, `
			SELECT p.id, p.code, p.name, p.description, pv.version, pv.max_devices, pv.throttle_kbps,
			       pv.quota_reset_strategy, pv.quota_reset_day, p.allow_renewal, p.allow_upgrade,
			       p.highlights, p.recommended
			  FROM plans p
			  JOIN plan_versions pv ON pv.tenant_id = p.tenant_id
			   AND pv.id = p.current_version_id
			   AND pv.status = 'published' AND pv.frozen_at IS NOT NULL
			 WHERE p.tenant_id = $1
			   AND p.status = 'active'
			   AND (
			     p.visibility = ANY($2)
			     OR (p.visibility = 'group' AND $3::uuid IS NOT NULL
			         AND EXISTS (SELECT 1 FROM users u
			                      WHERE u.tenant_id = p.tenant_id
			                        AND u.id = $3::uuid
			                        AND u.user_group_id = ANY(p.visible_group_ids)))
			   )
			   AND p.current_version_id IS NOT NULL
			   AND (p.visible_from  IS NULL OR p.visible_from  <= now())
			   AND (p.visible_until IS NULL OR p.visible_until >  now())
			   AND EXISTS (
			     SELECT 1 FROM prices offer
			      WHERE offer.tenant_id=p.tenant_id AND offer.product_id=p.product_id
			        AND offer.status='active' AND offer.currency IN ('CNY','USD')
			        AND (offer.valid_from IS NULL OR offer.valid_from<=now())
			        AND (offer.valid_until IS NULL OR offer.valid_until>now())
			        AND (offer.user_group_id IS NULL OR ($3::uuid IS NOT NULL AND EXISTS (
			          SELECT 1 FROM users price_viewer
			           WHERE price_viewer.tenant_id=p.tenant_id AND price_viewer.id=$3::uuid
			             AND price_viewer.user_group_id=offer.user_group_id)))
			   )
			 ORDER BY p.sort_order, p.created_at`,
			tenantID, visibilities, viewerID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var pv planView
			if err := rows.Scan(&pv.ID, &pv.Code, &pv.Name, &pv.Description, &pv.Version, &pv.MaxDevices, &pv.ThrottleKbps,
				&pv.QuotaResetStrategy, &pv.QuotaResetDay, &pv.AllowRenewal, &pv.AllowUpgrade,
				&pv.Highlights, &pv.Recommended); err != nil {
				return err
			}
			pv.Prices = []priceView{}
			pv.Quotas = []quotaView{}
			out = append(out, pv)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		for i := range out {
			// 配额跟随该套餐的当前版本：换版本时展示自动跟着变（SUB-002）
			qrows, err := tx.Query(ctx, `
				SELECT qd.metric, qd.limit_value, qd.unit, qd.period
				  FROM quota_definitions qd
				  JOIN plans pl ON pl.current_version_id = qd.plan_version_id
				 WHERE qd.tenant_id = $1 AND pl.id = $2
				 ORDER BY qd.metric`,
				tenantID, out[i].ID)
			if err != nil {
				return err
			}
			for qrows.Next() {
				var q quotaView
				if err := qrows.Scan(&q.Metric, &q.Limit, &q.Unit, &q.Period); err != nil {
					qrows.Close()
					return err
				}
				out[i].Quotas = append(out[i].Quotas, q)
			}
			qrows.Close()
			if err := qrows.Err(); err != nil {
				return err
			}

			prows, err := tx.Query(ctx, `
				SELECT pr.id, pr.currency, pr.unit_amount, pr.billing_interval,
				       pr.interval_count, pr.trial_days
				  FROM prices pr
				  JOIN plans pl ON pl.product_id = pr.product_id
				 WHERE pr.tenant_id = $1 AND pl.id = $2 AND pr.status = 'active'
				   AND pr.currency IN ('CNY','USD')
				   AND (pr.valid_from  IS NULL OR pr.valid_from  <= now())
				   AND (pr.valid_until IS NULL OR pr.valid_until >  now())
				   AND (
				     pr.user_group_id IS NULL
				     OR ($3::uuid IS NOT NULL AND EXISTS (
				       SELECT 1 FROM users u
				        WHERE u.tenant_id = pr.tenant_id
				          AND u.id = $3::uuid
				          AND u.user_group_id = pr.user_group_id
				     ))
				   )
				 ORDER BY pr.unit_amount`,
				tenantID, out[i].ID, viewerID)
			if err != nil {
				return err
			}
			for prows.Next() {
				var v priceView
				if err := prows.Scan(&v.ID, &v.Currency, &v.UnitAmount, &v.Interval,
					&v.IntervalCount, &v.TrialDays); err != nil {
					prows.Close()
					return err
				}
				out[i].Prices = append(out[i].Prices, v)
			}
			prows.Close()
			if err := prows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}

	httpx.OK(w, map[string]any{"plans": out})
}
