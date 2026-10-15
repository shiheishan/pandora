package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	appearancerepo "github.com/aegispanel/aegis/internal/module/appearance/repo"
	appearancesvc "github.com/aegispanel/aegis/internal/module/appearance/service"
	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	authrepo "github.com/aegispanel/aegis/internal/module/auth/repo"
	authsvc "github.com/aegispanel/aegis/internal/module/auth/service"
	catalogrepo "github.com/aegispanel/aegis/internal/module/catalog/repo"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	commissionrepo "github.com/aegispanel/aegis/internal/module/commission/repo"
	commissionsvc "github.com/aegispanel/aegis/internal/module/commission/service"
	contentrepo "github.com/aegispanel/aegis/internal/module/content/repo"
	contentsvc "github.com/aegispanel/aegis/internal/module/content/service"
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
	pluginrepo "github.com/aegispanel/aegis/internal/module/plugin/repo"
	pluginsvc "github.com/aegispanel/aegis/internal/module/plugin/service"
	promorepo "github.com/aegispanel/aegis/internal/module/promo/repo"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	reportrepo "github.com/aegispanel/aegis/internal/module/report/repo"
	reportsvc "github.com/aegispanel/aegis/internal/module/report/service"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	subrepo "github.com/aegispanel/aegis/internal/module/subscription/repo"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	supportrepo "github.com/aegispanel/aegis/internal/module/support/repo"
	supportsvc "github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/geoip"
	"github.com/aegispanel/aegis/internal/pkg/jwtx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/redisx"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
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
	content    *contentsvc.Service
	appearance *appearancesvc.Service
	support    *supportsvc.Service
	plugin     *pluginsvc.Service
	report     *reportsvc.Service
	// idemFPKey 是幂等请求指纹的 HMAC 密钥（从主密钥派生）
	idemFPKey []byte
	limiter   *ratelimit.Limiter
	// sealer 是封存密钥（节点 CA 私钥的封存在 serve 里用）
	sealer *crypto.Sealer
	queue  *asynq.Client
	tx     *tx.Manager
	geo    *geoip.Resolver
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
	nodeSvc.UseIdempotency(idemSvc)
	// IP 归属地（ip2region 离线库）：没放数据文件只记一条 Info、归属地留空；文件在却打不开才 Warn。启动都不受影响
	var geo *geoip.Resolver
	if _, statErr := os.Stat(cfg.GeoIP.IPv4DB); errors.Is(statErr, fs.ErrNotExist) || strings.TrimSpace(cfg.GeoIP.IPv4DB) == "" {
		r.log.Info("没有 IP 归属地数据文件，归属地留空", zap.String("ipv4_db", cfg.GeoIP.IPv4DB))
	} else if g, err := geoip.Open(cfg.GeoIP.IPv4DB, cfg.GeoIP.IPv6DB, r.log); err != nil {
		r.log.Warn("打开 IP 归属地数据库失败，归属地留空",
			zap.String("ipv4_db", cfg.GeoIP.IPv4DB), zap.String("ipv6_db", cfg.GeoIP.IPv6DB), zap.Error(err))
	} else {
		geo = g
	}
	identitySvc.UseGeo(geo)
	nodeSvc.UseGeo(geo)
	giftcardSvc := giftcardsvc.New(giftcardrepo.New(r.db), txm, auditSvc, idemSvc, catalogSvc, subSvc, ledgerSvc, identitySvc, keys, sealer)
	// 内容页：发版经 catalog 校验限定套餐、门户经 subscription 取生效套餐、后台经 identity 显示作者
	contentSvc := contentsvc.New(contentrepo.New(r.db), txm, auditSvc, catalogSvc, subSvc, identitySvc)
	// 外观：插槽更新时间按 settings 的站点时区显示
	appearanceSvc := appearancesvc.New(appearancerepo.New(r.db), txm, auditSvc, settingsSvc)

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
	// 可下发线路的来源（UseNodeSource）由 node 模块装配，没装配时生效订阅的拉取回 503
	subSvc.UseDelivery(subsvc.Delivery{
		Settings:      settingsSvc,
		Owners:        identitySvc,
		Hasher:        keys,
		Limiter:       limiter,
		PublicBaseURL: cfg.PublicBaseURL,
		// 订阅附加设置（信息节点、协议前缀、订阅域名）也在 settings
		Extras: settingsSvc,
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
	// 后台批量运营与风控：订阅条件、当前订阅与拉取日志经 subscription 取；批量生成经 queue 入队；
	// notify 群发经已注入的 Directory（identitySvc）取收件人；后台用户列表/详情/用户组的余额、Telegram 绑定、组引用
	// 分别经 ledger、notify、catalog/promo/node 取。
	identitySvc.UseOpsSubscriptions(subSvc)
	identitySvc.UseOpsOrders(opsOrderAdapter{s: orderSvc})
	identitySvc.UseOpsWallets(opsWalletAdapter{l: ledgerSvc})
	identitySvc.UseOpsTelegram(notifySvc)
	identitySvc.UseOpsGroupUsage(groupUsageAdapter{catalog: catalogSvc, promo: promoSvc, node: nodeSvc})
	identitySvc.UseJobQueue(queue)
	// 节点上报的流量经 subscription 扣量（配额 → 流量包 → 按日用量）
	nodeSvc.UseTrafficCharger(subSvc)
	nodeSvc.UseKeys(keys)
	// 节点池与套餐绑池：绑池表归 catalog，池归 node；node 经 catalog 读绑池、经 subscription 读在用版本、经 identity 读用户组名，
	// catalog 发布前、向导绑池时回调 node（发布门禁、锁池）
	nodeSvc.UseCatalog(catalogSvc)
	nodeSvc.UseVersionsInUse(subSvc)
	nodeSvc.UseUserGroups(identitySvc)
	catalogSvc.UsePublishGate(nodeSvc)
	catalogSvc.UsePoolGuard(nodeSvc)
	// 设备数限制：策略存在 settings；后台总览经 subscription、identity、catalog 批量取订阅、邮箱、套餐名；
	// 门户「我的套餐」的在线设备数经 node 取
	nodeSvc.UseDevices(settingsSvc, subSvc, identitySvc, catalogSvc)
	subSvc.UseOnlineDeviceCounter(nodeSvc)
	// 节点流量周期与时段倍率按站点时区算；ECH 私钥经封存密钥封存
	nodeSvc.UseSiteSettings(settingsSvc)
	nodeSvc.UseECHSealer(sealer)
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
	// 工单：提单人与客服的邮箱、显示名经 identity，可指派客服是 auth 的管理员名单；
	// 关联订单经 order 校验归属（建单时存下订单号），队列与详情的在用套餐经 subscription 批量读；审计页显示工单号
	supportSvc := supportsvc.New(supportrepo.New(r.db), txm, auditSvc, idemSvc, identitySvc, authSvc)
	// 实时推送：各属主模块在事务提交后发「什么变了」；serve 与 worker 都要装（到期、关窗、定时发布在 worker 里跑）
	rt := realtime.NewPublisher(r.redis)
	for _, m := range []interface{ UseRealtime(*realtime.Publisher) }{
		settingsSvc, catalogSvc, subSvc, orderSvc, nodeSvc, notifySvc, supportSvc,
	} {
		m.UseRealtime(rt)
	}
	supportSvc.UseOrders(supportOrders{o: orderSvc})
	supportSvc.UsePlanNames(supportPlans{s: subSvc})
	// 客服公开回复后，在回复的同一事务里经 notify 给提单人写「工单有新回复」
	supportSvc.UseReplyNotifier(supportReplyNotifier{n: notifySvc})
	auditSvc.UseResourceLabeler(supportsvc.ResourceTicket, supportSvc.TicketLabels)

	// 通知：扫描与公告定向要读用户、套餐、订阅，支付成功通知读最近履约的订单；Telegram webhook 地址由对外地址拼出
	notifySvc.UseDirectory(identitySvc)
	notifySvc.UsePlans(catalogSvc)
	notifySvc.UseSubscriptions(subSvc)
	notifySvc.UseOrders(orderSvc)
	notifySvc.UsePublicBaseURL(cfg.PublicBaseURL)
	// xb-tg：bot 的 /traffic、/sub 与入群审核经 subscription 取订阅摘要与订阅链接；管理员群组的工单提醒附提单人余额（ledger）；
	// 在管理员群组里回复工单提醒：auth 核对发消息的人仍是可登录的管理员，再以其身份经 support 回复（后台只读时拒绝）
	notifySvc.UseSubscriptionSummaries(subSvc)
	notifySvc.UseSubscribeLinks(notifySubscribeLinks{s: subSvc})
	notifySvc.UseBalances(notifyBalances{l: ledgerSvc})
	notifySvc.UseAdmins(authSvc)
	notifySvc.UseTicketReplier(notifyTicketReplier{support: supportSvc, switches: settingsSvc})
	// 工单规则（须等客服回复、自动关闭）在 settings；建单与用户回复时经 notify 往管理员群组排提醒
	supportSvc.UseSettings(settingsSvc)
	supportSvc.UseAdminNotifier(supportAdminNotifier{n: notifySvc})
	// 知识库占位变量：站点名与地址经 settings 与对外地址，订阅链接经 subscription
	contentSvc.UseSite(settingsSvc)
	contentSvc.UseSubscribeLinks(contentSubscribeLinks{s: subSvc})
	contentSvc.UsePublicBaseURL(cfg.PublicBaseURL)

	// 插件钩子：六个发事件的模块在业务事务里经 plugin 写待投递记录，提交后入队 plugin:dispatch；
	// 钩子地址允许 http 与内网只在开发环境（防 SSRF）
	pluginSvc := pluginsvc.New(pluginrepo.New(r.db), txm, auditSvc, idemSvc, sealer, queue,
		pluginsvc.Options{AllowPrivate: cfg.Env == config.EnvDevelopment})
	orderSvc.UsePluginEvents(pluginSvc)
	subSvc.UsePluginEvents(pluginSvc)
	identitySvc.UsePluginEvents(pluginSvc)
	supportSvc.UsePluginEvents(pluginSvc)
	giftcardSvc.UsePluginEvents(pluginSvc)
	notifySvc.UsePluginEvents(pluginSvc)
	// 看板与营收报表：数字一律经各属主模块的聚合读；report 只拥有报表调整与按天行为汇总两张表
	reportSvc := reportsvc.New(reportrepo.New(r.db), txm, auditSvc, reportsvc.Deps{
		Settings: settingsSvc, Users: identitySvc, Subscriptions: subSvc, Nodes: nodeSvc, Ledger: ledgerSvc,
		Orders: orderSvc, Payments: paymentSvc, Commissions: commissionSvc, Tickets: supportSvc, Notices: notifySvc,
		Activity: auditSvc, Redis: reportRedis{c: r.redis},
	}, reportsvc.Options{BackupDir: cfg.Backup.Dir})
	reportSvc.UseCache(r.redis)

	// 注册与登录（xb-reg）：注册试用、注销停订阅由 subscription 实现，注销取消待付单由 order 实现，
	// 注销作废佣金与在途提现由 commission 实现，注销解绑 Telegram 由 notify 实现；
	// 邮件链接免密登录读 settings 的开关与发信配置、经 notify 发信、按邮箱限频，链接拼在对外地址上
	identitySvc.UseTrialGranter(subSvc)
	identitySvc.UseAccountCloser(subSvc)
	identitySvc.UseOrderCanceller(orderSvc)
	identitySvc.UseCommissionVoider(commissionVoidAdapter{s: commissionSvc})
	ledgerSvc.UseAccountStates(identitySvc)
	identitySvc.UseTelegramUnbinder(notifySvc)
	// 邮件链接登录第 1 步的人机验证由 identity 校验（与注册、找回同一套）
	authSvc.UseMailLink(settingsSvc, notifySvc, identitySvc, limiter, cfg.PublicBaseURL)

	// 装配自检：所有带 Use* 注入的 service 在这里统一检查必需端口（各自的 MissingPorts），缺了就拒绝启动
	if err := checkPorts([]portCheck{
		{"settings", settingsSvc}, {"audit", auditSvc}, {"identity", identitySvc}, {"catalog", catalogSvc},
		{"promo", promoSvc}, {"subscription", subSvc}, {"payment", paymentSvc}, {"order", orderSvc},
		{"commission", commissionSvc}, {"giftcard", giftcardSvc}, {"node", nodeSvc}, {"support", supportSvc}, {"ledger", ledgerSvc},
		{"notify", notifySvc}, {"report", reportSvc}, {"content", contentSvc},
	}); err != nil {
		_ = queue.Close()
		return nil, err
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
		content:    contentSvc,
		appearance: appearanceSvc,
		support:    supportSvc,
		plugin:     pluginSvc,
		report:     reportSvc,
		idemFPKey:  keys.Derive(crypto.InfoHMACIdempotency, 32),
		limiter:    limiter,
		sealer:     sealer,
		queue:      queue,
		tx:         txm,
		geo:        geo,
	}, nil
}

func (s *services) close() {
	if s != nil && s.queue != nil {
		_ = s.queue.Close()
	}
	if s != nil && s.geo != nil {
		s.geo.Close()
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

// portCheck 是装配自检的一项：带 Use* 注入的 service 实现 MissingPorts，返回还没注入的必需端口名。
type portCheck struct {
	name string
	svc  interface{ MissingPorts() []string }
}

// checkPorts 汇总各 service 缺的必需端口；有缺的返回一条列出全部缺口的错误。
func checkPorts(checks []portCheck) error {
	var problems []string
	for _, c := range checks {
		if missing := c.svc.MissingPorts(); len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("%s %v", c.name, missing))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("缺少装配: %s", strings.Join(problems, "; "))
	}
	return nil
}
