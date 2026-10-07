package billing

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// quotaPeriodEndSQL 是新建或重置一行配额时的周期末：total 无周期末；day / month 按
// 自然周期从 start 起算；cycle 跟订阅走，取 cycleEnd。三个参数都是 SQL 表达式。
//
// 原先开通（initQuotaBalances）与变更套餐对 day / month 也一律写订阅周期末：年付套餐的
// 月流量要到一年后才到期，期间 RollQuotaPeriods 认为它没到期、从不滚动，用户一整年只有
// 一个月的流量；月付套餐的日限量同理变成了月限量。
func quotaPeriodEndSQL(period, start, cycleEnd string) string {
	return `CASE ` + period + `
	          WHEN 'total' THEN NULL::timestamptz
	          WHEN 'day'   THEN (` + start + `) + interval '1 day'
	          WHEN 'month' THEN (` + start + `) + interval '1 month'
	          ELSE ` + cycleEnd + ` END`
}

// 配额滚动的分批参数：每批一个短事务、至多 rollQuotaBatch 行，一轮至多 rollQuotaMaxBatches 批。
// 被 push 锁着而跳过的行，隔 rollQuotaRetryDelay 再补一遍；仍锁着的留给下一轮。
const (
	rollQuotaBatch      = 500
	rollQuotaMaxBatches = 100
	rollQuotaRetryDelay = 500 * time.Millisecond
)

// rollQuotaSQL 滚动一批到期的 day / month 配额，$1 租户、$2 批量上限。
//
// 加锁与 push 记账同序：push 按 ORDER BY id FOR UPDATE 锁配额行，这里同样按 id、且
// SKIP LOCKED——正在被记账的行这一批先跳过，不排队、不和 push 互相等（原先 due 不加锁、
// 按任意顺序 UPDATE，多实例或运行重叠时同一行会被滚两次，与 push 也可能死锁）。
//
// 清零前的用量（traffic_reset_logs.consumed_before）取 UPDATE 的 RETURNING OLD.consumed
// （PG18）：行已被本事务锁住，OLD 就是最新提交的值，含刚提交的并发 push 增量；原先取
// 语句快照里的 consumed，会少记这部分。
//
// 两类行到期：
//   - 正常行：period_end <= now()，新周期从旧 period_end 起；
//   - 周期末被错写成订阅周期末的旧行（见 quotaPeriodEndSQL）：period_end 比自然周期长出
//     一截（月多出 4 天以上、日多出 1 小时以上，容差吸收 Go AddDate 与 PG interval 在月末的
//     差异）。它们从 period_start 起按自然周期数到最近一个已过的边界，一步追上，不逐期补发
//     重置日志；之后就是正常行。
//
// 订阅本身已经过期的不滚：那种情况该做的是停服，不是给他发新一轮流量。
var rollQuotaSQL = `
	WITH due AS MATERIALIZED (
		SELECT qb.id, s.user_id, qb.period, qb.period_start, qb.period_end,
		       qb.period_end > qb.period_start + CASE qb.period
		           WHEN 'day' THEN interval '1 day 1 hour' ELSE interval '1 month 4 days' END
		         AS overlong
		  FROM quota_balances qb
		  JOIN subscriptions s
		    ON s.tenant_id = qb.tenant_id AND s.id = qb.subscription_id
		 WHERE qb.tenant_id = $1
		   AND qb.period IN ('day','month')
		   AND qb.period_end IS NOT NULL
		   AND s.status IN ('active','trialing','grace')
		   AND (s.current_period_end IS NULL OR s.current_period_end > now())
		   AND (qb.period_end <= now()
		        OR (qb.period_end > qb.period_start + CASE qb.period
		                WHEN 'day' THEN interval '1 day 1 hour' ELSE interval '1 month 4 days' END
		            AND qb.period_start + ` + quotaStepSQL("qb.period") + ` <= now()))
		 ORDER BY qb.id
		 LIMIT $2
		   FOR UPDATE OF qb SKIP LOCKED
	), stepped AS (
		SELECT d.id, d.user_id, d.period,
		       CASE
		         WHEN NOT d.overlong THEN d.period_end
		         WHEN d.period = 'day' THEN d.period_start
		              + floor(extract(epoch FROM now() - d.period_start) / 86400)::int * interval '1 day'
		         ELSE d.period_start
		              + (extract(year FROM age(now(), d.period_start))::int * 12
		                 + extract(month FROM age(now(), d.period_start))::int) * interval '1 month'
		       END AS new_start
		  FROM due d
	), rolled AS (
		UPDATE quota_balances qb
		   SET consumed = 0,
		       period_start = n.new_start,
		       period_end = n.new_start + ` + quotaStepSQL("n.period") + `,
		       notified_thresholds = '{}',
		       overage_applied_at = NULL,
		       updated_at = now()
		  FROM stepped n
		 WHERE qb.id = n.id
		RETURNING qb.id, qb.subscription_id, qb.metric, OLD.consumed AS consumed_before
	)
	INSERT INTO traffic_reset_logs
		(tenant_id, subscription_id, user_id, metric, reason,
		 consumed_before, consumed_after)
	SELECT $1, r.subscription_id, n.user_id, r.metric, 'cycle_roll', r.consumed_before, 0
	  FROM rolled r
	  JOIN stepped n ON n.id = r.id`

// quotaStepSQL 是 day / month 配额的自然周期长度（period 是 SQL 表达式）。这两类配额的
// 周期独立于订阅周期（年付套餐按月给流量、月付套餐按日限量）。
func quotaStepSQL(period string) string {
	return `CASE ` + period + ` WHEN 'day' THEN interval '1 day' ELSE interval '1 month' END`
}

// RollQuotaPeriods 把周期已过的配额滚到下一个周期，返回滚动的行数。
//
// 只处理 period 为 day / month 的配额 —— 那类的周期独立于订阅周期
// （比如年付套餐但流量按月给）。period='cycle' 的跟着订阅走，
// 由续费负责重置；period='total' 永不重置。
//
// 分批、每批一个短事务（rollQuotaSQL）；一批满额就接着下一批。跑完后若有行因被 push
// 锁着而跳过，隔一小会儿再补一遍，仍锁着的留给下一轮。计费不依赖滚动及时：push 侧
// 扣的是已开始的那一期（见 nodefabric 的记账），滚动晚几分钟只影响重置时刻。
func (s *Service) RollQuotaPeriods(ctx context.Context, tenantID string) (int, error) {
	total := 0
	for pass := 0; pass < 2; pass++ {
		if pass > 0 {
			select {
			case <-ctx.Done():
				return total, ctx.Err()
			case <-time.After(rollQuotaRetryDelay):
			}
		}
		for batch := 0; batch < rollQuotaMaxBatches; batch++ {
			var n int
			err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
				tag, err := tx.Exec(ctx, rollQuotaSQL, tenantID, rollQuotaBatch)
				if err != nil {
					return err
				}
				n = int(tag.RowsAffected())
				return nil
			})
			if err != nil {
				return total, err
			}
			total += n
			if n < rollQuotaBatch {
				break
			}
		}
	}
	return total, nil
}
