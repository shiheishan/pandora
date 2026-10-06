// [INPUT]: 依赖 platform/db 的租户事务，读 audit_events / audit_ip_clusters / users / subscription_fetch_log / subscription_credentials / subscriptions / subscription_usage_daily
// [OUTPUT]: 对外提供 UserActivity、UserActivityEvent、UserActivityIP、UserPeer、UserFetch、TimeseriesPoint 与 Service 的 UserActivity / UserPeers / UserFetches / ActivityTimeseries
// [POS]: adminops 的用户风控画像与行为时序读模型（从 api/admin 的 profile.go 下沉，与 audit.go / risk.go 同读审计表）：只给密文与哈希聚合，明文 IP 由 handler 用 Envelope 按表 AAD 解开；画像分三次事务读（行为与 IP、关联账号邮箱、订阅拉取），后两次失败时调用方照旧忽略、用已读到的部分

package adminops

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// UserActivity 是一个用户的审计侧画像：注册 IP、最近行为与按 IP 归并的统计，IP 全是密文。
type UserActivity struct {
	RegisteredIPEnc []byte
	Events          []UserActivityEvent
	IPs             []UserActivityIP
}

type UserActivityEvent struct {
	Action      string
	Outcome     string
	SourceIPEnc []byte
	UA          string
	Domain      string
	At          time.Time
}

type UserActivityIP struct {
	SourceIPEnc []byte
	Count       int
	First       time.Time
	Last        time.Time
	// Accounts 是同一 IP 下的账号数（含本人），AccountIDs 是这些账号
	Accounts   int
	AccountIDs []string
}

// UserPeer 是关联账号的 id 与邮箱。
type UserPeer struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// UserFetch 是一条订阅拉取记录，IP 与 UA 是 subscription_fetch_log 的密文。
type UserFetch struct {
	IPEnc  []byte
	UAEnc  []byte
	Family string
	Result string
	Format string
	At     time.Time
}

// UserActivity 读注册 IP、最近 80 条行为与前 20 个来源 IP 的归并统计。
func (s *Service) UserActivity(ctx context.Context, tenantID, userID string) (*UserActivity, error) {
	out := &UserActivity{Events: []UserActivityEvent{}, IPs: []UserActivityIP{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 注册 IP 只在这里出（要 security.audit.read），不进 iam.user.read 就能看的用户详情
		var regEnc []byte
		if err := tx.QueryRow(ctx, `
			SELECT source_ip_enc FROM audit_events
			 WHERE tenant_id = $1 AND action = 'user.registered' AND actor_id = $2::uuid
			 ORDER BY occurred_at, id LIMIT 1`, tenantID, userID).Scan(&regEnc); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out.RegisteredIPEnc = regEnc

		rows, err := tx.Query(ctx, `
			SELECT action, outcome, COALESCE(source_ip_enc, ''::bytea),
			       COALESCE(user_agent,''), COALESCE(api_domain,''), occurred_at
			  FROM audit_events
			 WHERE tenant_id = $1 AND actor_id = $2::uuid
			 ORDER BY occurred_at DESC LIMIT 80`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e UserActivityEvent
			if err := rows.Scan(&e.Action, &e.Outcome, &e.SourceIPEnc, &e.UA, &e.Domain, &e.At); err != nil {
				return err
			}
			out.Events = append(out.Events, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// 按 IP 归并，并算出每个 IP 上还有多少别的账号。
		// 用哈希做 JOIN，明文只在最后展示时解一次。
		irows, err := tx.Query(ctx, `
			SELECT COALESCE((array_agg(a.source_ip_enc ORDER BY a.occurred_at DESC))[1], ''::bytea),
			       count(*), min(a.occurred_at), max(a.occurred_at),
			       COALESCE(c.account_count, 1),
			       COALESCE(c.accounts, ARRAY[]::text[])
			  FROM audit_events a
			  LEFT JOIN audit_ip_clusters c
			    ON c.tenant_id = a.tenant_id AND c.source_ip_hash = a.source_ip_hash
			 WHERE a.tenant_id = $1 AND a.actor_id = $2::uuid
			   AND a.source_ip_hash IS NOT NULL
			 GROUP BY a.source_ip_hash, c.account_count, c.accounts
			 ORDER BY count(*) DESC LIMIT 20`, tenantID, userID)
		if err != nil {
			return err
		}
		defer irows.Close()
		for irows.Next() {
			var st UserActivityIP
			if err := irows.Scan(&st.SourceIPEnc, &st.Count, &st.First, &st.Last,
				&st.Accounts, &st.AccountIDs); err != nil {
				return err
			}
			out.IPs = append(out.IPs, st)
		}
		return irows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UserPeers 给关联账号补上邮箱（最多 50 个）。出错时返回已读到的部分与错误。
func (s *Service) UserPeers(ctx context.Context, tenantID string, ids []string) ([]UserPeer, error) {
	peers := []UserPeer{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id::text, COALESCE(email,'') FROM users
			  WHERE tenant_id = $1 AND id = ANY($2::uuid[]) LIMIT 50`, tenantID, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p UserPeer
			if err := rows.Scan(&p.ID, &p.Email); err != nil {
				return err
			}
			peers = append(peers, p)
		}
		return rows.Err()
	})
	return peers, err
}

// UserFetches 读最近 50 次订阅拉取与近 7 天成功拉取的不同来源数（按哈希算，不解密）。
// 出错时返回已读到的部分与错误。
//
// 这是判断「链接是不是被分享出去了」最直接的证据：
// 一个人的正常用量是几台设备定时拉，来源集中；
// 挂到群里的链接会在短时间内冒出一堆互不相干的地址。
func (s *Service) UserFetches(ctx context.Context, tenantID, userID string) ([]UserFetch, int, error) {
	fetches := []UserFetch{}
	var fetchSources int
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT COALESCE(l.ip_enc, ''::bytea), COALESCE(l.ua_enc, ''::bytea),
			       COALESCE(l.ua_family,''), l.result, COALESCE(l.format,''), l.fetched_at
			  FROM subscription_fetch_log l
			  JOIN subscription_credentials c ON c.id = l.credential_id
			 WHERE l.tenant_id = $1 AND c.user_id = $2::uuid
			 ORDER BY l.fetched_at DESC LIMIT 50`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f UserFetch
			if err := rows.Scan(&f.IPEnc, &f.UAEnc, &f.Family, &f.Result, &f.Format, &f.At); err != nil {
				return err
			}
			fetches = append(fetches, f)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// 不同来源数用哈希算，不需要解密
		return tx.QueryRow(ctx, `
			SELECT count(DISTINCT l.ip_hash)
			  FROM subscription_fetch_log l
			  JOIN subscription_credentials c ON c.id = l.credential_id
			 WHERE l.tenant_id = $1 AND c.user_id = $2::uuid
			   AND l.result = 'ok' AND l.fetched_at > now() - interval '7 days'`,
			tenantID, userID).Scan(&fetchSources)
	})
	return fetches, fetchSources, err
}

// TimeseriesPoint 是行为趋势图的一天。
type TimeseriesPoint struct {
	Day        string `json:"day"`
	Registered int    `json:"registered"`
	Logins     int    `json:"logins"`
	Orders     int    `json:"orders"`
	UniqueIPs  int    `json:"unique_ips"`
	// ActiveUsers 是当天有成功订阅拉取或有流量归属的去重用户数
	ActiveUsers int `json:"active_users"`
}

// ActivityTimeseries 返回最近 days 天按天聚合的行为统计。
//
// 全程只用哈希与计数，不解密任何来源信息 —— 画趋势不需要知道是谁。
func (s *Service) ActivityTimeseries(ctx context.Context, tenantID string, days int) ([]TimeseriesPoint, error) {
	out := []TimeseriesPoint{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// generate_series 补齐没有数据的日子。
		// 不补的话折线图会把「那天没人注册」画成一条直接跳过去的线，
		// 看起来像是数据缺失而不是真的没人。
		rows, err := tx.Query(ctx, `
			WITH d AS (
			  SELECT generate_series(
			    date_trunc('day', now()) - make_interval(days => $2 - 1),
			    date_trunc('day', now()), '1 day')::date AS day
			), active AS (
			  -- 两路来源去重：成功的订阅拉取（按会话时区切日，与本接口其余列一致），
			  -- 与按日流量（00072，按用户 / 站点时区记的日）
			  SELECT day, count(DISTINCT user_id)::int AS n FROM (
			    SELECT date_trunc('day', f.fetched_at)::date AS day, s.user_id
			      FROM subscription_fetch_log f
			      JOIN subscriptions s ON s.tenant_id = f.tenant_id AND s.id = f.subscription_id
			     WHERE f.tenant_id = $1 AND f.result = 'ok'
			       AND f.fetched_at >= date_trunc('day', now()) - make_interval(days => $2 - 1)
			    UNION
			    SELECT u.day, s.user_id
			      FROM subscription_usage_daily u
			      JOIN subscriptions s ON s.tenant_id = u.tenant_id AND s.id = u.subscription_id
			     WHERE u.tenant_id = $1 AND u.bytes > 0
			       AND u.day >= (date_trunc('day', now()) - make_interval(days => $2 - 1))::date
			  ) x GROUP BY day
			)
			SELECT to_char(d.day, 'MM-DD'),
			  count(*) FILTER (WHERE a.action = 'user.registered'),
			  count(*) FILTER (WHERE a.action = 'user.login' AND a.outcome = 'success'),
			  count(*) FILTER (WHERE a.action = 'order.created'),
			  count(DISTINCT a.source_ip_hash),
			  coalesce(max(ac.n), 0)
			  FROM d
			  LEFT JOIN active ac ON ac.day = d.day
			  LEFT JOIN audit_events a
			    ON a.tenant_id = $1 AND date_trunc('day', a.occurred_at)::date = d.day
			 GROUP BY d.day ORDER BY d.day`, tenantID, days)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p TimeseriesPoint
			if err := rows.Scan(&p.Day, &p.Registered, &p.Logins, &p.Orders, &p.UniqueIPs, &p.ActiveUsers); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
