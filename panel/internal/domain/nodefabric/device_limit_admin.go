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
	SubscriptionID string `json:"subscription_id"`
	// UserID 是订阅主人，前端凭它直接打开用户抽屉，不用再按邮箱搜（backlog 第 2 条）
	UserID     string     `json:"user_id"`
	Email      string     `json:"email"`
	Plan       string     `json:"plan"`
	Limit      int        `json:"limit"`
	Online     int        `json:"online"`
	Nodes      int        `json:"nodes"`
	Overridden bool       `json:"overridden"`
	Exceeded   bool       `json:"exceeded"`
	LastSeenAt *time.Time `json:"last_seen_at"`
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

// onlineDevicesSQL 是在线设备概览的主查询，$1 租户：在用订阅按在线设备数从多到少、
// 同数按创建时间从新到旧，取前 200 条。
//
// 先聚合、取前 200，最后才对这 200 行拼用户与套餐。原先从全部在用订阅出发，每条订阅
// 走一次在线设备视图的 LATERAL，再按 LATERAL 算出来的设备数排序取 200：排序键是聚合
// 结果，「先按索引取一页 id」套不上，5000 条在用订阅要先全部算一遍（每次约 2.3 万个
// 缓冲块，5k-r3 p50 171ms）。现在：
//   - online：窗口内的在线记录按订阅一次 GROUP BY（全租户只扫一遍 node_alive_ips）；
//   - ranked：有在线记录的在用订阅（至多几百条），并上「没有在线记录」的在用订阅按
//     创建时间取前 200 条补齐——设备数都是 0，排在有在线记录的后面，同数按创建时间，
//     与原来的全量排序一致；补齐那一支走 00120 的部分索引，取够 200 条就停；
//   - page：合并后取前 200，最后才 JOIN 套餐版本、套餐与用户。
//
// 口径与在线设备视图（00098）相同：窗口取 app.device_limit_window_minutes（标量子查询，
// 整条语句只算一次），按 IP 去重计设备、按节点去重计节点。
const onlineDevicesSQL = `
	WITH online AS MATERIALIZED (
	        SELECT x.subscription_id,
	               count(DISTINCT x.ip_hash) AS device_count,
	               count(DISTINCT x.node_id) AS node_count,
	               max(x.last_seen_at)       AS last_seen_at
	          FROM node_alive_ips x
	         WHERE x.tenant_id = $1
	           AND x.last_seen_at > now() - make_interval(mins =>
	                 (SELECT app.device_limit_window_minutes($1)))
	         GROUP BY x.subscription_id
	), ranked AS (
	        SELECT s.id, s.user_id, s.plan_id, s.plan_version_id, s.device_limit, s.created_at,
	               o.device_count, o.node_count, o.last_seen_at
	          FROM online o
	          JOIN subscriptions s ON s.tenant_id = $1 AND s.id = o.subscription_id
	         WHERE s.status IN ('active','trialing','grace')
	        UNION ALL
	        (SELECT s.id, s.user_id, s.plan_id, s.plan_version_id, s.device_limit, s.created_at,
	                0::bigint, 0::bigint, NULL::timestamptz
	           FROM subscriptions s
	          WHERE s.tenant_id = $1
	            AND s.status IN ('active','trialing','grace')
	            AND NOT EXISTS (SELECT 1 FROM online o WHERE o.subscription_id = s.id)
	          ORDER BY s.created_at DESC
	          LIMIT 200)
	), page AS (
	        SELECT * FROM ranked
	         ORDER BY device_count DESC, created_at DESC
	         LIMIT 200
	)
	SELECT page.id::text, page.user_id::text, COALESCE(u.email,''), COALESCE(p.name,''),
	       COALESCE(page.device_limit, pv.max_devices, 0),
	       page.device_count, page.node_count,
	       (page.device_limit IS NOT NULL),
	       page.last_seen_at
	  FROM page
	  JOIN plan_versions pv ON pv.id = page.plan_version_id
	  LEFT JOIN plans p ON p.id = page.plan_id
	  LEFT JOIN users u ON u.id = page.user_id
	 ORDER BY page.device_count DESC, page.created_at DESC`

// OnlineDevicesOverviewSQL 返回概览主查询原文（$1 租户），供 PG18 对照用例 EXPLAIN。
func OnlineDevicesOverviewSQL() string { return onlineDevicesSQL }

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

		rows, err := tx.Query(ctx, onlineDevicesSQL, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it OnlineDevice
			if err := rows.Scan(&it.SubscriptionID, &it.UserID, &it.Email, &it.Plan,
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
