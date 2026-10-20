package repo

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// DriftRows 找余额缓存与分录合计对不上的科目：样例是科目 id（新建的在前），每行带违例总数。
// debitNormalTypes 是借方余额为正的科目类型（其余按贷方为正）。分录全表按科目聚合一遍（与原先的看板口径相同），
// 随分录行数线性；巡检每 30 分钟一次。
func (r *Repo) DriftRows(ctx context.Context, debitNormalTypes []string) ([]integrity.Row, error) {
	if len(debitNormalTypes) == 0 {
		debitNormalTypes = []string{""}
	}
	var out []integrity.Row
	err := r.d.Conn(ctx).Raw(`
		SELECT a.id::text AS id, count(*) OVER () AS n
		  FROM ledger_accounts a
		  LEFT JOIN (
		        SELECT account_id,
		               coalesce(sum(amount) FILTER (WHERE direction = 'debit'), 0) AS d,
		               coalesce(sum(amount) FILTER (WHERE direction = 'credit'), 0) AS c
		          FROM ledger_entries GROUP BY account_id) s ON s.account_id = a.id
		 WHERE a.balance <> CASE WHEN a.account_type IN ? THEN coalesce(s.d, 0) - coalesce(s.c, 0)
		                         ELSE coalesce(s.c, 0) - coalesce(s.d, 0) END
		 ORDER BY a.id DESC
		 LIMIT ?`, debitNormalTypes, integrity.SampleLimit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// HoldMismatchRows 找冻结科目余额与未结冻结合计对不上的用户：user_balance_hold 科目的余额应恰好等于
// 该用户 status = 'held' 的 balance_holds 金额合计（冻结记贷、释放与扣款记借，金额都等于冻结行的 amount）。
// 样例是用户 id。冻结科目每个用过余额抵扣的用户一个；balance_holds 没有按状态的索引，筛 held 要扫一遍冻结表
// （每张用过余额抵扣的订单一行，远小于分录表），巡检每 30 分钟一次可以接受。
func (r *Repo) HoldMismatchRows(ctx context.Context) ([]integrity.Row, error) {
	var out []integrity.Row
	err := r.d.Conn(ctx).Raw(`
		SELECT a.owner_user_id::text AS id, count(*) OVER () AS n
		  FROM ledger_accounts a
		  LEFT JOIN (
		        SELECT user_id, sum(amount) AS held
		          FROM balance_holds
		         WHERE status = 'held'
		         GROUP BY user_id) h
		    ON h.user_id = a.owner_user_id
		 WHERE a.account_type = 'user_balance_hold'
		   AND a.balance <> coalesce(h.held, 0)
		 ORDER BY a.owner_user_id DESC
		 LIMIT ?`, integrity.SampleLimit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
