package app

import (
	"fmt"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	authrepo "github.com/aegispanel/aegis/internal/module/auth/repo"
	authsvc "github.com/aegispanel/aegis/internal/module/auth/service"
	idemrepo "github.com/aegispanel/aegis/internal/module/idempotency/repo"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	identityrepo "github.com/aegispanel/aegis/internal/module/identity/repo"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	notifyrepo "github.com/aegispanel/aegis/internal/module/notify/repo"
	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
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

	// 模块间注入：identity 定义接口，auth、notify 实现；audit 经 identity 显示操作者与资源
	identitySvc.UseSessionRevoker(authSvc)
	identitySvc.UseAdminGuard(authSvc)
	identitySvc.UseStaffGuard(authSvc)
	identitySvc.UseVerificationMailer(notifySvc)
	auditSvc.UseActorDirectory(identitySvc)
	auditSvc.UseResourceLabeler(identitysvc.ResourceUser, identitySvc.UserLabels)

	return &services{
		settings: settingsSvc,
		audit:    auditSvc,
		idem:     idemSvc,
		identity: identitySvc,
		auth:     authSvc,
		notify:   notifySvc,
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
