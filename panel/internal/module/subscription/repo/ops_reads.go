package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// uuidArray 把 uuid 切片编成 PostgreSQL uuid 数组字面量。
func uuidArray(ids []uuid.UUID) string {
	if len(ids) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(id.String())
	}
	b.WriteByte('}')
	return b.String()
}

// liveCond 是「生效订阅」的 SQL 片段；? 占位为 now（timestamptz）。
const liveCond = `(s.status = 'active' AND s.current_period_end > CAST(? AS timestamptz) AND s.renewal_closed_at IS NULL)`

// OpsMatch 是按「当前订阅」行筛用户的条件。
type OpsMatch struct {
	Now         time.Time
	RequireLive *bool
	PlanID      *uuid.UUID
	EndBefore   *time.Time
}

// OpsCurrentRow 是一个用户的当前订阅摘要（repo 层）。
type OpsCurrentRow struct {
	UserID    uuid.UUID `gorm:"column:user_id"`
	PlanID    uuid.UUID `gorm:"column:plan_id"`
	PeriodEnd time.Time `gorm:"column:period_end"`
	Live      bool      `gorm:"column:live"`
	LiveCount int       `gorm:"column:live_count"`
}

// OpsFetchFilter 是访问明细里订阅拉取的筛选。
type OpsFetchFilter struct {
	IPHash      []byte
	FilterUsers bool
	UserIDs     []uuid.UUID
	Outcome     string
	Limit       int
}

// OpsFetchRow 是一次订阅拉取（repo 层）。
type OpsFetchRow struct {
	UserID    *uuid.UUID `gorm:"column:user_id"`
	IPEnc     []byte     `gorm:"column:ip_enc"`
	UAEnc     []byte     `gorm:"column:ua_enc"`
	UAFamily  string     `gorm:"column:ua_family"`
	Result    string     `gorm:"column:result"`
	Format    string     `gorm:"column:format"`
	FetchedAt time.Time  `gorm:"column:fetched_at"`
}

// OpsUsersWithAny 返回至少有过一份订阅的用户 id（升序）。
func (r *Repo) OpsUsersWithAny(ctx context.Context) ([]uuid.UUID, error) {
	var out []uuid.UUID
	err := r.d.Conn(ctx).Raw(`SELECT DISTINCT user_id FROM subscriptions ORDER BY user_id`).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []uuid.UUID{}, nil
	}
	return out, nil
}

// OpsUsersWithLive 返回名下有生效订阅的用户 id（升序）。
func (r *Repo) OpsUsersWithLive(ctx context.Context, now time.Time) ([]uuid.UUID, error) {
	var out []uuid.UUID
	err := r.d.Conn(ctx).Raw(`
		SELECT DISTINCT s.user_id
		  FROM subscriptions s
		 WHERE `+liveCond+`
		 ORDER BY s.user_id`, now).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []uuid.UUID{}, nil
	}
	return out, nil
}

// OpsMatchUsers 按当前订阅行与可选条件筛用户 id（升序）。
func (r *Repo) OpsMatchUsers(ctx context.Context, m OpsMatch) ([]uuid.UUID, error) {
	q := `
		WITH cur AS (
		  SELECT DISTINCT ON (s.user_id) s.user_id, s.plan_id, s.current_period_end,
		         bool_or(` + liveCond + `) OVER (PARTITION BY s.user_id) AS has_live
		    FROM subscriptions s
		   ORDER BY s.user_id, (` + liveCond + `) DESC, s.current_period_end DESC, s.created_at DESC, s.id DESC)
		SELECT user_id FROM cur WHERE true`
	// liveCond 在上面出现两次（bool_or 与 ORDER BY），各占一个 now
	args := []any{m.Now, m.Now}
	if m.RequireLive != nil {
		q += ` AND has_live = ?`
		args = append(args, *m.RequireLive)
	}
	if m.PlanID != nil {
		q += ` AND plan_id = ?`
		args = append(args, *m.PlanID)
	}
	if m.EndBefore != nil {
		q += ` AND current_period_end >= ? AND current_period_end < ?`
		args = append(args, m.Now, *m.EndBefore)
	}
	q += ` ORDER BY user_id`

	var out []uuid.UUID
	err := r.d.Conn(ctx).Raw(q, args...).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []uuid.UUID{}, nil
	}
	return out, nil
}

// OpsCurrentByUsers 批量取用户的当前订阅行。
func (r *Repo) OpsCurrentByUsers(ctx context.Context, userIDs []uuid.UUID, now time.Time) ([]OpsCurrentRow, error) {
	if len(userIDs) == 0 {
		return []OpsCurrentRow{}, nil
	}
	var out []OpsCurrentRow
	err := r.d.Conn(ctx).Raw(`
		SELECT DISTINCT ON (s.user_id) s.user_id, s.plan_id, s.current_period_end AS period_end, (`+liveCond+`) AS live,
		       (count(*) FILTER (WHERE `+liveCond+`) OVER (PARTITION BY s.user_id))::int AS live_count
		  FROM subscriptions s
		 WHERE s.user_id = ANY(CAST(? AS uuid[]))
		 ORDER BY s.user_id, (`+liveCond+`) DESC, s.current_period_end DESC, s.created_at DESC, s.id DESC`,
		now, now, uuidArray(userIDs), now).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []OpsCurrentRow{}, nil
	}
	return out, nil
}

// OpsFetches 按条件列订阅拉取日志（时间倒序）。
func (r *Repo) OpsFetches(ctx context.Context, f OpsFetchFilter) ([]OpsFetchRow, error) {
	if f.FilterUsers && len(f.UserIDs) == 0 {
		return []OpsFetchRow{}, nil
	}
	q := `
		SELECT s.user_id, f.ip_enc, f.ua_enc, f.ua_family, f.result,
		       COALESCE(f.format, '') AS format, f.fetched_at
		  FROM subscription_fetch_log f
		  LEFT JOIN subscriptions s ON s.id = f.subscription_id
		 WHERE true`
	args := []any{}
	if len(f.IPHash) > 0 {
		q += ` AND f.ip_hash = ?`
		args = append(args, f.IPHash)
	}
	if f.FilterUsers {
		q += ` AND s.user_id = ANY(CAST(? AS uuid[]))`
		args = append(args, uuidArray(f.UserIDs))
	}
	switch f.Outcome {
	case "success":
		q += ` AND f.result = 'ok'`
	case "error":
		q += ` AND f.result <> 'ok'`
	}
	q += ` ORDER BY f.fetched_at DESC, f.id DESC LIMIT ?`
	args = append(args, f.Limit)

	var out []OpsFetchRow
	err := r.d.Conn(ctx).Raw(q, args...).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []OpsFetchRow{}, nil
	}
	return out, nil
}

// OpsUserFetches 列某用户经凭据关联的拉取日志（时间倒序）。
func (r *Repo) OpsUserFetches(ctx context.Context, userID uuid.UUID, limit int) ([]OpsFetchRow, error) {
	var out []OpsFetchRow
	err := r.d.Conn(ctx).Raw(`
		SELECT c.user_id, f.ip_enc, f.ua_enc, f.ua_family, f.result,
		       COALESCE(f.format, '') AS format, f.fetched_at
		  FROM subscription_fetch_log f
		  JOIN subscription_credentials c ON c.id = f.credential_id
		 WHERE c.user_id = ?
		 ORDER BY f.fetched_at DESC, f.id DESC LIMIT ?`, userID, limit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		return []OpsFetchRow{}, nil
	}
	return out, nil
}

// OpsFetchSources 统计用户自 since 以来成功拉取的不同来源 IP 数。
func (r *Repo) OpsFetchSources(ctx context.Context, userID uuid.UUID, since time.Time) (int, error) {
	var n int
	err := r.d.Conn(ctx).Raw(`
		SELECT count(DISTINCT f.ip_hash)::int
		  FROM subscription_fetch_log f
		  JOIN subscription_credentials c ON c.id = f.credential_id
		 WHERE c.user_id = ? AND f.result = 'ok' AND f.fetched_at > ?`, userID, since).Scan(&n).Error
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}
