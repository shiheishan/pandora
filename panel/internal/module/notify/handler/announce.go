package handler

import (
	"sort"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type planRefDTO struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Status string    `json:"status"`
}

type groupRefDTO struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type announcementDTO struct {
	ID                 uuid.UUID     `json:"id"`
	Title              string        `json:"title"`
	Body               string        `json:"body"`
	Severity           string        `json:"severity"`
	Pinned             bool          `json:"pinned"`
	Status             string        `json:"status"`
	Version            int           `json:"version"`
	TargetPlanIDs      []uuid.UUID   `json:"target_plan_ids"`
	PlanTargets        []planRefDTO  `json:"plan_targets"`
	TargetUserGroupIDs []uuid.UUID   `json:"target_user_group_ids"`
	UserGroupTargets   []groupRefDTO `json:"user_group_targets"`
	PublishAt          *time.Time    `json:"publish_at"`
	ExpiresAt          *time.Time    `json:"expires_at"`
	CreatedAt          time.Time     `json:"created_at"`
}

type listAnnouncementsResponse struct {
	Announcements []announcementDTO `json:"announcements"`
	Plans         []planRefDTO      `json:"plans"`
	UserGroups    []groupRefDTO     `json:"user_groups"`
}

type saveAnnouncementRequest struct {
	Title              string   `json:"title"`
	Body               string   `json:"body"`
	Severity           string   `json:"severity"`
	Pinned             bool     `json:"pinned"`
	TargetPlanIDs      []string `json:"target_plan_ids"`
	TargetUserGroupIDs []string `json:"target_user_group_ids"`
	PublishAt          string   `json:"publish_at"`
	ExpiresAt          string   `json:"expires_at"`
	Publish            bool     `json:"publish"`
	ExpectedVersion    int      `json:"expected_version"`
}

type saveAnnouncementResponse struct {
	ID      uuid.UUID `json:"id"`
	Status  string    `json:"status"`
	Version int       `json:"version"`
}

type withdrawAnnouncementRequest struct {
	ExpectedVersion int `json:"expected_version"`
}

type withdrawAnnouncementResponse struct {
	OK      bool `json:"ok"`
	Version int  `json:"version"`
}

func annPlanRefDTO(p service.PlanRef) planRefDTO {
	return planRefDTO{ID: p.ID, Name: p.Name, Status: p.Status}
}

func annGroupRefDTO(g service.GroupRef) groupRefDTO {
	return groupRefDTO{ID: g.ID, Name: g.Name}
}

func annViewToDTO(v service.AnnouncementView) announcementDTO {
	plans := make([]planRefDTO, len(v.PlanTargets))
	for i, p := range v.PlanTargets {
		plans[i] = annPlanRefDTO(p)
	}
	groups := make([]groupRefDTO, len(v.GroupTargets))
	for i, g := range v.GroupTargets {
		groups[i] = annGroupRefDTO(g)
	}
	return announcementDTO{
		ID:                 v.ID,
		Title:              v.Title,
		Body:               v.Body,
		Severity:           v.Severity,
		Pinned:             v.Pinned,
		Status:             v.Status,
		Version:            v.Version,
		TargetPlanIDs:      nonNilUUIDSlice(v.PlanIDs),
		PlanTargets:        plans,
		TargetUserGroupIDs: nonNilUUIDSlice(v.GroupIDs),
		UserGroupTargets:   groups,
		PublishAt:          v.PublishAt,
		ExpiresAt:          v.ExpiresAt,
		CreatedAt:          v.CreatedAt,
	}
}

func nonNilUUIDSlice(s []uuid.UUID) []uuid.UUID {
	if s == nil {
		return []uuid.UUID{}
	}
	return s
}

func parseAnnounceTime(field, raw string) (*time.Time, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, errs.Invalid(map[string]string{field: "时间必须是带时区的 RFC3339 格式"})
	}
	utc := t.UTC()
	return &utc, nil
}

func parseAnnounceUUIDList(field, message string, raw []string) ([]uuid.UUID, error) {
	seen := make(map[string]struct{}, len(raw))
	strs := make([]string, 0, len(raw))
	for _, value := range raw {
		id, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, errs.Invalid(map[string]string{field: message})
		}
		normalized := id.String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		strs = append(strs, normalized)
	}
	sort.Strings(strs)
	out := make([]uuid.UUID, len(strs))
	for i, s := range strs {
		out[i] = uuid.MustParse(s)
	}
	return out, nil
}

func (h *Handler) listAnnouncements(c *gin.Context) {
	ctx := c.Request.Context()
	data, err := h.svc.ListAnnouncements(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	anns := make([]announcementDTO, len(data.Announcements))
	for i, a := range data.Announcements {
		anns[i] = annViewToDTO(a)
	}
	plans := make([]planRefDTO, len(data.Plans))
	for i, p := range data.Plans {
		plans[i] = annPlanRefDTO(p)
	}
	groups := make([]groupRefDTO, len(data.UserGroups))
	for i, g := range data.UserGroups {
		groups[i] = annGroupRefDTO(g)
	}
	httpx.OK(c, listAnnouncementsResponse{
		Announcements: anns,
		Plans:         plans,
		UserGroups:    groups,
	})
}

func (h *Handler) saveAnnouncement(c *gin.Context) {
	ctx := c.Request.Context()
	var id *uuid.UUID
	if raw := strings.TrimSpace(c.Param("id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.Fail(c, errs.New(errs.NotFound))
			return
		}
		id = &parsed
	}
	var req saveAnnouncementRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	planIDs, err := parseAnnounceUUIDList("target_plan_ids", "包含格式不正确的套餐 ID", req.TargetPlanIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	groupIDs, err := parseAnnounceUUIDList("target_user_group_ids", "包含格式不正确的用户组 ID", req.TargetUserGroupIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	publishAt, err := parseAnnounceTime("publish_at", req.PublishAt)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	expiresAt, err := parseAnnounceTime("expires_at", req.ExpiresAt)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	saved, err := h.svc.SaveAnnouncement(ctx, service.AnnouncementInput{
		ID:              id,
		Title:           strings.TrimSpace(req.Title),
		Body:            strings.TrimSpace(req.Body),
		Severity:        req.Severity,
		Pinned:          req.Pinned,
		PlanIDs:         planIDs,
		GroupIDs:        groupIDs,
		PublishAt:       publishAt,
		ExpiresAt:       expiresAt,
		Publish:         req.Publish,
		ExpectedVersion: req.ExpectedVersion,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, saveAnnouncementResponse{ID: saved.ID, Status: saved.Status, Version: saved.Version})
}

func (h *Handler) withdrawAnnouncement(c *gin.Context) {
	ctx := c.Request.Context()
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req withdrawAnnouncementRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ver, err := h.svc.WithdrawAnnouncement(ctx, id, req.ExpectedVersion)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, withdrawAnnouncementResponse{OK: true, Version: ver})
}
