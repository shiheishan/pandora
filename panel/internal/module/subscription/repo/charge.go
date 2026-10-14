// 节点扣量批量写回：只给 service.ChargeTraffic 用。
// 锁序：配额行（按 id）→ 流量包（按订阅、created_at、id）→ 按日用量（按订阅 id）。
package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TrafficQuotaRow 是扣量时锁住的一条流量配额行。Room 是本行剩余额度（limit_value - consumed），不限量为 nil。
type TrafficQuotaRow struct {
	ID             uuid.UUID `gorm:"column:id"`
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	Period         string    `gorm:"column:period"`
	Room           *int64    `gorm:"column:room"`
}

// OpenTrafficPack 是扣量时锁住的一笔还有剩余的流量包。
type OpenTrafficPack struct {
	ID             uuid.UUID `gorm:"column:id"`
	SubscriptionID uuid.UUID `gorm:"column:subscription_id"`
	Remaining      int64     `gorm:"column:remaining"`
}

// QuotaAdd 是批量写回配额 consumed 的一项。
type QuotaAdd struct {
	ID     uuid.UUID `json:"id"`
	Amount int64     `json:"amount"`
}

// PackTake 是批量写回流量包 consumed_bytes 的一项。
type PackTake struct {
	ID     uuid.UUID `json:"id"`
	Amount int64     `json:"amount"`
}

// UsageAdd 是按日用量累加的一项。
type UsageAdd struct {
	SubscriptionID uuid.UUID `json:"subscription_id"`
	Bytes          int64     `json:"bytes"`
}

// SubscriptionIDsByNodeUIDs 批量把 node_uid 换成订阅 id；找不到的 uid 不在结果里。
func (r *Repo) SubscriptionIDsByNodeUIDs(ctx context.Context, uids []int64) (map[int64]uuid.UUID, error) {
	out := make(map[int64]uuid.UUID, len(uids))
	if len(uids) == 0 {
		return out, nil
	}
	var rows []struct {
		NodeUID int64     `gorm:"column:node_uid"`
		ID      uuid.UUID `gorm:"column:id"`
	}
	err := r.d.Conn(ctx).Raw(
		"SELECT node_uid, id FROM subscriptions WHERE node_uid IN ?",
		uids,
	).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	for _, row := range rows {
		out[row.NodeUID] = row.ID
	}
	return out, nil
}

// LockTrafficQuotas 锁住本批订阅在本期窗口内的 traffic.bytes 配额行（按 id 排序）。
// 窗口与名单资格同一口径（quota_window.go 的 trafficQuotaWindowCond）：本周期与滚动空窗那一行。
func (r *Repo) LockTrafficQuotas(ctx context.Context, subIDs []uuid.UUID, now time.Time) ([]TrafficQuotaRow, error) {
	if len(subIDs) == 0 {
		return nil, nil
	}
	var out []TrafficQuotaRow
	wNow, wGrace := trafficQuotaWindow(now)
	err := r.d.Conn(ctx).Raw(`
		SELECT qb.id, qb.subscription_id, qb.period, qb.remaining AS room
		  FROM quota_balances qb
		 WHERE qb.subscription_id IN ? AND `+trafficQuotaWindowCond+`
		 ORDER BY qb.id FOR UPDATE`, subIDs, wNow, wGrace).Scan(&out).Error
	return out, mapErr(err)
}

// LockOpenTrafficPacks 锁住本批订阅还有剩余的流量包（traffic_pack_grants_open_sub_idx；先到先扣顺序）。
func (r *Repo) LockOpenTrafficPacks(ctx context.Context, subIDs []uuid.UUID) ([]OpenTrafficPack, error) {
	if len(subIDs) == 0 {
		return nil, nil
	}
	var out []OpenTrafficPack
	err := r.d.Conn(ctx).Raw(`
		SELECT id, subscription_id, granted_bytes - consumed_bytes AS remaining
		  FROM traffic_pack_grants
		 WHERE subscription_id IN ? AND consumed_bytes < granted_bytes
		 ORDER BY subscription_id, created_at, id FOR UPDATE`, subIDs).Scan(&out).Error
	return out, mapErr(err)
}

// AddQuotaConsumed 批量把 consumed 饱和加到配额行上。
func (r *Repo) AddQuotaConsumed(ctx context.Context, adds []QuotaAdd) error {
	if len(adds) == 0 {
		return nil
	}
	b, err := json.Marshal(adds)
	if err != nil {
		return err
	}
	res := r.d.Conn(ctx).Exec(`
		UPDATE quota_balances q
		   SET consumed = CASE WHEN q.consumed > 9223372036854775807 - c.amount
		                       THEN 9223372036854775807 ELSE q.consumed + c.amount END,
		       updated_at = ?
		  FROM jsonb_to_recordset(?::jsonb) AS c(id uuid, amount bigint)
		 WHERE q.id = c.id`, now(), string(b))
	if res.Error != nil {
		return mapErr(res.Error)
	}
	if res.RowsAffected != int64(len(adds)) {
		return fmt.Errorf("subscription: 写回配额改到 %d 行，应为 %d 行", res.RowsAffected, len(adds))
	}
	return nil
}

// TakeTrafficPacks 批量把 consumed_bytes 加到流量包上。
func (r *Repo) TakeTrafficPacks(ctx context.Context, takes []PackTake) error {
	if len(takes) == 0 {
		return nil
	}
	b, err := json.Marshal(takes)
	if err != nil {
		return err
	}
	res := r.d.Conn(ctx).Exec(`
		UPDATE traffic_pack_grants g
		   SET consumed_bytes = g.consumed_bytes + c.amount,
		       updated_at = ?
		  FROM jsonb_to_recordset(?::jsonb) AS c(id uuid, amount bigint)
		 WHERE g.id = c.id`, now(), string(b))
	if res.Error != nil {
		return mapErr(res.Error)
	}
	if res.RowsAffected != int64(len(takes)) {
		return fmt.Errorf("subscription: 写回流量包改到 %d 行，应为 %d 行", res.RowsAffected, len(takes))
	}
	return nil
}

// AddUsageDaily 按日累加用量（day 形如 2026-10-12）；同一订阅同一天饱和相加。
func (r *Repo) AddUsageDaily(ctx context.Context, day string, adds []UsageAdd) error {
	if len(adds) == 0 {
		return nil
	}
	b, err := json.Marshal(adds)
	if err != nil {
		return err
	}
	return mapErr(r.d.Conn(ctx).Exec(`
		INSERT INTO subscription_usage_daily (subscription_id, day, bytes)
		SELECT c.subscription_id, ?::date, c.bytes
		  FROM jsonb_to_recordset(?::jsonb) AS c(subscription_id uuid, bytes bigint)
		 ORDER BY c.subscription_id
		ON CONFLICT (subscription_id, day) DO UPDATE
		   SET bytes = CASE WHEN subscription_usage_daily.bytes > 9223372036854775807 - EXCLUDED.bytes
		                    THEN 9223372036854775807 ELSE subscription_usage_daily.bytes + EXCLUDED.bytes END,
		       updated_at = now()`, day, string(b)).Error)
}
