// [INPUT]: 依赖 audit_events / subscription_fetch_log 与 Deps.Envelope 解密来源 IP，依赖 platform 的 db/httpx
// [OUTPUT]: 对外提供 decryptIP / decryptWith 解密助手、userProfile 风控画像、statsTimeseries 注册与活跃时序
// [POS]: api/admin 的用户画像与风控统计；解密助手被 access_log.go、audit_log.go、risk.go 共用，共享 IP 聚类已移到 risk.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

// 用户画像与风控统计。
//
// 三个问题要能回答：
//   这个账号都干了什么          → 行为时间线
//   还有谁和他从同一个地方来     → 关联账号
//   整体趋势有没有异常           → 时序统计
//
// 前两个要解密来源 IP，第三个只用哈希聚合就够 —— 画趋势图不需要知道
// 具体是哪个 IP，少解一次密就少一次泄露面。

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// decryptIP 还原一条审计记录里的来源地址。
// 解不开时返回空串而不是报错：早于加密上线的记录本来就没有密文。
func (h *handlers) decryptIP(enc []byte) string {
	return h.decryptWith(enc, "audit")
}

// decryptWith 按指定的 AAD 解密。
//
// 每张表用自己的 AAD：审计事件是 "audit"，订阅拉取日志是 "subfetch"。
// 用错 AAD 会解密失败而不是给出错值 —— 这正是要的效果，
// 密文被跨表挪动时能立刻发现，而不是安静地显示成另一个人的 IP。
func (h *handlers) decryptWith(enc []byte, aad string) string {
	if len(enc) == 0 || h.d.Envelope == nil {
		return ""
	}
	plain, err := h.d.Envelope.Open(enc, []byte(aad))
	if err != nil {
		return ""
	}
	return string(plain)
}

// userProfile 返回一个用户的行为画像。
func (h *handlers) userProfile(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	userID := chi.URLParam(r, "id")

	type event struct {
		Action  string `json:"action"`
		Outcome string `json:"outcome"`
		IP      string `json:"ip"`
		UA      string `json:"ua"`
		Domain  string `json:"domain"`
		At      any    `json:"at"`
	}
	type ipStat struct {
		IP    string `json:"ip"`
		Count int    `json:"count"`
		First any    `json:"first"`
		Last  any    `json:"last"`
		// Accounts 是同一 IP 下的其它账号数。大于 1 就值得看一眼 ——
		// 但共用出口 IP 在学校、公司、家庭网络里是常态，这只是线索。
		Accounts int `json:"accounts"`
	}

	events := []event{}
	ips := []ipStat{}
	related := map[string]bool{}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
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
			var e event
			var enc []byte
			if err := rows.Scan(&e.Action, &e.Outcome, &enc, &e.UA, &e.Domain, &e.At); err != nil {
				return err
			}
			e.IP = h.decryptIP(enc)
			events = append(events, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// 按 IP 归并，并算出每个 IP 上还有多少别的账号。
		// 用哈希做 JOIN，明文只在最后展示时解一次。
		irows, err := tx.Query(r.Context(), `
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
			var st ipStat
			var enc []byte
			var accounts []string
			if err := irows.Scan(&enc, &st.Count, &st.First, &st.Last,
				&st.Accounts, &accounts); err != nil {
				return err
			}
			st.IP = h.decryptIP(enc)
			for _, a := range accounts {
				if a != userID {
					related[a] = true
				}
			}
			ips = append(ips, st)
		}
		return irows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	// 关联账号补上邮箱，光给 UUID 没法判断
	type peer struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	peers := []peer{}
	if len(related) > 0 {
		list := make([]string, 0, len(related))
		for k := range related {
			list = append(list, k)
		}
		_ = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			rows, err := tx.Query(r.Context(),
				`SELECT id::text, COALESCE(email,'') FROM users
				  WHERE tenant_id = $1 AND id = ANY($2::uuid[]) LIMIT 50`, tenantID, list)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var p peer
				if err := rows.Scan(&p.ID, &p.Email); err != nil {
					return err
				}
				peers = append(peers, p)
			}
			return rows.Err()
		})
	}

	// 订阅拉取记录单独查一遍。
	//
	// 这是判断「链接是不是被分享出去了」最直接的证据：
	// 一个人的正常用量是几台设备定时拉，来源集中；
	// 挂到群里的链接会在短时间内冒出一堆互不相干的地址。
	type fetch struct {
		IP     string `json:"ip"`
		UA     string `json:"ua"`
		Family string `json:"family"`
		Result string `json:"result"`
		Format string `json:"format"`
		At     any    `json:"at"`
	}
	fetches := []fetch{}
	var fetchSources int

	_ = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
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
			var f fetch
			var ipEnc, uaEnc []byte
			if err := rows.Scan(&ipEnc, &uaEnc, &f.Family, &f.Result, &f.Format, &f.At); err != nil {
				return err
			}
			f.IP = h.decryptWith(ipEnc, "subfetch")
			f.UA = h.decryptWith(uaEnc, "subfetch")
			fetches = append(fetches, f)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// 不同来源数用哈希算，不需要解密
		return tx.QueryRow(r.Context(), `
			SELECT count(DISTINCT l.ip_hash)
			  FROM subscription_fetch_log l
			  JOIN subscription_credentials c ON c.id = l.credential_id
			 WHERE l.tenant_id = $1 AND c.user_id = $2::uuid
			   AND l.result = 'ok' AND l.fetched_at > now() - interval '7 days'`,
			tenantID, userID).Scan(&fetchSources)
	})

	httpx.OK(w, map[string]any{
		"events": events, "ips": ips, "related": peers,
		"fetches": fetches, "fetch_sources_7d": fetchSources,
	})
}

// statsTimeseries 返回按天聚合的行为统计，用于趋势图。
//
// 全程只用哈希与计数，不解密任何来源信息 —— 画趋势不需要知道是谁。
func (h *handlers) statsTimeseries(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	days := 14
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v >= 1 && v <= 90 {
		days = v
	}

	type point struct {
		Day        string `json:"day"`
		Registered int    `json:"registered"`
		Logins     int    `json:"logins"`
		Orders     int    `json:"orders"`
		UniqueIPs  int    `json:"unique_ips"`
	}
	out := []point{}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// generate_series 补齐没有数据的日子。
		// 不补的话折线图会把「那天没人注册」画成一条直接跳过去的线，
		// 看起来像是数据缺失而不是真的没人。
		rows, err := tx.Query(r.Context(), `
			WITH d AS (
			  SELECT generate_series(
			    date_trunc('day', now()) - make_interval(days => $2 - 1),
			    date_trunc('day', now()), '1 day')::date AS day
			)
			SELECT to_char(d.day, 'MM-DD'),
			  count(*) FILTER (WHERE a.action = 'user.registered'),
			  count(*) FILTER (WHERE a.action = 'user.login' AND a.outcome = 'success'),
			  count(*) FILTER (WHERE a.action = 'order.created'),
			  count(DISTINCT a.source_ip_hash)
			  FROM d
			  LEFT JOIN audit_events a
			    ON a.tenant_id = $1 AND date_trunc('day', a.occurred_at)::date = d.day
			 GROUP BY d.day ORDER BY d.day`, tenantID, days)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p point
			if err := rows.Scan(&p.Day, &p.Registered, &p.Logins, &p.Orders, &p.UniqueIPs); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"points": out})
}
