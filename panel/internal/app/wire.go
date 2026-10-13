package app

import (
	"context"
	"fmt"
	"time"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	authrepo "github.com/aegispanel/aegis/internal/module/auth/repo"
	authsvc "github.com/aegispanel/aegis/internal/module/auth/service"
	catalogrepo "github.com/aegispanel/aegis/internal/module/catalog/repo"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	commissionrepo "github.com/aegispanel/aegis/internal/module/commission/repo"
	commissionsvc "github.com/aegispanel/aegis/internal/module/commission/service"
	giftcardrepo "github.com/aegispanel/aegis/internal/module/giftcard/repo"
	giftcardsvc "github.com/aegispanel/aegis/internal/module/giftcard/service"
	idemrepo "github.com/aegispanel/aegis/internal/module/idempotency/repo"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	identityrepo "github.com/aegispanel/aegis/internal/module/identity/repo"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgerrepo "github.com/aegispanel/aegis/internal/module/ledger/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	noderepo "github.com/aegispanel/aegis/internal/module/node/repo"
	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
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
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	subrepo "github.com/aegispanel/aegis/internal/module/subscription/repo"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	supportrepo "github.com/aegispanel/aegis/internal/module/support/repo"
	supportsvc "github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/jwtx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/redisx"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// services 是 serve、worker、admin 子命令共用的业务服务，各模块的互相注入在 buildServices 一处完成。
type services struct {
	settings   *settingssvc.Service
	audit      *auditsvc.Service
	idem       *idemsvc.Service
	identity   *identitysvc.Service
	auth       *authsvc.Service
	notify     *notifysvc.Service
	ledger     *ledgersvc.Service
	catalog    *catalogsvc.Service
	promo      *promosvc.Service
	sub        *subsvc.Service
	payment    *paymentsvc.Service
	order      *ordersvc.Service
	commission *commissionsvc.Service
	giftcard   *giftcardsvc.Service
	node       *nodesvc.Service
	support    *supportsvc.Service
	// idemFPKey 是幂等请求指纹的 HMAC 密钥（从主密钥派生）
	idemFPKey []byte
	limiter   *ratelimit.Limiter
	// sealer 是封存密钥（节点 CA 私钥的封存在 serve 里用）
	sealer *crypto.Sealer
	queue  *asynq.Client
	tx     *tx.Manager
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
	commissionSvc := commissionsvc.New(commissionrepo.New(r.db), txm, ledgerSvc, settingsSvc, sealer, auditSvc)
	nodeSvc := nodesvc.New(noderepo.New(r.db), txm, auditSvc, r.redis)
	// 节点端一键安装：二进制从发布包的 pdnd-dist 目录发，安装脚本里写入面板地址
	nodeSvc.UsePdndDist(cfg.Pdnd.DistDir, cfg.PublicBaseURL)
	giftcardSvc := giftcardsvc.New(giftcardrepo.New(r.db), txm, auditSvc, idemSvc, catalogSvc, subSvc, ledgerSvc, identitySvc, keys, sealer)

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
	// 订阅分发（拉取、链接列表、换链接）：前缀与拉取上限在 settings、主人状态在 identity、限流在 Redis；
	// 可下发线路的来源（UseNodeSource）由 P5 的 node 模块装配，没装配前生效订阅的拉取回 503
	subSvc.UseDelivery(subsvc.Delivery{
		Settings:      settingsSvc,
		Owners:        identitySvc,
		Hasher:        keys,
		Limiter:       limiter,
		PublicBaseURL: cfg.PublicBaseURL,
		Render: render.Options{Singbox: render.SingboxTemplate{
			GeositeURLPrefix: cfg.Subscription.Singbox.GeositeURLPrefix,
			GeoIPURLPrefix:   cfg.Subscription.Singbox.GeoIPURLPrefix,
			BlockAds:         cfg.Subscription.Singbox.BlockAds,
		}},
	})
	// 订阅里影响节点名单的事实变了（开通、续期、换套餐…），提交后推进节点名单纪元
	subSvc.UseRosterNotifier(nodeSvc)
	// 名单与线路：node 经 subscription 取可服务订阅、经 identity 取账号状态与用户组、经 catalog 取版本限速与绑池；
	// 订阅拉取经 node 取线路；identity 改账号状态、用户组后推进名单纪元
	nodeSvc.UseDelivery(subSvc, identitySvc, catalogSvc)
	subSvc.UseNodeSource(nodeSvc)
	identitySvc.UseRosterNotifier(nodeSvc)
	// 节点上报的流量经 subscription 扣量（配额 → 流量包 → 按日用量）
	nodeSvc.UseTrafficCharger(subSvc)
	nodeSvc.UseKeys(keys)
	// 节点池与套餐绑池：绑池表归 catalog，池归 node；node 经 catalog 读绑池、经 identity 读用户组名，
	// catalog 发布前、向导绑池时回调 node（发布门禁、锁池）
	nodeSvc.UseCatalog(catalogSvc)
	nodeSvc.UseUserGroups(identitySvc)
	catalogSvc.UsePublishGate(nodeSvc)
	catalogSvc.UsePoolGuard(nodeSvc)
	// 设备数限制：策略存在 settings；后台总览经 subscription、identity、catalog 批量取订阅、邮箱、套餐名；
	// 门户「我的套餐」的在线设备数经 node 取
	nodeSvc.UseDevices(settingsSvc, subSvc, identitySvc, catalogSvc)
	subSvc.UseOnlineDeviceCounter(nodeSvc)
	// 渠道到账与线下收款经 order 结算（没注册时入账一律失败）
	paymentSvc.UseSettler(orderSvc)
	// 订单结算时按推荐关系计提佣金（同一结算事务）；同源（审计里共用来源 IP）的佣金转人工复核
	commissionSvc.UseReferrals(identitySvc)
	commissionSvc.UseSourceMatcher(auditSvc)
	orderSvc.UsePaidHook(commissionSvc)
	// 礼品卡：套餐卡「换掉那一份」按 order 的折算退剩余价值；新用户、付费用户、受邀用户三种条件
	giftcardSvc.UsePlanChangeValuer(orderSvc)
	giftcardSvc.UsePaidHistory(orderSvc)
	giftcardSvc.UseReferrals(identitySvc)
	if missing := giftcardSvc.MissingPorts(); len(missing) > 0 {
		_ = queue.Close()
		return nil, fmt.Errorf("giftcard 缺少装配: %v", missing)
	}
	if missing := commissionSvc.MissingPorts(); len(missing) > 0 {
		_ = queue.Close()
		return nil, fmt.Errorf("commission 缺少装配: %v", missing)
	}
	// 工单：提单人与客服的邮箱、显示名经 identity，可指派客服是 auth 的管理员名单；
	// 关联订单经 order 校验归属（建单时存下订单号），队列与详情的在用套餐经 subscription 批量读；审计页显示工单号
	supportSvc := supportsvc.New(supportrepo.New(r.db), txm, auditSvc, idemSvc, identitySvc, authSvc)
	supportSvc.UseOrders(supportOrders{o: orderSvc})
	supportSvc.UsePlanNames(supportPlans{s: subSvc})
	auditSvc.UseResourceLabeler(supportsvc.ResourceTicket, supportSvc.TicketLabels)
	if missing := supportSvc.MissingPorts(); len(missing) > 0 {
		_ = queue.Close()
		return nil, fmt.Errorf("support 缺少装配: %v", missing)
	}

	return &services{
		settings:   settingsSvc,
		audit:      auditSvc,
		idem:       idemSvc,
		identity:   identitySvc,
		auth:       authSvc,
		notify:     notifySvc,
		ledger:     ledgerSvc,
		catalog:    catalogSvc,
		promo:      promoSvc,
		sub:        subSvc,
		payment:    paymentSvc,
		order:      orderSvc,
		commission: commissionSvc,
		giftcard:   giftcardSvc,
		node:       nodeSvc,
		support:    supportSvc,
		idemFPKey:  keys.Derive(crypto.InfoHMACIdempotency, 32),
		limiter:    limiter,
		sealer:     sealer,
		queue:      queue,
		tx:         txm,
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

// supportOrders 让 support 经 order 校验门户用户关联的订单：MyOrder 按 ctx 里的门户主体限定本人，
// 不存在或不是本人的回 errs.NotFound。
type supportOrders struct{ o *ordersvc.Service }

// OwnedOrderNo 返回门户主体名下这张订单的订单号。
func (a supportOrders) OwnedOrderNo(ctx context.Context, orderID uuid.UUID) (string, error) {
	d, err := a.o.MyOrder(ctx, orderID)
	if err != nil {
		return "", err
	}
	return d.Order.OrderNo, nil
}

// supportPlans 让 support 经 subscription 批量取提单人当前生效订阅的套餐名（与订阅的「当前订阅」同一口径）。
type supportPlans struct{ s *subsvc.Service }

// ActivePlanNames 返回当前订阅生效的用户的套餐名；没有生效订阅的用户不在结果里。
func (a supportPlans) ActivePlanNames(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	sums, err := a.s.SummariesByUsers(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(sums))
	for id, us := range sums {
		if us.Current != nil && us.Current.State == subsvc.StateLive {
			out[id] = us.Current.PlanName
		}
	}
	return out, nil
}

var (
	_ supportsvc.Orders    = supportOrders{}
	_ supportsvc.PlanNames = supportPlans{}
)
