package admin

// 设备数限制的管理接口。
//
// 三件事：看谁超了、调某条订阅的额度、切换判定模式。

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// listOnlineDevices 返回当前在线设备概览，超限的排在前面。
func (h *handlers) listOnlineDevices(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())

	type row struct {
		SubscriptionID string `json:"subscription_id"`
		Email          string `json:"email"`
		Plan           string `json:"plan"`
		Limit          int    `json:"limit"`
		Online         int    `json:"online"`
		Nodes          int    `json:"nodes"`
		Overridden     bool   `json:"overridden"`
		Exceeded       bool   `json:"exceeded"`
		LastSeenAt     any    `json:"last_seen_at"`
	}
	out := []row{}
	mode := "loose"
	grace := 1

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		_ = tx.QueryRow(r.Context(), `
			SELECT COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id = $1 AND key = 'device_limit.mode'), 'loose'),
			       COALESCE((SELECT (value #>> '{}')::int FROM system_settings
			                  WHERE tenant_id = $1 AND key = 'device_limit.grace'), 1)`,
			tenantID).Scan(&mode, &grace)

		// 用 LEFT JOIN 而不是从视图出发：没有人在线的订阅也要能看到，
		// 否则「这个用户到底几台设备」这个问题在他离线时就查不了了
		rows, err := tx.Query(r.Context(), `
			SELECT s.id::text, COALESCE(u.email,''), COALESCE(p.name,''),
			       COALESCE(s.device_limit, pv.max_devices, 0),
			       COALESCE(d.device_count, 0), COALESCE(d.node_count, 0),
			       (s.device_limit IS NOT NULL),
			       d.last_seen_at
			  FROM subscriptions s
			  JOIN plan_versions pv ON pv.id = s.plan_version_id
			  LEFT JOIN plans p ON p.id = s.plan_id
			  LEFT JOIN users u ON u.id = s.user_id
			  LEFT JOIN subscription_online_devices d ON d.subscription_id = s.id
			 WHERE s.tenant_id = $1
			   AND s.status IN ('active','trialing','grace')
			 ORDER BY COALESCE(d.device_count,0) DESC, s.created_at DESC
			 LIMIT 200`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it row
			if err := rows.Scan(&it.SubscriptionID, &it.Email, &it.Plan,
				&it.Limit, &it.Online, &it.Nodes, &it.Overridden, &it.LastSeenAt); err != nil {
				return err
			}
			it.Exceeded = it.Limit > 0 && it.Online > it.Limit+grace
			out = append(out, it)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"devices": out, "mode": mode, "grace": grace})
}

type deviceLimitReq struct {
	// Limit 为 nil 表示恢复成套餐规定，0 表示不限制
	Limit *int `json:"limit"`
}

// setDeviceLimit 调整某条订阅的设备数额度。
func (h *handlers) setDeviceLimit(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	subID := chi.URLParam(r, "id")

	var req deviceLimitReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Limit != nil && (*req.Limit < 0 || *req.Limit > 1000) {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "设备数需在 0 到 1000 之间"))
		return
	}

	err := h.d.Pool.InTx(r.Context(), db.Scope{
		TenantID: tenantID,
		ActorID:  httpx.PrincipalFrom(r.Context()).UserID,
	}, func(tx pgx.Tx) error {
		// 传 nil 就把覆盖清掉，回到套餐规定。
		// 用一个单独的「恢复默认」语义而不是让管理员手填套餐值：
		// 套餐额度日后调整时，手填的那些不会跟着变，会悄悄变成过期配置。
		_, err := tx.Exec(r.Context(), `
			UPDATE subscriptions SET device_limit = $3
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, subID, req.Limit)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.d.Log.Info("管理员调整设备数限制", "subscription", subID, "limit", req.Limit)
	httpx.OK(w, map[string]any{"ok": true})
}

type deviceModeReq struct {
	Mode  string `json:"mode"`
	Grace *int   `json:"grace"`
}

// setDeviceMode 切换判定模式。
func (h *handlers) setDeviceMode(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req deviceModeReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Mode != "loose" && req.Mode != "strict" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "模式只能是 loose 或 strict"))
		return
	}
	if req.Grace != nil && (*req.Grace < 0 || *req.Grace > 5) {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "宽容值需在 0 到 5 之间"))
		return
	}

	actor := httpx.PrincipalFrom(r.Context()).UserID
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO system_settings (tenant_id, key, value, updated_by)
				VALUES ($1, 'device_limit.mode', to_jsonb($2::text), $3::uuid)
				ON CONFLICT (tenant_id, key)
				DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by,
				              version = system_settings.version + 1, updated_at = now()`,
				tenantID, req.Mode, actor); err != nil {
				return err
			}
			if req.Grace != nil {
				if _, err := tx.Exec(r.Context(), `
					INSERT INTO system_settings (tenant_id, key, value, updated_by)
					VALUES ($1, 'device_limit.grace', to_jsonb($2::int), $3::uuid)
					ON CONFLICT (tenant_id, key)
					DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by,
					              version = system_settings.version + 1, updated_at = now()`,
					tenantID, *req.Grace, actor); err != nil {
					return err
				}
			}
			return nil
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.d.Log.Info("管理员切换设备限制模式", "mode", req.Mode)
	httpx.OK(w, map[string]any{"ok": true})
}
