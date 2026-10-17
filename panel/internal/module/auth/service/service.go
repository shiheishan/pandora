// Package service 是 auth 模块的业务层：登录、刷新、登出、重认证、会话（Redis）、快捷登录、
// 管理员授予与取消、最后一个管理员保护。
//
// 后台权限只分有和没有：在管理员名单里且账号 active 就能做全部后台操作，没有角色与权限码。
// 会话状态全在 Redis（store.go）；PG 只有管理员名单（repo）。
// 用户与口令属于 identity，经 Users 接口调用（*identitysvc.Service 实现）。
package service

import (
	"context"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/auth/repo"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/jwtx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Users 是 auth 对 identity service 的全部要求；*identitysvc.Service 实现它（app 装配时传入）。
type Users interface {
	// Authenticate 校验邮箱与口令：口令错或用户不存在返回 identity.ErrInvalidCredentials，停用返回 ErrAccountInactive；账号存在时出错也带回 User。
	Authenticate(ctx context.Context, audience, email, password string) (identitysvc.User, error)
	// FindByEmail 按邮箱找用户；不存在返回 errs.NotFound。
	FindByEmail(ctx context.Context, email string) (identitysvc.User, error)
	Get(ctx context.Context, id uuid.UUID) (identitysvc.User, error)
	// Profile 取用户资料（含显示名）；不存在返回 errs.Unauthorized。
	Profile(ctx context.Context, id uuid.UUID) (identitysvc.Profile, error)
	GetMany(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]identitysvc.User, error)
	// VerifyPassword 校验已知用户的口令；错了返回 errs.Unauthorized。
	VerifyPassword(ctx context.Context, audience string, userID uuid.UUID, password string) error
	// TouchLastLogin 记最近登录时间（会话建成后调用）。
	TouchLastLogin(ctx context.Context, userID uuid.UUID) error
}

// UserStatusActive 是 identity 里可登录用户的状态取值。
const UserStatusActive = "active"

// auditWriter 是 audit service 的写入面；*auditsvc.Service 实现它，单测用假实现。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// store 是 service 对 repo 的全部要求；*repo.Repo 实现它，单测用假实现。
type store interface {
	// IsAdmin 报告用户是否在管理员名单里（不看账号状态）。
	IsAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
	// InsertAdmin 把用户加进名单；已在名单里返回 false。
	InsertAdmin(ctx context.Context, userID uuid.UUID, grantedBy *uuid.UUID) (bool, error)
	// DeleteAdmin 把用户移出名单；本来不在返回 false。
	DeleteAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
	// Admins 返回管理员名单。
	Admins(ctx context.Context) ([]repo.AdminRow, error)
	// LockAdminSet 在当前事务里取管理员集合的事务级咨询锁。
	LockAdminSet(ctx context.Context) error
}

var _ store = (*repo.Repo)(nil)

// Config 是会话寿命等参数，取自 config.Auth。
type Config struct {
	AccessTTL        time.Duration
	PublicSessionTTL time.Duration
	AdminSessionTTL  time.Duration
	AdminIdleTimeout time.Duration
}

// Service 是 auth 模块对外的唯一入口。
type Service struct {
	store    store
	tx       txRunner
	sessions *sessionStore
	keys     *crypto.Keys
	issuers  map[string]*jwtx.Issuer
	users    Users
	audit    auditWriter
	cfg      Config
	now      func() time.Time

	mailSettings  MailLinkSettings
	mailCaptcha   MailLinkCaptcha
	mailer        MailLinkMailer
	mailLimiter   *ratelimit.Limiter
	publicBaseURL string
}

// New 构造 auth service。public、admin 是两个域各自的签发器（不同密钥）；users 是 identity service。
func New(r *repo.Repo, txm *tx.Manager, rdb *redis.Client, keys *crypto.Keys,
	public, admin *jwtx.Issuer, users Users, audit *auditsvc.Service, cfg Config) *Service {
	return newService(r, txm, rdb, keys, public, admin, users, audit, cfg)
}

func newService(st store, txr txRunner, rdb *redis.Client, keys *crypto.Keys,
	public, admin *jwtx.Issuer, users Users, audit auditWriter, cfg Config) *Service {
	return &Service{
		store:    st,
		tx:       txr,
		sessions: newSessionStore(rdb),
		keys:     keys,
		issuers:  map[string]*jwtx.Issuer{authn.AudiencePublic: public, authn.AudienceAdmin: admin},
		users:    users,
		audit:    audit,
		cfg:      cfg,
		now:      time.Now,
	}
}

// Tokens 是登录、刷新、快捷登录、重认证的结果。
type Tokens struct {
	AccessToken string
	ExpiresIn   int // 秒
	UserID      uuid.UUID
	// Refresh 是新的 refresh cookie 值（<sid>.<随机串>）；为空表示不改 cookie（刷新宽限期内、重认证）。
	Refresh string
	// RefreshExpires 是 cookie 的过期时间（会话的绝对过期）。
	RefreshExpires time.Time
}

// SessionInfo 是门户「我的登录设备」的一行。
type SessionInfo struct {
	ID         string
	Current    bool
	UserAgent  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// QuickLoginLink 是新签发的快捷登录令牌；明文只返回这一次。
type QuickLoginLink struct {
	Token     string
	ExpiresAt time.Time
	ExpiresIn int
}

// AdminProfile 是后台 GET /me 的内容。
type AdminProfile struct {
	UserID      uuid.UUID
	Email       string
	DisplayName *string
	Reauthed    bool
}

// AdminSummary 是 pandora admin list 的一行。
type AdminSummary struct {
	UserID    uuid.UUID
	Email     string
	Status    string
	CreatedAt time.Time
}

// 对外的固定错误。口令错、用户不存在、停用、登后台的不是管理员一律 errInvalidCredentials（IAM-006）。
var (
	errInvalidCredentials = errs.Newf(errs.Unauthorized, "邮箱或密码不正确")
	errSessionInvalid     = errs.New(errs.Unauthorized)
	// ErrStaffPortalLogin 是后台人员登录门户时的中性提示（不带后台路径）。
	ErrStaffPortalLogin = errs.Newf(errs.Forbidden, "该账号是管理账号，不能登录用户门户，请从管理后台的登录入口登录")
)

// actorID 取 ctx 里登录主体的用户 ID；没有登录主体（命令行）时为 nil。
func actorID(ctx context.Context) *uuid.UUID {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return nil
	}
	id := p.UserID
	return &id
}

// sessionTTL 返回某个域的会话绝对寿命。
func (s *Service) sessionTTL(audience string) time.Duration {
	if audience == authn.AudienceAdmin {
		return s.cfg.AdminSessionTTL
	}
	return s.cfg.PublicSessionTTL
}

// idleTimeout 返回某个域的空闲超时；门户没有（0）。
func (s *Service) idleTimeout(audience string) time.Duration {
	if audience == authn.AudienceAdmin {
		return s.cfg.AdminIdleTimeout
	}
	return 0
}

// issuer 返回某个域的签发器；未知域返回 nil。
func (s *Service) issuer(audience string) *jwtx.Issuer {
	return s.issuers[audience]
}
