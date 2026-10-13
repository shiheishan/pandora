package service

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

var _ identitysvc.OpsSubscriptions = (*Service)(nil)

// UserIDsBySubFilter 按订阅条件给出用户 id 与排除语义。
func (s *Service) UserIDsBySubFilter(ctx context.Context, f identitysvc.SubFilter) (ids []uuid.UUID, exclude bool, err error) {
	now := s.clock()

	positive := f.State == "active" || f.State == "expired" || f.HasLive != nil && *f.HasLive ||
		f.PlanID != nil || f.ExpiresWithinDays > 0

	if f.State == "none" && (positive || f.HasLive != nil && *f.HasLive) {
		return []uuid.UUID{}, false, nil
	}
	if f.State == "active" && f.HasLive != nil && !*f.HasLive {
		return []uuid.UUID{}, false, nil
	}
	if f.State == "expired" && f.HasLive != nil && *f.HasLive {
		return []uuid.UUID{}, false, nil
	}

	if !positive {
		if f.State == "none" {
			ids, err = s.repo.OpsUsersWithAny(ctx)
			return ids, true, subErr(err)
		}
		if f.HasLive != nil && !*f.HasLive {
			ids, err = s.repo.OpsUsersWithLive(ctx, now)
			return ids, true, subErr(err)
		}
		return nil, false, nil
	}

	var requireLive *bool
	switch {
	case f.State == "active" || f.HasLive != nil && *f.HasLive:
		requireLive = ptrBool(true)
	case f.State == "expired" || f.HasLive != nil && !*f.HasLive:
		requireLive = ptrBool(false)
	}

	var endBefore *time.Time
	if f.ExpiresWithinDays > 0 {
		t := now.AddDate(0, 0, f.ExpiresWithinDays)
		endBefore = &t
	}

	ids, err = s.repo.OpsMatchUsers(ctx, repo.OpsMatch{
		Now:         now,
		RequireLive: requireLive,
		PlanID:      f.PlanID,
		EndBefore:   endBefore,
	})
	return ids, false, subErr(err)
}

func ptrBool(v bool) *bool { return &v }

// CurrentSubs 批量取用户的当前订阅摘要。
func (s *Service) CurrentSubs(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]identitysvc.CurrentSub, error) {
	rows, err := s.repo.OpsCurrentByUsers(ctx, userIDs, s.clock())
	if err != nil {
		return nil, subErr(err)
	}
	planSet := make(map[uuid.UUID]struct{}, len(rows))
	for _, r := range rows {
		planSet[r.PlanID] = struct{}{}
	}
	planIDs := make([]uuid.UUID, 0, len(planSet))
	for id := range planSet {
		planIDs = append(planIDs, id)
	}
	plans, err := s.catalog.PlansByIDs(ctx, planIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]identitysvc.CurrentSub, len(rows))
	for _, r := range rows {
		name := ""
		if p, ok := plans[r.PlanID]; ok {
			name = p.Name
		}
		out[r.UserID] = identitysvc.CurrentSub{
			PlanName:  name,
			PeriodEnd: r.PeriodEnd,
			Live:      r.Live,
			LiveCount: r.LiveCount,
		}
	}
	return out, nil
}

// Fetches 是访问明细里的订阅拉取列表。
func (s *Service) Fetches(ctx context.Context, q identitysvc.FetchQuery) ([]identitysvc.FetchRecord, error) {
	// identity 按 offset+limit 取，再与审计归并后切页；这里只防无界（与 audit 同为 10000）
	limit := q.Limit
	if limit < 1 {
		limit = 50
	}
	if limit > 10000 {
		limit = 10000
	}
	outcome := q.Outcome
	if outcome != "" && outcome != "success" && outcome != "error" {
		outcome = ""
	}

	var ipHash []byte
	if strings.TrimSpace(q.IP) != "" {
		if s.delivery == nil || s.delivery.Hasher == nil {
			return nil, errs.Wrap(errs.Internal, errors.New("subscription: 拉取日志哈希未装配"))
		}
		ipHash = s.delivery.Hasher.HMAC(crypto.InfoHMACSubAudit, []byte(normalizeIPForHMAC(q.IP)))
	}

	rows, err := s.repo.OpsFetches(ctx, repo.OpsFetchFilter{
		IPHash:      ipHash,
		FilterUsers: q.FilterUsers,
		UserIDs:     q.UserIDs,
		Outcome:     outcome,
		Limit:       limit,
	})
	if err != nil {
		return nil, subErr(err)
	}
	return s.opsFetchRowsToRecords(rows), nil
}

// UserFetches 是画像里一个用户最近 50 次拉取与近 7 天成功拉取的不同来源数。
func (s *Service) UserFetches(ctx context.Context, userID uuid.UUID) ([]identitysvc.FetchRecord, int, error) {
	now := s.clock()
	rows, err := s.repo.OpsUserFetches(ctx, userID, 50)
	if err != nil {
		return nil, 0, subErr(err)
	}
	sources, err := s.repo.OpsFetchSources(ctx, userID, now.Add(-7*24*time.Hour))
	if err != nil {
		return nil, 0, subErr(err)
	}
	return s.opsFetchRowsToRecords(rows), sources, nil
}

func (s *Service) opsFetchRowsToRecords(rows []repo.OpsFetchRow) []identitysvc.FetchRecord {
	out := make([]identitysvc.FetchRecord, len(rows))
	for i, r := range rows {
		out[i] = identitysvc.FetchRecord{
			UserID:    r.UserID,
			IP:        s.openFetchLogField(r.IPEnc),
			UserAgent: s.openFetchLogField(r.UAEnc),
			Family:    r.UAFamily,
			Result:    r.Result,
			Format:    r.Format,
			At:        r.FetchedAt,
		}
	}
	return out
}

func (s *Service) openFetchLogField(enc []byte) string {
	if len(enc) == 0 || s.sealer == nil {
		return ""
	}
	plain, err := s.sealer.Open(enc, []byte(fetchLogAAD))
	if err != nil {
		return ""
	}
	return string(plain)
}

func normalizeIPForHMAC(ip string) string {
	raw := strings.TrimSpace(ip)
	if raw == "" {
		return ""
	}
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr.String()
	}
	return raw
}
