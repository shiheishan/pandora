// Package service 是 content 模块的业务层：版本化的知识库与自定义页面（后台发版、归档、列表），
// 门户按套餐、平台、客户端版本与语言过滤后的可见文章，以及「这篇有帮助吗」的反馈。别的模块只调本包。
// 门户读单篇文章时按用户替换占位变量、按有无有效订阅处理区块（render.go）。
//
// 正文只当纯文本 / Markdown 源存储与下发，两端都不渲染成可信 HTML，所以这里不是 XSS 边界。
// 每次保存是一个新版本（同一 slug 版本号递增）；发布时把「同一受众」（语言、平台、客户端版本范围、
// 限定套餐、可见性都相同）里已发布的旧版本归档，受众不同的旧版本照常可见。
package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/content/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// Plans 是 content 要的目录读取面：发版时校验限定套餐存在。*catalogsvc.Service 直接满足。
type Plans interface {
	PlansByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Plan, error)
}

// Subscriptions 是 content 要的订阅读取面：限定套餐的文章只对在这些套餐上有生效订阅的用户可见。
// *subsvc.Service 直接满足（subscription/service/content_reads.go）。
type Subscriptions interface {
	LivePlanIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}

// Site 是 content 要的站点设置（占位变量 {{siteName}}、{{siteUrl}}）；*settingssvc.Service 直接满足。
type Site interface {
	Site(ctx context.Context) (settingssvc.Site, error)
	// SiteURL 是当前生效的主域名（以 / 结尾，后台可改；读快照，改了立即生效）
	SiteURL(ctx context.Context) string
}

// SubscribeLinks 是占位变量 {{subscribeUrl}} 要的订阅链接（属 subscription），由 app 适配器实现。
type SubscribeLinks interface {
	// CurrentLink 返回 ctx 里门户主体最新一份生效订阅的订阅链接；没有生效订阅返回空串。
	CurrentLink(ctx context.Context) (string, error)
}

// Users 是 content 要的用户读取面：后台版本历史显示作者名。*identitysvc.Service 直接满足
// （identity/service/content_reads.go）。
type Users interface {
	DisplayNames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
}

// Service 是 content 模块对外的业务入口。
type Service struct {
	repo  *repo.Repo
	tx    txRunner
	audit auditWriter
	plans Plans
	subs  Subscriptions
	users Users
	site  Site
	links SubscribeLinks
	now   func() time.Time
}

// New 构造 content service：plans 传 catalog service，subs 传 subscription service，users 传 identity service。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, plans Plans, subs Subscriptions, users Users) *Service {
	return &Service{repo: r, tx: txm, audit: audit, plans: plans, subs: subs, users: users, now: time.Now}
}

// UseSite 注入站点设置端口；只在启动装配时调用，不并发安全。
func (s *Service) UseSite(x Site) { s.site = x }

// UseSubscribeLinks 注入订阅链接端口；只在启动装配时调用，不并发安全。
func (s *Service) UseSubscribeLinks(x SubscribeLinks) { s.links = x }

// MissingPorts 返回尚未注入的必需端口名。
func (s *Service) MissingPorts() []string {
	var missing []string
	if s.site == nil {
		missing = append(missing, "Site")
	}
	if s.links == nil {
		missing = append(missing, "SubscribeLinks")
	}
	return missing
}

// 审计动作名。
const (
	ActionVersionCreated = "content.version_created"
	ActionArchived       = "content.archived"
)

// ResourcePage 是审计里内容页的资源类型。
const ResourcePage = "content_page"

// 取值集合与缺省值。
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusArchived  = "archived"

	VisibilityAuthenticated = "authenticated"
	VisibilityInternal      = "internal"

	DefaultKind   = "kb_article"
	DefaultLocale = "zh-CN"

	// PlatformAny 表示不按平台过滤（帮助中心要看到全部平台的客户端教程）。
	PlatformAny = "any"
)

// Page 是一个版本。列表不带正文；作者两项只在后台列表里填。
type Page struct {
	ID               uuid.UUID
	Slug             string
	Kind             string
	Category         string
	Version          int
	Title            string
	Summary          string
	Body             string
	Locale           string
	SanitizerVersion string
	// TargetPlatforms、TargetPlanIDs 已排序去重；永不为 nil
	TargetPlatforms  []string
	MinClientVersion string
	MaxClientVersion string
	TargetPlanIDs    []string
	Visibility       string
	Status           string
	ReviewDueAt      *time.Time
	PublishedAt      *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	// LatestVersion 是这个 slug 所有版本里最大的版本号（后台用）
	LatestVersion int
	// IsLatestInAudience 表示同一受众里没有更新的版本（后台用）
	IsLatestInAudience bool
	CreatedBy          *uuid.UUID
	CreatedByName      *string
}

// ListFilter 是后台列表的过滤条件；Limit 不在 1–500 内按 200。
type ListFilter struct {
	Kind, Status, Query string
	Limit               int
}

// PublishInput 是发一个新版本的输入。
type PublishInput struct {
	Slug             string
	Kind             string
	Category         string
	Title            string
	Summary          string
	Body             string
	Locale           string
	TargetPlatforms  []string
	MinClientVersion string
	MaxClientVersion string
	TargetPlanIDs    []string
	Visibility       string
	Status           string
	ReviewDueAt      *time.Time
	// ExpectedLatestVersion 是编辑时看到的最新版本号（新文章为 0），对不上回 409。
	ExpectedLatestVersion int
}

// PublishResult 是发版结果。
type PublishResult struct {
	ID      uuid.UUID
	Slug    string
	Version int
	Status  string
}

// ArchiveResult 是归档结果；已经是归档状态时 AlreadyArchived 为真、不重复记审计。
type ArchiveResult struct {
	ID              uuid.UUID
	Version         int
	Status          string
	AlreadyArchived bool
}

// VisibleFilter 是门户读取的过滤条件。
type VisibleFilter struct {
	Kind, Platform, ClientVersion, Locale string
	// Query 对标题、摘要、正文做不区分大小写的包含匹配（≤100 字）；只看每篇文章可见的那一版，旧版本命中不算。只用于列表。
	Query string
}
