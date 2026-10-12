package app

import (
	"fmt"
	"time"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	authrepo "github.com/aegispanel/aegis/internal/module/auth/repo"
	authsvc "github.com/aegispanel/aegis/internal/module/auth/service"
	catalogrepo "github.com/aegispanel/aegis/internal/module/catalog/repo"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemrepo "github.com/aegispanel/aegis/internal/module/idempotency/repo"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	identityrepo "github.com/aegispanel/aegis/internal/module/identity/repo"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgerrepo "github.com/aegispanel/aegis/internal/module/ledger/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	notifyrepo "github.com/aegispanel/aegis/internal/module/notify/repo"
	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
	orderrepo "github.com/aegispanel/aegis/internal/module/order/repo"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	paymentrepo "github.com/aegispanel/aegis/internal/module/payment/repo"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	promorepo "github.com/aegispanel/aegis/internal/module/promo/repo"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subrepo "github.com/aegispanel/aegis/internal/module/subscription/repo"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/jwtx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/redisx"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/hibiken/asynq"
)

// services 是 serve、worker、admin 子命令共用的业务服务，各模块的互相注入在 buildServices 一处完成。
type services struct {
	settings *settingssvc.Service
	audit    *auditsvc.Service
	idem     *idemsvc.Service
	identity *identitysvc.Service
	auth     *authsvc.Service
	notify   *notifysvc.Service
	ledger   *ledgersvc.Service
	catalog  *catalogsvc.Service
	promo    *promosvc.Service
	sub      *subsvc.Service
	payment  *paymentsvc.Service
	order    *ordersvc.Service
	limiter  *ratelimit.Limiter
	queue    *asynq.Client
	tx       *tx.Manager
}

// asynqRedis 给 asynq 的连接参数：与业务客户端同一套解析（redisx.Options），用户名、口令、库号、TLS、超时都同源。
func asynqRedis(cfg *config.Config) (asynq.RedisConnOpt, error) {
	opt, err := redisx.Options(cfg.Redis)
	if err != nil {
		return nil, fmt.Errorf("解析 Redis URL: %w", err)
	}
	return asynq.RedisClientOpt{
		Network:      opt.Network,
		Addr:         opt.Addr,
		Username:     opt.Username,
		Password:     opt.Password,
		DB:           opt.DB,
		DialTimeout:  opt.DialTimeout,
		ReadTimeout:  opt.ReadTimeout,
		WriteTimeout: opt.WriteTimeout,
		PoolSize:     opt.PoolSize,
		TLSConfig:    opt.TLSConfig,
	}, nil
}

// buildServices 构造全部业务服务并完成模块间注入。
func buildServices(r *runtime) (*services, error) {
	cfg := r.cfg
	keys, err := crypto.NewKeys(cfg.Secrets.MasterKey.Reveal())
	if err != nil {
		return nil, fmt.Errorf("主密钥: %w", err)
	}
	sealer, err := keys.Sealer()
	if err != nil {
		return nil, fmt.Errorf("派生封存密钥: %w", err)
	}
	publicIssuer, err := newIssuer(jwtx.Public, cfg.Auth.PublicJWTSecret, cfg.Auth.PublicJWTPreviousSecret, cfg.Auth.AccessTTL)
	if err != nil {
		return nil, fmt.Errorf("门户 JWT 签发器: %w", err)
	}
	adminIssuer, err := newIssuer(jwtx.Admin, cfg.Auth.AdminJWTSecret, cfg.Auth.AdminJWTPreviousSecret, cfg.Auth.AccessTTL)
	if err != nil {
		return nil, fmt.Errorf("后台 JWT 签发器: %w", err)
	}
	redisOpt, err := asynqRedis(cfg)
	if err != nil {
		return nil, err
	}

	txm := tx.NewManager(r.db)
	limiter := ratelimit.New(r.redis)

	auditSvc, err := auditsvc.New(auditrepo.New(r.db), keys)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	settingsSvc := settingssvc.New(settingsrepo.New(r.db), txm, sealer, r.redis, auditSvc)
	idemSvc := idemsvc.New(idemrepo.New(r.db), txm)
	identitySvc := identitysvc.New(identityrepo.New(r.db), txm, settingsSvc, auditSvc, keys, r.redis, limiter, cfg)
	authSvc := authsvc.New(authrepo.New(r.db), txm, r.redis, keys, publicIssuer, adminIssuer, identitySvc, auditSvc,
		authsvc.Config{
			AccessTTL:        cfg.Auth.AccessTTL,
			PublicSessionTTL: cfg.Auth.PublicSessionTTL,
			AdminSessionTTL:  cfg.Auth.AdminSessionTTL,
			AdminIdleTimeout: cfg.Auth.AdminIdleTimeout,
		})

	queue := asynq.NewClient(redisOpt)
	notifySvc, err := notifysvc.New(notifyrepo.New(r.db), txm, keys, settingsSvc, auditSvc, queue)
	if err != nil {
		_ = queue.Close()
		return nil, fmt.Errorf("notify: %w", err)
	}

	ledgerSvc := ledgersvc.New(ledgerrepo.New(r.db), txm, settingsSvc, auditSvc)
	catalogSvc := catalogsvc.New(catalogrepo.New(r.db), txm, auditSvc, identitySvc)
	promoSvc := promosvc.New(promorepo.New(r.db), txm, auditSvc, settingsSvc)
	paymentSvc := paymentsvc.New(paymentrepo.New(r.db), txm, ledgerSvc, identitySvc, settingsSvc, auditSvc, sealer,
		paymentsvc.Config{
			PublicBaseURL: cfg.PublicBaseURL,
			// 渠道地址允许内网只在开发环境（防 SSRF）
			AllowPrivateHosts: cfg.Env == config.EnvDevelopment,
		})
	subSvc := subsvc.New(subrepo.New(r.db), txm, auditSvc, settingsSvc, catalogSvc, identitySvc, sealer)

	orderSvc := ordersvc.New(orderrepo.New(r.db), txm, catalogSvc, ledgerSvc, promoSvc, subSvc, paymentSvc, identitySvc, idemSvc, auditSvc)

	// 模块间注入：identity 定义接口，auth、notify 实现；audit 经 identity 显示操作者与资源
	identitySvc.UseSessionRevoker(authSvc)
	identitySvc.UseAdminGuard(authSvc)
	identitySvc.UseStaffGuard(authSvc)
	identitySvc.UseVerificationMailer(notifySvc)
	auditSvc.UseActorDirectory(identitySvc)
	auditSvc.UseResourceLabeler(identitysvc.ResourceUser, identitySvc.UserLabels)
	// promo 经 identity 取用户组与邮箱、经适配器取 catalog 的在售价格
	promoSvc.UseUsers(identitySvc)
	promoSvc.UseCatalog(promoCatalog{c: catalogSvc})
	// 渠道到账与线下收款经 order 结算（没注册时入账一律失败）
	paymentSvc.UseSettler(orderSvc)

	return &services{
		settings: settingsSvc,
		audit:    auditSvc,
		idem:     idemSvc,
		identity: identitySvc,
		auth:     authSvc,
		notify:   notifySvc,
		ledger:   ledgerSvc,
		catalog:  catalogSvc,
		promo:    promoSvc,
		sub:      subSvc,
		payment:  paymentSvc,
		order:    orderSvc,
		limiter:  limiter,
		queue:    queue,
		tx:       txm,
	}, nil
}

func (s *services) close() {
	if s != nil && s.queue != nil {
		_ = s.queue.Close()
	}
}

// newIssuer 用当前密钥与可选的上一代密钥（只验不签）构造签发器。
func newIssuer(domain jwtx.Domain, current, previous config.Secret, ttl time.Duration) (*jwtx.Issuer, error) {
	var prev []byte
	if previous != "" {
		prev = []byte(previous.Reveal())
	}
	return jwtx.NewIssuer(domain, []byte(current.Reveal()), prev, ttl)
}
