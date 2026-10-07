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
		//
		// 先只取这个用户最常用的 20 个来源（mine），再逐个按哈希数同源账号。原来 LEFT JOIN
		// 视图 audit_ip_clusters，规划器要先把全租户 90 天的审计按 IP 整个 GROUP BY 一遍；
		// 这里的 LATERAL 与视图同一口径（近 90 天、用户事件、actor_id 非空、多于一个账号才
		// 算同源），只走 audit_events_ip_idx 查这 20 个哈希。并列时按最近出现、再按哈希排，
		// 结果稳定（原来并列的先后未定义）。
		irows, err := tx.Query(ctx, `
			WITH mine AS MATERIALIZED (
			  SELECT a.source_ip_hash,
			         COALESCE((array_agg(a.source_ip_enc ORDER BY a.occurred_at DESC))[1], ''::bytea) AS ip_enc,
			         count(*) AS n, min(a.occurred_at) AS first_at, max(a.occurred_at) AS last_at
			    FROM audit_events a
			   WHERE a.tenant_id = $1 AND a.actor_id = $2::uuid
			     AND a.source_ip_hash IS NOT NULL
			   GROUP BY a.source_ip_hash
			   ORDER BY count(*) DESC, max(a.occurred_at) DESC, a.source_ip_hash
			   LIMIT 20
			)
			SELECT m.ip_enc, m.n, m.first_at, m.last_at,
			       COALESCE(c.account_count, 1),
			       COALESCE(c.accounts, ARRAY[]::text[])
			  FROM mine m
			  LEFT JOIN LATERAL (
			        SELECT count(DISTINCT e.actor_id) AS account_count,
			               array_agg(DISTINCT e.actor_id::text) AS accounts
			          FROM audit_events e
			         WHERE e.tenant_id = $1
			           AND e.source_ip_hash IS NOT NULL AND e.source_ip_hash = m.source_ip_hash
			           AND e.actor_kind = 'user' AND e.actor_id IS NOT NULL
			           AND e.occurred_at > now() - interval '90 days'
			        HAVING count(DISTINCT e.actor_id) > 1) c ON true
			 ORDER BY m.n DESC, m.last_at DESC, m.source_ip_hash`, tenantID, userID)
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
//
// 已结束且可用的日子读按天汇总（00107 的 activity_daily，由保留期任务重算），今天与缺行或
// 不可用的日子实时算；两边经同一个口径函数 app.activity_daily_compute（按会话时区切日，没有数据的
// 日子补 0，不让折线图把「那天没人注册」画成数据缺失），见 activity_rollup.go。
func (s *Service) ActivityTimeseries(ctx context.Context, tenantID string, days int) ([]TimeseriesPoint, error) {
	var out []TimeseriesPoint
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) (err error) {
		out, err = activityTimeseriesTx(ctx, tx, tenantID, days)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
