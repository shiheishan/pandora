package realtime

// 数据库变更监听。
//
// PostgreSQL 的触发器把每次写入通过 pg_notify 发出来，这里接住它，
// 转成实时事件推给对应的浏览器。整条链路是：
//
//	写入任意一张表
//	  → 触发器 app.notify_change()
//	  → pg_notify('aegis_change', …)
//	  → 这个监听器
//	  → Valkey 广播（让所有实例的连接都能收到）
//	  → SSE
//	  → 前端重新拉数据
//
// 好处是覆盖面：不管这次写入来自 API、后台任务还是运维手工执行的 SQL，
// 界面都会跟着变。代价是多了一条要照看的链路，所以下面对每种失败
// 都做了明确处理，而不是让它静默停摆 —— 一个不再推送的监听器
// 从外部看和「系统很安静」完全一样。

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// change 是触发器发来的载荷。
type change struct {
	Table  string `json:"tbl"`
	Op     string `json:"op"`
	Tenant string `json:"tenant"`
	User   string `json:"user"`
	ID     string `json:"id"`
}

// topicFor 把表名映射成前端认得的主题。
//
// 前端不需要知道数据库有哪些表，它只关心「哪一块要刷新」。
// 这层映射让表结构调整不至于波及前端。
func topicFor(table string) string {
	switch table {
	case "orders":
		return "orders.changed"
	case "subscriptions", "subscription_credentials", "quota_balances":
		return "subscriptions.changed"
	case "tickets", "ticket_messages":
		return "tickets.changed"
	case "plans", "plan_versions", "prices":
		return "plans.changed"
	case "nodes":
		return "nodes.changed"
	case "announcements":
		return "announcements.changed"
	default:
		// 兜底。前端对任何事件的反应都是重新拉数据，所以落到这里
		// 不影响正确性，只是日志里看不出是哪一类变更。
		return "data.changed"
	}
}

// StartDBListener 起一个后台监听，把数据库变更转成实时事件。
//
// 它自己持有一条独立连接：LISTEN 是连接级的状态，
// 从连接池里借一条用完还回去的话，还回去的瞬间监听就断了。
func StartDBListener(ctx context.Context, pool *pgxpool.Pool, hub *Hub, log *slog.Logger) {
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			if err := listenOnce(ctx, pool, hub, log); err != nil && ctx.Err() == nil {
				// 连接断了就重来。这里必须无限重试 ——
				// 放弃的话页面会永远停在旧数据上，而且没有任何报错，
				// 用户只会觉得「这个面板怎么老是要刷新」。
				log.Warn("数据库变更监听中断，5 秒后重连", "err", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}
	}()
}

func listenOnce(ctx context.Context, pool *pgxpool.Pool, hub *Hub, log *slog.Logger) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN aegis_change"); err != nil {
		return err
	}
	log.Info("数据库变更监听已就绪")

	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var c change
		if err := json.Unmarshal([]byte(n.Payload), &c); err != nil {
			continue
		}
		if c.Tenant == "" {
			continue
		}

		topic := topicFor(c.Table)
		payload := map[string]any{"table": c.Table, "op": c.Op}
		if c.ID != "" {
			payload["id"] = c.ID
		}

		// 有归属的变更只推给本人，没有的推给整个租户。
		//
		// 这个判断是权限边界：把一条带 user_id 的变更误推成全租户广播，
		// 等于告诉所有人「某某刚下了单」。所以宁可判断得保守 ——
		// 只要行里有 user_id，就只发给他一个人。
		if c.User != "" {
			hub.Publish(ctx, ChannelUser(c.Tenant, c.User), topic, payload)
		} else {
			hub.Publish(ctx, ChannelPublic(c.Tenant), topic, payload)
		}
		// 管理端另抄一份：后台要看到租户内所有人的动静
		hub.Publish(ctx, ChannelAdmin(c.Tenant), topic, payload)
	}
}
