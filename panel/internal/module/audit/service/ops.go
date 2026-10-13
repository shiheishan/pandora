package service

import (
	"context"
	"encoding/hex"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/module/audit/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// accessMaxRows 是访问明细审计这一路单次最多取的行数。
const accessMaxRows = 10000

// UserActivity 组装用户作为操作者的审计画像。
func (s *Service) UserActivity(ctx context.Context, userID uuid.UUID) (Activity, error) {
	out := Activity{
		Events: []ActivityEvent{},
		IPs:    []ActivityIP{},
	}
	regEnc, err := s.repo.RegisteredIPEnc(ctx, userID)
	if err != nil {
		return Activity{}, err
	}
	out.RegisteredIP = s.openIP(regEnc)

	events, err := s.repo.ActorEvents(ctx, userID, 80)
	if err != nil {
		return Activity{}, err
	}
	for _, e := range events {
		out.Events = append(out.Events, ActivityEvent{
			Action:    e.Action,
			Outcome:   e.Outcome,
			IP:        s.openIP(e.IPEnc),
			UserAgent: e.UserAgent,
			Audience:  e.Audience,
			At:        e.OccurredAt,
		})
	}

	since := time.Now().Add(-ClusterWindow)
	ipRows, err := s.repo.ActorIPStats(ctx, userID, since, 20)
	if err != nil {
		return Activity{}, err
	}
	for _, row := range ipRows {
		ids := splitAccountIDs(row.AccountIDs)
		if row.Accounts <= 1 {
			ids = []uuid.UUID{}
		}
		out.IPs = append(out.IPs, ActivityIP{
			IP:         s.openIP(row.IPEnc),
			Count:      row.N,
			First:      row.FirstAt,
			Last:       row.LastAt,
			Accounts:   row.Accounts,
			AccountIDs: ids,
		})
	}
	return out, nil
}

// AccessEvents 按条件查询访问明细审计行。
func (s *Service) AccessEvents(ctx context.Context, q AccessQuery) ([]AccessEvent, error) {
	// identity 按 offset+limit 取，再与订阅拉取归并后切页；这里只防无界
	limit := q.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > accessMaxRows {
		limit = accessMaxRows
	}
	f := repoAccessFilter(q, limit)
	if ip := strings.TrimSpace(q.IP); ip != "" {
		// 写入时存的是规范写法（gin ClientIP），查询也按规范写法算哈希
		if a, err := netip.ParseAddr(ip); err == nil {
			ip = a.String()
		}
		f.IPHash = s.keys.HMAC(crypto.InfoHMACIP, []byte(ip))
	}
	rows, err := s.repo.AccessEvents(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]AccessEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, AccessEvent{
			Action:    row.Action,
			Outcome:   row.Outcome,
			IP:        s.openIP(row.IPEnc),
			UserAgent: row.UserAgent,
			ActorID:   row.ActorID,
			At:        row.OccurredAt,
		})
	}
	return out, nil
}

// IPClusters 列出近窗内同源 IP 聚类。
func (s *Service) IPClusters(ctx context.Context, includeReviewed bool) ([]Cluster, error) {
	since := time.Now().Add(-ClusterWindow)
	rows, err := s.repo.Clusters(ctx, since, includeReviewed, ClusterListLimit)
	if err != nil {
		return nil, err
	}
	out := make([]Cluster, 0, len(rows))
	for _, row := range rows {
		c := Cluster{
			Key:        hex.EncodeToString(row.IPHash),
			IP:         s.openIP(row.IPEnc),
			Accounts:   row.Accounts,
			Events:     row.Events,
			First:      row.FirstSeen,
			Last:       row.LastSeen,
			AccountIDs: splitAccountIDs(row.AccountIDs),
		}
		if row.Decision != nil && row.DecidedAt != nil {
			c.Review = &ClusterReview{
				Decision:  *row.Decision,
				DecidedAt: *row.DecidedAt,
				ExpiresAt: row.ExpiresAt,
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// ClusterMembers 返回聚类 key 对应的成员账号（至少两个）。
func (s *Service) ClusterMembers(ctx context.Context, key string) ([]uuid.UUID, error) {
	hash, err := s.parseClusterKey(key)
	if err != nil {
		return nil, err
	}
	since := time.Now().Add(-ClusterWindow)
	ids, err := s.repo.ClusterMembers(ctx, hash, since)
	if err != nil {
		return nil, err
	}
	if len(ids) < 2 {
		return nil, errs.New(errs.NotFound)
	}
	return ids, nil
}

// MarkClusterNormal 将聚类标记为正常；须在调用方事务里调用。
func (s *Service) MarkClusterNormal(ctx context.Context, key, note string) (time.Time, error) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return time.Time{}, errs.New(errs.Unauthorized)
	}
	note = strings.TrimSpace(note)
	if err := validateClusterNote(note); err != nil {
		return time.Time{}, err
	}
	if _, err := s.ClusterMembers(ctx, key); err != nil {
		return time.Time{}, err
	}
	hash, err := s.parseClusterKey(key)
	if err != nil {
		return time.Time{}, err
	}
	expires := time.Now().Add(ClusterNormalTTL).UTC().Truncate(time.Microsecond)
	id, err := s.repo.UpsertClusterReview(ctx, hash, ClusterNormal, note, p.UserID, &expires)
	if err != nil {
		return time.Time{}, err
	}
	if err := s.Write(ctx, Event{
		Action:       "risk.ip_cluster.mark_normal",
		ResourceType: "ip_cluster_review",
		ResourceID:   &id,
		Data: map[string]any{
			"key":        key,
			"note":       note,
			"expires_at": expires,
		},
	}); err != nil {
		return time.Time{}, err
	}
	return expires, nil
}

// MarkClusterDisabled 将聚类标记为已禁用复核结论；须在调用方事务里调用，不写审计。
func (s *Service) MarkClusterDisabled(ctx context.Context, key, note string) (uuid.UUID, error) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return uuid.Nil, errs.New(errs.Unauthorized)
	}
	note = strings.TrimSpace(note)
	if err := validateClusterNote(note); err != nil {
		return uuid.Nil, err
	}
	hash, err := s.parseClusterKey(key)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := s.repo.UpsertClusterReview(ctx, hash, ClusterDisabled, note, p.UserID, nil)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// RecordSourceIPView 记录后台查看明文来源 IP 的审计。
func (s *Service) RecordSourceIPView(ctx context.Context, view string, userID *uuid.UUID, filter map[string]any) error {
	data := filter
	if data == nil {
		data = map[string]any{}
	}
	e := Event{
		Action: "security.source_ip_viewed",
		Data: map[string]any{
			"view":   view,
			"filter": data,
		},
	}
	if userID != nil {
		e.ResourceType = "user"
		e.ResourceID = userID
	}
	return s.Write(ctx, e)
}

// CSVSafe 防止表格软件把单元格当公式（identity 用户导出复用）。
func CSVSafe(str string) string {
	return csvSafe(str)
}

func (s *Service) openIP(enc []byte) string {
	if len(enc) == 0 {
		return ""
	}
	plain, err := s.sealer.Open(enc, ipAAD)
	if err != nil {
		return ""
	}
	return string(plain)
}

func (s *Service) parseClusterKey(key string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(key))
	if err != nil || len(b) != 32 {
		return nil, errs.New(errs.NotFound)
	}
	return b, nil
}

func validateClusterNote(note string) error {
	if strings.Contains(note, "\x00") {
		return errs.Invalid(map[string]string{"note": "备注不能包含非法字符"})
	}
	if utf8.RuneCountInString(note) > 500 {
		return errs.Invalid(map[string]string{"note": "备注不能超过 500 字"})
	}
	return nil
}

func splitAccountIDs(s string) []uuid.UUID {
	s = strings.TrimSpace(s)
	if s == "" {
		return []uuid.UUID{}
	}
	parts := strings.Split(s, ",")
	out := make([]uuid.UUID, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := uuid.Parse(p)
		if err != nil {
			continue
		}
		out = append(out, id)
	}
	return out
}

func repoAccessFilter(q AccessQuery, limit int) repo.AccessFilter {
	return repo.AccessFilter{
		Include:      q.Include,
		Exclude:      q.Exclude,
		FilterActors: q.FilterActors,
		ActorIDs:     q.ActorIDs,
		Outcome:      q.Outcome,
		Limit:        limit,
	}
}
