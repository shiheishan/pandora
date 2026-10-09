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

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
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

// 过期后的通知（用户 2026-10-07）：到期当时一条，过期后第 1 天、第 7 天各一条召回，
// 之后不再打扰。都按订阅 status = 'expired' 扫（过期扫描在 aegis-admin 上把它写进库，
// billing/expire.go），窗口连续不留空洞，停机跨过窗口后恢复扫描照样补上；每条靠
// dedupe_key 只发一次，键里带周期末：续费恢复后再次到期是新的一次。
//
// 召回只发给原地续费窗口还开着（过期不满 30 天）、而且名下没有别的在用订阅的用户：
// 已经换了别的套餐的人不该再被催续费。
var expiredNotices = []struct {
	code  string
	label int    // 过期第几天（到期当时为 0）
	lower string // 已过期时长 >= lower
	upper string // 已过期时长 < upper
}{
	{code: "subscription.expired", label: 0, lower: "0 days", upper: "1 day"},
	{code: "subscription.recall", label: 1, lower: "1 day", upper: "7 days"},
	{code: "subscription.recall", label: 7, lower: "7 days", upper: "30 days"},
}

// noticeTime 把通知里的时刻显示到分钟，时区同按日流量的切日口径（nodefabric.UsageLocation：
// 用户时区，未设跟随站点时区）。
func noticeTime(t time.Time, userTZ, tenantTZ string) string {
	return t.In(nodefabric.UsageLocation(userTZ, tenantTZ)).Format("2006-01-02 15:04")
}

// ScanExpiring 扫出即将到期的订阅并排队提醒，同一事务里接着排过期通知与召回
// （scanExpiredNotices）。返回真正新排的行数。
//
// 原先这里带着 s.auto_renew = false：这一列默认 true、没有任何代码改它（也没有真正的
// 自动扣款），7 / 3 / 1 天的提醒一条都发不出去。去掉。
func (s *Service) ScanExpiring(ctx context.Context, tenantID string) (int, error) {
	queued := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		for _, win := range expiryWindows {
			rows, err := tx.Query(ctx, `
				SELECT s.id::text, s.user_id::text, p.name, s.current_period_end,
				       u.timezone, t.timezone
				  FROM subscriptions s
				  JOIN plans p ON p.id = s.plan_id
				  JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
				  JOIN tenants t ON t.id = s.tenant_id
				 WHERE s.tenant_id = $1
				   AND s.status IN ('active','trialing')
				   AND s.current_period_end IS NOT NULL
				   AND s.current_period_end >  now() + make_interval(days => $2)
				   AND s.current_period_end <= now() + make_interval(days => $3)`,
				tenantID, win.lower, win.upper)
			if err != nil {
				return err
			}
			items, err := collectSubscriptionNotices(rows)
			if err != nil {
				return err
			}

			for _, it := range items {
				endAt := noticeTime(it.periodEnd, it.userTZ, it.tenantTZ)
				vars := map[string]string{
					"plan":       it.plan,
					"days":       fmt.Sprint(win.label),
					"expires_at": endAt,
				}
				// 键里带区间标签：进入下一个更紧急的区间时会再提醒一次，
				// 而同一个区间内反复扫描只发一条。带上周期末：去重键永久唯一，
				// 不带的话续费之后的下一个周期再也收不到这一档提醒
				key := fmt.Sprintf("expiring:%s:%dd:%d", it.subID, win.label, it.periodEnd.Unix())
				n, err := s.Enqueue(ctx, tx, tenantID, it.userID,
					"subscription.expiring", vars, key)
				if err != nil {
					return err
				}
				// 插件复用同一个 dedupe 键：到期提醒的分档规则在这里，
				// 让插件那边再算一遍只会两边不一致。
				if err := plugin.EmitSubscriptionExpiring(ctx, tx, tenantID, key,
					it.subID, it.userID, it.plan, endAt, win.label); err != nil {
					return err
				}
				queued += n
			}
		}
		n, err := s.scanExpiredNotices(ctx, tx, tenantID)
		queued += n
		return err
	})
	return queued, err
}

// subscriptionNotice 是到期类通知扫到的一条订阅。
type subscriptionNotice struct {
	subID, userID, plan string
	periodEnd           time.Time
	userTZ, tenantTZ    string
}

func collectSubscriptionNotices(rows pgx.Rows) ([]subscriptionNotice, error) {
	defer rows.Close()
	var items []subscriptionNotice
	for rows.Next() {
		var it subscriptionNotice
		if err := rows.Scan(&it.subID, &it.userID, &it.plan, &it.periodEnd,
			&it.userTZ, &it.tenantTZ); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// scanExpiredNotices 排到期当时的通知与过期后的召回（见 expiredNotices）。
func (s *Service) scanExpiredNotices(ctx context.Context, tx pgx.Tx, tenantID string) (int, error) {
	queued := 0
	for _, win := range expiredNotices {
		rows, err := tx.Query(ctx, `
			SELECT s.id::text, s.user_id::text, p.name, s.current_period_end,
			       u.timezone, t.timezone
			  FROM subscriptions s
			  JOIN plans p ON p.id = s.plan_id
			  JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
			  JOIN tenants t ON t.id = s.tenant_id
			 WHERE s.tenant_id = $1
			   AND s.status = 'expired' AND s.renewal_closed_at IS NULL
			   AND s.current_period_end <= now() - $2::interval
			   AND s.current_period_end >  now() - $3::interval
			   AND ($4 = 0 OR NOT EXISTS (
			         SELECT 1 FROM subscriptions o
			          WHERE o.tenant_id = s.tenant_id AND o.user_id = s.user_id
			            AND o.id <> s.id
			            AND o.status IN ('active','trialing','grace','past_due')
			            AND (o.current_period_end IS NULL OR o.current_period_end > now())))`,
			tenantID, win.lower, win.upper, win.label)
		if err != nil {
			return queued, err
		}
		items, err := collectSubscriptionNotices(rows)
		if err != nil {
			return queued, err
		}
		for _, it := range items {
			vars := map[string]string{
				"plan":       it.plan,
				"expired_at": noticeTime(it.periodEnd, it.userTZ, it.tenantTZ),
			}
			key := fmt.Sprintf("expired:%s:%d", it.subID, it.periodEnd.Unix())
			if win.label > 0 {
				vars["days"] = fmt.Sprint(win.label)
				key = fmt.Sprintf("recall:%s:%dd:%d", it.subID, win.label, it.periodEnd.Unix())
			}
			n, err := s.Enqueue(ctx, tx, tenantID, it.userID, win.code, vars, key)
			if err != nil {
				return queued, err
			}
			queued += n
		}
	}
	return queued, nil
}

// ScanQuota 扫出流量接近用尽的订阅。
//
// 可用量 = 套餐本期额度 + 挂在这一份上的流量包剩余（D-E-1）。套餐额度用完后扣量转到
// 流量包，订阅配额行的 consumed 就停在额度上；只看套餐额度的话，买了流量包的
// 用户照样会收到「流量即将用尽」。购买模型统一后流量包按份挂（扣量也只扣这一份的包），
// 这里按订阅取余量，走 idx_traffic_pack_grants_open_sub —— 与扣量同一口径。
func (s *Service) ScanQuota(ctx context.Context, tenantID string) (int, error) {
	queued := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		items, err := scanQuotaCrossings(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		for _, it := range items {
			remain := it.total - it.consumed
			if remain < 0 {
				remain = 0
			}
			vars := map[string]string{
				"plan":      it.plan,
				"percent":   fmt.Sprint(it.pct),
				"remaining": humanBytes(remain),
			}
			// 键里带上档位：用量继续涨到下一档时会再提醒一次，同一档内反复扫描只发一条
			key := fmt.Sprintf("quota:%s:%d", it.subID, it.pct)
			n, err := s.Enqueue(ctx, tx, tenantID, it.userID,
				"quota.warning", vars, key)
			if err != nil {
				return err
			}
			if err := plugin.EmitTrafficExhausted(ctx, tx, tenantID, key,
				it.subID, it.userID, it.plan, it.consumed, it.total, it.pct); err != nil {
				return err
			}
			queued += n
		}
		return nil
	})
	return queued, err
}

// quotaCrossing 是一条用量跨过预警线的订阅：pct 是它此刻所在的最高一档。
type quotaCrossing struct {
	subID, userID, plan string
	consumed, total     int64
	pct                 int
}

// scanQuotaCrossings 一遍扫出所有跨过任一预警线的订阅，每条订阅只带它所在的最高一档
// （quotaThresholds 里已跨过的最大值）。
//
// 原先每一档各扫一遍（两档就是两遍整张配额表连订阅、套餐、流量包），同一条订阅在 96% 时还要靠
// 「只取刚跨过这条线的」排除较低一档。现在只扫一遍、每行直接定档，不再有跨档互斥的写法，
// 加一档阈值不加一遍扫描。结果与分档各扫一遍逐行相同（PG18 对照：scan_quota_pg18_test.go）。
//
// 流量包余量的 LATERAL 只对可能跨线的行做：可用量 = 套餐额度 + 流量包余量，余量不为负，
// 所以连套餐额度本身都没用到最低一档的订阅（绝大多数）不可能跨线，在连流量包之前就被挡掉。
func scanQuotaCrossings(ctx context.Context, tx pgx.Tx, tenantID string) ([]quotaCrossing, error) {
	lowest := quotaThresholds[0]
	for _, t := range quotaThresholds {
		lowest = min(lowest, t)
	}
	rows, err := tx.Query(ctx, `
		WITH usage AS (
			SELECT q.subscription_id, s.user_id, p.name, q.consumed,
			       COALESCE(q.granted,0) + COALESCE(q.granted_addon,0) AS plan_total,
			       COALESCE(q.granted,0) + COALESCE(q.granted_addon,0) + pk.pack_left AS total
			  FROM quota_balances q
			  JOIN subscriptions s ON s.id = q.subscription_id
			  JOIN plans p ON p.id = s.plan_id
			 CROSS JOIN LATERAL (
			       SELECT COALESCE(sum(g.granted_bytes - g.consumed_bytes), 0)::bigint AS pack_left
			         FROM traffic_pack_grants g
			        WHERE g.tenant_id = q.tenant_id AND g.subscription_id = q.subscription_id
			          AND g.consumed_bytes < g.granted_bytes) pk
			 WHERE q.tenant_id = $1
			   AND q.metric = 'traffic.bytes'
			   AND s.status IN ('active','trialing')
			   AND q.consumed * 100 >= (COALESCE(q.granted,0) + COALESCE(q.granted_addon,0)) * $3::int)
		SELECT subscription_id::text, user_id::text, name, consumed, total,
		       (SELECT max(t) FROM unnest($2::int[]) AS th(t) WHERE consumed * 100 >= total * t) AS pct
		  FROM usage
		 WHERE plan_total > 0
		   AND consumed * 100 >= total * $3::int
		 ORDER BY pct, subscription_id`,
		tenantID, quotaThresholds, lowest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []quotaCrossing
	for rows.Next() {
		var it quotaCrossing
		if err := rows.Scan(&it.subID, &it.userID, &it.plan, &it.consumed, &it.total, &it.pct); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
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
			}
			// 一个订单只通知一次，与扫描频率无关
			key := "order-paid:" + it.orderID
			n, err := s.Enqueue(ctx, tx, tenantID, it.userID, "order.paid", vars, key)
			if err != nil {
				return err
			}
			// 只数真正新排的：2 小时窗口里每轮都会扫到同一单，撞键的不算
			queued += n
		}
		return nil
	})
	return queued, err
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
