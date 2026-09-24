// [INPUT]: 依赖 subscription_events / orders / order_items / quota_balances 的只读查询，依赖 checkout.go 的 addInterval，math/big 做无溢出的有理数比较
// [OUTPUT]: 对包内提供 prorationBasis、trafficAllowance、prorationCredit、loadProrationBasis
// [POS]: billing 变更套餐（D-E-2）的剩余价值折算：plan_change.go 在下单与试算时调用，本文件只算数不写库
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

// 剩余价值 = 本周期付费合计 × min(剩余时间比例, 剩余流量比例)，向下取整到分。
//
// 「本周期」从最近一次让周期重新起算的事件开始：开通（activated）、从过期
// 状态续回（renewed 且 from_status 为 expired/past_due/grace）、上一次变更套餐
// （plan_changed）。提前续费是在原周期末往后叠，不改周期起点，所以一个周期
// 可能由好几张付费单拼成 —— 基数是它们的合计，而不是最近一张。
//
// 每张单的「付费」按 max(小计 − 折扣 − 已退款, 0) 计。0 元单、全额券、人工
// 赠送单因此为 0；礼品卡开通、礼品卡加天数没有订单，天然不计。变更单的小计
// 减折扣里包含它用掉的上一段剩余价值 —— 那部分本来就是真金白银折过来的，
// 不计进去，连续变更两次就会把第一次的钱弄丢。
//
// 时间比例按「已付费天数先用、赠送天数最后用」（2026-09-24 用户拍板）：
//
//	付费总时长 P = 从周期起点起，依次把每张付费单的计费周期接上去的长度
//	已用       = now − 周期起点
//	剩余付费   = min(P − 已用, 周期末 − now)，不小于 0
//	时间比例   = 剩余付费 / P
//
// 礼品卡加的天数排在付费天数之后，所以永远折不成钱；只有一张单、没有赠送
// 天数时，这与「剩余时间 / 整个周期」一模一样。
//
// 流量比例只看跟着整个付费周期走的 traffic.bytes 配额（period 为 cycle 或
// total）：剩余 / (上限 + 人工调整)。按日、按月的配额只反映当天、当月，拿它
// 折一年期的钱会把一个月用得多的人折得几乎为零，所以不参与。不限量的套餐
// 没有流量配额行，流量比例视为 1。

import (
	"context"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// trafficAllowance 是一条参与折算的流量配额：Cap = 上限 + 人工调整。
type trafficAllowance struct {
	Cap      int64
	Consumed int64
}

// prorationBasis 是折算的全部输入。
type prorationBasis struct {
	// Value 是本周期付费合计（最小货币单位），Currency 是它的币种；
	// 本周期没有付费单时 Value 为 0、Currency 为空。
	Value    int64
	Currency string
	// PaidSpan 是本周期付费单买下的总时长。
	PaidSpan    time.Duration
	PeriodStart time.Time
	PeriodEnd   time.Time
	Traffic     []trafficAllowance
}

// prorationCredit 算出剩余价值：floor(Value × min(时间比例, 流量比例))。
// 全程用有理数比较与整数除法，不经浮点 —— 钱的取整只能发生一次，而且只能向下。
func prorationCredit(b prorationBasis, now time.Time) int64 {
	if b.Value <= 0 || b.PaidSpan <= 0 {
		return 0
	}
	remaining := min(b.PaidSpan-now.Sub(b.PeriodStart), b.PeriodEnd.Sub(now), b.PaidSpan)
	if remaining <= 0 {
		return 0
	}
	num, den := big.NewInt(int64(remaining)), big.NewInt(int64(b.PaidSpan))

	for _, t := range b.Traffic {
		left := max(min(t.Cap-t.Consumed, t.Cap), 0)
		if t.Cap <= 0 || left == 0 {
			return 0
		}
		// left/cap < num/den  ⇔  left·den < num·cap
		l := new(big.Int).Mul(big.NewInt(left), den)
		r := new(big.Int).Mul(num, big.NewInt(t.Cap))
		if l.Cmp(r) < 0 {
			num, den = big.NewInt(left), big.NewInt(t.Cap)
		}
	}

	credit := new(big.Int).Mul(big.NewInt(b.Value), num)
	credit.Quo(credit, den)
	return credit.Int64()
}

// errProrationMixedCurrency 在本周期的付费单不是同一种币种时返回：
// 不同币种的钱加不到一起，也就没有「剩余价值」可言。
var errProrationMixedCurrency = httpx.New(httpx.CodeConflict, "这条订阅本周期的付费订单币种不一致，无法折算")

// loadProrationBasis 读出订阅本周期的折算输入。调用方已锁住订阅行。
func loadProrationBasis(ctx context.Context, tx pgx.Tx, tenantID, subID string,
	periodStart, periodEnd time.Time) (prorationBasis, error) {

	b := prorationBasis{PeriodStart: periodStart, PeriodEnd: periodEnd}

	// 事件按 id（uuidv7，插入时刻有序）排：同一订阅的事件由订阅行锁串行写入，
	// 插入顺序就是发生顺序；occurred_at 是事务开始时刻，等锁的事务会比
	// 先提交的那个还早。
	rows, err := tx.Query(ctx, `
		WITH restart AS (
			SELECT e.id FROM subscription_events e
			 WHERE e.tenant_id = $1 AND e.subscription_id = $2::uuid
			   AND (e.event_type IN ('activated', 'plan_changed')
			        OR (e.event_type = 'renewed'
			            AND e.from_status IN ('expired', 'past_due', 'grace')))
			 ORDER BY e.id DESC LIMIT 1
		)
		SELECT o.currency::text,
		       greatest(o.subtotal_amount - o.discount_amount - o.refunded_amount, 0),
		       oi.snapshot_interval, oi.snapshot_interval_count
		  FROM restart r
		  JOIN subscription_events e
		    ON e.tenant_id = $1 AND e.subscription_id = $2::uuid AND e.id >= r.id
		   AND e.event_type IN ('activated', 'renewed', 'plan_changed')
		  JOIN orders o ON o.tenant_id = e.tenant_id AND o.id = e.order_id
		   AND o.kind IN ('new', 'renewal', 'upgrade')
		   AND o.status IN ('fulfilled', 'partially_refunded', 'refunded')
		  JOIN LATERAL (
			SELECT i.snapshot_interval, i.snapshot_interval_count
			  FROM order_items i
			 WHERE i.tenant_id = o.tenant_id AND i.order_id = o.id AND i.plan_id IS NOT NULL
			 ORDER BY i.created_at LIMIT 1) oi ON true
		 ORDER BY e.id`, tenantID, subID)
	if err != nil {
		return b, err
	}
	paidUntil := periodStart
	for rows.Next() {
		var currency, interval string
		var value int64
		var count int16
		if err := rows.Scan(&currency, &value, &interval, &count); err != nil {
			rows.Close()
			return b, err
		}
		if b.Currency != "" && b.Currency != currency {
			rows.Close()
			return b, errProrationMixedCurrency
		}
		b.Currency = currency
		b.Value += value
		paidUntil = addInterval(paidUntil, interval, int(count))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return b, err
	}
	b.PaidSpan = paidUntil.Sub(periodStart)

	rows, err = tx.Query(ctx, `
		SELECT limit_value + granted_addon + adjusted, consumed
		  FROM quota_balances
		 WHERE tenant_id = $1 AND subscription_id = $2::uuid
		   AND metric = 'traffic.bytes' AND period IN ('cycle', 'total')
		   AND limit_value IS NOT NULL
		 ORDER BY id`, tenantID, subID)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	for rows.Next() {
		var t trafficAllowance
		if err := rows.Scan(&t.Cap, &t.Consumed); err != nil {
			return b, err
		}
		b.Traffic = append(b.Traffic, t)
	}
	return b, rows.Err()
}
