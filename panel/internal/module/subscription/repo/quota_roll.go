package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// QuotaRollCandidate 是一条到期待滚的配额行，带上所属订阅滚动要用的事实。
type QuotaRollCandidate struct {
	ID                    uuid.UUID `gorm:"column:id"`
	SubscriptionID        uuid.UUID `gorm:"column:subscription_id"`
	UserID                uuid.UUID `gorm:"column:user_id"`
	PlanID                uuid.UUID `gorm:"column:plan_id"`
	PlanVersionID         uuid.UUID `gorm:"column:plan_version_id"`
	Metric                string    `gorm:"column:metric"`
	Period                string    `gorm:"column:period"`
	PeriodStart           time.Time `gorm:"column:period_start"`
	PeriodEnd             time.Time `gorm:"column:period_end"`
	SubPeriodEnd          time.Time `gorm:"column:sub_period_end"`
	SnapshotInterval      string    `gorm:"column:snapshot_interval"`
	SnapshotIntervalCount int       `gorm:"column:snapshot_interval_count"`
}

// QuotaRoll 是一条配额行滚动后的新周期；SetLimit 为真时上限改成 Limit（nil 为不限），否则不动上限。
type QuotaRoll struct {
	ID          uuid.UUID
	PeriodStart time.Time
	PeriodEnd   time.Time
	SetLimit    bool
	Limit       *int64
}

// RolledQuota 是实际滚动了的一行；ConsumedBefore 取 RETURNING OLD.consumed（行已被本事务锁住，是最新提交的值）。
type RolledQuota struct {
	ID             uuid.UUID `gorm:"column:id"`
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	Metric         string    `gorm:"column:metric"`
	ConsumedBefore int64     `gorm:"column:consumed_before"`
}

// quotaRollDueWhere 是「哪些配额行到期」的条件（qb 配额行、s 订阅；4 个参数都是 now），锁行与探测共用一份，口径不会两样。
//
// 这里只筛选，新周期的起止在 service 里用 Go 算；interval 只用于筛选，与 Go 的 AddDate 在月末相差几天时
// 由 service 跳过、下一轮再滚。
//   - day / month：周期末已过；或周期末被错写得过长（日多出 1 小时以上、月多出 4 天以上）且起点之后已满一个自然周期。
//   - cycle：周期末已过、订阅已续到更晚（提前续费留下的本期行）。
//
// 订阅已过期或走过到期日的不滚。OR 分支让这条筛选不走 period_end 索引（配额行数随订阅线性，10 分钟一次）。
const quotaRollDueWhere = `
 WHERE qb.period_end IS NOT NULL
   AND s.status = 'active'
   AND s.current_period_end > ?
   AND ((qb.period IN ('day', 'month')
         AND (qb.period_end <= ?
              OR (qb.period_end > qb.period_start + CASE qb.period WHEN 'day' THEN interval '1 day 1 hour' ELSE interval '1 month 4 days' END
                  AND qb.period_start + CASE qb.period WHEN 'day' THEN interval '1 day' ELSE interval '1 month' END <= ?)))
     OR (qb.period = 'cycle' AND qb.period_end <= ? AND s.current_period_end > qb.period_end))`

// LockDueQuotaRolls 锁住一批到期的配额行（见 quotaRollDueWhere），按 id 加 FOR UPDATE SKIP LOCKED：
// 与节点扣量同序，正在被扣量锁着的行这批跳过。订阅行一并加 FOR NO KEY UPDATE SKIP LOCKED：滚动要切版本（改 subscriptions），
// 别的路径都是先锁订阅、再锁配额，这里不能拿着配额锁再去等订阅锁（审查 1016-A2 应改 1）；订阅正被锁着的，这批跳过，下一遍再滚。
func (r *Repo) LockDueQuotaRolls(ctx context.Context, now time.Time, limit int) ([]QuotaRollCandidate, error) {
	var out []QuotaRollCandidate
	err := r.d.Conn(ctx).Raw(`
		SELECT qb.id, qb.subscription_id, s.user_id, s.plan_id, s.plan_version_id, qb.metric, qb.period, qb.period_start, qb.period_end,
		       s.current_period_end AS sub_period_end, s.snapshot_interval, s.snapshot_interval_count
		  FROM quota_balances qb
		  JOIN subscriptions s ON s.id = qb.subscription_id`+quotaRollDueWhere+`
		 ORDER BY qb.id
		 LIMIT ?
		   FOR UPDATE OF qb SKIP LOCKED
		   FOR NO KEY UPDATE OF s SKIP LOCKED`, now, now, now, now, limit).Scan(&out).Error
	return out, mapErr(err)
}

// QuotaRollsRemain 探测还有没有到期的行（不加锁）：滚完一遍之后还有，说明有行被锁着跳过了。
func (r *Repo) QuotaRollsRemain(ctx context.Context, now time.Time) (bool, error) {
	var remain bool
	err := r.d.Conn(ctx).Raw(`
		SELECT EXISTS (
		  SELECT 1 FROM quota_balances qb
		    JOIN subscriptions s ON s.id = qb.subscription_id`+quotaRollDueWhere+`)`,
		now, now, now, now).Scan(&remain).Error
	return remain, mapErr(err)
}

// ApplyQuotaRolls 用一条 UPDATE … FROM (VALUES …) 把一批配额行滚进新周期：已用量与本期送的量清零、周期改成给定起止、按需改上限；
// 返回实际改到的行与清零前的用量。只在事务里、且行已被 LockDueQuotaRolls 锁住时调用。
func (r *Repo) ApplyQuotaRolls(ctx context.Context, rolls []QuotaRoll, at time.Time) ([]RolledQuota, error) {
	out := []RolledQuota{}
	if len(rolls) == 0 {
		return out, nil
	}
	var values strings.Builder
	args := make([]any, 0, 1+5*len(rolls))
	args = append(args, at)
	for i, q := range rolls {
		if i > 0 {
			values.WriteString(", ")
		}
		values.WriteString("(?::uuid, ?::timestamptz, ?::timestamptz, ?::boolean, ?::bigint)")
		args = append(args, q.ID, q.PeriodStart, q.PeriodEnd, q.SetLimit, q.Limit)
	}
	err := r.d.Conn(ctx).Raw(`
		UPDATE quota_balances AS qb
		   SET consumed = 0,
		       bonus_value = 0,
		       period_start = v.ps,
		       period_end = v.pe,
		       limit_value = CASE WHEN v.set_limit THEN v.lim ELSE qb.limit_value END,
		       updated_at = ?
		  FROM (VALUES `+values.String()+`) AS v(id, ps, pe, set_limit, lim)
		 WHERE qb.id = v.id
		RETURNING qb.id, qb.subscription_id, qb.metric, OLD.consumed AS consumed_before`, args...).Scan(&out).Error
	return out, mapErr(err)
}

// VersionSwitch 是滚动时把一份订阅从旧版本切到套餐当前版本。
type VersionSwitch struct{ SubscriptionID, From, To uuid.UUID }

// SwitchPlanVersions 用一条 UPDATE … FROM (VALUES …) 批量切版本：只改 plan_version_id 仍是 From 的行（并发换套餐、续费改过的不动），
// 同时写 updated_at；返回实际改到的订阅 id。只在事务里调用。
func (r *Repo) SwitchPlanVersions(ctx context.Context, sw []VersionSwitch, at time.Time) ([]uuid.UUID, error) {
	if len(sw) == 0 {
		return []uuid.UUID{}, nil
	}
	var values strings.Builder
	args := make([]any, 0, 1+3*len(sw))
	args = append(args, at)
	for i, w := range sw {
		if i > 0 {
			values.WriteString(", ")
		}
		values.WriteString("(?::uuid, ?::uuid, ?::uuid)")
		args = append(args, w.SubscriptionID, w.From, w.To)
	}
	var rows []struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	err := r.d.Conn(ctx).Raw(`
		UPDATE subscriptions AS s
		   SET plan_version_id = v.to_version,
		       updated_at = ?
		  FROM (VALUES `+values.String()+`) AS v(id, from_version, to_version)
		 WHERE s.id = v.id
		   AND s.plan_version_id = v.from_version
		RETURNING s.id`, args...).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out, nil
}
