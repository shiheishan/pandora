// [INPUT]: 依赖 platform/config 的配置、domain/* 各服务的构造与后台循环、api/admin 的 NewRouter
// [OUTPUT]: 对外提供 可执行入口 aegis-admin：装配管理控制台网关并启动工单超时升级、定时公告、配额周期滚动、佣金解冻等后台循环
// [POS]: panel/cmd 的 admin 网关进程，与 aegis-public 分进程分端口；mark-paid 与人工开单履约后的节点通知经 nodefabric.NotifyUsersChanged 发出；客服回复通知经 support.SetReplyNotifier 接到 notify，只排队不派发
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Command aegis-admin 是管理控制台网关（Admin 域）。
//
// 与 aegis-public 完全独立的进程与端口：管理面故障不影响用户下单续费，
// 用户面被打爆也不会挤掉管理员的处置能力（ARC-002）。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/api/admin"
	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/content"
	"github.com/aegispanel/aegis/internal/domain/giftcard"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/geoip"
	"github.com/aegispanel/aegis/internal/platform/logging"
	"github.com/aegispanel/aegis/internal/platform/realtime"
	"github.com/aegispanel/aegis/internal/platform/server"
	"github.com/aegispanel/aegis/internal/platform/token"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg.Env, "aegis-admin")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	closeResourcesOnReturn := true

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() {
		if closeResourcesOnReturn {
			pool.Close()
		}
	}()

	redisOpt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("解析 Redis 连接串: %w", err)
	}
	rdb := redis.NewClient(redisOpt)
	defer func() {
		if closeResourcesOnReturn {
			_ = rdb.Close()
		}
	}()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("缓存不可达: %w", err)
	}

	// 用 admin 域自己的签名密钥。这把密钥与 public 域不同，
	// 是「令牌不可跨域使用」的物理保证（EXT-001）。
	issuer := token.NewIssuer(string(config.DomainAdmin),
		cfg.JWTSecrets[config.DomainAdmin], cfg.AccessTokenTTL)

	identitySvc := identity.NewService(pool, issuer, cfg.RefreshTokenTTL, cfg.MasterKey, !cfg.IsProduction())
	// 销售能力必须显式授权，默认关闭（详见 salescap.go）。
	// 不注入的话定价与上架会一直 503 —— 这正是它上线以来的状态。
	salesCap := salesCapabilityFromEnv()
	if !salesCap.AllowsP0BSales() {
		log.Warn("销售能力未授权：定价与套餐上架将返回 503。" +
			"如需启用，在 .env 里设置 AEGIS_SALES_ENABLED=1 后重启 aegis-admin")
	}
	opsSvc := adminops.NewService(pool, salesCap)
	contentSvc := content.New(pool)
	supportSvc := support.NewService(pool)
	nodeSigner, err := crypto.NewSigner(cfg.ConfigSigningSeed)
	if err != nil {
		return fmt.Errorf("初始化配置签名器: %w", err)
	}
	nodeSvc := nodefabric.NewService(pool, nodeSigner)

	// 信封加密器：审计里的来源 IP 落库前用它加密
	envelope, err := crypto.NewEnvelope(cfg.MasterKey)
	if err != nil {
		return fmt.Errorf("初始化信封加密: %w", err)
	}

	// 审计里的来源信息：哈希用于关联分析，密文供后台查看。
	//
	// 哈希函数与 identity 用的是同一个（crypto.HashIdentifier），
	// 否则同一个 IP 在两处会算出不同的值，按 IP 反查账号时会漏掉一半记录。
	audit.Configure(
		func(ip string) []byte { return crypto.HashIdentifier(cfg.MasterKey, ip) },
		func(b []byte) ([]byte, error) { return envelope.Seal(b, []byte("audit")) },
	)

	rtHub := realtime.NewHub(rdb, log)
	defer func() {
		if closeResourcesOnReturn {
			rtHub.Close()
		}
	}()
	nodeSvc.AttachRealtime(rtHub)

	// 管理端只用到 billing 的后台作业部分（佣金解冻），
	// 下单与支付仍然只在 public 网关里发生
	billingSvc := billing.NewService(pool, envelope)
	// 管理员手动标记已付、人工开单走的也是同一条履约路径，同样要通知节点；
	// 发布只经 nodefabric 的 NotifyUsersChanged 一处
	billingSvc.SetUsersChangedNotifier(nodeSvc.NotifyUsersChanged)
	// 管理端只用它把到点的定时公告转正、给工单回复排队，不投递任何消息，所以没有 sender
	notifySvc := notify.New(pool, log, cfg.MasterKey)
	// 客服非内部回复在同一事务里给提单人排 ticket.replied（R115）。本进程不跑派发
	// 循环，Kick 在这里是空操作，排好的通知由 public 网关的扫描循环投递
	supportSvc.SetReplyNotifier(notifySvc)
	appearanceSvc := appearance.New(pool)
	// 插件钩子在生产模式下拒绝内网地址；devMode 传 !IsProduction()，
	// 和支付渠道那边用的是同一个判据（SEC-007）。
	pluginSvc := plugin.New(pool, envelope, !cfg.IsProduction())
	// 礼品卡把「发什么」交回给计费域执行，自己只管「该不该发」
	giftCardSvc := giftcard.New(pool, log, billingSvc.GiftGranter())
	smtpProvider := notify.NewDBSMTPProvider(pool, envelope)

	// IP 归属地库。缺文件不算致命：后台照常可用，只是归属地列留空。
	// 风控明细里最要紧的是 IP 和时间，归属地是帮着判断的旁证。
	geoResolver, geoErr := geoip.Open(geoDatabasePath())
	if geoErr != nil {
		log.Warn("IP 归属地库不可用，归属地列将留空",
			"path", geoDatabasePath(), "err", geoErr)
	} else {
		defer geoResolver.Close()
	}
	// 节点接入时用同一份库把公网 IP 自动识别成地区，填到 servers.region，
	// 管理员新建服务器时不必手抄地区。没配库就静默降级（region 留空）。
	nodeSvc.SetGeoIP(geoResolver)

	handler := admin.NewRouter(admin.Deps{
		GeoIP:          geoResolver,
		Realtime:       rtHub,
		Envelope:       envelope,
		Billing:        billingSvc,
		Content:        contentSvc,
		SMTPProvider:   smtpProvider,
		Notify:         notifySvc,
		GiftCard:       giftCardSvc,
		TelegramSender: notify.NewDynamicTelegramSender(pool, envelope, middleware.DefaultTenantID),
		Appearance:     appearanceSvc,
		Plugin:         pluginSvc,
		Cfg:            cfg, Pool: pool, Redis: rdb, Log: log,
		Issuer: issuer, Identity: identitySvc, Ops: opsSvc, Support: supportSvc, Node: nodeSvc,
		// salt 从主密钥派生，和 public 域算出来的是同一个值 —— 换发订阅
		// 凭据时两边写进去的哈希必须一致，否则用户拿到的新链接验不过。
		Subscription: subscription.New(pool, crypto.SubscriptionAuditSalt(cfg.MasterKey), envelope),
	})

	log.Info("AegisPanel Admin 控制台就绪", "addr", cfg.AdminAddr)

	// SLA 扫描：把首次响应超时的工单自动升级（OPS-001 验收「超时自动升级」）。
	//
	// 放在 admin 网关而不是单独的 worker 进程：这台机器只有 1 核，
	// 多一个常驻进程的代价大于收益；而 EscalateOverdue 本身是幂等的，
	// 将来拆成独立 worker 或换成 cron 也不需要改动业务代码。
	var workers sync.WaitGroup
	workers.Add(4)
	go func() {
		defer workers.Done()
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			// 启动后先跑一次，不必等第一个 tick
			sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			n, err := supportSvc.EscalateOverdue(sctx, middleware.DefaultTenantID)
			cancel()
			switch {
			case err != nil:
				log.Error("工单 SLA 扫描失败", "error", err.Error())
			case n > 0:
				log.Warn("工单因首次响应超时被自动升级", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// 定时公告到点转正。
	//
	// 没有这一步，scheduled 状态会永远停在那里 —— 运营以为排好了
	// 周二早上的维护通知，实际上它一直没发出去。
	go func() {
		defer workers.Done()
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			n, err := notifySvc.PublishDueAnnouncements(sctx, middleware.DefaultTenantID)
			cancel()
			switch {
			case err != nil:
				log.Error("定时公告发布失败", "error", err.Error())
			case n > 0:
				log.Info("定时公告已发布", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// 配额周期滚动：把 day / month 型配额滚到下一周期。
	//
	// 跑得比周期短得多是必须的：整点跑一次的话，一个月付流量包
	// 会在月初到点后最长等一小时才恢复 —— 那一小时里用户是断网的。
	go func() {
		defer workers.Done()
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			sctx, cancel := context.WithTimeout(ctx, time.Minute)
			n, err := billingSvc.RollQuotaPeriods(sctx, middleware.DefaultTenantID)
			cancel()
			switch {
			case err != nil:
				log.Error("配额周期滚动失败", "error", err.Error())
			case n > 0:
				log.Info("配额已滚入新周期", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// 佣金解冻：把冻结期已过的佣金转成可提现。
	//
	// 一小时一次足够：冻结期以天计，用户不会盯着秒表等解冻。
	// 跑得太勤只是让一个纯写事务反复抢锁，对谁都没好处。
	go func() {
		defer workers.Done()
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			sctx, cancel := context.WithTimeout(ctx, time.Minute)
			n, err := billingSvc.SettleMatured(sctx, middleware.DefaultTenantID)
			cancel()
			switch {
			case err != nil:
				log.Error("佣金解冻失败", "error", err.Error())
			case n > 0:
				log.Info("佣金已解冻", "count", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	serverErr := server.RunContext(ctx, server.Options{
		Addr:            cfg.AdminAddr,
		Handler:         handler,
		Log:             log,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	stop()
	drainErr := waitForAdminWorkers(&workers, cfg.ShutdownTimeout)
	if drainErr != nil {
		// A timed-out worker may still own a database connection or use Redis.
		// Do not race it with graceful closers (pgxpool.Close can itself wait
		// forever); main will return this fatal error and os.Exit will reclaim
		// process resources.
		closeResourcesOnReturn = false
		log.Error("admin background workers did not stop before shutdown deadline",
			"error", drainErr.Error())
	}
	return errors.Join(serverErr, drainErr)
}

var errAdminWorkerDrainTimeout = errors.New("admin worker drain timed out")

func waitForAdminWorkers(workers *sync.WaitGroup, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return fmt.Errorf("%w after %s", errAdminWorkerDrainTimeout, timeout)
	}
}

// geoDatabasePath 是 ip2region 数据文件的位置。
//
// 走环境变量而不是配置表：这个路径由部署决定（文件跟着发布产物走），
// 不是运营会去改的东西。
func geoDatabasePath() string {
	if p := strings.TrimSpace(os.Getenv("AEGIS_GEOIP_DB")); p != "" {
		return p
	}
	return "/opt/aegispanel/geoip/ip2region_v4.xdb"
}
