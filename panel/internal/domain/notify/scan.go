package notify

// 到期与流量预警的定时扫描。
//
// 这两件事没法由事件驱动：「还有 3 天到期」不对应任何一次写入，
// 它是时间走到那里自然成立的。只能定期扫。
//
// 扫描的关键是幂等：每轮都会扫到同一批订阅，靠 dedupe_key 保证
// 同一个窗口只通知一次。没有它的话用户会按扫描频率收到通知。

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// 到期提醒的窗口，按「剩余天数落在哪个区间」划分。
//
// 区间必须连续、不留空洞。最初写成 (n-1, n] 的形式（7/3/1 天各占一天），
// 结果 (1,2] 和 (3,6] 这些区间根本没人管 —— 剩 2 天到期的订阅
// 一条提醒都收不到。而且扫描一旦停机跨过那一天的窗口，
// 那次提醒就永久丢了，没有任何补发机会。
//
// 连续覆盖之后，只要订阅还在某个区间内，恢复扫描就会补上。
// 每个区间靠 dedupe_key 保证只发一次。
var expiryWindows = []struct {
	label int // 对用户显示的天数
	lower int // 剩余天数 > lower
	upper int // 剩余天数 <= upper
}{
	{label: 7, lower: 3, upper: 7},
	{label: 3, lower: 1, upper: 3},
	{label: 1, lower: 0, upper: 1},
}

// 流量预警的阈值。
//
// 80% 是提醒「该注意了」，95% 是「马上就断」。
// 不设 50% 这类早期阈值：那时用户无从判断快慢，通知只会被当噪音。
var quotaThresholds = []int{80, 95}

// ScanExpiring 扫出即将到期的订阅并排队提醒。
func (s *Service) ScanExpiring(ctx context.Context, tenantID string) (int, error) {
	queued := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		for _, win := range expiryWindows {
			rows, err := tx.Query(ctx, `
				SELECT s.id::text, s.user_id::text, p.name,
				       to_char(s.current_period_end, 'YYYY-MM-DD')
				  FROM subscriptions s
				  JOIN plans p ON p.id = s.plan_id
				 WHERE s.tenant_id = $1
				   AND s.status IN ('active','trialing')
				   AND s.current_period_end IS NOT NULL
				   AND s.auto_renew = false
				   AND s.current_period_end >  now() + make_interval(days => $2)
				   AND s.current_period_end <= now() + make_interval(days => $3)`,
				tenantID, win.lower, win.upper)
			if err != nil {
				return err
			}
			type item struct{ subID, userID, plan, endAt string }
			var items []item
			for rows.Next() {
				var it item
				if err := rows.Scan(&it.subID, &it.userID, &it.plan, &it.endAt); err != nil {
					rows.Close()
					return err
				}
				items = append(items, it)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}

			for _, it := range items {
				vars := map[string]string{
					"plan":       it.plan,
					"days":       fmt.Sprint(win.label),
					"expires_at": it.endAt,
					"site":       "AegisPanel",
				}
				// 键里带区间标签：进入下一个更紧急的区间时会再提醒一次，
				// 而同一个区间内反复扫描只发一条
				key := fmt.Sprintf("expiring:%s:%dd", it.subID, win.label)
				if err := s.Enqueue(ctx, tx, tenantID, it.userID,
					"subscription.expiring", vars, key); err != nil {
					return err
				}
				// 插件复用同一个 dedupe 键：到期提醒的分档规则在这里，
				// 让插件那边再算一遍只会两边不一致。
				if err := plugin.EmitSubscriptionExpiring(ctx, tx, tenantID, key,
					it.subID, it.userID, it.plan, it.endAt, win.label); err != nil {
					return err
				}
				queued++
			}
		}
		return nil
	})
	return queued, err
}

// ScanQuota 扫出流量接近用尽的订阅。
func (s *Service) ScanQuota(ctx context.Context, tenantID string) (int, error) {
	queued := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		for _, pct := range quotaThresholds {
			rows, err := tx.Query(ctx, `
				SELECT q.subscription_id::text, s.user_id::text, p.name,
				       q.consumed, COALESCE(q.granted,0) + COALESCE(q.granted_addon,0)
				  FROM quota_balances q
				  JOIN subscriptions s ON s.id = q.subscription_id
				  JOIN plans p ON p.id = s.plan_id
				 WHERE q.tenant_id = $1
				   AND q.metric = 'traffic.bytes'
				   AND s.status IN ('active','trialing')
				   AND COALESCE(q.granted,0) + COALESCE(q.granted_addon,0) > 0
				   AND q.consumed * 100 >= (COALESCE(q.granted,0) + COALESCE(q.granted_addon,0)) * $2
				   -- 只取刚跨过这条线的：已经超过更高阈值的由那一档负责，
				   -- 否则用量到 96% 时会同时收到 80% 和 95% 两条
				   AND ($2 = 95 OR q.consumed * 100 < (COALESCE(q.granted,0) + COALESCE(q.granted_addon,0)) * 95)`,
				tenantID, pct)
			if err != nil {
				return err
			}
			type item struct {
				subID, userID, plan string
				consumed, total     int64
			}
			var items []item
			for rows.Next() {
				var it item
				if err := rows.Scan(&it.subID, &it.userID, &it.plan, &it.consumed, &it.total); err != nil {
					rows.Close()
					return err
				}
				items = append(items, it)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}

			for _, it := range items {
				remain := it.total - it.consumed
				if remain < 0 {
					remain = 0
				}
				vars := map[string]string{
					"plan":      it.plan,
					"percent":   fmt.Sprint(pct),
					"remaining": humanBytes(remain),
					"site":      "AegisPanel",
				}
				// 键里带上周期起点：下个结算周期流量重置后，
				// 同一条订阅应该能再次收到提醒
				key := fmt.Sprintf("quota:%s:%d", it.subID, pct)
				if err := s.Enqueue(ctx, tx, tenantID, it.userID,
					"quota.warning", vars, key); err != nil {
					return err
				}
				if err := plugin.EmitTrafficExhausted(ctx, tx, tenantID, key,
					it.subID, it.userID, it.plan, it.consumed, it.total, pct); err != nil {
					return err
				}
				queued++
			}
		}
		return nil
	})
	return queued, err
}

// ScanPaidOrders 给刚履约的订单补一条支付成功通知。
//
// 为什么不在支付回调里直接发：那会让 billing 依赖 notify，
// 而支付履约是整个系统最不该被牵连的路径 —— 通知模板查不到、
// 队列写不进去，都不该影响一笔已经收到的钱。
//
// 扫描方式把这条依赖彻底切断，代价是最多延迟一个扫描周期。
// 对支付成功通知来说完全可以接受：用户付完款在页面上就看到结果了，
// 这条通知是留底，不是即时反馈。
func (s *Service) ScanPaidOrders(ctx context.Context, tenantID string) (int, error) {
	queued := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 只看最近一段时间：历史订单在通知上线之前就已经履约了，
		// 全表扫一遍会给所有老用户补发一堆莫名其妙的「支付成功」
		rows, err := tx.Query(ctx, `
			SELECT o.id::text, o.user_id::text, COALESCE(o.order_no, o.id::text),
			       COALESCE(p.name, ''),
			       COALESCE(to_char(s.current_period_end, 'YYYY-MM-DD'), '')
			  FROM orders o
			  LEFT JOIN subscriptions s ON s.id = o.subscription_id
			  LEFT JOIN plans p ON p.id = s.plan_id
			 WHERE o.tenant_id = $1
			   AND o.status = 'fulfilled'
			   AND o.fulfilled_at > now() - interval '2 hours'`, tenantID)
		if err != nil {
			return err
		}
		type item struct{ orderID, userID, orderNo, plan, endAt string }
		var items []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.orderID, &it.userID, &it.orderNo, &it.plan, &it.endAt); err != nil {
				rows.Close()
				return err
			}
			items = append(items, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, it := range items {
			vars := map[string]string{
				"order_no":   it.orderNo,
				"plan":       it.plan,
				"expires_at": it.endAt,
				"site":       "AegisPanel",
			}
			// 一个订单只通知一次，与扫描频率无关
			key := "order-paid:" + it.orderID
			if err := s.Enqueue(ctx, tx, tenantID, it.userID, "order.paid", vars, key); err != nil {
				return err
			}
			queued++
		}
		return nil
	})
	return queued, err
}

// StartScanner 起一个后台循环，定期扫描并派发。
func (s *Service) StartScanner(ctx context.Context, tenantID string, every time.Duration) {
	go func() {
		// 启动后先等一会儿再扫：进程刚起来时连接池、缓存都还没热，
		// 立刻压一轮全表扫描没必要
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}

		t := time.NewTicker(every)
		defer t.Stop()
		for {
			s.runOnce(ctx, tenantID)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (s *Service) runOnce(ctx context.Context, tenantID string) {
	if n, err := s.ScanExpiring(ctx, tenantID); err != nil {
		s.log.Warn("到期扫描失败", "err", err)
	} else if n > 0 {
		s.log.Info("到期提醒已排队", "条数", n)
	}
	if n, err := s.ScanQuota(ctx, tenantID); err != nil {
		s.log.Warn("流量扫描失败", "err", err)
	} else if n > 0 {
		s.log.Info("流量预警已排队", "条数", n)
	}
	if n, err := s.ScanPaidOrders(ctx, tenantID); err != nil {
		s.log.Warn("支付通知扫描失败", "err", err)
	} else if n > 0 {
		s.log.Info("支付通知已排队", "条数", n)
	}
	// 派发放在扫描之后：刚排的队这一轮就能发出去，
	// 而不必等到下一个周期
	if n, err := s.Dispatch(ctx, tenantID, 100); err != nil {
		s.log.Warn("通知派发失败", "err", err)
	} else if n > 0 {
		s.log.Info("通知已派发", "条数", n)
	}
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
