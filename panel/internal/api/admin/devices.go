// [INPUT]: 依赖 platform/db 的租户事务、platform/audit 的审计写入、platform/httpx 的响应与错误
// [OUTPUT]: 对外提供 handlers 的 listOnlineDevices / setDeviceLimit / setDeviceMode 三个处理器
// [POS]: api/admin 的设备数限制接口：在线概览、单订阅覆盖、全局判定模式；两条写接口都写审计，订阅不存在回 404
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

// 设备数限制的管理接口。
//
// 三件事：看谁超了、调某条订阅的额度、切换判定模式。

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
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
	if _, err := uuid.Parse(subID); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}

	actor := httpx.PrincipalFrom(r.Context()).UserID
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			// 先锁住读出旧值：订阅不存在要回 404（原先 UPDATE 影响 0 行也回 200），
			// 审计也要记下改之前是多少。
			var before *int
			if err := tx.QueryRow(r.Context(), `
				SELECT device_limit FROM subscriptions
				 WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`,
				tenantID, subID).Scan(&before); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.NotFoundOrForbidden()
				}
				return err
			}
			// 传 nil 就把覆盖清掉，回到套餐规定。
			// 用一个单独的「恢复默认」语义而不是让管理员手填套餐值：
			// 套餐额度日后调整时，手填的那些不会跟着变，会悄悄变成过期配置。
			if _, err := tx.Exec(r.Context(), `
				UPDATE subscriptions SET device_limit = $3
				 WHERE tenant_id = $1 AND id = $2::uuid`,
				tenantID, subID, req.Limit); err != nil {
				return err
			}
			return audit.Write(r.Context(), tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "subscription.device_limit_changed", ResourceType: "subscription",
				ResourceID: &subID, APIDomain: "admin", Outcome: "success",
				RequestID:    httpx.RequestIDFrom(r.Context()),
				BeforeDigest: map[string]any{"device_limit": before},
				AfterDigest:  map[string]any{"device_limit": req.Limit},
			})
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
			// 模式切换影响全租户能否连上，必须留下是谁在什么时候切的
			return audit.Write(r.Context(), tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "device_limit.mode_changed", ResourceType: "system_settings",
				APIDomain: "admin", Outcome: "success",
				RequestID:   httpx.RequestIDFrom(r.Context()),
				AfterDigest: map[string]any{"mode": req.Mode, "grace": req.Grace},
			})
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.d.Log.Info("管理员切换设备限制模式", "mode", req.Mode)
	httpx.OK(w, map[string]any{"ok": true})
}
