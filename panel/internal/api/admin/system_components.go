// [INPUT]: 依赖 platform/db 的租户事务（节点、payment_events 未处理的支付回调、通知投递）、Deps.Redis 的 PING、platform/realtime 的 SSEConnections，依赖 system_status.go 的 backupStatus 结果
// [OUTPUT]: 对外提供 systemComponent 与 handlers.systemComponents
// [POS]: api/admin 系统状态的组件清单（契约后台-01 GET v1/system/status 的 state / components）：8 个组件各自 ok / warn / down / unknown，任一 warn 或 down 总状态即 degraded
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

type systemComponent struct {
	Key       string         `json:"key"`
	State     string         `json:"state"`
	LatencyMS *float64       `json:"latency_ms,omitempty"`
	Metrics   map[string]any `json:"metrics"`
	Message   string         `json:"message,omitempty"`
}

func millis(d time.Duration) *float64 {
	v := float64(d.Microseconds()) / 1000
	return &v
}

// systemComponents 逐项探测。任何一项失败只把那一项标成 down / unknown，
// 不让整个接口报错：系统状态页恰恰要在部分组件坏掉时还能打开。
func (h *handlers) systemComponents(r *http.Request, database map[string]any, backup map[string]any) (string, []systemComponent) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tenantID := httpx.TenantIDFrom(r.Context())
	var out []systemComponent

	// --- postgres：一次 SELECT 1 的往返 ---
	pg := systemComponent{Key: "postgres", State: "ok", Metrics: map[string]any{}}
	start := time.Now()
	var one int
	if err := h.d.Pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		pg.State, pg.Message = "down", "数据库不可达"
	} else {
		pg.LatencyMS = millis(time.Since(start))
		for _, k := range []string{"size_bytes", "connections", "max_connections"} {
			pg.Metrics[k] = database[k]
		}
	}
	out = append(out, pg)

	// --- valkey：PING ---
	vk := systemComponent{Key: "valkey", State: "unknown", Metrics: map[string]any{}}
	if h.d.Redis != nil {
		start = time.Now()
		if err := h.d.Redis.Ping(ctx).Err(); err != nil {
			vk.State, vk.Message = "down", "Valkey 不可达"
		} else {
			vk.State, vk.LatencyMS = "ok", millis(time.Since(start))
		}
	}
	out = append(out, vk)

	// --- 库里的三组计数：节点、支付回调、通知投递（按渠道） ---
	nodes := systemComponent{Key: "node_fabric", State: "unknown", Metrics: map[string]any{}}
	callbacks := systemComponent{Key: "payment_callbacks", State: "unknown", Metrics: map[string]any{}}
	channels := map[string]*systemComponent{
		"email":    {Key: "mail", State: "unknown", Metrics: map[string]any{}},
		"telegram": {Key: "telegram", State: "unknown", Metrics: map[string]any{}},
	}
	_ = h.d.Pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var total, online, lagging int64
		if err := tx.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE last_heartbeat_at >= now() - interval '90 seconds'),
			       count(*) FILTER (WHERE applied_config_version < desired_config_version)
			  FROM nodes
			 WHERE tenant_id = $1 AND status <> 'destroyed' AND serving_status <> 'retired'`,
			tenantID).Scan(&total, &online, &lagging); err == nil {
			nodes.Metrics = map[string]any{"total": total, "online": online, "config_lagging": lagging}
			nodes.State = "ok"
			if online < total || lagging > 0 {
				nodes.State = "warn"
			}
		}
		// 契约写的来源是 00036 建的回调收据表，没有任何代码写入、登记为孤儿表
		// （RESERVED-TABLES.md），回调收据实际落在 payment_events：pending / failed
		// 且收到超过 1 分钟仍未处理的，才是卡住的回调
		var pending int64
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM payment_events
			 WHERE tenant_id = $1 AND processing_status IN ('pending','failed')
			   AND received_at < now() - interval '1 minute'`, tenantID).Scan(&pending); err == nil {
			callbacks.Metrics = map[string]any{"pending": pending}
			callbacks.State = "ok"
			if pending > 0 {
				callbacks.State = "warn"
			}
		}
		for channel, c := range channels {
			var queued, retrying, failed int64
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FILTER (WHERE status = 'queued'),
				       count(*) FILTER (WHERE status = 'queued' AND attempts > 0),
				       count(*) FILTER (WHERE status = 'failed')
				  FROM notification_deliveries WHERE tenant_id = $1 AND channel = $2`,
				tenantID, channel).Scan(&queued, &retrying, &failed); err != nil {
				continue
			}
			c.Metrics = map[string]any{"queued": queued, "retrying": retrying, "failed_total": failed}
			// failed_total 是累计值，只拿「正在重试」判断眼下有没有投递问题
			c.State = "ok"
			if retrying > 0 {
				c.State = "warn"
			}
		}
		return nil
	})
	out = append(out, nodes, callbacks, *channels["email"], *channels["telegram"])

	// --- sse：各网关进程上报到 Valkey 的连接数之和 ---
	sse := systemComponent{Key: "sse", State: "unknown", Metrics: map[string]any{}}
	if h.d.Redis != nil {
		if n, err := realtime.SSEConnections(ctx, h.d.Redis); err == nil {
			sse.State, sse.Metrics = "ok", map[string]any{"connections": n}
		}
	}
	out = append(out, sse)

	// --- backup：由备份目录的探测结果派生 ---
	bk := systemComponent{Key: "backup", State: "unknown", Metrics: map[string]any{}}
	if readable, _ := backup["readable"].(bool); readable {
		stale, _ := backup["stale"].(bool)
		identity, _ := backup["identity_configured"].(bool)
		offsite, _ := backup["offsite_configured"].(bool)
		missing, _ := backup["missing_checksum"].(int)
		bk.Metrics = map[string]any{"latest_age_hours": backup["latest_age_hours"], "stale": stale,
			"identity_configured": identity, "offsite_configured": offsite}
		bk.State = "ok"
		if stale || !identity || !offsite || missing > 0 {
			bk.State = "warn"
		}
	} else if msg, _ := backup["message"].(string); msg != "" {
		bk.Message = msg
	}
	out = append(out, bk)

	state := "ok"
	for _, c := range out {
		if c.State == "warn" || c.State == "down" {
			state = "degraded"
		}
	}
	return state, out
}
