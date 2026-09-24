// [INPUT]: 依赖 platform/config 的配置、domain/* 各服务的构造与后台循环、api/public 的 NewRouter
// [OUTPUT]: 对外提供 可执行入口 aegis-public：装配用户门户网关并启动通知扫描、插件投递、预留过期等后台循环
// [POS]: panel/cmd 的 public 网关进程；identity 的注册验证码经这里接上 notify（SetVerificationMailer）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Command aegis-public 是用户门户 API 网关（Public 域）。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/api/public"
	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/content"
	"github.com/aegispanel/aegis/internal/domain/giftcard"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
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

	log := logging.New(cfg.Env, "aegis-public")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	redisOpt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("解析 Redis 连接串: %w", err)
	}
	rdb := redis.NewClient(redisOpt)
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("缓存不可达: %w", err)
	}

	issuer := token.NewIssuer(string(config.DomainPublic),
		cfg.JWTSecrets[config.DomainPublic], cfg.AccessTokenTTL)

	identitySvc := identity.NewService(pool, issuer, cfg.RefreshTokenTTL, cfg.MasterKey, !cfg.IsProduction())
	contentSvc := content.New(pool)

	supportSvc := support.NewService(pool)

	// 信封加密器：支付渠道凭据、订阅 token 落库前都用它加密（SEC-010）
	envelope, err := crypto.NewEnvelope(cfg.MasterKey)
	if err != nil {
		return fmt.Errorf("初始化信封加密: %w", err)
	}

	billingSvc := billing.NewService(pool, envelope)
	// 礼品卡把「发什么」交回给计费域执行，自己只判断「该不该发」
	giftCardSvc := giftcard.New(pool, log, billingSvc.GiftGranter())

	paymentSvc := billing.NewPaymentService(pool, envelope, cfg.MasterKey, cfg.PublicBaseURL, !cfg.IsProduction())

	// 审计日志里 IP/UA 的哈希盐。
	//
	// 从主密钥派生而不是单独配一项：它必须在重启后保持一致（否则同一个 IP
	// 前后会算出不同哈希，多来源检测立刻失效），又不该与主密钥相同 ——
	// 万一审计库泄露，也不能倒推出任何与加密相关的东西。
	subSalt := crypto.SubscriptionAuditSalt(cfg.MasterKey)

	// 实时推送中枢。多实例之间靠 Valkey 广播互通 ——
	// 用户连在这个进程上，改数据的请求却可能落在另一个进程。
	// 审计里的来源信息：哈希用于关联分析，密文供后台查看。
	//
	// 哈希函数与 identity 用的是同一个（crypto.HashIdentifier），
	// 否则同一个 IP 在两处会算出不同的值，按 IP 反查账号时会漏掉一半记录。
	audit.Configure(
		func(ip string) []byte { return crypto.HashIdentifier(cfg.MasterKey, ip) },
		func(b []byte) ([]byte, error) { return envelope.Seal(b, []byte("audit")) },
	)

	rtHub := realtime.NewHub(rdb, log)
	defer rtHub.Close()

	// 付款履约之后立刻告诉节点「用户集合变了」，否则要等节点那轮 15 秒
	// 轮询，用户付完款会有十几秒连不上。billingSvc 建得比 rtHub 早，
	// 所以用 setter 而不是构造参数。
	billingSvc.SetUsersChangedNotifier(func(ctx context.Context, tenantID string) {
		rtHub.Publish(ctx, realtime.ChannelNodeAll(tenantID),
			realtime.TopicNodeUsersChanged, map[string]any{})
	})

	// 数据库变更监听：任何一张被关注的表发生写入，都会自动推到前端。
	// 这样新增功能不必记得「顺手发条推送」——覆盖面由触发器保证。
	realtime.StartDBListener(ctx, pool.Pool, rtHub, log)

	// 通知：站内信总是可用；邮件要配了 SMTP 才启用，
	// 没配时相关投递会被标成 suppressed（未配置），而不是攒成失败记录。
	// SMTP 配置在数据库里，管理员随时可改。发信器总是装上：
	// 配置为空时投递会被标成 suppressed，而不是在启动时就把渠道摘掉 ——
	// 否则后台填好 SMTP 之后还得重启进程才能发信。
	smtpProvider := notify.NewDBSMTPProvider(pool, envelope)
	mailSender := notify.NewDynamicSMTPSender(smtpProvider, middleware.DefaultTenantID)
	// Telegram 与 SMTP 并列注册：到期提醒、流量预警这些既有通知
	// 只认 template_code，多一个渠道不用改它们一行代码。
	tgSender := notify.NewDynamicTelegramSender(pool, envelope, middleware.DefaultTenantID)
	notifySvc := notify.New(pool, log, subSalt, mailSender, tgSender)
	// 注册验证码经 notify 投递：注册第 1 步在同一事务里排队，提交后催派发
	identitySvc.SetVerificationMailer(notifySvc)
	appearanceSvc := appearance.New(pool)
	// 扫描间隔 5 分钟：到期提醒按天计，流量预警的阈值也不会分钟级跨越，
	// 扫太密只是白白压库。
	notifySvc.StartScanner(ctx, middleware.DefaultTenantID, 5*time.Minute)
	// 插件投递也放在 public：事件几乎都在这个进程里产生，
	// 派发跟着一起跑省得两个服务抢同一批队列行。
	// 一分钟一轮 —— 插件多半是记账、同步这类事，比通知更该及时。
	plugin.New(pool, envelope, !cfg.IsProduction()).
		StartScanner(ctx, middleware.DefaultTenantID, time.Minute)
	waitReservationExpiry := startReservationExpiryWorker(ctx, billingSvc, log)
	if err := notify.EnsureTelegramWebhookSecret(ctx, pool, envelope,
		middleware.DefaultTenantID); err != nil {
		return fmt.Errorf("初始化 Telegram webhook secret: %w", err)
	}

	handler := public.NewRouter(public.Deps{
		Realtime:       rtHub,
		Notify:         notifySvc,
		Content:        contentSvc,
		GiftCard:       giftCardSvc,
		TelegramSender: tgSender,
		Envelope:       envelope,
		Appearance:     appearanceSvc,
		Subscription:   subscription.New(pool, subSalt, envelope),
		Cfg:            cfg, Pool: pool, Redis: rdb, Log: log,
		Issuer: issuer, Identity: identitySvc,
		Billing: billingSvc, Payments: paymentSvc, Support: supportSvc,
	})

	// env 已由 logging.New 作为固定字段附加，此处不再重复
	log.Info("AegisPanel Public 网关就绪", "addr", cfg.PublicAddr)

	serverErr := server.RunContext(ctx, server.Options{
		Addr:            cfg.PublicAddr,
		Handler:         handler,
		Log:             log,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	// Cancel synchronously before deferred resource cleanup. In particular, the
	// DB LISTEN goroutine must release its acquired connection before pool.Close.
	stop()
	waitReservationExpiry()
	return serverErr
}

// atoiOr 解析端口号，失败时用默认值。
// 配置写错不该让整个服务起不来 —— 邮件发不出去是可降级的。
func startReservationExpiryWorker(ctx context.Context, svc *billing.Service,
	log *slog.Logger) func() {
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				_, err := svc.ExpireDueReservations(
					runCtx, middleware.DefaultTenantID, 50)
				cancel()
				if err != nil {
					log.Error("reservation expiry scan failed", "error", err)
				}
			}
		}
	}()
	return workers.Wait
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}
