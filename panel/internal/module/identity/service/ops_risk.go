package service

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// UserProfile 返回后台用户画像（含关联账号与订阅拉取摘要）。
func (s *Service) UserProfile(ctx context.Context, userID uuid.UUID) (UserProfile, error) {
	_, ok, err := s.repo.UserByID(ctx, userID)
	if err != nil {
		return UserProfile{}, err
	}
	if !ok {
		return UserProfile{}, errs.New(errs.NotFound)
	}

	activity, err := s.ops.UserActivity(ctx, userID)
	if err != nil {
		return UserProfile{}, err
	}
	if err := s.ops.RecordSourceIPView(ctx, auditsvc.ViewUserProfile, &userID, nil); err != nil {
		return UserProfile{}, err
	}

	out := UserProfile{
		Events:         []ProfileEvent{},
		IPs:            []ProfileIP{},
		Related:        []UserRef{},
		Fetches:        []FetchRecord{},
		RegisteredIP:   activity.RegisteredIP,
		FetchSources7d: 0,
	}
	for _, e := range activity.Events {
		out.Events = append(out.Events, ProfileEvent{
			Action:    e.Action,
			Outcome:   e.Outcome,
			IP:        e.IP,
			UserAgent: e.UserAgent,
			Domain:    e.Audience,
			At:        e.At,
		})
	}
	for _, ip := range activity.IPs {
		out.IPs = append(out.IPs, ProfileIP{
			IP:       ip.IP,
			Count:    ip.Count,
			First:    ip.First,
			Last:     ip.Last,
			Accounts: ip.Accounts,
		})
	}

	relatedSet := make(map[uuid.UUID]struct{})
	for _, ip := range activity.IPs {
		for _, aid := range ip.AccountIDs {
			if aid != userID {
				relatedSet[aid] = struct{}{}
			}
		}
	}
	relatedIDs := make([]uuid.UUID, 0, len(relatedSet))
	for id := range relatedSet {
		relatedIDs = append(relatedIDs, id)
	}
	sort.Slice(relatedIDs, func(i, j int) bool {
		return bytes.Compare(relatedIDs[i][:], relatedIDs[j][:]) < 0
	})
	if len(relatedIDs) > 50 {
		relatedIDs = relatedIDs[:50]
	}
	if len(relatedIDs) > 0 {
		emails, err := s.repo.Emails(ctx, relatedIDs)
		if err != nil {
			plog.From(ctx).Warn("用户画像查询关联账号邮箱失败", zap.String("user", userID.String()), zap.Error(err))
		} else {
			out.Related = make([]UserRef, 0, len(relatedIDs))
			for _, id := range relatedIDs {
				if em, ok := emails[id]; ok {
					out.Related = append(out.Related, UserRef{ID: id, Email: em})
				}
			}
			sort.Slice(out.Related, func(i, j int) bool {
				return strings.Compare(out.Related[i].Email, out.Related[j].Email) < 0
			})
		}
	}

	if subs, err := s.needSubs(); err == nil {
		fetches, sources, err := subs.UserFetches(ctx, userID)
		if err != nil {
			plog.From(ctx).Warn("用户画像查询订阅拉取记录失败", zap.String("user", userID.String()), zap.Error(err))
		} else {
			out.Fetches = fetches
			if out.Fetches == nil {
				out.Fetches = []FetchRecord{}
			}
			out.FetchSources7d = sources
		}
	}

	return out, nil
}

// IPClusters 列出同源 IP 聚类及成员账号摘要。
func (s *Service) IPClusters(ctx context.Context, includeReviewed bool) ([]IPCluster, error) {
	clusters, err := s.ops.IPClusters(ctx, includeReviewed)
	if err != nil {
		return nil, err
	}
	filter := map[string]any{
		"include_reviewed": includeReviewed,
		"clusters":         len(clusters),
	}
	if err := s.ops.RecordSourceIPView(ctx, auditsvc.ViewIPClusters, nil, filter); err != nil {
		return nil, err
	}

	idSet := make(map[uuid.UUID]struct{})
	for _, c := range clusters {
		for _, id := range c.AccountIDs {
			idSet[id] = struct{}{}
		}
	}
	allIDs := make([]uuid.UUID, 0, len(idSet))
	for id := range idSet {
		allIDs = append(allIDs, id)
	}

	usersByID := make(map[uuid.UUID]struct {
		email  string
		status string
	})
	if len(allIDs) > 0 {
		users, err := s.repo.UsersByIDs(ctx, allIDs)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			usersByID[u.ID] = struct {
				email  string
				status string
			}{email: u.Email, status: u.Status}
		}
	}

	subsMap := map[uuid.UUID]CurrentSub{}
	if len(allIDs) > 0 {
		subs, err := s.needSubs()
		if err != nil {
			return nil, err
		}
		if subsMap, err = subs.CurrentSubs(ctx, allIDs); err != nil {
			return nil, err
		}
	}

	out := make([]IPCluster, 0, len(clusters))
	for _, c := range clusters {
		geo, kind := locate(c.IP)
		ic := IPCluster{
			Key:         c.Key,
			IP:          c.IP,
			Geo:         geo,
			NetworkKind: kind,
			Risk:        clusterRisk(c.Accounts, kind),
			Accounts:    c.Accounts,
			Events:      c.Events,
			First:       c.First,
			Last:        c.Last,
			Emails:      []string{},
			Users:       []ClusterUser{},
		}
		if c.Review != nil {
			ic.Review = &ClusterReview{
				Decision:  c.Review.Decision,
				DecidedAt: c.Review.DecidedAt,
				ExpiresAt: c.Review.ExpiresAt,
			}
		}
		for _, id := range c.AccountIDs {
			u, ok := usersByID[id]
			if !ok {
				continue
			}
			var activePlan *string
			if sub, ok := subsMap[id]; ok && sub.Live {
				activePlan = &sub.PlanName
			}
			ic.Users = append(ic.Users, ClusterUser{
				ID:         id,
				Email:      u.email,
				Status:     u.status,
				ActivePlan: activePlan,
			})
		}
		sort.Slice(ic.Users, func(i, j int) bool {
			return strings.Compare(ic.Users[i].Email, ic.Users[j].Email) < 0
		})
		ic.Emails = make([]string, len(ic.Users))
		for i, u := range ic.Users {
			ic.Emails[i] = u.Email
		}
		out = append(out, ic)
	}
	if out == nil {
		out = []IPCluster{}
	}
	return out, nil
}

// ReviewIPCluster 将聚类标记为正常。
func (s *Service) ReviewIPCluster(ctx context.Context, key, note string) (time.Time, error) {
	var expires time.Time
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		t, err := s.ops.MarkClusterNormal(ctx, key, note)
		if err != nil {
			return err
		}
		expires = t
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return expires, nil
}

// DisableIPClusterAccounts 批量停用聚类中选中的账号。
func (s *Service) DisableIPClusterAccounts(ctx context.Context, key string, userIDs []uuid.UUID, reason string) (DisableClusterResult, error) {
	p, err := principal(ctx)
	if err != nil {
		return DisableClusterResult{}, err
	}

	fields := map[string]string{}
	reason, ok := cleanText(reason)
	if !ok {
		fields["reason"] = "原因必须为 5 到 500 字"
	} else {
		n := utf8.RuneCountInString(reason)
		if n < 5 || n > 500 {
			fields["reason"] = "原因必须为 5 到 500 字"
		}
	}

	ids := dedupeSortUUIDs(userIDs)
	if len(ids) == 0 || len(ids) > 200 {
		fields["user_ids"] = "请选择 1 到 200 个账号"
	}
	if len(fields) > 0 {
		return DisableClusterResult{}, errs.Invalid(fields)
	}

	result := DisableClusterResult{Skipped: []ClusterSkip{}}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		members, err := s.ops.ClusterMembers(ctx, key)
		if err != nil {
			return err
		}
		memberSet := make(map[uuid.UUID]struct{}, len(members))
		for _, m := range members {
			memberSet[m] = struct{}{}
		}

		skip := func(id uuid.UUID, why string) {
			result.Skipped = append(result.Skipped, ClusterSkip{UserID: id, Reason: why})
		}

		var disabled []uuid.UUID
		for _, id := range ids {
			if id == p.UserID {
				skip(id, SkipSelf)
				continue
			}
			if _, in := memberSet[id]; !in {
				skip(id, SkipNotMember)
				continue
			}
			u, locked, err := s.repo.LockUser(ctx, id)
			if err != nil {
				return err
			}
			if !locked {
				skip(id, SkipNotMember)
				continue
			}
			// 后台人员一律不随批量停用带走，须走用户管理单独处置。
			staff, err := s.isStaff(ctx, id)
			if err != nil {
				return err
			}
			if staff {
				skip(id, SkipAdministrator)
				continue
			}
			if u.Status == StatusSuspended || u.Status == StatusBanned {
				skip(id, SkipAlreadyDisabled)
				continue
			}
			if err := s.repo.SetUserStatus(ctx, id, StatusSuspended); err != nil {
				return err
			}
			s.notifyRoster(ctx, id)
			revoker, err := s.needRevoker()
			if err != nil {
				return err
			}
			if err := revoker.RevokeUser(ctx, id, ""); err != nil {
				return err
			}
			if err := s.writeAudit(ctx, auditsvc.Event{
				Action:       ActionStatusChange,
				ResourceType: ResourceUser,
				ResourceID:   &id,
				Data: map[string]any{
					"before":           u.Status,
					"status":           StatusSuspended,
					"reason":           reason,
					"sessions_revoked": true,
					"source":           "ip_cluster",
					"key":              key,
				},
			}); err != nil {
				return err
			}
			disabled = append(disabled, id)
		}
		result.Disabled = len(disabled)

		var reviewID *uuid.UUID
		if len(disabled) > 0 {
			id, err := s.ops.MarkClusterDisabled(ctx, key, reason)
			if err != nil {
				return err
			}
			reviewID = &id
		}

		disabledStr := make([]string, len(disabled))
		for i, id := range disabled {
			disabledStr[i] = id.String()
		}
		skippedPayload := make([]map[string]string, len(result.Skipped))
		for i, sk := range result.Skipped {
			skippedPayload[i] = map[string]string{
				"user_id": sk.UserID.String(),
				"reason":  sk.Reason,
			}
		}
		auditOutcome := ""
		if len(disabled) == 0 {
			auditOutcome = auditsvc.OutcomeFailure
		} else if len(result.Skipped) > 0 {
			auditOutcome = auditsvc.OutcomePartial
		}
		e := auditsvc.Event{
			Action:  "risk.ip_cluster.disable",
			Outcome: auditOutcome,
			Data: map[string]any{
				"key":      key,
				"reason":   reason,
				"disabled": disabledStr,
				"skipped":  skippedPayload,
			},
		}
		if reviewID != nil {
			e.ResourceType = "ip_cluster_review"
			e.ResourceID = reviewID
		}
		return s.writeAudit(ctx, e)
	})
	if err != nil {
		return DisableClusterResult{}, err
	}
	if result.Skipped == nil {
		result.Skipped = []ClusterSkip{}
	}
	return result, nil
}

func dedupeSortUUIDs(ids []uuid.UUID) []uuid.UUID {
	if len(ids) == 0 {
		return nil
	}
	m := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	out := make([]uuid.UUID, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i][:], out[j][:]) < 0
	})
	return out
}
