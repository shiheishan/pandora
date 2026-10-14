package repo

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// PullRow 是订阅拉取按令牌哈希一次读到的东西：凭据、订阅、本期流量配额与挂在这一份上的流量包余量。
type PullRow struct {
	CredentialID     uuid.UUID  `gorm:"column:credential_id"`
	TokenHash        []byte     `gorm:"column:token_hash"`
	CredentialStatus string     `gorm:"column:credential_status"`
	GraceUntil       *time.Time `gorm:"column:grace_until"`
	SubscriptionID   uuid.UUID  `gorm:"column:subscription_id"`
	UserID           uuid.UUID  `gorm:"column:user_id"`
	PlanID           uuid.UUID  `gorm:"column:plan_id"`
	PlanVersionID    uuid.UUID  `gorm:"column:plan_version_id"`
	ProxyUUID        uuid.UUID  `gorm:"column:proxy_uuid"`
	Status           string     `gorm:"column:status"`
	PeriodEnd        time.Time  `gorm:"column:period_end"`
	RenewalClosedAt  *time.Time `gorm:"column:renewal_closed_at"`
	Label            *string    `gorm:"column:label"`
	TrafficLimit     *int64     `gorm:"column:traffic_limit"`
	TrafficConsumed  *int64     `gorm:"column:traffic_consumed"`
	// TrafficPeriodEnd 是同一行配额的周期末（下次重置时刻）；没有配额行或 total 类为 nil。信息节点的「距下次重置」用它
	TrafficPeriodEnd *time.Time `gorm:"column:traffic_period_end"`
	PackRemaining    int64      `gorm:"column:pack_remaining"`
}

// PullByTokenHash 按令牌哈希读拉取所需的一行；没有返回 false。
// 流量配额取本期窗口里最紧的限量行（余量最小）；窗口里全是不限量行时取到 NULL 上限，按不限显示。
// 同一行的周期末顺带取出（信息节点的「距下次重置」），不另查一次。
func (r *Repo) PullByTokenHash(ctx context.Context, hash []byte, now time.Time) (PullRow, bool, error) {
	var row PullRow
	wNow, wGrace := trafficQuotaWindow(now)
	err := r.d.Conn(ctx).Raw(`
		SELECT sc.id AS credential_id,
		       sc.token_hash,
		       sc.status AS credential_status,
		       sc.grace_until,
		       s.id AS subscription_id,
		       s.user_id,
		       s.plan_id,
		       s.plan_version_id,
		       s.proxy_uuid,
		       s.status AS status,
		       s.current_period_end AS period_end,
		       s.renewal_closed_at,
		       s.label,
		       q.limit_value AS traffic_limit,
		       q.consumed AS traffic_consumed,
		       q.period_end AS traffic_period_end,
		       (SELECT coalesce(sum(g.granted_bytes - g.consumed_bytes), 0)::bigint
		          FROM traffic_pack_grants g
		         WHERE g.subscription_id = s.id AND g.consumed_bytes < g.granted_bytes) AS pack_remaining
		  FROM subscription_credentials sc
		  JOIN subscriptions s ON s.id = sc.subscription_id
		  LEFT JOIN LATERAL (
			SELECT qb.limit_value, qb.consumed, qb.period_end
			  FROM quota_balances qb
			 WHERE qb.subscription_id = s.id AND `+trafficQuotaWindowCond+`
			 ORDER BY (qb.limit_value IS NULL), (qb.limit_value - qb.consumed), qb.period_end DESC NULLS LAST
			 LIMIT 1
		  ) q ON true
		 WHERE sc.token_hash = ?`, wNow, wGrace, hash).Scan(&row).Error
	if err != nil {
		return PullRow{}, false, mapErr(err)
	}
	if row.CredentialID == uuid.Nil {
		return PullRow{}, false, nil
	}
	return row, true, nil
}

// TouchCredential 记一次拉取：fetch_count + 1、last_fetched_at = at。
func (r *Repo) TouchCredential(ctx context.Context, credID uuid.UUID, at time.Time) error {
	err := r.d.Conn(ctx).Exec(
		"UPDATE subscription_credentials SET fetch_count = fetch_count + 1, last_fetched_at = ? WHERE id = ?",
		at, credID,
	).Error
	return mapErr(err)
}

// LinkRow 是门户链接列表的一行：一份没停用的订阅上的有效凭据，带订阅状态与近期来源数。
type LinkRow struct {
	CredentialID    uuid.UUID  `gorm:"column:credential_id"`
	SubscriptionID  uuid.UUID  `gorm:"column:subscription_id"`
	TokenEncrypted  []byte     `gorm:"column:token_encrypted"`
	FetchCount      int64      `gorm:"column:fetch_count"`
	LastFetchedAt   *time.Time `gorm:"column:last_fetched_at"`
	DistinctSources int        `gorm:"column:distinct_sources"`
	Status          string     `gorm:"column:status"`
	PeriodEnd       time.Time  `gorm:"column:period_end"`
	RenewalClosedAt *time.Time `gorm:"column:renewal_closed_at"`
}

// LinksByUser 读用户每份续费窗口没关的订阅上 status='active' 的凭据，按凭据创建时间倒序。
func (r *Repo) LinksByUser(ctx context.Context, userID uuid.UUID, since time.Time) ([]LinkRow, error) {
	var out []LinkRow
	err := r.d.Conn(ctx).Raw(`
		SELECT sc.id AS credential_id,
		       sc.subscription_id,
		       sc.token_encrypted,
		       sc.fetch_count,
		       sc.last_fetched_at,
		       (SELECT count(DISTINCT f.ip_hash)::int
		          FROM subscription_fetch_log f
		         WHERE f.credential_id = sc.id AND f.result = 'ok' AND f.fetched_at > ?) AS distinct_sources,
		       s.status AS status,
		       s.current_period_end AS period_end,
		       s.renewal_closed_at
		  FROM subscription_credentials sc
		  JOIN subscriptions s ON s.id = sc.subscription_id
		 WHERE s.user_id = ?
		   AND sc.status = 'active'
		   AND s.renewal_closed_at IS NULL
		 ORDER BY sc.created_at DESC, sc.id DESC`, since, userID).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []LinkRow{}, nil
	}
	return out, nil
}

// ActiveCredential 读一份订阅当前的有效凭据（status='active'）；没有返回 false。
func (r *Repo) ActiveCredential(ctx context.Context, subID uuid.UUID) (model.Credential, bool, error) {
	var c model.Credential
	err := r.d.Conn(ctx).Where("subscription_id = ? AND status = 'active'", subID).Take(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Credential{}, false, nil
		}
		return model.Credential{}, false, mapErr(err)
	}
	return c, true, nil
}

// RevokeActiveCredentials 吊销一份订阅的有效凭据（换链接）；返回被吊销凭据的 id（实时推送用）。
func (r *Repo) RevokeActiveCredentials(ctx context.Context, subID uuid.UUID, reason string, at time.Time, grace *time.Time) ([]uuid.UUID, error) {
	var rows []struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	err := r.d.Conn(ctx).Raw(`
		UPDATE subscription_credentials
		   SET status = 'revoked', revoked_at = ?, revoked_reason = ?,
		       rotated_count = rotated_count + 1, rotated_at = ?, grace_until = ?
		 WHERE subscription_id = ? AND status = 'active'
	RETURNING id`, at, reason, at, grace, subID).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out, nil
}

// RotateProxyUUID 给订阅换一个新的节点身份，返回新值；订阅不存在返回 uuid.Nil 与 nil 错误。
func (r *Repo) RotateProxyUUID(ctx context.Context, subID uuid.UUID) (uuid.UUID, error) {
	var out struct {
		ProxyUUID uuid.UUID `gorm:"column:proxy_uuid"`
	}
	err := r.d.Conn(ctx).Raw(
		"UPDATE subscriptions SET proxy_uuid = gen_random_uuid(), updated_at = now() WHERE id = ? RETURNING proxy_uuid",
		subID,
	).Scan(&out).Error
	if err != nil {
		return uuid.Nil, mapErr(err)
	}
	return out.ProxyUUID, nil
}
