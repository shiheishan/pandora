// Package service 是 auth 模块的业务层：登录、刷新、登出、重认证、会话（Redis）、快捷登录、
// 权限展开、角色授予与最后一个管理员保护。
//
// 会话状态全在 Redis（store.go）；PG 只有权限字典、角色与用户角色绑定（repo）。
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
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Users 是 auth 对 identity service 的全部要求；*identitysvc.Service 实现它（app 装配时传入）。
type Users interface {
	// Authenticate 校验邮箱与口令；口令错或用户不存在返回 errs.Unauthorized。
	Authenticate(ctx context.Context, audience, email, password string) (identitysvc.User, error)
	// FindByEmail 按邮箱找用户；不存在返回 errs.NotFound。
	FindByEmail(ctx context.Context, email string) (identitysvc.User, error)
	Get(ctx context.Context, id uuid.UUID) (identitysvc.User, error)
	GetMany(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]identitysvc.User, error)
	// VerifyPassword 校验已知用户的口令；错了返回 errs.Unauthorized。
	VerifyPassword(ctx context.Context, audience string, userID uuid.UUID, password string) error
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
	// Permissions 返回用户当前生效（未过期绑定）的权限码，去重、升序。
	Permissions(ctx context.Context, userID uuid.UUID) ([]string, error)
	// HasActiveRole 报告用户是否持有任何未过期的角色绑定。
	HasActiveRole(ctx context.Context, userID uuid.UUID) (bool, error)
	// UserRoles 返回用户未过期绑定的角色（按 name、code 排序）。
	UserRoles(ctx context.Context, userID uuid.UUID) ([]repo.RoleRow, error)
	// RoleByCode 按角色码取角色；不存在 ok 为 false。
	RoleByCode(ctx context.Context, code string) (role repo.RoleRow, ok bool, err error)
	// UpsertUserRole 授予（或把已有绑定改成长期绑定）。
	UpsertUserRole(ctx context.Context, userID, roleID uuid.UUID, grantedBy *uuid.UUID) error
	// DeleteUserRole 收回绑定；本来没有返回 false。
	DeleteUserRole(ctx context.Context, userID, roleID uuid.UUID) (bool, error)
	// LockAdminSet 在当前事务里取最后一个管理员保护的事务级咨询锁。
	LockAdminSet(ctx context.Context) error
	// PermanentHolders 返回经长期绑定持有 permission 的用户（去重）。
	PermanentHolders(ctx context.Context, permission string) ([]uuid.UUID, error)
	// RoleHolders 返回持有未过期绑定的用户及其角色码、权限数。
	RoleHolders(ctx context.Context) ([]repo.HolderRow, error)
	// Roles 返回全部角色及权限数（按 code 排序）。
	Roles(ctx context.Context) ([]repo.RoleSummaryRow, error)
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
	// Permissions 只在后台登录时有值。
	Permissions []string
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

// RoleRef 是角色的码与名称。
type RoleRef struct {
	Code string
	Name string
}

// AdminProfile 是后台 GET /me 的内容。
type AdminProfile struct {
	UserID      uuid.UUID
	Email       string
	Roles       []RoleRef
	Permissions []string
	Reauthed    bool
}

// AdminSummary 是 pandora admin list 的一行。
type AdminSummary struct {
	UserID      uuid.UUID
	Email       string
	Status      string
	Roles       []string
	Permissions int
}

// RoleSummary 是 pandora admin roles 的一行。
type RoleSummary struct {
	Code        string
	Name        string
	Permissions int
}

// 对外的固定错误。口令错、用户不存在、停用、后台无角色一律 errInvalidCredentials（IAM-006）。
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
