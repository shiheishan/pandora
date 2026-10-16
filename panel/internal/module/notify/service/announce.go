package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/module/notify/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/google/uuid"
)

const (
	annSeverityInfo     = "info"
	annSeverityNotice   = "notice"
	annSeverityWarning  = "warning"
	annSeverityCritical = "critical"

	annListLimit   = 200
	annPortalLimit = 20
	annPlansLimit  = 500

	annAuditActionSaved     = "announcement.saved"
	annAuditActionWithdrawn = "announcement.withdrawn"
	annAuditActionPublished = "announcement.published"
	annResourceType         = "announcement"
)

// annVersionConflict 是版本不符的冲突错误；每次新建，免得共享的错误值被下游改写。
func annVersionConflict() error {
	return errs.Newf(errs.Conflict, "公告已被其他管理员修改，请刷新后重试")
}

func annPortsPlansDir(s *Service) error {
	if s.plans == nil || s.dir == nil {
		return errs.Wrap(errs.Internal, errors.New("notify: 端口未装配"))
	}
	return nil
}

func annPortsPortal(s *Service) error {
	if s.dir == nil || s.subs == nil {
		return errs.Wrap(errs.Internal, errors.New("notify: 端口未装配"))
	}
	return nil
}

func annContentSHA256(title, body string) string {
	sum := sha256.Sum256([]byte(title + "\n" + body))
	return hex.EncodeToString(sum[:])
}

type annAuditSnap struct {
	ContentSHA256      string     `json:"content_sha256"`
	Severity           string     `json:"severity"`
	Pinned             bool       `json:"pinned"`
	TargetPlanIDs      []string   `json:"target_plan_ids"`
	TargetUserGroupIDs []string   `json:"target_user_group_ids"`
	Status             string     `json:"status"`
	PublishAt          *time.Time `json:"publish_at,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	PublishedAt        *time.Time `json:"published_at,omitempty"`
	Version            int        `json:"version"`
}

func annAuditFromRow(title, body string, row repo.AnnouncementRow) annAuditSnap {
	return annAuditSnap{
		ContentSHA256:      annContentSHA256(title, body),
		Severity:           row.Severity,
		Pinned:             row.Pinned,
		TargetPlanIDs:      uuidStrings(row.TargetPlanIDs),
		TargetUserGroupIDs: uuidStrings(row.TargetUserGroupIDs),
		Status:             row.Status,
		PublishAt:          row.PublishAt,
		ExpiresAt:          row.ExpiresAt,
		PublishedAt:        row.PublishedAt,
		Version:            row.Version,
	}
}

func uuidStrings(ids []uuid.UUID) []string {
	if len(ids) == 0 {
		return []string{}
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// announcementStatus 根据是否发布与定时时间计算状态与时间字段。
func announcementStatus(publish bool, publishAt *time.Time, now time.Time) (status string, outPublishAt, publishedAt *time.Time) {
	outPublishAt = publishAt
	if !publish {
		return model.AnnouncementDraft, outPublishAt, nil
	}
	if publishAt != nil && publishAt.After(now) {
		return model.AnnouncementScheduled, outPublishAt, nil
	}
	publishedAt = &now
	if outPublishAt == nil {
		outPublishAt = &now
	}
	return model.AnnouncementPublished, outPublishAt, publishedAt
}

// checkAnnouncementWindow 下线时间必须晚于发布时间（两者都有时）。
func checkAnnouncementWindow(publishAt, expiresAt *time.Time) error {
	if publishAt != nil && expiresAt != nil && !expiresAt.After(*publishAt) {
		return errs.Invalid(map[string]string{"expires_at": "下线时间必须晚于发布时间"})
	}
	return nil
}

// canSaveAs 检查从 current 保存为 next 是否允许。
func canSaveAs(current, next string) error {
	if current == model.AnnouncementWithdrawn {
		return errs.Newf(errs.Conflict, "已撤回公告不可重新编辑，请新建公告")
	}
	if current == model.AnnouncementPublished && next != model.AnnouncementPublished {
		return errs.Newf(errs.Conflict, "已发布公告只能保持发布状态；如需下线请使用撤回")
	}
	return nil
}

func annValidSeverity(sev string) bool {
	switch sev {
	case annSeverityInfo, annSeverityNotice, annSeverityWarning, annSeverityCritical:
		return true
	default:
		return sev == ""
	}
}

// validateAnnouncement 校验公告保存输入（业务规则）。
func validateAnnouncement(in AnnouncementInput) error {
	fields := map[string]string{}
	titleLen := utf8.RuneCountInString(in.Title)
	if titleLen < 2 || titleLen > 160 {
		fields["title"] = "标题长度必须在 2 到 160 字之间"
	}
	bodyLen := utf8.RuneCountInString(in.Body)
	if bodyLen < 2 || bodyLen > 20000 {
		fields["body"] = "正文长度必须在 2 到 20000 字之间"
	}
	if strings.ContainsRune(in.Title, 0) {
		fields["title"] = "不能包含空字符"
	}
	if strings.ContainsRune(in.Body, 0) {
		fields["body"] = "不能包含空字符"
	}
	if !annValidSeverity(in.Severity) {
		fields["severity"] = "级别只能是 info / notice / warning / critical"
	}
	if in.ID == nil {
		if in.ExpectedVersion != 0 {
			fields["expected_version"] = "新建公告的期望版本必须为 0"
		}
	} else if in.ExpectedVersion <= 0 {
		fields["expected_version"] = "编辑公告必须提交当前版本"
	}
	if len(in.PlanIDs) > 200 {
		fields["target_plan_ids"] = "不能超过 200 个"
	}
	if len(in.GroupIDs) > 200 {
		fields["target_user_group_ids"] = "不能超过 200 个"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

func annNormalizeSeverity(sev string) string {
	if sev == "" {
		return annSeverityInfo
	}
	return sev
}

func annPlanRefsForList(plans []catalogsvc.PlanSummary) []PlanRef {
	var active, rest []PlanRef
	for _, p := range plans {
		ref := PlanRef{ID: p.ID, Name: p.Name, Status: p.Status}
		if p.Status == catalogsvc.PlanActive {
			active = append(active, ref)
		} else {
			rest = append(rest, ref)
		}
	}
	out := make([]PlanRef, 0, len(active)+len(rest))
	out = append(out, active...)
	out = append(out, rest...)
	if len(out) > annPlansLimit {
		out = out[:annPlansLimit]
	}
	return out
}

func annGroupRefs(groups []identitysvc.Group) []GroupRef {
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})
	out := make([]GroupRef, len(groups))
	for i, g := range groups {
		out[i] = GroupRef{ID: g.ID, Name: g.Name}
	}
	return out
}

func annPlanTargets(planIDs []uuid.UUID, byID map[uuid.UUID]catalogsvc.Plan) []PlanRef {
	out := make([]PlanRef, len(planIDs))
	for i, id := range planIDs {
		if p, ok := byID[id]; ok {
			out[i] = PlanRef{ID: id, Name: p.Name, Status: p.Status}
		} else {
			out[i] = PlanRef{ID: id, Name: "不可用套餐", Status: "missing"}
		}
	}
	return out
}

func annGroupTargets(groupIDs []uuid.UUID, groups []identitysvc.Group) []GroupRef {
	byID := map[uuid.UUID]identitysvc.Group{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	var out []GroupRef
	for _, id := range groupIDs {
		if g, ok := byID[id]; ok {
			out = append(out, GroupRef{ID: g.ID, Name: g.Name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if out == nil {
		return []GroupRef{}
	}
	return out
}

func annNonNilUUIDs(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}

func (s *Service) annValidateTargets(ctx context.Context, planIDs, groupIDs []uuid.UUID) error {
	if len(planIDs) > 0 {
		found, err := s.plans.PlansByIDs(ctx, planIDs)
		if err != nil {
			return err
		}
		if len(found) != len(planIDs) {
			return errs.Invalid(map[string]string{"target_plan_ids": "包含不存在的套餐"})
		}
	}
	if len(groupIDs) > 0 {
		groups, err := s.dir.ListGroups(ctx)
		if err != nil {
			return err
		}
		byID := map[uuid.UUID]bool{}
		for _, g := range groups {
			byID[g.ID] = true
		}
		for _, id := range groupIDs {
			if !byID[id] {
				return errs.Invalid(map[string]string{"target_user_group_ids": "包含不存在的用户组"})
			}
		}
	}
	return nil
}

// ListAnnouncements 读后台公告列表及定向选择器数据。
func (s *Service) ListAnnouncements(ctx context.Context) (AdminAnnouncements, error) {
	if err := annPortsPlansDir(s); err != nil {
		return AdminAnnouncements{}, err
	}
	rows, err := s.store.ListAnnouncements(ctx, annListLimit)
	if err != nil {
		return AdminAnnouncements{}, err
	}
	planList, err := s.plans.ListPlans(ctx, catalogsvc.PlanFilter{})
	if err != nil {
		return AdminAnnouncements{}, err
	}
	groups, err := s.dir.ListGroups(ctx)
	if err != nil {
		return AdminAnnouncements{}, err
	}

	planByID := map[uuid.UUID]catalogsvc.Plan{}
	for _, ps := range planList {
		planByID[ps.ID] = ps.Plan
	}

	anns := make([]AnnouncementView, len(rows))
	for i, row := range rows {
		planIDs := annNonNilUUIDs(row.TargetPlanIDs)
		groupIDs := annNonNilUUIDs(row.TargetUserGroupIDs)
		anns[i] = AnnouncementView{
			ID:           row.ID,
			Title:        row.Title,
			Body:         row.Body,
			Severity:     row.Severity,
			Pinned:       row.Pinned,
			Status:       row.Status,
			Version:      row.Version,
			PlanIDs:      planIDs,
			PlanTargets:  annPlanTargets(planIDs, planByID),
			GroupIDs:     groupIDs,
			GroupTargets: annGroupTargets(groupIDs, groups),
			PublishAt:    row.PublishAt,
			ExpiresAt:    row.ExpiresAt,
			CreatedAt:    row.CreatedAt,
		}
	}
	return AdminAnnouncements{
		Announcements: anns,
		Plans:         annPlanRefsForList(planList),
		UserGroups:    annGroupRefs(groups),
	}, nil
}

// SaveAnnouncement 新建或编辑公告。
func (s *Service) SaveAnnouncement(ctx context.Context, in AnnouncementInput) (AnnouncementSaved, error) {
	if err := annPortsPlansDir(s); err != nil {
		return AnnouncementSaved{}, err
	}
	if err := validateAnnouncement(in); err != nil {
		return AnnouncementSaved{}, err
	}
	now := s.now().UTC()
	severity := annNormalizeSeverity(in.Severity)
	status, publishAt, publishedAt := announcementStatus(in.Publish, in.PublishAt, now)
	if err := checkAnnouncementWindow(publishAt, in.ExpiresAt); err != nil {
		return AnnouncementSaved{}, err
	}
	if err := s.annValidateTargets(ctx, in.PlanIDs, in.GroupIDs); err != nil {
		return AnnouncementSaved{}, err
	}

	planIDs := annNonNilUUIDs(in.PlanIDs)
	groupIDs := annNonNilUUIDs(in.GroupIDs)

	var saved AnnouncementSaved
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var before *annAuditSnap
		row := repo.AnnouncementRow{
			Announcement: model.Announcement{
				Title:       in.Title,
				Body:        in.Body,
				Severity:    severity,
				Pinned:      in.Pinned,
				Status:      status,
				PublishAt:   publishAt,
				ExpiresAt:   in.ExpiresAt,
				PublishedAt: publishedAt,
			},
			TargetPlanIDs:      planIDs,
			TargetUserGroupIDs: groupIDs,
		}

		if in.ID == nil {
			principal, ok := authn.From(ctx)
			if !ok {
				return errs.New(errs.Unauthorized)
			}
			uid := principal.UserID
			row.CreatedBy = &uid
			if err := s.store.InsertAnnouncement(ctx, &row); err != nil {
				return err
			}
			s.announcementChanged(ctx, realtime.OpInsert, row.ID)
			saved = AnnouncementSaved{ID: row.ID, Status: row.Status, Version: row.Version}
		} else {
			cur, ok, err := s.store.LockAnnouncement(ctx, *in.ID)
			if err != nil {
				return err
			}
			if !ok {
				return errs.New(errs.NotFound)
			}
			if cur.Version != in.ExpectedVersion {
				return annVersionConflict()
			}
			if cur.Status == model.AnnouncementPublished && in.PublishAt != nil &&
				(cur.PublishAt == nil || !in.PublishAt.Equal(*cur.PublishAt)) {
				return errs.Invalid(map[string]string{"publish_at": "已发布公告不能修改发布时间"})
			}
			if err := canSaveAs(cur.Status, status); err != nil {
				return err
			}
			if status == model.AnnouncementPublished && cur.Status == model.AnnouncementPublished {
				if cur.PublishedAt != nil {
					row.PublishedAt = cur.PublishedAt
				}
				if cur.PublishAt != nil {
					row.PublishAt = cur.PublishAt
				}
			}
			if err := checkAnnouncementWindow(row.PublishAt, in.ExpiresAt); err != nil {
				return err
			}
			bs := annAuditFromRow(cur.Title, cur.Body, cur)
			before = &bs
			row.ID = cur.ID
			row.Version = cur.Version
			newVer, ok, err := s.store.UpdateAnnouncement(ctx, row, in.ExpectedVersion)
			if err != nil {
				return err
			}
			if !ok {
				return annVersionConflict()
			}
			s.announcementChanged(ctx, realtime.OpUpdate, row.ID)
			saved = AnnouncementSaved{ID: row.ID, Status: status, Version: newVer}
			row.Version = newVer
		}

		after := annAuditFromRow(in.Title, in.Body, row)
		var beforeAny any
		if before != nil {
			beforeAny = *before
		}
		resID := saved.ID
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       annAuditActionSaved,
			ResourceType: annResourceType,
			ResourceID:   &resID,
			Data: map[string]any{
				"before": beforeAny,
				"after":  after,
			},
		})
	})
	if err != nil {
		return AnnouncementSaved{}, err
	}
	return saved, nil
}

// WithdrawAnnouncement 撤回一条公告。
func (s *Service) WithdrawAnnouncement(ctx context.Context, id uuid.UUID, expectedVersion int) (int, error) {
	if expectedVersion <= 0 {
		return 0, errs.Invalid(map[string]string{"expected_version": "撤回公告必须提交当前版本"})
	}
	var newVersion int
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, ok, err := s.store.LockAnnouncement(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		if cur.Version != expectedVersion {
			return annVersionConflict()
		}
		if cur.Status == model.AnnouncementWithdrawn {
			return errs.Newf(errs.Conflict, "这条公告已经撤回过了")
		}
		principal, ok := authn.From(ctx)
		if !ok {
			return errs.New(errs.Unauthorized)
		}
		ver, ok, err := s.store.WithdrawAnnouncement(ctx, id, principal.UserID, expectedVersion)
		if err != nil {
			return err
		}
		if !ok {
			return annVersionConflict()
		}
		s.announcementChanged(ctx, realtime.OpUpdate, id)
		newVersion = ver
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       annAuditActionWithdrawn,
			ResourceType: annResourceType,
			ResourceID:   &id,
			Data: map[string]any{
				"before": map[string]any{"status": cur.Status, "version": expectedVersion},
				"after":  map[string]any{"status": model.AnnouncementWithdrawn, "version": newVersion},
			},
		})
	})
	if err != nil {
		return 0, err
	}
	return newVersion, nil
}

// PublishDueAnnouncements 把到点的定时公告转为已发布。
func (s *Service) PublishDueAnnouncements(ctx context.Context) (int, error) {
	var n int
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		published, err := s.store.PublishDueAnnouncements(ctx, s.now().UTC())
		if err != nil {
			return err
		}
		for _, p := range published {
			s.announcementChanged(ctx, realtime.OpUpdate, p.ID)
			resID := p.ID
			if err := s.audit.Write(ctx, auditsvc.Event{
				Action:       annAuditActionPublished,
				ResourceType: annResourceType,
				ResourceID:   &resID,
				Data: map[string]any{
					"status":  model.AnnouncementPublished,
					"version": p.Version,
				},
			}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// MyAnnouncements 返回门户用户当前可见的公告。
func (s *Service) MyAnnouncements(ctx context.Context, userID uuid.UUID) ([]PortalAnnouncement, error) {
	if err := annPortsPortal(s); err != nil {
		return nil, err
	}
	groupID, err := s.dir.UserGroupID(ctx, userID)
	if err != nil {
		return nil, err
	}
	planIDs, err := s.subs.LivePlanIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.VisibleAnnouncements(ctx, s.now().UTC(), groupID, planIDs, annPortalLimit)
	if err != nil {
		return nil, err
	}
	out := make([]PortalAnnouncement, len(rows))
	for i, a := range rows {
		pub := a.CreatedAt
		if a.PublishedAt != nil {
			pub = *a.PublishedAt
		}
		out[i] = PortalAnnouncement{
			ID:          a.ID,
			Title:       a.Title,
			Body:        a.Body,
			Severity:    a.Severity,
			Pinned:      a.Pinned,
			PublishedAt: pub,
		}
	}
	return out, nil
}
