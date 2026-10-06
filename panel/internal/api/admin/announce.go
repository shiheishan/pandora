package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type listAnnouncementsResponse struct {
	Announcements []notify.AdminAnnouncement      `json:"announcements"`
	Plans         []notify.AnnouncementPlanTarget `json:"plans"`
	UserGroups    []notify.AnnouncementGroupRef   `json:"user_groups"`
}

func (h *handlers) listAnnouncements(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	actorID := ""
	if principal := httpx.PrincipalFrom(r.Context()); principal != nil {
		actorID = principal.UserID
	}
	list, err := h.d.Notify.ListAdminAnnouncements(r.Context(), tenantID, actorID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listAnnouncementsResponse{Announcements: list.Announcements, Plans: list.Plans, UserGroups: list.UserGroups})
}

type announceReq struct {
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Severity        string   `json:"severity"`
	Pinned          bool     `json:"pinned"`
	PlanIDs         []string `json:"target_plan_ids"`
	GroupIDs        []string `json:"target_user_group_ids"`
	PublishAt       string   `json:"publish_at"`
	ExpiresAt       string   `json:"expires_at"`
	Publish         bool     `json:"publish"`
	ExpectedVersion int      `json:"expected_version"`
}

type withdrawAnnouncementReq struct {
	ExpectedVersion int `json:"expected_version"`
}

func parseAnnounceTime(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, httpx.New(httpx.CodeValidationFailed, "时间必须是带时区的 RFC3339 格式")
	}
	t = t.UTC()
	return &t, nil
}

func normalizeAnnouncePlanIDs(raw []string) ([]string, error) {
	return normalizeAnnounceIDs(raw, "target_plan_ids", "包含格式不正确的套餐 ID")
}

// normalizeAnnounceIDs 解析、去重并排序一组 uuid；排序让审计摘要与版本比较稳定。
func normalizeAnnounceIDs(raw []string, field, message string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		id, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, httpx.Invalid(map[string]string{field: message})
		}
		normalized := id.String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out, nil
}

func announcementActor(r *http.Request) (string, error) {
	principal := httpx.PrincipalFrom(r.Context())
	if principal == nil || principal.UserID == "" {
		return "", httpx.NotFoundOrForbidden()
	}
	return principal.UserID, nil
}

type saveAnnouncementResponse struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Version int    `json:"version"`
}

func (h *handlers) saveAnnouncement(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	rawID := chi.URLParam(r, "id")
	if rawID != "" {
		id, err := uuid.Parse(rawID)
		if err != nil {
			httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
			return
		}
		rawID = id.String()
	}

	var req announceReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Body = strings.TrimSpace(req.Body)
	fields := map[string]string{}
	if len([]rune(req.Title)) < 2 || len([]rune(req.Title)) > 160 {
		fields["title"] = "标题长度必须在 2 到 160 字之间"
	}
	if len([]rune(req.Body)) < 2 || len([]rune(req.Body)) > 20000 {
		fields["body"] = "正文长度必须在 2 到 20000 字之间"
	}
	switch req.Severity {
	case "info", "notice", "warning", "critical":
	case "":
		req.Severity = "info"
	default:
		fields["severity"] = "级别只能是 info / notice / warning / critical"
	}
	if rawID == "" && req.ExpectedVersion != 0 {
		fields["expected_version"] = "新建公告的期望版本必须为 0"
	}
	if rawID != "" && req.ExpectedVersion <= 0 {
		fields["expected_version"] = "编辑公告必须提交当前版本"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(fields))
		return
	}

	planIDs, err := normalizeAnnouncePlanIDs(req.PlanIDs)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	groupIDs, err := normalizeAnnounceIDs(req.GroupIDs, "target_user_group_ids", "包含格式不正确的用户组 ID")
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	publishAt, err := parseAnnounceTime(req.PublishAt)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	expiresAt, err := parseAnnounceTime(req.ExpiresAt)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if publishAt != nil && expiresAt != nil && !expiresAt.After(*publishAt) {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "下线时间必须晚于发布时间"))
		return
	}

	status := "draft"
	var publishedAt *time.Time
	if req.Publish {
		now := time.Now().UTC()
		if publishAt != nil && publishAt.After(now) {
			status = "scheduled"
		} else {
			status = "published"
			publishedAt = &now
			if publishAt == nil {
				publishAt = &now
			}
		}
	}
	content, err := json.Marshal(map[string]string{"title": req.Title, "body": req.Body})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	actorID, err := announcementActor(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	newID, newVersion, err := h.d.Notify.SaveAdminAnnouncement(r.Context(), notify.SaveAnnouncementInput{
		TenantID: tenantID, ActorID: actorID, ID: rawID,
		Content: string(content), Severity: req.Severity, Pinned: req.Pinned,
		PlanIDs: planIDs, GroupIDs: groupIDs, Status: status,
		PublishAt: publishAt, ExpiresAt: expiresAt, PublishedAt: publishedAt,
		ExpectedVersion: req.ExpectedVersion,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, saveAnnouncementResponse{ID: newID, Status: status, Version: newVersion})
}

type withdrawAnnouncementResponse struct {
	OK      bool `json:"ok"`
	Version int  `json:"version"`
}

func (h *handlers) withdrawAnnouncement(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}
	var req withdrawAnnouncementReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.ExpectedVersion <= 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"expected_version": "撤回公告必须提交当前版本"}))
		return
	}
	actorID, err := announcementActor(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	newVersion, err := h.d.Notify.WithdrawAdminAnnouncement(r.Context(), tenantID, actorID, id.String(), req.ExpectedVersion)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, withdrawAnnouncementResponse{OK: true, Version: newVersion})
}
