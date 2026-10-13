package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/content/service"
)

// adminPageView 是后台内容页 JSON 形状（对照 domain/content.Page 与前端 pageSchema）。
type adminPageView struct {
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
	CreatedBy        *string    `json:"created_by,omitempty"`
	CreatedByName    *string    `json:"created_by_name,omitempty"`
}

// portalPageView 是门户可见文章 JSON 形状（不暴露内部字段）。
type portalPageView struct {
	Slug             string     `json:"slug"`
	Kind             string     `json:"kind"`
	Category         string     `json:"category,omitempty"`
	Version          int        `json:"version"`
	Title            string     `json:"title"`
	Summary          string     `json:"summary,omitempty"`
	Body             string     `json:"body,omitempty"`
	Locale           string     `json:"locale"`
	TargetPlatforms  []string   `json:"target_platforms"`
	MinClientVersion string     `json:"min_client_version,omitempty"`
	MaxClientVersion string     `json:"max_client_version,omitempty"`
	PublishedAt      *time.Time `json:"published_at,omitempty"`
}

// publishRequest 是发新版本的请求体（对照前端 KbBody）。
type publishRequest struct {
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

// archiveRequest 是归档请求体。
type archiveRequest struct {
	ExpectedVersion int `json:"expected_version"`
}

// feedbackRequest 是「有帮助吗」请求体；helpful 用指针，漏传不能默认成 false。
type feedbackRequest struct {
	Helpful *bool `json:"helpful"`
	Version int   `json:"version"`
}

func adminPageFrom(p service.Page) adminPageView {
	out := adminPageView{
		ID:               p.ID.String(),
		Slug:             p.Slug,
		Kind:             p.Kind,
		Category:         p.Category,
		Version:          p.Version,
		Title:            p.Title,
		Summary:          p.Summary,
		Body:             p.Body,
		Locale:           p.Locale,
		SanitizerVersion: p.SanitizerVersion,
		TargetPlatforms:  nonNil(p.TargetPlatforms),
		MinClientVersion: p.MinClientVersion,
		MaxClientVersion: p.MaxClientVersion,
		TargetPlanIDs:    nonNil(p.TargetPlanIDs),
		Visibility:       p.Visibility,
		Status:           p.Status,
		ReviewDueAt:      p.ReviewDueAt,
		PublishedAt:      p.PublishedAt,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
		IsLatestAudience: p.IsLatestInAudience,
	}
	if p.LatestVersion != 0 {
		out.LatestVersion = p.LatestVersion
	}
	if p.CreatedBy != nil {
		s := p.CreatedBy.String()
		out.CreatedBy = &s
	}
	if p.CreatedByName != nil {
		out.CreatedByName = p.CreatedByName
	}
	return out
}

func portalPageFrom(p service.Page) portalPageView {
	return portalPageView{
		Slug:             p.Slug,
		Kind:             p.Kind,
		Category:         p.Category,
		Version:          p.Version,
		Title:            p.Title,
		Summary:          p.Summary,
		Body:             p.Body,
		Locale:           p.Locale,
		TargetPlatforms:  nonNil(p.TargetPlatforms),
		MinClientVersion: p.MinClientVersion,
		MaxClientVersion: p.MaxClientVersion,
		PublishedAt:      p.PublishedAt,
	}
}

func (r publishRequest) publishInput() service.PublishInput {
	return service.PublishInput{
		Slug:                  r.Slug,
		Kind:                  r.Kind,
		Category:              r.Category,
		Title:                 r.Title,
		Summary:               r.Summary,
		Body:                  r.Body,
		Locale:                r.Locale,
		TargetPlatforms:       r.TargetPlatforms,
		MinClientVersion:      r.MinClientVersion,
		MaxClientVersion:      r.MaxClientVersion,
		TargetPlanIDs:         r.TargetPlanIDs,
		Visibility:            r.Visibility,
		Status:                r.Status,
		ReviewDueAt:           r.ReviewDueAt,
		ExpectedLatestVersion: r.ExpectedLatestVersion,
	}
}

// nonNil 让数组字段在 JSON 里永远是 []，不是 null（前端 schema 要求数组）。
func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
