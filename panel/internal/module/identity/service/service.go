// Package service 是 identity 模块的业务层：用户、口令、注册、找回密码、改密、邀请码、用户组，
// 以及后台的用户管理。别的模块只调本包；auth、notify 经 ports.go 的接口反向提供能力，启动装配时注入。
//
// 账号枚举防护（IAM-006）贯穿注册、登录、找回密码三个入口：账号存不存在，对外的状态码、正文与耗时都一样。
package service

import (
	"context"
	"errors"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/geoip"
	"github.com/aegispanel/aegis/internal/pkg/pluginevents"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// settingsReader 是 identity 用到的 settings 读取面。
type settingsReader interface {
	RegistrationPolicy(ctx context.Context) (settingssvc.Registration, error)
	MailConfigured(ctx context.Context) (bool, error)
	Signup(ctx context.Context) (settingssvc.Signup, error)
}

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Service 是 identity 模块对外的唯一入口。
type Service struct {
	repo     *repo.Repo
	tx       txRunner
	events   pluginevents.Emitter // 插件钩子出站事件，装配时注入（plugin_events.go）
	settings settingsReader
	audit    auditWriter
	keys     *crypto.Keys
	rdb      *redis.Client
	limiter  *ratelimit.Limiter
	hasher   *passwordHasher
	// devMode 为 true（非生产环境）时注册与找回的 start 响应带验证码，e2e 依赖它。
	devMode bool
	now     func() time.Time

	// 下面四个由 auth、notify 实现，启动装配时经 Use* 注入。
	revoker    SessionRevoker
	adminGuard AdminGuard
	mailer     VerificationMailer
	staff      StaffGuard

	// roster 由 node 实现（roster_notify.go）：账号状态、用户组变了，提交后通知节点名单重算。
	roster RosterNotifier

	// xb-reg 端口：注册试用、注销停订阅、注销取消待付订单、注销作废佣金、解绑 Telegram
	// （subscription / order / commission / notify 实现，启动装配时注入）。
	trials         TrialGranter
	closer         AccountCloser
	cancelOrders   OrderCanceller
	voidCommission CommissionVoider
	tgUnbind       TelegramUnbinder
	captcha        *captchaVerifier

	// 批量运营与风控（ops_types.go）：ops 是 audit 的读与复核写；subs 由 subscription 实现、queue 是 asynq 客户端，装配时注入。
	ops    auditOps
	subs   OpsSubscriptions
	orders OpsOrders
	queue  JobQueue

	// 后台用户列表、详情与用户组（admin_views.go）：余额经 ledger、Telegram 绑定经 notify、组引用经 catalog/promo/node，装配时注入。
	wallets    OpsWallets
	telegram   OpsTelegram
	groupUsage OpsGroupUsage

	geo *geoip.Resolver
}

// New 构造 identity service。keys 用于验证码、收件目标与限流键的 HMAC；rdb 存改密失败计数；
// limiter 做按账号维度的严格限流；cfg 取口令哈希并发闸与是否生产环境。
func New(r *repo.Repo, txm *tx.Manager, settings *settingssvc.Service, audit *auditsvc.Service,
	keys *crypto.Keys, rdb *redis.Client, limiter *ratelimit.Limiter, cfg *config.Config) *Service {
	return &Service{
		repo:     r,
		tx:       txm,
		settings: settings,
		audit:    audit,
		ops:      audit,
		keys:     keys,
		rdb:      rdb,
		limiter:  limiter,
		hasher:   newPasswordHasher(cfg.Auth.PasswordHash),
		devMode:  !cfg.IsProduction(),
		now:      time.Now,
		captcha:  newCaptchaVerifier(cfg.PublicBaseURL),
	}
}

// UseSessionRevoker 只在启动装配时调用（不并发安全）。
func (s *Service) UseSessionRevoker(r SessionRevoker) { s.revoker = r }

// UseAdminGuard 只在启动装配时调用（不并发安全）。
func (s *Service) UseAdminGuard(g AdminGuard) { s.adminGuard = g }

// UseVerificationMailer 只在启动装配时调用（不并发安全）。
func (s *Service) UseVerificationMailer(m VerificationMailer) { s.mailer = m }

// UseStaffGuard 只在启动装配时调用（不并发安全）。
func (s *Service) UseStaffGuard(g StaffGuard) { s.staff = g }

// 没注入就调用到时返回 internal_error，不 panic。
var errNotWired = errors.New("identity: 依赖未注入")

func (s *Service) needRevoker() (SessionRevoker, error) {
	if s.revoker == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.revoker, nil
}

func (s *Service) needAdminGuard() (AdminGuard, error) {
	if s.adminGuard == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.adminGuard, nil
}

func (s *Service) needMailer() (VerificationMailer, error) {
	if s.mailer == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.mailer, nil
}

func (s *Service) needStaff() (StaffGuard, error) {
	if s.staff == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.staff, nil
}

// isStaff 经 StaffGuard 判断用户是否管理员。
func (s *Service) isStaff(ctx context.Context, userID uuid.UUID) (bool, error) {
	g, err := s.needStaff()
	if err != nil {
		return false, err
	}
	return g.IsStaff(ctx, userID)
}

// principal 取 ctx 里的登录主体；没有时回 401。
func principal(ctx context.Context) (authn.Principal, error) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return authn.Principal{}, errs.New(errs.Unauthorized)
	}
	return p, nil
}

// writeAudit 在 ctx 的事务里写一条审计。
func (s *Service) writeAudit(ctx context.Context, e auditsvc.Event) error {
	return s.audit.Write(ctx, e)
}

// writeSelfAudit 写匿名流程（注册、找回密码）里「本人对自己账号」的审计：被操作的用户记为资源 user；
// ctx 里没有登录主体时操作者也记成这个用户（门户域），有主体时不传 ActorID，由审计记那个主体。
func (s *Service) writeSelfAudit(ctx context.Context, userID uuid.UUID, e auditsvc.Event) error {
	e.ResourceType = ResourceUser
	e.ResourceID = &userID
	if p, ok := authn.From(ctx); !ok || p.UserID == uuid.Nil {
		e.ActorID = &userID
		e.Audience = authn.AudiencePublic
	} else {
		e.ActorID = nil
		e.Audience = ""
	}
	return s.writeAudit(ctx, e)
}

// 对外的固定错误。同一个变量只读共享，调用方可以 errors.Is 比较。
var (
	// ErrInvalidCredentials 是邮箱不存在或口令不对。
	ErrInvalidCredentials = errs.Newf(errs.Unauthorized, "邮箱或密码不正确")
	// ErrAccountInactive 是账号已停用或封禁：对外与 ErrInvalidCredentials 完全相同（IAM-006），
	// 单独一个变量只为让 auth 在失败审计里区分原因。
	ErrAccountInactive = errs.Newf(errs.Unauthorized, "邮箱或密码不正确")
	// ErrWrongCurrentPassword 是改密时当前口令不对。
	ErrWrongCurrentPassword = errs.Newf(errs.Unauthorized, "当前密码不正确")
	// ErrRegistrationUnavailable 是注册关闭、邀请码无效、注册令牌无效或验证码错（统一一句）。
	ErrRegistrationUnavailable = errs.Newf(errs.Forbidden, "注册当前不可用或邀请码无效")
	// ErrPasswordResetUnavailable 是没配邮件服务时的找回密码（入口本该隐藏）。
	ErrPasswordResetUnavailable = errs.Newf(errs.Forbidden, "找回密码当前不可用，请联系客服")
	// ErrPasswordHashBusy 是排队等口令哈希名额超时。
	ErrPasswordHashBusy = errs.Newf(errs.Unavailable, "口令校验繁忙，请稍后重试")
)

// errCodeInvalid 是找回密码第 2 步一切「对不上」的统一回应：没有验证码、过期、次数用完、账号不可用。
func errCodeInvalid() error {
	return errs.Invalid(map[string]string{"code": "验证码错误或已失效，请重新获取"})
}

// MissingPorts 返回还没注入的必需端口名（装配完成后由 internal/app 统一检查）。
// 不算必需：geo（没放 IP 归属地数据文件时就是 nil，归属地留空）。
func (s *Service) MissingPorts() []string {
	var out []string
	for _, p := range []struct {
		name string
		ok   bool
	}{
		{"SessionRevoker", s.revoker != nil},
		{"AdminGuard", s.adminGuard != nil},
		{"StaffGuard", s.staff != nil},
		{"VerificationMailer", s.mailer != nil},
		{"RosterNotifier", s.roster != nil},
		{"OpsSubscriptions", s.subs != nil},
		{"OpsOrders", s.orders != nil},
		{"OpsWallets", s.wallets != nil},
		{"OpsTelegram", s.telegram != nil},
		{"OpsGroupUsage", s.groupUsage != nil},
		{"JobQueue", s.queue != nil},
		{"PluginEvents", s.events != nil},
		{"TrialGranter", s.trials != nil},
		{"AccountCloser", s.closer != nil},
		{"OrderCanceller", s.cancelOrders != nil},
		{"CommissionVoider", s.voidCommission != nil},
		{"TelegramUnbinder", s.tgUnbind != nil},
	} {
		if !p.ok {
			out = append(out, p.name)
		}
	}
	return out
}
