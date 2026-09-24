// [INPUT]: 依赖 platform 的 db/audit/httpx，读写 content_pages，读 users（版本作者名）
// [OUTPUT]: 对外提供 Page、ListFilter、PublishInput 与 Service 的后台列表 / 读取 / 发布 / 归档、门户可见性判定与读取
// [POS]: domain/content 的主服务：版本化知识库与自定义页面，门户可见性一处判定（visible）；后台列表带版本作者 created_by / created_by_name
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package content implements versioned knowledge-base and custom-page delivery.
// Stored bodies are treated as plain text/Markdown source; neither API renders
// trusted HTML. This keeps publication useful without creating an XSS boundary.
package content

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type Service struct{ pool *db.Pool }

func New(pool *db.Pool) *Service { return &Service{pool: pool} }

type Page struct {
	ID               string     `json:"id"`
	Slug             string     `json:"slug"`
	Kind             string     `json:"kind"`
	Category         string     `json:"category,omitempty"`
	Version          int        `json:"version"`
	Title            string     `json:"title"`
	Summary          string     `json:"summary,omitempty"`
	Body             string     `json:"body,omitempty"`
	Locale           string     `json:"locale"`
	SanitizerVersion string     `json:"sanitizer_version"`
	TargetPlatforms  []string   `json:"target_platforms"`
	MinClientVersion string     `json:"min_client_version,omitempty"`
	MaxClientVersion string     `json:"max_client_version,omitempty"`
	TargetPlanIDs    []string   `json:"target_plan_ids"`
	Visibility       string     `json:"visibility"`
	Status           string     `json:"status"`
	ReviewDueAt      *time.Time `json:"review_due_at,omitempty"`
	PublishedAt      *time.Time `json:"published_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	LatestVersion    int        `json:"latest_version,omitempty"`
	IsLatestAudience bool       `json:"is_latest_in_audience,omitempty"`
	// 版本作者只在后台列表里填（「版本历史」的 by）；门户读路径不选这两列，恒为空不输出
	CreatedBy     *string `json:"created_by,omitempty"`
	CreatedByName *string `json:"created_by_name,omitempty"`
}

type ListFilter struct {
	Kind, Status, Query string
	Limit               int
}

type PublishInput struct {
	Slug                  string     `json:"slug"`
	Kind                  string     `json:"kind"`
	Category              string     `json:"category"`
	Title                 string     `json:"title"`
	Summary               string     `json:"summary"`
	Body                  string     `json:"body"`
	Locale                string     `json:"locale"`
	TargetPlatforms       []string   `json:"target_platforms"`
	MinClientVersion      string     `json:"min_client_version"`
	MaxClientVersion      string     `json:"max_client_version"`
	TargetPlanIDs         []string   `json:"target_plan_ids"`
	Visibility            string     `json:"visibility"`
	Status                string     `json:"status"`
	ReviewDueAt           *time.Time `json:"review_due_at"`
	ExpectedLatestVersion int        `json:"expected_latest_version"`
}

type PublishResult struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Version int    `json:"version"`
	Status  string `json:"status"`
}

type ArchiveResult struct {
	ID              string `json:"id"`
	Version         int    `json:"version"`
	Status          string `json:"status"`
	AlreadyArchived bool   `json:"already_archived"`
}

var (
	slugPattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	localePattern  = regexp.MustCompile(`^[a-z]{2,3}(?:-[A-Z]{2})?$`)
	versionPattern = regexp.MustCompile(`^v?[0-9]+(?:\.[0-9]+){0,2}$`)
)

var allowedKinds = map[string]bool{
	"page": true, "kb_article": true, "tutorial": true, "legal": true,
}

var allowedPlatforms = map[string]bool{
	"web": true, "windows": true, "macos": true, "linux": true,
	"android": true, "ios": true,
}

func normalizePublishInput(in PublishInput) (PublishInput, error) {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Kind = strings.TrimSpace(in.Kind)
	in.Category = strings.TrimSpace(in.Category)
	in.Title = strings.TrimSpace(in.Title)
	in.Summary = strings.TrimSpace(in.Summary)
	in.Body = strings.TrimSpace(in.Body)
	in.Locale = strings.TrimSpace(in.Locale)
	in.MinClientVersion = strings.TrimSpace(in.MinClientVersion)
	in.MaxClientVersion = strings.TrimSpace(in.MaxClientVersion)
	in.Visibility = strings.TrimSpace(in.Visibility)
	in.Status = strings.TrimSpace(in.Status)
	if in.Kind == "" {
		in.Kind = "kb_article"
	}
	if in.Locale == "" {
		in.Locale = "zh-CN"
	}
	if in.Visibility == "" {
		in.Visibility = "authenticated"
	}
	if in.Status == "" {
		in.Status = "draft"
	}

	fields := map[string]string{}
	if len(in.Slug) > 80 || !slugPattern.MatchString(in.Slug) {
		fields["slug"] = "标识只能由小写字母、数字和单个连字符组成，最长 80 字符"
	}
	if !allowedKinds[in.Kind] {
		fields["kind"] = "类型必须是 page / kb_article / tutorial / legal"
	}
	if n := utf8.RuneCountInString(in.Category); n > 80 {
		fields["category"] = "分类最长 80 字"
	}
	if n := utf8.RuneCountInString(in.Title); n < 2 || n > 160 {
		fields["title"] = "标题需在 2–160 字之间"
	}
	if n := utf8.RuneCountInString(in.Summary); n > 500 {
		fields["summary"] = "摘要最长 500 字"
	}
	if n := utf8.RuneCountInString(in.Body); n < 10 || n > 100000 {
		fields["body"] = "正文需在 10–100000 字之间"
	}
	if !localePattern.MatchString(in.Locale) {
		fields["locale"] = "语言格式不正确"
	}
	if in.Visibility != "authenticated" && in.Visibility != "internal" {
		fields["visibility"] = "可见性必须是 authenticated / internal"
	}
	if in.Status != "draft" && in.Status != "published" {
		fields["status"] = "状态必须是 draft / published"
	}
	if in.MinClientVersion != "" && !versionPattern.MatchString(in.MinClientVersion) {
		fields["min_client_version"] = "最低客户端版本格式不正确"
	}
	if in.MaxClientVersion != "" && !versionPattern.MatchString(in.MaxClientVersion) {
		fields["max_client_version"] = "最高客户端版本格式不正确"
	}
	if in.MinClientVersion != "" && in.MaxClientVersion != "" && compareVersion(in.MinClientVersion, in.MaxClientVersion) > 0 {
		fields["max_client_version"] = "最高客户端版本不能低于最低版本"
	}

	platformSeen := map[string]bool{}
	in.TargetPlatforms = slicesSortedUnique(in.TargetPlatforms, func(raw string) (string, bool) {
		value := strings.ToLower(strings.TrimSpace(raw))
		if !allowedPlatforms[value] {
			fields["target_platforms"] = "包含不支持的平台"
			return value, false
		}
		if platformSeen[value] {
			return value, false
		}
		platformSeen[value] = true
		return value, true
	})
	planSeen := map[string]bool{}
	validPlans := make([]string, 0, len(in.TargetPlanIDs))
	for _, raw := range in.TargetPlanIDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			fields["target_plan_ids"] = "套餐 ID 格式不正确"
			continue
		}
		value := id.String()
		if !planSeen[value] {
			planSeen[value] = true
			validPlans = append(validPlans, value)
		}
	}
	sort.Strings(validPlans)
	in.TargetPlanIDs = validPlans
	if len(fields) > 0 {
		return PublishInput{}, httpx.Invalid(fields)
	}
	return in, nil
}

func slicesSortedUnique(values []string, normalize func(string) (string, bool)) []string {
	out := make([]string, 0, len(values))
	for _, raw := range values {
		if value, ok := normalize(raw); ok {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Service) ListAdmin(ctx context.Context, tenantID, actorID string, filter ListFilter) ([]Page, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 200
	}
	out := []Page{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text,slug::text,kind,coalesce(category,''),version,
			       coalesce(content->>'title',''),coalesce(content->>'summary',''),
			       coalesce(content->>'locale','zh-CN'),sanitizer_version,
			       target_platforms,coalesce(min_client_version,''),coalesce(max_client_version,''),
			       target_plan_ids::text[],visibility,status,
			       (SELECT max(allp.version) FROM content_pages allp
			         WHERE allp.tenant_id=cp.tenant_id AND allp.slug=cp.slug),
			       NOT EXISTS (SELECT 1 FROM content_pages newer
			         WHERE newer.tenant_id=cp.tenant_id AND newer.slug=cp.slug
			           AND newer.version>cp.version
			           AND coalesce(newer.content->>'locale','zh-CN')=coalesce(cp.content->>'locale','zh-CN')
			           AND newer.target_platforms=cp.target_platforms
			           AND coalesce(newer.min_client_version,'')=coalesce(cp.min_client_version,'')
			           AND coalesce(newer.max_client_version,'')=coalesce(cp.max_client_version,'')
			           AND newer.target_plan_ids=cp.target_plan_ids AND newer.visibility=cp.visibility),
			       review_due_at,published_at,
			       created_at,updated_at,
			       cp.created_by::text,
			       (SELECT coalesce(nullif(btrim(u.display_name), ''), u.email::text) FROM users u
			         WHERE u.tenant_id = cp.tenant_id AND u.id = cp.created_by)
			  FROM content_pages cp
			 WHERE tenant_id=$1
			   AND ($2='' OR kind=$2)
			   AND ($3='' OR status=$3)
			   AND ($4='' OR slug::text ILIKE '%'||$4||'%'
			        OR coalesce(content->>'title','') ILIKE '%'||$4||'%')
			 ORDER BY slug,version DESC LIMIT $5`,
			tenantID, filter.Kind, filter.Status, strings.TrimSpace(filter.Query), filter.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var page Page
			if err := rows.Scan(&page.ID, &page.Slug, &page.Kind, &page.Category, &page.Version,
				&page.Title, &page.Summary, &page.Locale, &page.SanitizerVersion,
				&page.TargetPlatforms, &page.MinClientVersion, &page.MaxClientVersion,
				&page.TargetPlanIDs, &page.Visibility, &page.Status,
				&page.LatestVersion, &page.IsLatestAudience, &page.ReviewDueAt,
				&page.PublishedAt, &page.CreatedAt, &page.UpdatedAt,
				&page.CreatedBy, &page.CreatedByName); err != nil {
				return err
			}
			out = append(out, page)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) GetAdmin(ctx context.Context, tenantID, actorID, pageID string) (*Page, error) {
	if _, err := uuid.Parse(pageID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	var page Page
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT id::text,slug::text,kind,coalesce(category,''),version,
			       coalesce(content->>'title',''),coalesce(content->>'summary',''),
			       coalesce(content->>'body',''),coalesce(content->>'locale','zh-CN'),
			       sanitizer_version,target_platforms,coalesce(min_client_version,''),
			       coalesce(max_client_version,''),target_plan_ids::text[],visibility,status,
			       (SELECT max(allp.version) FROM content_pages allp
			         WHERE allp.tenant_id=cp.tenant_id AND allp.slug=cp.slug),
			       NOT EXISTS (SELECT 1 FROM content_pages newer
			         WHERE newer.tenant_id=cp.tenant_id AND newer.slug=cp.slug
			           AND newer.version>cp.version
			           AND coalesce(newer.content->>'locale','zh-CN')=coalesce(cp.content->>'locale','zh-CN')
			           AND newer.target_platforms=cp.target_platforms
			           AND coalesce(newer.min_client_version,'')=coalesce(cp.min_client_version,'')
			           AND coalesce(newer.max_client_version,'')=coalesce(cp.max_client_version,'')
			           AND newer.target_plan_ids=cp.target_plan_ids AND newer.visibility=cp.visibility),
			       review_due_at,published_at,created_at,updated_at
			  FROM content_pages cp WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, pageID).
			Scan(&page.ID, &page.Slug, &page.Kind, &page.Category, &page.Version,
				&page.Title, &page.Summary, &page.Body, &page.Locale, &page.SanitizerVersion,
				&page.TargetPlatforms, &page.MinClientVersion, &page.MaxClientVersion,
				&page.TargetPlanIDs, &page.Visibility, &page.Status,
				&page.LatestVersion, &page.IsLatestAudience, &page.ReviewDueAt,
				&page.PublishedAt, &page.CreatedAt, &page.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &page, nil
}

func (s *Service) PublishVersion(ctx context.Context, tenantID, actorID, requestID string, input PublishInput) (*PublishResult, error) {
	in, err := normalizePublishInput(input)
	if err != nil {
		return nil, err
	}
	contentJSON, err := json.Marshal(map[string]string{
		"title": in.Title, "summary": in.Summary, "body": in.Body, "locale": in.Locale,
	})
	if err != nil {
		return nil, err
	}
	result := &PublishResult{Slug: in.Slug, Status: in.Status}
	err = s.pool.InTxSerializable(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		if len(in.TargetPlanIDs) > 0 {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM plans
				WHERE tenant_id=$1 AND id=ANY($2::uuid[])`, tenantID, in.TargetPlanIDs).Scan(&count); err != nil {
				return err
			}
			if count != len(in.TargetPlanIDs) {
				return httpx.Invalid(map[string]string{"target_plan_ids": "包含不存在的套餐"})
			}
		}
		var latest int
		err := tx.QueryRow(ctx, `SELECT version FROM content_pages
			WHERE tenant_id=$1 AND slug=$2 ORDER BY version DESC LIMIT 1 FOR UPDATE`,
			tenantID, in.Slug).Scan(&latest)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if latest != in.ExpectedLatestVersion {
			return httpx.New(httpx.CodeConflict, "内容已产生新版本，请刷新后重试")
		}
		result.Version = latest + 1
		var superseded []map[string]any
		if in.Status == "published" {
			rows, err := tx.Query(ctx, `
				UPDATE content_pages SET status='archived'
				 WHERE tenant_id=$1 AND slug=$2 AND status='published'
				   AND coalesce(content->>'locale','zh-CN')=$3
				   AND target_platforms=$4::text[]
				   AND coalesce(min_client_version,'')=$5
				   AND coalesce(max_client_version,'')=$6
				   AND target_plan_ids=$7::uuid[] AND visibility=$8
				 RETURNING id::text,version`, tenantID, in.Slug, in.Locale,
				in.TargetPlatforms, in.MinClientVersion, in.MaxClientVersion,
				in.TargetPlanIDs, in.Visibility)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				var version int
				if err := rows.Scan(&id, &version); err != nil {
					rows.Close()
					return err
				}
				superseded = append(superseded, map[string]any{"id": id, "version": version})
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
		}
		var publishedAt *time.Time
		if in.Status == "published" {
			now := time.Now().UTC()
			publishedAt = &now
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO content_pages
			  (tenant_id,slug,kind,category,version,content,sanitizer_version,
			   target_platforms,min_client_version,max_client_version,target_plan_ids,
			   visibility,status,review_due_at,published_at,created_by)
			VALUES ($1,$2,$3,NULLIF($4,''),$5,$6::jsonb,'plain-v1',$7,
			        NULLIF($8,''),NULLIF($9,''),$10::uuid[],$11,$12,$13,$14,$15::uuid)
			RETURNING id::text`, tenantID, in.Slug, in.Kind, in.Category, result.Version,
			string(contentJSON), in.TargetPlatforms, in.MinClientVersion, in.MaxClientVersion,
			in.TargetPlanIDs, in.Visibility, in.Status, in.ReviewDueAt, publishedAt, actorID).
			Scan(&result.ID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "content.version_created", ResourceType: "content_page", ResourceID: &result.ID,
			APIDomain: "admin", Outcome: "success", RequestID: requestID,
			AfterDigest: map[string]any{"slug": in.Slug, "kind": in.Kind,
				"version": result.Version, "status": in.Status, "visibility": in.Visibility,
				"superseded": superseded},
		})
	})
	if err != nil {
		if db.IsUniqueViolation(err) || db.IsSerializationFailure(err) {
			return nil, httpx.New(httpx.CodeConflict, "内容版本已变化，请刷新后重试")
		}
		return nil, err
	}
	return result, nil
}

func (s *Service) Archive(ctx context.Context, tenantID, actorID, requestID, pageID string, expectedVersion int) (*ArchiveResult, error) {
	if _, err := uuid.Parse(pageID); err != nil || expectedVersion <= 0 {
		return nil, httpx.NotFoundOrForbidden()
	}
	result := &ArchiveResult{ID: pageID}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT version,status FROM content_pages
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, pageID).
			Scan(&result.Version, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if result.Version != expectedVersion {
			return httpx.New(httpx.CodeConflict, "内容版本已变化，请刷新后重试")
		}
		result.Status = "archived"
		if status == "archived" {
			result.AlreadyArchived = true
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE content_pages SET status='archived'
			WHERE tenant_id=$1 AND id=$2::uuid AND version=$3`, tenantID, pageID, expectedVersion); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID,
			Action: "content.archived", ResourceType: "content_page", ResourceID: &pageID,
			APIDomain: "admin", Outcome: "success", RequestID: requestID,
			BeforeDigest: map[string]any{"status": status, "version": expectedVersion},
			AfterDigest:  map[string]any{"status": "archived", "version": expectedVersion},
		})
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type VisibleFilter struct {
	Kind, Platform, ClientVersion, Locale string
}

func (s *Service) ListVisible(ctx context.Context, tenantID, userID string, filter VisibleFilter) ([]Page, error) {
	return s.visible(ctx, tenantID, userID, "", filter, false)
}

func (s *Service) GetVisible(ctx context.Context, tenantID, userID, slug string, filter VisibleFilter) (*Page, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugPattern.MatchString(slug) {
		return nil, httpx.NotFoundOrForbidden()
	}
	pages, err := s.visible(ctx, tenantID, userID, slug, filter, true)
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, httpx.NotFoundOrForbidden()
	}
	return &pages[0], nil
}

func (s *Service) visible(ctx context.Context, tenantID, userID, slug string, filter VisibleFilter, includeBody bool) ([]Page, error) {
	if filter.Kind != "" && !allowedKinds[filter.Kind] {
		return nil, httpx.Invalid(map[string]string{"kind": "无效的内容类型"})
	}
	filter.Platform = strings.ToLower(strings.TrimSpace(filter.Platform))
	if filter.Platform != "" && !allowedPlatforms[filter.Platform] {
		return nil, httpx.Invalid(map[string]string{"platform": "无效的平台"})
	}
	if filter.ClientVersion != "" && !versionPattern.MatchString(filter.ClientVersion) {
		return nil, httpx.Invalid(map[string]string{"client_version": "客户端版本格式不正确"})
	}
	filter.Locale = strings.TrimSpace(filter.Locale)
	if filter.Locale == "" {
		filter.Locale = "zh-CN"
	}
	if !localePattern.MatchString(filter.Locale) {
		return nil, httpx.Invalid(map[string]string{"locale": "语言格式不正确"})
	}
	out := []Page{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		bodyExpr := `''`
		if includeBody {
			bodyExpr = `coalesce(content->>'body','')`
		}
		rows, err := tx.Query(ctx, `
			SELECT id::text,slug::text,kind,coalesce(category,''),version,
			       coalesce(content->>'title',''),coalesce(content->>'summary',''),
			       `+bodyExpr+`,coalesce(content->>'locale','zh-CN'),
			       sanitizer_version,target_platforms,coalesce(min_client_version,''),
			       coalesce(max_client_version,''),target_plan_ids::text[],visibility,status,
			       review_due_at,published_at,created_at,updated_at
			  FROM content_pages cp
			 WHERE cp.tenant_id=$1 AND cp.status='published'
			   AND cp.visibility='authenticated'
			   AND ($3='' OR cp.kind=$3)
			   AND (cardinality(cp.target_platforms)=0 OR ($4<>'' AND $4=ANY(cp.target_platforms)))
			   AND ($5='' OR cp.slug=$5)
			   AND coalesce(cp.content->>'locale','zh-CN')=$6
			   AND (cardinality(cp.target_plan_ids)=0 OR EXISTS (
			       SELECT 1 FROM subscriptions s
			        WHERE s.tenant_id=$1 AND s.user_id=$2::uuid
			          AND s.status IN ('trialing','active','past_due','grace')
			          AND s.plan_id=ANY(cp.target_plan_ids)))
			 ORDER BY cp.slug,cp.version DESC LIMIT 200`,
			tenantID, userID, filter.Kind, filter.Platform, slug, filter.Locale)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() {
			var page Page
			if err := rows.Scan(&page.ID, &page.Slug, &page.Kind, &page.Category, &page.Version,
				&page.Title, &page.Summary, &page.Body, &page.Locale, &page.SanitizerVersion,
				&page.TargetPlatforms, &page.MinClientVersion, &page.MaxClientVersion,
				&page.TargetPlanIDs, &page.Visibility, &page.Status, &page.ReviewDueAt,
				&page.PublishedAt, &page.CreatedAt, &page.UpdatedAt); err != nil {
				return err
			}
			if !seen[page.Slug] && versionVisible(page.MinClientVersion, page.MaxClientVersion, filter.ClientVersion) {
				seen[page.Slug] = true
				out = append(out, page)
			}
		}
		return rows.Err()
	})
	if err == nil {
		sort.SliceStable(out, func(i, j int) bool {
			left, right := out[i].PublishedAt, out[j].PublishedAt
			if left != nil && right != nil && !left.Equal(*right) {
				return left.After(*right)
			}
			if left != nil && right == nil {
				return true
			}
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		})
	}
	return out, err
}

func versionVisible(minimum, maximum, actual string) bool {
	if minimum == "" && maximum == "" {
		return true
	}
	if actual == "" {
		return false
	}
	return (minimum == "" || compareVersion(actual, minimum) >= 0) &&
		(maximum == "" || compareVersion(actual, maximum) <= 0)
}

func compareVersion(a, b string) int {
	parse := func(raw string) [3]int {
		raw = strings.TrimPrefix(raw, "v")
		parts := strings.Split(raw, ".")
		var out [3]int
		for i := 0; i < len(parts) && i < 3; i++ {
			out[i], _ = strconv.Atoi(parts[i])
		}
		return out
	}
	left, right := parse(a), parse(b)
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}
