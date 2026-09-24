// [INPUT]: 依赖 platform/db 的租户事务，读 tickets / ticket_messages / withdrawals / nodes / orders / notification_deliveries 与 app.verify_ledger_all()；通知积压与 dashboard.go 共用 dashboardNotificationBacklogSQL
// [OUTPUT]: 对外提供 DashboardTasks、DashboardTaskKinds 与各项结构、Service.DashboardTasks
// [POS]: domain/adminops 的仪表盘「需要处理」汇总（契约后台-01 GET v1/dashboard/tasks）：一个事务里按调用方有权看的项逐项计数，没权限的项不查也不出现
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// DashboardTaskKinds 是每一项与看它所需的读权限，顺序即响应里的顺序。
// 权限与各项原有列表接口一致：没有 GET v1/tickets 的人，这里也看不到工单数。
var DashboardTaskKinds = []struct{ Kind, Permission string }{
	{"tickets_open", "ops.ticket.read"},
	// 分销读权限统一在 marketing.commission.read（与 GET v1/withdrawals 一致）
	{"withdrawals_pending", "marketing.commission.read"},
	{"nodes_offline", "node.read"},
	{"orders_pending_stale", "billing.order.read"},
	{"notifications_backlog", "ops.notification.read"},
	{"ledger_drift", "billing.ledger.read"},
}

// 超时未支付的门槛：下单 30 分钟后本该被过期作业处理，仍挂着说明作业没跑或卡住
const staleOrderSeconds = 1800

type DashboardTasks struct {
	AsOf  time.Time `json:"as_of"`
	Items []any     `json:"items"`
}

type TicketsOpenTask struct {
	Kind              string `json:"kind"`
	Count             int64  `json:"count"`
	HighPriority      int64  `json:"high_priority"`
	OldestWaitSeconds *int64 `json:"oldest_wait_seconds"`
}

type CurrencyAmount struct {
	Currency string `json:"currency"`
	Amount   int64  `json:"amount"`
}

type WithdrawalsPendingTask struct {
	Kind    string           `json:"kind"`
	Count   int64            `json:"count"`
	Amounts []CurrencyAmount `json:"amounts"`
}

type NodeRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type NodesOfflineTask struct {
	Kind                  string    `json:"kind"`
	Count                 int64     `json:"count"`
	Sample                []NodeRef `json:"sample"`
	LongestOfflineSeconds *int64    `json:"longest_offline_seconds"`
}

type OrdersPendingStaleTask struct {
	Kind             string `json:"kind"`
	Count            int64  `json:"count"`
	ThresholdSeconds int    `json:"threshold_seconds"`
}

type NotificationsBacklogTask struct {
	Kind         string `json:"kind"`
	Queued       int64  `json:"queued"`
	FailedTotal  int64  `json:"failed_total"`
	BacklogState string `json:"backlog_state"`
}

type LedgerDriftTask struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

// DashboardTasks 汇总「需要处理」。can 判断调用方是否持有某项的读权限。
func (s *Service) DashboardTasks(ctx context.Context, tenantID string, can func(string) bool) (*DashboardTasks, error) {
	out := &DashboardTasks{Items: []any{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&out.AsOf); err != nil {
			return err
		}
		for _, k := range DashboardTaskKinds {
			if !can(k.Permission) {
				continue
			}
			item, err := dashboardTask(ctx, tx, tenantID, k.Kind)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, item)
		}
		return nil
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}

func dashboardTask(ctx context.Context, tx pgx.Tx, tenantID, kind string) (any, error) {
	switch kind {
	case "tickets_open":
		// 等待时长从用户最后一次发言算起（没发言过就从建单算）：pending_agent 的
		// 工单往往建了很久，但用户是刚追问的
		t := TicketsOpenTask{Kind: kind}
		err := tx.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE t.priority IN ('high','urgent')),
			       floor(extract(epoch FROM now() - min(coalesce(m.last_user_at, t.created_at))))::bigint
			  FROM tickets t
			  LEFT JOIN LATERAL (
			        SELECT max(tm.created_at) AS last_user_at FROM ticket_messages tm
			         WHERE tm.tenant_id = t.tenant_id AND tm.ticket_id = t.id
			           AND tm.author_kind = 'user') m ON true
			 WHERE t.tenant_id = $1 AND t.status IN ('open','pending_agent','escalated')`,
			tenantID).Scan(&t.Count, &t.HighPriority, &t.OldestWaitSeconds)
		return t, err

	case "withdrawals_pending":
		t := WithdrawalsPendingTask{Kind: kind, Amounts: []CurrencyAmount{}}
		rows, err := tx.Query(ctx, `
			SELECT currency::text, count(*), sum(amount)::bigint
			  FROM withdrawals
			 WHERE tenant_id = $1 AND status IN ('requested','reviewing')
			 GROUP BY currency ORDER BY currency`, tenantID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var a CurrencyAmount
			var n int64
			if err := rows.Scan(&a.Currency, &n, &a.Amount); err != nil {
				return nil, err
			}
			t.Count += n
			t.Amounts = append(t.Amounts, a)
		}
		return t, rows.Err()

	case "nodes_offline":
		// 口径与 GET v1/nodes 的 stale 一致：没退役、心跳超过 90 秒（含从没心跳过）
		t := NodesOfflineTask{Kind: kind, Sample: []NodeRef{}}
		rows, err := tx.Query(ctx, `
			SELECT n.id::text, coalesce(nullif(n.display_name, ''), n.name),
			       count(*) OVER (),
			       floor(extract(epoch FROM now() - min(n.last_heartbeat_at) OVER ()))::bigint
			  FROM nodes n
			 WHERE n.tenant_id = $1 AND n.status <> 'destroyed' AND n.serving_status <> 'retired'
			   AND (n.last_heartbeat_at IS NULL OR n.last_heartbeat_at < now() - interval '90 seconds')
			 ORDER BY n.last_heartbeat_at ASC NULLS LAST, n.id
			 LIMIT 3`, tenantID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var n NodeRef
			if err := rows.Scan(&n.ID, &n.Name, &t.Count, &t.LongestOfflineSeconds); err != nil {
				return nil, err
			}
			t.Sample = append(t.Sample, n)
		}
		return t, rows.Err()

	case "orders_pending_stale":
		t := OrdersPendingStaleTask{Kind: kind, ThresholdSeconds: staleOrderSeconds}
		err := tx.QueryRow(ctx, `
			SELECT count(*) FROM orders
			 WHERE tenant_id = $1 AND status = 'pending_payment'
			   AND created_at < now() - make_interval(secs => $2)`,
			tenantID, staleOrderSeconds).Scan(&t.Count)
		return t, err

	case "notifications_backlog":
		b, err := scanNotificationBacklog(ctx, tx, tenantID)
		if err != nil {
			return nil, err
		}
		return NotificationsBacklogTask{Kind: kind,
			Queued:      b.Counts.Ready + b.Counts.Scheduled,
			FailedTotal: b.Counts.FailedTotal, BacklogState: b.BacklogState}, nil

	case "ledger_drift":
		// 与 overview.ledger_drift_accounts 同一个函数
		t := LedgerDriftTask{Kind: kind}
		err := tx.QueryRow(ctx, `SELECT count(*) FROM app.verify_ledger_all()`).Scan(&t.Count)
		return t, err
	}
	return nil, nil
}
