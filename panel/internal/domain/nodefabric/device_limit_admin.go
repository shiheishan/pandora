// [INPUT]: 依赖 uniproxy.go 的 DeviceWindowMinutes，依赖 platform 的 audit/db/httpx，读写 system_settings 的 device_limit.*、subscriptions.device_limit，读 subscription_online_devices 视图与迁移 00094 的 app.device_limit_window_minutes
// [OUTPUT]: 对外提供 OnlineDevice、DeviceOverview、DeviceLimitPolicyInput 与 Service 的 ListOnlineDevices / SetSubscriptionDeviceLimit / SetDeviceLimitPolicy
// [POS]: domain/nodefabric 的设备数限制后台用例（从 api/admin 的 devices.go 下沉）：判定模式、宽容值与识别窗口的执行方就是本包的 UniProxy 用户下发，读写放在同一处；窗口只经库函数读（R103），两条写都同事务审计，订阅不存在回 404

package nodefabric

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// OnlineDevice 是在线设备概览的一行（一条在用订阅）。
type OnlineDevice struct {
	SubscriptionID string     `json:"subscription_id"`
	Email          string     `json:"email"`
	Plan           string     `json:"plan"`
	Limit          int        `json:"limit"`
	Online         int        `json:"online"`
	Nodes          int        `json:"nodes"`
	Overridden     bool       `json:"overridden"`
	Exceeded       bool       `json:"exceeded"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
}

// DeviceOverview 是在线设备概览与当前生效的判定策略。
type DeviceOverview struct {
	Devices       []OnlineDevice
	Mode          string
	Grace         int
	WindowMinutes int
}

// DeviceLimitPolicyInput 是全局判定策略的已校验输入；Grace / WindowMinutes 为 nil 表示不改。
type DeviceLimitPolicyInput struct {
	ActorID       string
	Mode          string
	Grace         *int
	WindowMinutes *int
}

// ListOnlineDevices 返回当前在线设备概览，超限的排在前面。
func (s *Service) ListOnlineDevices(ctx context.Context, tenantID string) (*DeviceOverview, error) {
	out := []OnlineDevice{}
	mode := "loose"
	grace := 1
	window := DeviceWindowMinutes[0]

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id = $1 AND key = 'device_limit.mode'), 'loose'),
			       COALESCE((SELECT (value #>> '{}')::int FROM system_settings
			                  WHERE tenant_id = $1 AND key = 'device_limit.grace'), 1)`,
			tenantID).Scan(&mode, &grace)
		// 窗口取库里的同一个函数：视图按它算在线数，这里回显的就是在线数实际用的窗口。
		// 函数自己把缺行与非法值折成 5，读不出来只可能是库坏了，照实报错。
		if err := tx.QueryRow(ctx,
			`SELECT app.device_limit_window_minutes($1)`, tenantID).Scan(&window); err != nil {
			return err
		}

		// 用 LEFT JOIN 而不是从视图出发：没有人在线的订阅也要能看到，
		// 否则「这个用户到底几台设备」这个问题在他离线时就查不了了
		rows, err := tx.Query(ctx, `
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
			var it OnlineDevice
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
		return nil, err
	}
	return &DeviceOverview{Devices: out, Mode: mode, Grace: grace, WindowMinutes: window}, nil
}

// SetSubscriptionDeviceLimit 调整某条订阅的设备数额度；limit 为 nil 表示恢复成套餐规定。
func (s *Service) SetSubscriptionDeviceLimit(ctx context.Context, tenantID, actor, subID string, limit *int) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			// 先锁住读出旧值：订阅不存在要回 404（原先 UPDATE 影响 0 行也回 200），
			// 审计也要记下改之前是多少。
			var before *int
			if err := tx.QueryRow(ctx, `
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
			if _, err := tx.Exec(ctx, `
				UPDATE subscriptions SET device_limit = $3
				 WHERE tenant_id = $1 AND id = $2::uuid`,
				tenantID, subID, limit); err != nil {
				return err
			}
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "subscription.device_limit_changed", ResourceType: "subscription",
				ResourceID: &subID, APIDomain: "admin", Outcome: "success",
				RequestID:    httpx.RequestIDFrom(ctx),
				BeforeDigest: map[string]any{"device_limit": before},
				AfterDigest:  map[string]any{"device_limit": limit},
			})
		})
}

// SetDeviceLimitPolicy 切换判定模式，可选地改宽容值与设备识别窗口。
func (s *Service) SetDeviceLimitPolicy(ctx context.Context, tenantID string, in DeviceLimitPolicyInput) error {
	actor := in.ActorID
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `
				INSERT INTO system_settings (tenant_id, key, value, updated_by)
				VALUES ($1, 'device_limit.mode', to_jsonb($2::text), $3::uuid)
				ON CONFLICT (tenant_id, key)
				DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by,
				              version = system_settings.version + 1, updated_at = now()`,
				tenantID, in.Mode, actor); err != nil {
				return err
			}
			if in.Grace != nil {
				if _, err := tx.Exec(ctx, `
					INSERT INTO system_settings (tenant_id, key, value, updated_by)
					VALUES ($1, 'device_limit.grace', to_jsonb($2::int), $3::uuid)
					ON CONFLICT (tenant_id, key)
					DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by,
					              version = system_settings.version + 1, updated_at = now()`,
					tenantID, *in.Grace, actor); err != nil {
					return err
				}
			}
			var windowBefore int
			if in.WindowMinutes != nil {
				// 窗口决定 strict 模式下谁被当成超限，和模式同级：一起审计、一起挂 reauth
				if err := tx.QueryRow(ctx,
					`SELECT app.device_limit_window_minutes($1)`, tenantID).Scan(&windowBefore); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO system_settings (tenant_id, key, value, value_schema, updated_by)
					VALUES ($1, 'device_limit.window_minutes', to_jsonb($2::int),
					        '{"enum":[5,10,30,60]}'::jsonb, $3::uuid)
					ON CONFLICT (tenant_id, key)
					DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by,
					              version = system_settings.version + 1, updated_at = now()`,
					tenantID, *in.WindowMinutes, actor); err != nil {
					return err
				}
			}
			// 模式切换影响全租户能否连上，必须留下是谁在什么时候切的
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "device_limit.mode_changed", ResourceType: "system_settings",
				APIDomain: "admin", Outcome: "success",
				RequestID:    httpx.RequestIDFrom(ctx),
				BeforeDigest: deviceWindowBefore(in.WindowMinutes, windowBefore),
				AfterDigest: map[string]any{"mode": in.Mode, "grace": in.Grace,
					"window_minutes": in.WindowMinutes},
			})
		})
}

// deviceWindowBefore 只在这次改了窗口时记下改之前的生效值（缺行即 5）。
func deviceWindowBefore(requested *int, before int) any {
	if requested == nil {
		return nil
	}
	return map[string]any{"window_minutes": before}
}
