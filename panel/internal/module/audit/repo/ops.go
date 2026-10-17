package repo

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// AccessFilter 是访问明细审计侧的筛选。
type AccessFilter struct {
	Include, Exclude []string
	IPHash           []byte
	FilterActors     bool
	ActorIDs         []uuid.UUID
	Outcome          string
	Limit            int
}

// ActorEventRow 是画像里的一条审计原始行。
type ActorEventRow struct {
	Action     string    `gorm:"column:action"`
	Outcome    string    `gorm:"column:outcome"`
	IPEnc      []byte    `gorm:"column:ip_enc"`
	UserAgent  string    `gorm:"column:user_agent"`
	Audience   string    `gorm:"column:audience"`
	OccurredAt time.Time `gorm:"column:occurred_at"`
}

// ActorIPRow 是按来源 IP 归并的统计行。
type ActorIPRow struct {
	IPEnc      []byte    `gorm:"column:ip_enc"`
	N          int       `gorm:"column:n"`
	FirstAt    time.Time `gorm:"column:first_at"`
	LastAt     time.Time `gorm:"column:last_at"`
	Accounts   int       `gorm:"column:accounts"`
	AccountIDs string    `gorm:"column:account_ids"`
}

// AccessRow 是访问明细里的一条审计行。
type AccessRow struct {
	Action     string     `gorm:"column:action"`
	Outcome    string     `gorm:"column:outcome"`
	IPEnc      []byte     `gorm:"column:ip_enc"`
	UserAgent  string     `gorm:"column:user_agent"`
	ActorID    *uuid.UUID `gorm:"column:actor_id"`
	OccurredAt time.Time  `gorm:"column:occurred_at"`
}

// ClusterRow 是同源 IP 聚类列表的一行。
type ClusterRow struct {
	IPHash     []byte     `gorm:"column:ip_hash"`
	Accounts   int        `gorm:"column:accounts"`
	Events     int        `gorm:"column:events"`
	FirstSeen  time.Time  `gorm:"column:first_seen"`
	LastSeen   time.Time  `gorm:"column:last_seen"`
	AccountIDs string     `gorm:"column:account_ids"`
	IPEnc      []byte     `gorm:"column:ip_enc"`
	Decision   *string    `gorm:"column:decision"`
	DecidedAt  *time.Time `gorm:"column:decided_at"`
	ExpiresAt  *time.Time `gorm:"column:expires_at"`
}

// RegisteredIPEnc 取用户注册那条审计（本人为操作者）的来源 IP 密文；没有为 nil。
func (r *Repo) RegisteredIPEnc(ctx context.Context, userID uuid.UUID) ([]byte, error) {
	// bytea 单列扫进结构体字段；没有行时 RowsAffected 为 0
	var row struct {
		IPEnc []byte `gorm:"column:ip_enc"`
	}
	res := r.d.Conn(ctx).Raw(`
		SELECT ip_enc FROM audit_events
		 WHERE action = 'user.registered' AND actor_id = ? AND ip_enc IS NOT NULL
		 ORDER BY occurred_at, id LIMIT 1`, userID).Scan(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 || len(row.IPEnc) == 0 {
		return nil, nil
	}
	return row.IPEnc, nil
}

// ActorEvents 取用户作为操作者的最近审计。
func (r *Repo) ActorEvents(ctx context.Context, userID uuid.UUID, limit int) ([]ActorEventRow, error) {
	var rows []ActorEventRow
	err := r.d.Conn(ctx).Raw(`
		SELECT action, outcome, ip_enc,
		       COALESCE(user_agent,'') AS user_agent,
		       COALESCE(actor_audience,'') AS audience,
		       occurred_at
		  FROM audit_events
		 WHERE actor_id = ?
		 ORDER BY occurred_at DESC, id DESC
		 LIMIT ?`, userID, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []ActorEventRow{}, nil
	}
	return rows, nil
}

// ActorIPStats 取用户最常用的来源 IP 及近窗内同源账号数。
func (r *Repo) ActorIPStats(ctx context.Context, userID uuid.UUID, since time.Time, limit int) ([]ActorIPRow, error) {
	var rows []ActorIPRow
	err := r.d.Conn(ctx).Raw(`
		WITH mine AS (
		  SELECT a.ip_hash,
		         (array_agg(a.ip_enc ORDER BY a.occurred_at DESC) FILTER (WHERE a.ip_enc IS NOT NULL))[1] AS ip_enc,
		         count(*) AS n, min(a.occurred_at) AS first_at, max(a.occurred_at) AS last_at
		    FROM audit_events a
		   WHERE a.actor_id = ? AND a.ip_hash IS NOT NULL
		   GROUP BY a.ip_hash
		   ORDER BY count(*) DESC, max(a.occurred_at) DESC, a.ip_hash
		   LIMIT ?
		)
		SELECT m.ip_enc, m.n, m.first_at, m.last_at,
		       COALESCE(c.account_count, 1) AS accounts,
		       COALESCE(c.account_ids, '') AS account_ids
		  FROM mine m
		  LEFT JOIN LATERAL (
		        SELECT count(DISTINCT e.actor_id) AS account_count,
		               string_agg(DISTINCT e.actor_id::text, ',') AS account_ids
		          FROM audit_events e
		         WHERE e.ip_hash IS NOT NULL AND e.ip_hash = m.ip_hash
		           AND e.actor_kind = 'user' AND e.actor_id IS NOT NULL
		           AND e.occurred_at > ?
		        HAVING count(DISTINCT e.actor_id) > 1) c ON true
		 ORDER BY m.n DESC, m.last_at DESC, m.ip_hash`, userID, limit, since).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []ActorIPRow{}, nil
	}
	return rows, nil
}

// AccessEvents 按筛选取访问明细审计行。
func (r *Repo) AccessEvents(ctx context.Context, f AccessFilter) ([]AccessRow, error) {
	if f.FilterActors && len(f.ActorIDs) == 0 {
		return []AccessRow{}, nil
	}
	tx := r.d.Conn(ctx).Table("audit_events")
	if len(f.Include) > 0 {
		parts := make([]string, len(f.Include))
		args := make([]any, len(f.Include))
		for i, p := range f.Include {
			parts[i] = "action LIKE ? ESCAPE '\\'"
			args[i] = db.EscapeLike(p) + "%"
		}
		tx = tx.Where("("+strings.Join(parts, " OR ")+")", args...)
	}
	if len(f.Exclude) > 0 {
		parts := make([]string, len(f.Exclude))
		args := make([]any, len(f.Exclude))
		for i, p := range f.Exclude {
			parts[i] = "action LIKE ? ESCAPE '\\'"
			args[i] = db.EscapeLike(p) + "%"
		}
		tx = tx.Where("NOT ("+strings.Join(parts, " OR ")+")", args...)
	}
	if len(f.IPHash) > 0 {
		tx = tx.Where("ip_hash = ?", f.IPHash)
	}
	if f.FilterActors {
		tx = tx.Where("actor_id = ANY(CAST(? AS uuid[]))", uuidArray(f.ActorIDs))
	}
	if f.Outcome == "error" {
		tx = tx.Where("outcome <> ?", "success")
	} else if f.Outcome != "" {
		tx = tx.Where("outcome = ?", f.Outcome)
	}
	var rows []AccessRow
	err := tx.Select(
		"action", "outcome", "ip_enc",
		"COALESCE(user_agent,'') AS user_agent",
		"actor_id", "occurred_at",
	).Order("occurred_at DESC, id DESC").Limit(f.Limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []AccessRow{}, nil
	}
	return rows, nil
}

// Clusters 列出近窗内多账号同源 IP 聚类。
func (r *Repo) Clusters(ctx context.Context, since time.Time, includeReviewed bool, limit int) ([]ClusterRow, error) {
	var rows []ClusterRow
	err := r.d.Conn(ctx).Raw(`
		WITH c AS (
		  SELECT ip_hash, count(DISTINCT actor_id) AS accounts, count(*) AS events,
		         min(occurred_at) AS first_seen, max(occurred_at) AS last_seen,
		         string_agg(DISTINCT actor_id::text, ',') AS account_ids
		    FROM audit_events
		   WHERE ip_hash IS NOT NULL AND actor_kind = 'user' AND actor_id IS NOT NULL AND occurred_at > ?
		   GROUP BY ip_hash
		  HAVING count(DISTINCT actor_id) > 1)
		SELECT c.ip_hash, c.accounts, c.events, c.first_seen, c.last_seen, c.account_ids,
		       (SELECT a.ip_enc FROM audit_events a WHERE a.ip_hash = c.ip_hash AND a.ip_enc IS NOT NULL
		         ORDER BY a.occurred_at DESC LIMIT 1) AS ip_enc,
		       r.decision, r.decided_at, r.expires_at
		  FROM c
		  LEFT JOIN ip_cluster_reviews r ON r.ip_hash = c.ip_hash
		 WHERE ? OR r.decision IS DISTINCT FROM 'normal' OR r.expires_at <= now()
		 ORDER BY c.accounts DESC, c.last_seen DESC, c.ip_hash
		 LIMIT ?`, since, includeReviewed, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []ClusterRow{}, nil
	}
	return rows, nil
}

// ClusterMembers 取聚类在窗内的成员账号 id。
func (r *Repo) ClusterMembers(ctx context.Context, hash []byte, since time.Time) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.d.Conn(ctx).Raw(`
		SELECT DISTINCT actor_id FROM audit_events
		 WHERE ip_hash = ? AND actor_kind = 'user' AND actor_id IS NOT NULL AND occurred_at > ?
		 ORDER BY actor_id`, hash, since).Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	if ids == nil {
		return []uuid.UUID{}, nil
	}
	return ids, nil
}

// UpsertClusterReview 写入或更新聚类复核结论。
func (r *Repo) UpsertClusterReview(ctx context.Context, hash []byte, decision, note string, decidedBy uuid.UUID, expiresAt *time.Time) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.d.Conn(ctx).Raw(`
		INSERT INTO ip_cluster_reviews (decision, ip_hash, note, decided_by, decided_at, expires_at)
		VALUES (?, ?, ?, ?, now(), ?)
		ON CONFLICT (ip_hash) DO UPDATE SET
		  decision = EXCLUDED.decision,
		  note = EXCLUDED.note,
		  decided_by = EXCLUDED.decided_by,
		  decided_at = EXCLUDED.decided_at,
		  expires_at = EXCLUDED.expires_at
		RETURNING id`, decision, hash, note, decidedBy, expiresAt).Row().Scan(&id)
	return id, err
}

func uuidArray(ids []uuid.UUID) string {
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
