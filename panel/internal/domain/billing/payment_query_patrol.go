// [INPUT]: 依赖 payment_query.go 的 QueryOrderPayment、domain/payment 的 ErrNotSupported，依赖迁移 00097 在 payment_intents 上加的 query_attempts / next_query_at，依赖 platform/db、log/slog
// [OUTPUT]: 对外提供 PaymentQueryPatrol 节流参数与 DefaultPaymentQueryPatrol、PaymentQueryPatrolStats、PaymentService.ReconcileDuePayments；包内提供 nextPaymentQueryAt 退避计算
// [POS]: billing 主动查单的定时巡检（PAY-009 降级补偿的核心）：aegis-public 每轮调一次，逐个认领到期的在途支付意图去渠道查单；认领是短事务 + FOR UPDATE SKIP LOCKED + 把 next_query_at 推到未来，多实例并发也不会同时查同一单，查单本身不占行锁
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// PaymentQueryPatrol 是巡检的节流参数。
type PaymentQueryPatrol struct {
	// FirstDelay 是发起支付之后多久开始查：正常回调几秒内就到，太早查只是白打渠道。
	// 也是退避的基数：第 k 次查完，下一次在 FirstDelay × 2^(k-1) 之后。
	FirstDelay time.Duration
	// MaxAttempts 是同一条支付意图最多查几次，到了就不再查。
	MaxAttempts int
	// BatchLimit 是一轮最多查几单，防止积压时一轮把渠道打爆、也把这一轮拖得过长。
	BatchLimit int
	// Spacing 是同一轮里两次查单之间的间隔，给渠道的查询接口留余量。
	Spacing time.Duration
	// LastCallLead 是订单过期前留出的「最后一查」：退避的下一次若落在过期之后，
	// 就提前到过期前这么久再查一次，付款发生在最后几分钟、回调又丢了的单不至于漏掉。
	LastCallLead time.Duration
}

// DefaultPaymentQueryPatrol 的取值按 30 分钟的订单有效期来定：5 分钟后首查，
// 之后 10、20 分钟各一次，过期前 90 秒再最后一查；没有有效期的订单最多查到
// 第 6 次（约 2.6 小时）为止。一轮 20 单、间隔 500ms，一轮最多约 10 秒的渠道
// 请求（不计超时），远小于一分钟一轮的间隔。
var DefaultPaymentQueryPatrol = PaymentQueryPatrol{
	FirstDelay:   5 * time.Minute,
	MaxAttempts:  6,
	BatchLimit:   20,
	Spacing:      500 * time.Millisecond,
	LastCallLead: 90 * time.Second,
}

// paymentQueryMinGap 是两次巡检查单的最小间隔，也是认领后其他实例看不到这一单的
// 最短时长；必须明显长于单次渠道查询的超时，否则查单还没回来就可能被别的实例再认领。
const paymentQueryMinGap = time.Minute

// PaymentQueryPatrolStats 是一轮巡检的计数，供调用方打日志。
type PaymentQueryPatrolStats struct {
	Queried    int // 认领并查过的单数
	Reconciled int // 补记了的（结清订单或进挂账）
	Failed     int // 渠道没查成或补记被拒的
}

// nextPaymentQueryAt 算第 attempts 次查完之后的下一次查单时间。
//
// 指数退避：FirstDelay × 2^(attempts-1)。订单有过期时间时，若退避的下一次落在
// 「过期前 LastCallLead」之后，而那个时刻离现在还有至少 paymentQueryMinGap，
// 就改在那个时刻做最后一查；已经过了最后一查的点，就照退避排，订单届时已过期，
// 扫描条件自然不会再选中它。
func nextPaymentQueryAt(now time.Time, attempts int, orderExpires *time.Time, p PaymentQueryPatrol) time.Time {
	if attempts < 1 {
		attempts = 1
	}
	delay := p.FirstDelay
	for i := 1; i < attempts && delay < 24*time.Hour; i++ {
		delay *= 2
	}
	if delay < paymentQueryMinGap {
		delay = paymentQueryMinGap
	}
	next := now.Add(delay)
	if orderExpires != nil {
		lastCall := orderExpires.Add(-p.LastCallLead)
		if next.After(lastCall) && !lastCall.Before(now.Add(paymentQueryMinGap)) {
			next = lastCall
		}
	}
	return next
}

// paymentQueryClaim 是认领到的一条到期支付意图。
type paymentQueryClaim struct {
	IntentID string
	OrderID  string
}

// claimDuePaymentQuery 认领一条到期的在途支付意图，并在同一个短事务里把它的
// 下一次查单时间推到未来、次数加一。
//
// 选中条件：意图在途（created / requires_action / processing）、订单待支付且未
// 过期、渠道启用、次数没到上限、到了下一次查单时间（没查过的按发起时间 +
// FirstDelay）。FOR UPDATE OF pi SKIP LOCKED 让并发的另一个实例跳过正被认领的行；
// 提交之后 next_query_at 已在未来，别的实例按条件也选不中它。只锁意图行一把锁，
// 不碰订单行，与结算主链「订单 → 意图」的锁序不会成环。
func (s *PaymentService) claimDuePaymentQuery(ctx context.Context, tenantID string, p PaymentQueryPatrol) (*paymentQueryClaim, error) {
	var claim *paymentQueryClaim
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var (
			c            paymentQueryClaim
			attempts     int
			now          time.Time
			orderExpires *time.Time
		)
		err := tx.QueryRow(ctx, `
			SELECT pi.id::text, pi.order_id::text, pi.query_attempts, o.expires_at, now()
			  FROM payment_intents pi
			  JOIN orders o
			    ON o.tenant_id = pi.tenant_id AND o.id = pi.order_id
			  JOIN payment_providers pp
			    ON pp.tenant_id = pi.tenant_id AND pp.id = pi.provider_id
			 WHERE pi.tenant_id = $1
			   AND pi.status IN ('created', 'requires_action', 'processing')
			   AND o.status IN ('pending_payment', 'processing')
			   AND (o.expires_at IS NULL OR o.expires_at > now())
			   AND pp.enabled
			   AND pi.query_attempts < $2
			   AND coalesce(pi.next_query_at,
			                pi.created_at + make_interval(secs => $3)) <= now()
			 ORDER BY coalesce(pi.next_query_at,
			                   pi.created_at + make_interval(secs => $3)), pi.id
			 LIMIT 1
			 FOR UPDATE OF pi SKIP LOCKED`,
			tenantID, p.MaxAttempts, p.FirstDelay.Seconds(),
		).Scan(&c.IntentID, &c.OrderID, &attempts, &orderExpires, &now)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		next := nextPaymentQueryAt(now, attempts+1, orderExpires, p)
		tag, err := tx.Exec(ctx, `
			UPDATE payment_intents
			   SET query_attempts = query_attempts + 1, next_query_at = $3
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, c.IntentID, next)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("payment query claim lost")
		}
		claim = &c
		return nil
	})
	return claim, err
}

// stopPaymentQueries 把意图的次数直接拉满：渠道明确不支持查单时，再排多少次都是白查。
func (s *PaymentService) stopPaymentQueries(ctx context.Context, tenantID, intentID string, p PaymentQueryPatrol) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE payment_intents SET query_attempts = greatest(query_attempts, $3)
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, intentID, p.MaxAttempts)
		return err
	})
}

// ReconcileDuePayments 跑一轮巡检：逐个认领到期的在途支付意图，向渠道查单，
// 查到已付就补记。逐个认领而不是一次认领一批：一轮被超时截断时，没轮到的单
// 不会白白消耗一次次数；多实例也能自然地交错分担。
//
// 单个订单查不成只记日志、计入 Failed，不中断这一轮；只有认领本身出错
// （数据库不可用）才返回错误。
func (s *PaymentService) ReconcileDuePayments(ctx context.Context, tenantID string, p PaymentQueryPatrol, log *slog.Logger) (PaymentQueryPatrolStats, error) {
	var stats PaymentQueryPatrolStats
	for stats.Queried < p.BatchLimit {
		if stats.Queried > 0 && p.Spacing > 0 {
			select {
			case <-ctx.Done():
				return stats, nil
			case <-time.After(p.Spacing):
			}
		}
		if ctx.Err() != nil {
			return stats, nil
		}
		claim, err := s.claimDuePaymentQuery(ctx, tenantID, p)
		if err != nil {
			if ctx.Err() != nil {
				return stats, nil
			}
			return stats, err
		}
		if claim == nil {
			return stats, nil
		}
		stats.Queried++

		res, err := s.QueryOrderPayment(ctx, tenantID, claim.OrderID, "")
		if err != nil {
			stats.Failed++
			// httpx.Error 的 Unwrap 带出内部原因，渠道的 ErrNotSupported 认得出来
			if errors.Is(err, payment.ErrNotSupported) {
				if stopErr := s.stopPaymentQueries(ctx, tenantID, claim.IntentID, p); stopErr != nil {
					log.Error("停止对不支持查单的渠道巡检失败",
						"order_id", claim.OrderID, "error", stopErr.Error())
				}
			}
			log.Warn("巡检查单未成",
				"order_id", claim.OrderID, "intent_id", claim.IntentID, "error", err.Error())
			continue
		}
		if res.Reconciled {
			stats.Reconciled++
			log.Warn("巡检查单补记了回调没送到的收款",
				"order_id", res.OrderID, "order_no", res.OrderNo,
				"provider", res.ProviderCode, "quarantine_kind", res.QuarantineKind,
				"order_status", res.OrderStatus)
		}
	}
	return stats, nil
}
