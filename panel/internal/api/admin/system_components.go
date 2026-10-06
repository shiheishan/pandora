package admin

import (
	"context"
	"net/http"
	"time"

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

// statusDeref 取指针指向的值，nil 给零值（与原先从 map 断言失败取零值同义）。
func statusDeref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

func millis(d time.Duration) *float64 {
	v := float64(d.Microseconds()) / 1000
	return &v
}

// postgresDatabaseKeys 是 postgres 组件 ok 时必须齐全的 metrics（契约 R52）。
var postgresDatabaseKeys = []string{"size_bytes", "connections", "max_connections"}

// postgresComponent 由 SELECT 1 的结果与数据库统计拼出 postgres 组件：
// 连不上为 down；连得上但统计没读到为 warn，metrics 里一个键都不写
// （而不是写成 null）；两者都好才是 ok，三个键齐全。
func postgresComponent(pingErr error, latency time.Duration, database map[string]any) systemComponent {
	pg := systemComponent{Key: "postgres", State: "ok", Metrics: map[string]any{}}
	if pingErr != nil {
		pg.State, pg.Message = "down", "数据库不可达"
		return pg
	}
	pg.LatencyMS = millis(latency)
	for _, k := range postgresDatabaseKeys {
		if _, ok := database[k]; !ok {
			pg.State, pg.Message = "warn", "数据库可以连通，但体积与连接数统计读取失败"
			return pg
		}
	}
	for _, k := range postgresDatabaseKeys {
		pg.Metrics[k] = database[k]
	}
	return pg
}

// systemComponents 逐项探测。任何一项失败只把那一项标成 down / unknown，
// 不让整个接口报错：系统状态页恰恰要在部分组件坏掉时还能打开。
func (h *handlers) systemComponents(r *http.Request, database map[string]any, backup backupStatusView) (string, []systemComponent) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tenantID := httpx.TenantIDFrom(r.Context())
	var out []systemComponent

	// --- postgres：一次 SELECT 1 的往返 ---
	start := time.Now()
	pingErr := h.d.Ops.PingDatabase(ctx)
	out = append(out, postgresComponent(pingErr, time.Since(start), database))

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
	counts := h.d.Ops.SystemCounts(ctx, tenantID, []string{"email", "telegram"})
	if n := counts.Nodes; n != nil {
		nodes.Metrics = map[string]any{"total": n.Total, "online": n.Online, "config_lagging": n.Lagging}
		nodes.State = "ok"
		if n.Online < n.Total || n.Lagging > 0 {
			nodes.State = "warn"
		}
	}
	if pending := counts.PendingCallbacks; pending != nil {
		callbacks.Metrics = map[string]any{"pending": *pending}
		callbacks.State = "ok"
		if *pending > 0 {
			callbacks.State = "warn"
		}
	}
	for channel, c := range channels {
		d, ok := counts.Deliveries[channel]
		if !ok {
			continue
		}
		c.Metrics = map[string]any{"queued": d.Queued, "retrying": d.Retrying, "failed_total": d.Failed}
		// failed_total 是累计值，只拿「正在重试」判断眼下有没有投递问题
		c.State = "ok"
		if d.Retrying > 0 {
			c.State = "warn"
		}
	}
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
	if backup.Readable {
		stale := statusDeref(backup.Stale)
		identity := statusDeref(backup.IdentityConfigured)
		offsite := statusDeref(backup.OffsiteConfigured)
		missing := statusDeref(backup.MissingChecksum)
		// 没有备份文件时 latest_age_hours 为 null（键在、值空）
		var age any
		if backup.LatestAgeHours != nil {
			age = *backup.LatestAgeHours
		}
		bk.Metrics = map[string]any{"latest_age_hours": age, "stale": stale,
			"identity_configured": identity, "offsite_configured": offsite}
		bk.State = "ok"
		if stale || !identity || !offsite || missing > 0 {
			bk.State = "warn"
		}
	} else if backup.Message != "" {
		bk.Message = backup.Message
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
