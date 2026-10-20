package service

import (
	"context"
	"encoding/hex"
	"net/netip"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/google/uuid"
)

// 本文件的类型段由块负责人定稿：后台「溯源总览」要的两个审计聚合读（identity 的 trace.go 调用）。
// 只读 audit_events，不依赖任何业务模块；IP 在这里解封成明文交出去，按明文查询时在这里算 InfoHMACIP 哈希。
// 方法实现写在本文件类型段之后。

// ViewTrace 是溯源总览「显示」明文 IP 时审计 security.source_ip_viewed 的 data.view。
const ViewTrace = "trace"

// TraceIPQuery 是按来源 IP 聚合审计的条件。只算带 ip_hash、actor_kind 为 user 或 anonymous 的事件
// （后台管理员自己的操作不算，否则管理员的出口会排第一）。
type TraceIPQuery struct {
	// Since 是时间窗起点（不含）
	Since time.Time
	// Limit 是按事件数降序取的头部 IP 数（0 = 只要 ExtraIPs）
	Limit int
	// ExtraIPs 是不论排名都要带回的明文 IP（按 netip 规范写法算 InfoHMACIP 哈希；窗内没有事件的不在结果里）
	ExtraIPs []string
}

// TraceIPSet 是按来源 IP 聚合的审计。三个总数是整个时间窗的（不受 Limit 影响），
// 只在 Limit > 0 且结果至少有一行时才算；只补查 ExtraIPs（Limit = 0）时为 0。
type TraceIPSet struct {
	DistinctIPs   int
	Logins        int // action = user.login 且 outcome = success
	LoginFailures int // action = user.login_failed
	// Rows 先是头部（事件数降序、最近出现降序），再是只因 ExtraIPs 带回的
	Rows []TraceIP
}

// TraceIP 是一个来源 IP 在窗内的审计统计。
type TraceIP struct {
	// IP 是解封后的明文（解不开为空串）
	IP      string
	Events  int
	UAKinds int // 不同 user_agent 数
	// Accounts 是 actor_kind = user 的不同操作者数；AccountIDs 最多 20 个
	Accounts   int
	AccountIDs []uuid.UUID
	Last       time.Time
}

// TraceUserQuery 是按用户聚合审计的条件（只算 actor_kind = user、带 ip_hash 的事件）。
type TraceUserQuery struct {
	Since time.Time
	// Sort 是 ips / logins / login_ips / shared_ips / ua_kinds，都按降序；不认识的按 ips
	Sort string
	// Limit 是按 Sort 取的头部用户数（0 = 只要 UserIDs）
	Limit int
	// UserIDs 是不论排名都要带回的用户（窗内没有事件的不在结果里）
	UserIDs []uuid.UUID
}

// TraceUser 是一个用户在窗内作为操作者的审计统计。
type TraceUser struct {
	UserID uuid.UUID
	Logins int
	// LoginIPs 是有过登录成功的不同 IP 数；DistinctIPs 是全部事件的不同 IP 数
	LoginIPs    int
	DistinctIPs int
	UAKinds     int
	// SharedIPs 是窗内也有别的用户（actor_kind = user）出现过的 IP 数
	SharedIPs int
	Last      time.Time
	// IPs 最多 100 个（次数降序），IP 为明文
	IPs []TraceUserIP
}

// TraceUserIP 是一个用户在一个来源 IP 上的事件数；Shared 同 TraceUser.SharedIPs 的口径。
type TraceUserIP struct {
	IP     string
	Count  int
	Shared bool
}

const (
	traceIPLimitMax   = 5000
	traceUserLimitMax = 500
	traceExtraIPMax   = 500
	traceUserIDMax    = 500
)

// TraceIPs 按来源 IP 聚合时间窗内的审计（溯源总览的概览与 IP 排行）。
func (s *Service) TraceIPs(ctx context.Context, q TraceIPQuery) (TraceIPSet, error) {
	since := q.Since
	if since.IsZero() {
		since = time.Now().Add(-24 * time.Hour)
	}
	limit := q.Limit
	if limit < 0 {
		limit = 0
	}
	if limit > traceIPLimitMax {
		limit = traceIPLimitMax
	}
	extraHex := traceExtraIPHexes(s.keys, q.ExtraIPs)
	rows, err := s.repo.TraceIPs(ctx, since, limit, extraHex)
	if err != nil {
		return TraceIPSet{}, err
	}
	out := TraceIPSet{Rows: []TraceIP{}}
	if len(rows) == 0 {
		return out, nil
	}
	out.DistinctIPs = rows[0].DistinctIPs
	out.Logins = rows[0].TotalLogins
	out.LoginFailures = rows[0].TotalFailures
	for _, row := range rows {
		out.Rows = append(out.Rows, TraceIP{
			IP:         s.openIP(row.IPEnc),
			Events:     row.Events,
			UAKinds:    row.UAKinds,
			Accounts:   row.Accounts,
			AccountIDs: splitAccountIDs(row.AccountIDs),
			Last:       row.LastAt,
		})
	}
	return out, nil
}

// TraceUsers 按用户聚合时间窗内的审计（溯源总览的账号综合排行）。
func (s *Service) TraceUsers(ctx context.Context, q TraceUserQuery) ([]TraceUser, error) {
	since := q.Since
	if since.IsZero() {
		since = time.Now().Add(-24 * time.Hour)
	}
	limit := q.Limit
	if limit < 0 {
		limit = 0
	}
	if limit > traceUserLimitMax {
		limit = traceUserLimitMax
	}
	ids := dedupeUserIDs(q.UserIDs)
	rows, err := s.repo.TraceUsers(ctx, since, q.Sort, limit, ids)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []TraceUser{}, nil
	}
	out := make([]TraceUser, 0)
	var cur *TraceUser
	for _, row := range rows {
		if cur == nil || cur.UserID != row.UserID {
			out = append(out, TraceUser{
				UserID:      row.UserID,
				Logins:      row.Logins,
				LoginIPs:    row.LoginIPs,
				DistinctIPs: row.IPs,
				UAKinds:     row.UAKinds,
				SharedIPs:   row.SharedIPs,
				Last:        row.LastAt,
				IPs:         []TraceUserIP{},
			})
			cur = &out[len(out)-1]
		}
		cur.IPs = append(cur.IPs, TraceUserIP{
			IP:     s.openIP(row.IPEnc),
			Count:  row.IPCount,
			Shared: row.IPShared,
		})
	}
	return out, nil
}

func traceExtraIPHexes(keys *crypto.Keys, extra []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(extra))
	for _, raw := range extra {
		ip := strings.TrimSpace(raw)
		if ip == "" {
			continue
		}
		if a, err := netip.ParseAddr(ip); err == nil {
			ip = a.String()
		}
		h := hex.EncodeToString(keys.HMAC(crypto.InfoHMACIP, []byte(ip)))
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
		if len(out) >= traceExtraIPMax {
			break
		}
	}
	return out
}

func dedupeUserIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{})
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= traceUserIDMax {
			break
		}
	}
	return out
}
