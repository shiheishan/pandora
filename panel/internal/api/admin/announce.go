package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type announcePlanTarget struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type announceRow struct {
	ID          string               `json:"id"`
	Title       string               `json:"title"`
	Body        string               `json:"body"`
	Severity    string               `json:"severity"`
	Pinned      bool                 `json:"pinned"`
	Status      string               `json:"status"`
	Version     int                  `json:"version"`
	PlanIDs     []string             `json:"target_plan_ids"`
	PlanTargets []announcePlanTarget `json:"plan_targets"`
	PublishAt   *time.Time           `json:"publish_at"`
	ExpiresAt   *time.Time           `json:"expires_at"`
	CreatedAt   time.Time            `json:"created_at"`
}

func (h *handlers) listAnnouncements(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	actorID := ""
	if principal := httpx.PrincipalFrom(r.Context()); principal != nil {
		actorID = principal.UserID
	}
	out := []announceRow{}
	plans := []announcePlanTarget{}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT a.id::text,
			       COALESCE(a.content->>'title',''), COALESCE(a.content->>'body',''),
			       a.severity, a.pinned, a.status, a.version,
			       a.target_plan_ids::text[],
			       COALESCE((
			         SELECT jsonb_agg(jsonb_build_object(
			           'id', target.plan_id::text,
			           'name', COALESCE(p.name, '不可用套餐'),
			           'status', COALESCE(p.status, 'missing')) ORDER BY target.ordinality)
			           FROM unnest(a.target_plan_ids) WITH ORDINALITY AS target(plan_id, ordinality)
			           LEFT JOIN plans p ON p.tenant_id=a.tenant_id AND p.id=target.plan_id
			       ), '[]'::jsonb),
			       a.publish_at, a.expires_at, a.created_at
			  FROM announcements a
			 WHERE a.tenant_id = $1
			 ORDER BY a.pinned DESC, COALESCE(a.published_at, a.created_at) DESC
			 LIMIT 200`, tenantID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a announceRow
			var targetsJSON []byte
			if err := rows.Scan(&a.ID, &a.Title, &a.Body, &a.Severity, &a.Pinned,
				&a.Status, &a.Version, &a.PlanIDs, &targetsJSON,
				&a.PublishAt, &a.ExpiresAt, &a.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(targetsJSON, &a.PlanTargets); err != nil {
				return err
			}
			out = append(out, a)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		planRows, err := tx.Query(r.Context(), `SELECT id::text,name,status FROM plans
			WHERE tenant_id=$1 ORDER BY status='active' DESC, sort_order, name LIMIT 500`, tenantID)
		if err != nil {
			return err
		}
		defer planRows.Close()
		for planRows.Next() {
			var plan announcePlanTarget
			if err := planRows.Scan(&plan.ID, &plan.Name, &plan.Status); err != nil {
				return err
			}
			plans = append(plans, plan)
		}
		return planRows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"announcements": out, "plans": plans})
}

type announceReq struct {
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Severity        string   `json:"severity"`
	Pinned          bool     `json:"pinned"`
	PlanIDs         []string `json:"target_plan_ids"`
	PublishAt       string   `json:"publish_at"`
	ExpiresAt       string   `json:"expires_at"`
	Publish         bool     `json:"publish"`
	ExpectedVersion int      `json:"expected_version"`
}

type withdrawAnnouncementReq struct {
	ExpectedVersion int `json:"expected_version"`
}

type announcementAuditState struct {
	ContentSHA256 string     `json:"content_sha256"`
	Severity      string     `json:"severity"`
	Pinned        bool       `json:"pinned"`
	PlanIDs       []string   `json:"target_plan_ids"`
	Status        string     `json:"status"`
	PublishAt     *time.Time `json:"publish_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	PublishedAt   *time.Time `json:"published_at,omitempty"`
	Version       int        `json:"version"`
}

func announcementAuditSnapshot(content string, severity string, pinned bool, planIDs []string,
	status string, publishAt, expiresAt, publishedAt *time.Time, version int) announcementAuditState {
	sum := sha256.Sum256([]byte(content))
	return announcementAuditState{
		ContentSHA256: fmt.Sprintf("%x", sum[:]), Severity: severity, Pinned: pinned,
		PlanIDs: append([]string(nil), planIDs...), Status: status,
		PublishAt: publishAt, ExpiresAt: expiresAt, PublishedAt: publishedAt, Version: version,
	}
}

func validateAnnouncementTransition(current, next string) error {
	if current == "withdrawn" {
		return httpx.New(httpx.CodeConflict, "已撤回公告不可重新编辑，请新建公告")
	}
	if current == "published" && next != "published" {
		return httpx.New(httpx.CodeConflict, "已发布公告只能保持发布状态；如需下线请使用撤回")
	}
	return nil
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
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		id, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, httpx.Invalid(map[string]string{"target_plan_ids": "包含格式不正确的套餐 ID"})
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

func announcementActor(r *http.Request) (string, *string, error) {
	principal := httpx.PrincipalFrom(r.Context())
	if principal == nil || principal.UserID == "" {
		return "", nil, httpx.NotFoundOrForbidden()
	}
	actorID := principal.UserID
	return actorID, &actorID, nil
}

func validateAnnouncementPlansContext(ctx context.Context, tx pgx.Tx, tenantID string, planIDs []string) error {
	if len(planIDs) == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM plans
		WHERE tenant_id=$1 AND id=ANY($2::uuid[])`, tenantID, planIDs).Scan(&count); err != nil {
		return err
	}
	if count != len(planIDs) {
		return httpx.Invalid(map[string]string{"target_plan_ids": "包含不存在或不属于当前租户的套餐"})
	}
	return nil
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
	actorID, actorPtr, err := announcementActor(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	newID := rawID
	newVersion := 1
	err = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		if err := validateAnnouncementPlansContext(r.Context(), tx, tenantID, planIDs); err != nil {
			return err
		}
		var before any
		if rawID == "" {
			if err := tx.QueryRow(r.Context(), `
				INSERT INTO announcements
				  (tenant_id,content,severity,pinned,target_plan_ids,status,
				   publish_at,expires_at,published_at,created_by)
				VALUES ($1,$2::jsonb,$3,$4,$5::uuid[],$6,$7,$8,$9,$10::uuid)
				RETURNING id::text,version`, tenantID, string(content), req.Severity,
				req.Pinned, planIDs, status, publishAt, expiresAt, publishedAt, actorID).
				Scan(&newID, &newVersion); err != nil {
				return err
			}
		} else {
			var (
				currentContent                     string
				currentSeverity, currentStatus     string
				currentPinned                      bool
				currentPlanIDs                     []string
				currentPublishAt, currentExpiresAt *time.Time
				currentPublishedAt                 *time.Time
			)
			if err := tx.QueryRow(r.Context(), `SELECT content::text,severity,pinned,target_plan_ids::text[],
				status,publish_at,expires_at,published_at,version
				FROM announcements WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, rawID).
				Scan(&currentContent, &currentSeverity, &currentPinned, &currentPlanIDs,
					&currentStatus, &currentPublishAt, &currentExpiresAt, &currentPublishedAt,
					&newVersion); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.NotFoundOrForbidden()
				}
				return err
			}
			if newVersion != req.ExpectedVersion {
				return httpx.New(httpx.CodeConflict, "公告已被其他管理员修改，请刷新后重试")
			}
			if err := validateAnnouncementTransition(currentStatus, status); err != nil {
				return err
			}
			if status == "published" && currentStatus == "published" && currentPublishedAt != nil {
				publishedAt = currentPublishedAt
			}
			before = announcementAuditSnapshot(currentContent, currentSeverity, currentPinned,
				currentPlanIDs, currentStatus, currentPublishAt, currentExpiresAt,
				currentPublishedAt, newVersion)
			if err := tx.QueryRow(r.Context(), `
				UPDATE announcements
				   SET content=$3::jsonb,severity=$4,pinned=$5,target_plan_ids=$6::uuid[],
				       status=$7,publish_at=$8,expires_at=$9,published_at=$10,
				       version=version+1,updated_at=now()
				 WHERE tenant_id=$1 AND id=$2::uuid AND version=$11
				 RETURNING version`, tenantID, rawID, string(content), req.Severity,
				req.Pinned, planIDs, status, publishAt, expiresAt, publishedAt,
				req.ExpectedVersion).Scan(&newVersion); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.New(httpx.CodeConflict, "公告已被其他管理员修改，请刷新后重试")
				}
				return err
			}
		}
		after := announcementAuditSnapshot(string(content), req.Severity, req.Pinned,
			planIDs, status, publishAt, expiresAt, publishedAt, newVersion)
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorPtr,
			Action: "announcement.saved", ResourceType: "announcement", ResourceID: &newID,
			APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(r.Context()),
			BeforeDigest: before, AfterDigest: after,
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"id": newID, "status": status, "version": newVersion})
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
	actorID, actorPtr, err := announcementActor(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	idString := id.String()
	newVersion := req.ExpectedVersion
	err = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var currentStatus string
		if err := tx.QueryRow(r.Context(), `SELECT status,version FROM announcements
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, idString).
			Scan(&currentStatus, &newVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if newVersion != req.ExpectedVersion {
			return httpx.New(httpx.CodeConflict, "公告已被其他管理员修改，请刷新后重试")
		}
		if currentStatus == "withdrawn" {
			return httpx.New(httpx.CodeConflict, "这条公告已经撤回过了")
		}
		if err := tx.QueryRow(r.Context(), `UPDATE announcements
			SET status='withdrawn',withdrawn_at=now(),withdrawn_by=$3::uuid,
			    version=version+1,updated_at=now()
			WHERE tenant_id=$1 AND id=$2::uuid AND version=$4 RETURNING version`,
			tenantID, idString, actorID, req.ExpectedVersion).Scan(&newVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeConflict, "公告已被其他管理员修改，请刷新后重试")
			}
			return err
		}
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorPtr,
			Action: "announcement.withdrawn", ResourceType: "announcement", ResourceID: &idString,
			APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(r.Context()),
			BeforeDigest: map[string]any{"status": currentStatus, "version": req.ExpectedVersion},
			AfterDigest:  map[string]any{"status": "withdrawn", "version": newVersion},
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true, "version": newVersion})
}
