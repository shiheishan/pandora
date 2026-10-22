package service

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// 本文件实现 identity 的 TraceFetches（后台「安全与运维 › 溯源总览」的拉取这一路）。
// 写法照 ops_reads.go：每个方法一条 SQL，明文 IP 在 service 里解封，identity 按明文与审计合并。

var _ identitysvc.TraceFetches = (*Service)(nil)

// traceLimitIPs 与 traceLimitUsers 是两条聚合各自取头部数量的上限。
const (
	traceLimitIPs   = 5000
	traceLimitUsers = 500
	// traceExtraMax 是 ExtraIPs / UserIDs 最多带回的数量。
	traceExtraMax = 500
)

// TraceFetchIPs 按来源 IP 聚合时间窗内的拉取日志（溯源总览的概览与 IP 排行）。
func (s *Service) TraceFetchIPs(ctx context.Context, q identitysvc.TraceFetchIPQuery) (identitysvc.TraceFetchIPSet, error) {
	limit := q.Limit
	if limit < 0 {
		limit = 0
	}
	if limit > traceLimitIPs {
		limit = traceLimitIPs
	}
	since := q.Since
	if since.IsZero() {
		since = s.clock().Add(-24 * time.Hour)
	}

	extraHex, err := s.traceExtraIPHashes(q.ExtraIPs)
	if err != nil {
		return identitysvc.TraceFetchIPSet{}, err
	}

	rows, err := s.repo.TraceIPs(ctx, since, limit, extraHex)
	if err != nil {
		return identitysvc.TraceFetchIPSet{}, subErr(err)
	}

	out := identitysvc.TraceFetchIPSet{Rows: make([]identitysvc.TraceFetchIP, 0, len(rows))}
	if len(rows) > 0 {
		out.DistinctIPs = rows[0].DistinctIPs
		out.Fetches = rows[0].TotalFetches
		out.Failures = rows[0].TotalFailures
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, identitysvc.TraceFetchIP{
			IP:         s.openFetchLogField(r.IPEnc),
			Fetches:    r.Fetches,
			UAKinds:    r.UAKinds,
			Accounts:   r.Accounts,
			AccountIDs: splitTraceIDs(r.AccountIDs),
			Last:       r.LastAt,
		})
	}
	return out, nil
}

// TraceFetchUsers 按用户聚合时间窗内的拉取日志（订阅更新排行、账号综合排行）。
func (s *Service) TraceFetchUsers(ctx context.Context, q identitysvc.TraceFetchUserQuery) ([]identitysvc.TraceFetchUser, error) {
	limit := q.Limit
	if limit < 0 {
		limit = 0
	}
	if limit > traceLimitUsers {
		limit = traceLimitUsers
	}
	since := q.Since
	if since.IsZero() {
		since = s.clock().Add(-24 * time.Hour)
	}

	ids := make([]uuid.UUID, 0, len(q.UserIDs))
	seen := make(map[uuid.UUID]struct{}, len(q.UserIDs))
	for _, id := range q.UserIDs {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) >= traceExtraMax {
			break
		}
	}

	rows, err := s.repo.TraceUsers(ctx, since, q.Sort, limit, ids)
	if err != nil {
		return nil, subErr(err)
	}

	out := make([]identitysvc.TraceFetchUser, 0, len(rows))
	for _, r := range rows {
		// 同一用户的多行连着来：标量取第一行，IP 逐行追加
		if len(out) == 0 || out[len(out)-1].UserID != r.UserID {
			out = append(out, identitysvc.TraceFetchUser{
				UserID:      r.UserID,
				Fetches:     r.Fetches,
				Failures:    r.Failures,
				DistinctIPs: r.IPs,
				UAKinds:     r.UAKinds,
				Families:    splitTraceFamilies(r.Families),
				SharedIPs:   r.SharedIPs,
				Last:        r.LastAt,
				IPs:         make([]identitysvc.TraceIPCount, 0, 1),
			})
		}
		if r.IPCount != nil {
			u := &out[len(out)-1]
			u.IPs = append(u.IPs, identitysvc.TraceIPCount{
				IP:     s.openFetchLogField(r.IPEnc),
				Count:  *r.IPCount,
				Shared: r.IPShared != nil && *r.IPShared,
			})
		}
	}
	return out, nil
}

// traceExtraIPHashes 把明文 IP 规整后按拉取日志的盐算哈希（小写 hex），去重、跳过空串，最多 traceExtraMax 个。
// 只有真要算哈希时才要求装配 Hasher；没装配回与 Fetches 相同的错误。
func (s *Service) traceExtraIPHashes(ips []string) ([]string, error) {
	cleaned := make([]string, 0, len(ips))
	for _, ip := range ips {
		if v := normalizeIPForHMAC(ip); v != "" {
			cleaned = append(cleaned, v)
		}
	}
	if len(cleaned) == 0 {
		return nil, nil
	}
	if s.delivery == nil || s.delivery.Hasher == nil {
		return nil, errs.Wrap(errs.Internal, errors.New("subscription: 拉取日志哈希未装配"))
	}
	out := make([]string, 0, len(cleaned))
	seen := make(map[string]struct{}, len(cleaned))
	for _, ip := range cleaned {
		h := hex.EncodeToString(s.delivery.Hasher.HMAC(crypto.InfoHMACSubAudit, []byte(ip)))
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
		if len(out) >= traceExtraMax {
			break
		}
	}
	return out, nil
}

// splitTraceIDs 把逗号分隔的用户 id 串解析成切片；空串返回空切片，解析失败的跳过。
func splitTraceIDs(s string) []uuid.UUID {
	if s == "" {
		return []uuid.UUID{}
	}
	parts := strings.Split(s, ",")
	out := make([]uuid.UUID, 0, len(parts))
	for _, p := range parts {
		id, err := uuid.Parse(strings.TrimSpace(p))
		if err != nil {
			continue
		}
		out = append(out, id)
	}
	return out
}

// splitTraceFamilies 把逗号分隔的客户端家族串解析成切片；空串返回空切片。
func splitTraceFamilies(s string) []string {
	if s == "" {
		return []string{}
	}
	out := make([]string, 0, 4)
	for _, p := range strings.Split(s, ",") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
