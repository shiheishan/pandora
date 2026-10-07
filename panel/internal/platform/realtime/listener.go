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
	// quota_balances 已移出变更通知（迁移 00076）：它随每次流量上报更新、
	// 又没有 user_id，挂着就是给全租户的高频广播
	case "subscriptions", "subscription_credentials", "traffic_pack_grants":
		return "subscriptions.changed"
	case "tickets", "ticket_messages":
		return "tickets.changed"
	case "plans", "plan_versions", "prices", "traffic_packs":
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
// 它从池里借一条连接独占：LISTEN 是连接级的状态，监听期间不能还回去。
// 退出时这条连接直接销毁而不是还回池里（见 listenOnce），否则带着 LISTEN 的
// 连接会被别的请求借走，通知在它的缓冲里越积越多。
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
	// 不还回池：从池里摘下并关闭。连接池归还时不做任何清理（db.OpenWithOptions），
	// 会话级的 LISTEN 一旦回到池里就会跟着连接被复用。池会按需补建新连接。
	defer func() {
		raw := conn.Hijack()
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = raw.Close(closeCtx)
	}()

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
		for _, channel := range channelsFor(c) {
			hub.Publish(ctx, channel, topic, payload)
		}
	}
}

// adminOnlyTables 是变更只推管理端、不推门户公共频道的表。
//
// nodes：门户只在「我的订阅」里列出可用节点（名称 / 协议 / 倍率），改为每 60 秒定时
// 重拉（portal/screens/common/subscriptions.ts 的 useSubscriptionNodes），不再听
// nodes.changed。原先节点行没有 user_id，每一次节点写入都广播给全租户每一条门户连接，
// 连同节点 UUID 一起——门户用户本不该知道套餐外节点的存在与变动时刻。只推管理端，
// 门户的扇出与订阅页的重拉风暴一起消失。
var adminOnlyTables = map[string]bool{"nodes": true}

// channelsFor 决定一条变更推到哪些频道。
//
// 有归属的变更只推给本人，没有的推给整个租户（adminOnlyTables 里的表除外）。
//
// 这个判断是权限边界：把一条带 user_id 的变更误推成全租户广播，
// 等于告诉所有人「某某刚下了单」。所以宁可判断得保守 ——
// 只要行里有 user_id，就只发给他一个人。
//
// 管理端另抄一份：后台要看到租户内所有人的动静。
func channelsFor(c change) []string {
	switch {
	case c.User != "":
		return []string{ChannelUser(c.Tenant, c.User), ChannelAdmin(c.Tenant)}
	case adminOnlyTables[c.Table]:
		return []string{ChannelAdmin(c.Tenant)}
	default:
		return []string{ChannelPublic(c.Tenant), ChannelAdmin(c.Tenant)}
	}
}
