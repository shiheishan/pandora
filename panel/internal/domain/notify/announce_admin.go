// [INPUT]: 依赖 platform 的 db 租户事务（带操作人）、audit 同事务审计、httpx 的错误模型与请求 ID；读写 announcements，读 plans / user_groups 做定向校验与展示
// [OUTPUT]: 对外提供 Service.ListAdminAnnouncements / SaveAdminAnnouncement / WithdrawAdminAnnouncement 与 AdminAnnouncement、AnnouncementPlanTarget、AnnouncementGroupRef、AdminAnnouncementList、SaveAnnouncementInput
// [POS]: domain/notify 的公告后台读写（从 api/admin/announce.go 下沉）：草稿 / 定时 / 发布 / 撤回的状态机、乐观并发（FOR UPDATE + version CAS）与事务内审计都在这里；与 announce.go 的门户可见口径、定时发布读同一张表；请求校验与时间解析留在 handler

package notify

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// AnnouncementPlanTarget 是公告定向的套餐；套餐已不存在时名称与状态给占位值。
type AnnouncementPlanTarget struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// AnnouncementGroupRef 是公告定向的用户组。
type AnnouncementGroupRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AdminAnnouncement 是后台公告列表的一行。
type AdminAnnouncement struct {
	ID          string                   `json:"id"`
	Title       string                   `json:"title"`
	Body        string                   `json:"body"`
	Severity    string                   `json:"severity"`
	Pinned      bool                     `json:"pinned"`
	Status      string                   `json:"status"`
	Version     int                      `json:"version"`
	PlanIDs     []string                 `json:"target_plan_ids"`
	PlanTargets []AnnouncementPlanTarget `json:"plan_targets"`
	// 用户组定向：门户 notify/announce.go 一直按这一列过滤，此前后台读写都没暴露
	GroupIDs     []string               `json:"target_user_group_ids"`
	GroupTargets []AnnouncementGroupRef `json:"user_group_targets"`
	PublishAt    *time.Time             `json:"publish_at"`
	ExpiresAt    *time.Time             `json:"expires_at"`
	CreatedAt    time.Time              `json:"created_at"`
}

// AdminAnnouncementList 是后台公告页一次读到的全部内容：公告、可定向的套餐与用户组。
type AdminAnnouncementList struct {
	Announcements []AdminAnnouncement
	Plans         []AnnouncementPlanTarget
	UserGroups    []AnnouncementGroupRef
}

// ListAdminAnnouncements 读本租户最近 200 条公告，以及定向选择器要用的套餐与用户组。
func (s *Service) ListAdminAnnouncements(ctx context.Context, tenantID, actorID string) (*AdminAnnouncementList, error) {
	out := []AdminAnnouncement{}
	plans := []AnnouncementPlanTarget{}
	var groups []AnnouncementGroupRef

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
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
			       coalesce(a.target_user_group_ids::text[], '{}'),
			       COALESCE((
			         SELECT jsonb_agg(jsonb_build_object('id', g.id::text, 'name', g.name) ORDER BY g.name)
			           FROM user_groups g
			          WHERE g.tenant_id = a.tenant_id AND g.id = ANY(a.target_user_group_ids)
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
			var a AdminAnnouncement
			var targetsJSON, groupsJSON []byte
			if err := rows.Scan(&a.ID, &a.Title, &a.Body, &a.Severity, &a.Pinned,
				&a.Status, &a.Version, &a.PlanIDs, &targetsJSON, &a.GroupIDs, &groupsJSON,
				&a.PublishAt, &a.ExpiresAt, &a.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(targetsJSON, &a.PlanTargets); err != nil {
				return err
			}
			if err := json.Unmarshal(groupsJSON, &a.GroupTargets); err != nil {
				return err
			}
			out = append(out, a)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		planRows, err := tx.Query(ctx, `SELECT id::text,name,status FROM plans
			WHERE tenant_id=$1 ORDER BY status='active' DESC, sort_order, name LIMIT 500`, tenantID)
		if err != nil {
			return err
		}
		for planRows.Next() {
			var plan AnnouncementPlanTarget
			if err := planRows.Scan(&plan.ID, &plan.Name, &plan.Status); err != nil {
				planRows.Close()
				return err
			}
			plans = append(plans, plan)
		}
		planRows.Close()
		if err := planRows.Err(); err != nil {
			return err
		}
		groupRows, err := tx.Query(ctx, `SELECT id::text, name FROM user_groups
			WHERE tenant_id = $1 ORDER BY name LIMIT 500`, tenantID)
		if err != nil {
			return err
		}
		groups, err = pgx.CollectRows(groupRows, pgx.RowToStructByPos[AnnouncementGroupRef])
		return err
	})
	if err != nil {
		return nil, err
	}
	if groups == nil {
		groups = []AnnouncementGroupRef{}
	}
	return &AdminAnnouncementList{Announcements: out, Plans: plans, UserGroups: groups}, nil
}

type announcementAuditState struct {
	ContentSHA256 string     `json:"content_sha256"`
	Severity      string     `json:"severity"`
	Pinned        bool       `json:"pinned"`
	PlanIDs       []string   `json:"target_plan_ids"`
	GroupIDs      []string   `json:"target_user_group_ids"`
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

func validateAnnouncementGroupsContext(ctx context.Context, tx pgx.Tx, tenantID string, groupIDs []string) error {
	if len(groupIDs) == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_groups
		WHERE tenant_id=$1 AND id=ANY($2::uuid[])`, tenantID, groupIDs).Scan(&count); err != nil {
		return err
	}
	if count != len(groupIDs) {
		return httpx.Invalid(map[string]string{"target_user_group_ids": "包含不存在或不属于当前租户的用户组"})
	}
	return nil
}

// SaveAnnouncementInput 是已校验、已规范化的一次公告保存：ID 为空即新建。
// Status / PublishAt / PublishedAt 由调用方按「是否发布、发布时间是否在未来」算好。
type SaveAnnouncementInput struct {
	TenantID        string
	ActorID         string
	ID              string
	Content         string
	Severity        string
	Pinned          bool
	PlanIDs         []string
	GroupIDs        []string
	Status          string
	PublishAt       *time.Time
	ExpiresAt       *time.Time
	PublishedAt     *time.Time
	ExpectedVersion int
}

// SaveAdminAnnouncement 新建或编辑一条公告，返回公告 id 与新版本号。
// 编辑先 FOR UPDATE 锁行、核对期望版本与状态机，再按 version CAS 更新；同事务写 announcement.saved 审计。
func (s *Service) SaveAdminAnnouncement(ctx context.Context, in SaveAnnouncementInput) (string, int, error) {
	tenantID := in.TenantID
	actorID := in.ActorID
	actorPtr := &actorID
	planIDs, groupIDs := in.PlanIDs, in.GroupIDs
	status := in.Status
	publishAt, expiresAt, publishedAt := in.PublishAt, in.ExpiresAt, in.PublishedAt

	newID := in.ID
	newVersion := 1
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		if err := validateAnnouncementPlansContext(ctx, tx, tenantID, planIDs); err != nil {
			return err
		}
		if err := validateAnnouncementGroupsContext(ctx, tx, tenantID, groupIDs); err != nil {
			return err
		}
		var before any
		if in.ID == "" {
			if err := tx.QueryRow(ctx, `
				INSERT INTO announcements
				  (tenant_id,content,severity,pinned,target_plan_ids,status,
				   publish_at,expires_at,published_at,created_by,target_user_group_ids)
				VALUES ($1,$2::jsonb,$3,$4,$5::uuid[],$6,$7,$8,$9,$10::uuid,$11::uuid[])
				RETURNING id::text,version`, tenantID, in.Content, in.Severity,
				in.Pinned, planIDs, status, publishAt, expiresAt, publishedAt, actorID, groupIDs).
				Scan(&newID, &newVersion); err != nil {
				return err
			}
		} else {
			var (
				currentContent                     string
				currentSeverity, currentStatus     string
				currentPinned                      bool
				currentPlanIDs, currentGroupIDs    []string
				currentPublishAt, currentExpiresAt *time.Time
				currentPublishedAt                 *time.Time
			)
			if err := tx.QueryRow(ctx, `SELECT content::text,severity,pinned,target_plan_ids::text[],
				status,publish_at,expires_at,published_at,version,coalesce(target_user_group_ids::text[],'{}')
				FROM announcements WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, in.ID).
				Scan(&currentContent, &currentSeverity, &currentPinned, &currentPlanIDs,
					&currentStatus, &currentPublishAt, &currentExpiresAt, &currentPublishedAt,
					&newVersion, &currentGroupIDs); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.NotFoundOrForbidden()
				}
				return err
			}
			if newVersion != in.ExpectedVersion {
				return httpx.New(httpx.CodeConflict, "公告已被其他管理员修改，请刷新后重试")
			}
			if err := validateAnnouncementTransition(currentStatus, status); err != nil {
				return err
			}
			if status == "published" && currentStatus == "published" && currentPublishedAt != nil {
				publishedAt = currentPublishedAt
			}
			snapshot := announcementAuditSnapshot(currentContent, currentSeverity, currentPinned,
				currentPlanIDs, currentStatus, currentPublishAt, currentExpiresAt,
				currentPublishedAt, newVersion)
			snapshot.GroupIDs = currentGroupIDs
			before = snapshot
			if err := tx.QueryRow(ctx, `
				UPDATE announcements
				   SET content=$3::jsonb,severity=$4,pinned=$5,target_plan_ids=$6::uuid[],
				       status=$7,publish_at=$8,expires_at=$9,published_at=$10,
				       target_user_group_ids=$12::uuid[],
				       version=version+1,updated_at=now()
				 WHERE tenant_id=$1 AND id=$2::uuid AND version=$11
				 RETURNING version`, tenantID, in.ID, in.Content, in.Severity,
				in.Pinned, planIDs, status, publishAt, expiresAt, publishedAt,
				in.ExpectedVersion, groupIDs).Scan(&newVersion); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.New(httpx.CodeConflict, "公告已被其他管理员修改，请刷新后重试")
				}
				return err
			}
		}
		after := announcementAuditSnapshot(in.Content, in.Severity, in.Pinned,
			planIDs, status, publishAt, expiresAt, publishedAt, newVersion)
		after.GroupIDs = groupIDs
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorPtr,
			Action: "announcement.saved", ResourceType: "announcement", ResourceID: &newID,
			APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: before, AfterDigest: after,
		})
	})
	return newID, newVersion, err
}

// WithdrawAdminAnnouncement 撤回一条公告，返回新版本号：锁行、核对期望版本、
// 已撤回的不能再撤，按 version CAS 置 withdrawn，同事务写 announcement.withdrawn 审计。
func (s *Service) WithdrawAdminAnnouncement(ctx context.Context, tenantID, actorID, idString string, expectedVersion int) (int, error) {
	actorPtr := &actorID
	newVersion := expectedVersion
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var currentStatus string
		if err := tx.QueryRow(ctx, `SELECT status,version FROM announcements
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, idString).
			Scan(&currentStatus, &newVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if newVersion != expectedVersion {
			return httpx.New(httpx.CodeConflict, "公告已被其他管理员修改，请刷新后重试")
		}
		if currentStatus == "withdrawn" {
			return httpx.New(httpx.CodeConflict, "这条公告已经撤回过了")
		}
		if err := tx.QueryRow(ctx, `UPDATE announcements
			SET status='withdrawn',withdrawn_at=now(),withdrawn_by=$3::uuid,
			    version=version+1,updated_at=now()
			WHERE tenant_id=$1 AND id=$2::uuid AND version=$4 RETURNING version`,
			tenantID, idString, actorID, expectedVersion).Scan(&newVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeConflict, "公告已被其他管理员修改，请刷新后重试")
			}
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorPtr,
			Action: "announcement.withdrawn", ResourceType: "announcement", ResourceID: &idString,
			APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"status": currentStatus, "version": expectedVersion},
			AfterDigest:  map[string]any{"status": "withdrawn", "version": newVersion},
		})
	})
	return newVersion, err
}
